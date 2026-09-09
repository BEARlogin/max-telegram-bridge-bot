package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

var tgPreviewableURLPattern = regexp.MustCompile(`(?i)https?://[^\s<]+`)

func tgMediaStateFromMessage(msg *TGMessage) (TgMediaState, bool) {
	if msg == nil {
		return TgMediaState{}, false
	}
	state := TgMediaState{TgMsgID: msg.MessageID, MediaGroupID: msg.MediaGroupID}
	var uniqueID string
	switch {
	case len(msg.Photo) > 0:
		state.Kind = "photo"
		state.FileID = msg.Photo[len(msg.Photo)-1].FileID
		uniqueID = msg.Photo[len(msg.Photo)-1].FileUniqueID
	case msg.Video != nil:
		state.Kind, state.FileID, state.FileName = "video", msg.Video.FileID, msg.Video.FileName
		uniqueID = msg.Video.FileUniqueID
	case msg.Animation != nil:
		state.Kind, state.FileID, state.FileName = "animation", msg.Animation.FileID, msg.Animation.FileName
		uniqueID = msg.Animation.FileUniqueID
	case msg.Document != nil:
		state.Kind, state.FileID, state.FileName = "document", msg.Document.FileID, msg.Document.FileName
		state.MimeType = msg.Document.MimeType
		uniqueID = msg.Document.FileUniqueID
	case msg.Audio != nil:
		state.Kind, state.FileID, state.FileName = "audio", msg.Audio.FileID, msg.Audio.FileName
		uniqueID = msg.Audio.FileUniqueID
	case msg.Voice != nil:
		state.Kind, state.FileID, state.FileName = "voice", msg.Voice.FileID, msg.Voice.FileName
		uniqueID = msg.Voice.FileUniqueID
	case msg.VideoNote != nil:
		state.Kind, state.FileID = "video_note", msg.VideoNote.FileID
		uniqueID = msg.VideoNote.FileUniqueID
	default:
		return TgMediaState{}, false
	}
	if state.FileID == "" {
		return TgMediaState{}, false
	}
	state.Fingerprint = state.Kind + ":" + state.FileID
	if uniqueID != "" {
		state.Fingerprint = state.Kind + ":u:" + uniqueID
	}
	return state, true
}

func sameTgMediaIdentity(previous, current TgMediaState) bool {
	if previous.Kind != current.Kind {
		return false
	}
	if previous.Fingerprint == current.Fingerprint {
		return true
	}
	// Legacy rows used the unstable file_id. The first edit upgrades them to
	// file_unique_id without attempting a destructive MAX attachment rewrite.
	return previous.Fingerprint == previous.Kind+":"+previous.FileID &&
		previous.FileID == current.FileID &&
		strings.HasPrefix(current.Fingerprint, current.Kind+":u:")
}

func (b *Bridge) saveTgMediaState(msg *TGMessage) {
	if state, ok := tgMediaStateFromMessage(msg); ok {
		b.repo.SaveTgMediaState(msg.Chat.ID, state)
	}
}

func replaceTgMediaState(states []TgMediaState, replacement TgMediaState) []TgMediaState {
	for i := range states {
		if states[i].TgMsgID == replacement.TgMsgID {
			states[i] = replacement
			return states
		}
	}
	return append(states, replacement)
}

func tgMessageHasPreviewableURL(msg *TGMessage) bool {
	if msg == nil {
		return false
	}
	text := msg.Caption
	entities := msg.CaptionEntities
	if text == "" {
		text = msg.Text
		entities = msg.Entities
	}
	if tgPreviewableURLPattern.MatchString(text) {
		return true
	}
	for _, entity := range entities {
		if entity.Type == "text_link" && tgPreviewableURLPattern.MatchString(entity.URL) {
			return true
		}
	}
	return false
}

func tgMediaEditHasPreviewableURL(msg *TGMessage, outgoingText string) bool {
	return tgMessageHasPreviewableURL(msg) || tgPreviewableURLPattern.MatchString(outgoingText)
}

type tgMediaEditPlan struct {
	states  []TgMediaState
	rebuild bool
}

func (b *Bridge) planTgMediaEdit(edited *TGMessage, maxMsgID, outgoingText string) (tgMediaEditPlan, error) {
	current, hasMedia := tgMediaStateFromMessage(edited)
	if !hasMedia {
		return tgMediaEditPlan{}, fmt.Errorf("edited Telegram message has no supported media")
	}
	previous, known := b.repo.GetTgMediaState(edited.Chat.ID, edited.MessageID)
	mediaChanged := known && !sameTgMediaIdentity(previous, current)
	if !mediaChanged && !tgMediaEditHasPreviewableURL(edited, outgoingText) {
		if !known || previous.Fingerprint != current.Fingerprint || previous.FileID != current.FileID {
			b.repo.SaveTgMediaState(edited.Chat.ID, current)
		}
		return tgMediaEditPlan{}, nil
	}

	states := []TgMediaState{current}
	if edited.MediaGroupID != "" {
		states = replaceTgMediaState(
			b.repo.ListTgMediaStates(edited.Chat.ID, maxMsgID),
			current,
		)
		if err := validateAlbumMediaStates(states, edited.MediaGroupID); err != nil {
			// Retain the observed item so a later edit can complete legacy album state,
			// but do not issue an unsafe edit that could erase the other attachments.
			b.repo.SaveTgMediaState(edited.Chat.ID, current)
			return tgMediaEditPlan{}, err
		}
		mappedIDs := b.repo.ListTgMsgIDs(maxMsgID, edited.Chat.ID)
		if len(mappedIDs) != len(states) {
			b.repo.SaveTgMediaState(edited.Chat.ID, current)
			return tgMediaEditPlan{}, fmt.Errorf("album media state is incomplete: %d of %d mapped items", len(states), len(mappedIDs))
		}
	}
	return tgMediaEditPlan{states: states, rebuild: true}, nil
}

func (b *Bridge) editTgMediaCaptionInMax(
	ctx context.Context,
	edited *TGMessage,
	maxChatID int64,
	maxMsgID, text string,
) error {
	plan, err := b.planTgMediaEdit(edited, maxMsgID, text)
	if err != nil {
		return err
	}
	if !plan.rebuild {
		return b.editMaxTextOnly(ctx, maxChatID, maxMsgID, text, "html")
	}
	if err := b.editTgCrosspostMediaInMax(ctx, edited, plan.states, maxChatID, maxMsgID, text); err != nil {
		// A URL-bearing text-only PUT is destructive: MAX can replace existing
		// media with a generated share preview. Keep the old post untouched.
		if tgMediaEditHasPreviewableURL(edited, text) {
			return err
		}
		if fallbackErr := b.editMaxTextOnly(ctx, maxChatID, maxMsgID, text, "html"); fallbackErr != nil {
			return fmt.Errorf("replace media: %v; text fallback: %w", err, fallbackErr)
		}
	}
	return nil
}

// editTgCrosspostMediaInMax replaces the complete MAX attachment set. For an
// album, states must contain every Telegram item mapped to the same MAX post.
func (b *Bridge) editTgCrosspostMediaInMax(
	ctx context.Context,
	msg *TGMessage,
	states []TgMediaState,
	maxChatID int64,
	maxMsgID, text string,
) error {
	ctx = b.withMaxToken(ctx, b.maxTokenFor(ctx, maxChatID))
	m := maxbot.NewMessage().SetChat(maxChatID).SetText(text).SetFormat(maxschemes.HTML)
	for _, state := range states {
		switch state.Kind {
		case "photo":
			uploaded, err := b.uploadTgPhotoToMax(ctx, state.FileID)
			if err != nil {
				return fmt.Errorf("upload replacement photo: %w", err)
			}
			m.AddPhoto(uploaded)
		case "video", "animation", "video_note":
			name := state.FileName
			if name == "" {
				name = "video.mp4"
			}
			uploaded, err := b.uploadTgMediaToMax(ctx, state.FileID, maxschemes.VIDEO, name)
			if err != nil {
				return fmt.Errorf("upload replacement video: %w", err)
			}
			m.AddVideo(uploaded)
		case "document":
			name, uploadType, _ := tgDocumentMaxSpec(state.FileName, state.MimeType)
			uploaded, err := b.uploadTgMediaToMax(ctx, state.FileID, uploadType, name)
			if err != nil {
				return fmt.Errorf("upload replacement document: %w", err)
			}
			m.AddFile(uploaded)
		case "audio":
			name := state.FileName
			if name == "" {
				name = "audio.mp3"
			}
			uploaded, err := b.uploadTgMediaToMax(ctx, state.FileID, maxschemes.FILE, name)
			if err != nil {
				return fmt.Errorf("upload replacement audio: %w", err)
			}
			m.AddFile(uploaded)
		case "voice":
			uploaded, err := b.uploadTgMediaToMax(ctx, state.FileID, maxschemes.AUDIO, "voice.ogg")
			if err != nil {
				return fmt.Errorf("upload replacement voice: %w", err)
			}
			m.AddAudio(uploaded)
		default:
			return fmt.Errorf("unsupported replacement media kind %q", state.Kind)
		}
	}
	if len(states) == 0 {
		return fmt.Errorf("empty media replacement")
	}
	if err := b.maxClientFor(ctx, maxChatID).Messages.EditMessage(ctx, maxMsgID, m); err != nil {
		return fmt.Errorf("edit MAX media: %w", err)
	}
	for _, state := range states {
		b.repo.SaveTgMediaState(msg.Chat.ID, state)
	}
	slog.Info("TG→MAX crosspost media replaced", "mid", maxMsgID, "items", len(states),
		"tgChat", msg.Chat.ID, "tgMsg", msg.MessageID)
	return nil
}

func validateAlbumMediaStates(states []TgMediaState, groupID string) error {
	if len(states) < 2 {
		return fmt.Errorf("album media state is incomplete")
	}
	for _, state := range states {
		if state.MediaGroupID != groupID {
			return fmt.Errorf("album media state belongs to another group")
		}
	}
	return nil
}
