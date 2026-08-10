package main

import (
	"context"
	"fmt"
	"log/slog"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
	maxschemes "github.com/max-messenger/max-bot-api-client-go/schemes"
)

func tgMediaStateFromMessage(msg *TGMessage) (TgMediaState, bool) {
	if msg == nil {
		return TgMediaState{}, false
	}
	state := TgMediaState{TgMsgID: msg.MessageID, MediaGroupID: msg.MediaGroupID}
	switch {
	case len(msg.Photo) > 0:
		state.Kind = "photo"
		state.FileID = msg.Photo[len(msg.Photo)-1].FileID
	case msg.Video != nil:
		state.Kind, state.FileID, state.FileName = "video", msg.Video.FileID, msg.Video.FileName
	case msg.Animation != nil:
		state.Kind, state.FileID, state.FileName = "animation", msg.Animation.FileID, msg.Animation.FileName
	case msg.Document != nil:
		state.Kind, state.FileID, state.FileName = "document", msg.Document.FileID, msg.Document.FileName
		state.MimeType = msg.Document.MimeType
	case msg.Audio != nil:
		state.Kind, state.FileID, state.FileName = "audio", msg.Audio.FileID, msg.Audio.FileName
	case msg.Voice != nil:
		state.Kind, state.FileID, state.FileName = "voice", msg.Voice.FileID, msg.Voice.FileName
	case msg.VideoNote != nil:
		state.Kind, state.FileID = "video_note", msg.VideoNote.FileID
	default:
		return TgMediaState{}, false
	}
	if state.FileID == "" {
		return TgMediaState{}, false
	}
	state.Fingerprint = state.Kind + ":" + state.FileID
	return state, true
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

// editTgCrosspostMediaInMax replaces the complete MAX attachment set. For an
// album, states must contain every Telegram item mapped to the same MAX post.
func (b *Bridge) editTgCrosspostMediaInMax(
	ctx context.Context,
	msg *TGMessage,
	states []TgMediaState,
	maxChatID int64,
	maxMsgID, text string,
) {
	ctx = b.withMaxToken(ctx, b.maxTokenFor(ctx, maxChatID))
	m := maxbot.NewMessage().SetChat(maxChatID).SetText(text).SetFormat(maxschemes.HTML)
	for _, state := range states {
		switch state.Kind {
		case "photo":
			uploaded, err := b.uploadTgPhotoToMax(ctx, state.FileID)
			if err != nil {
				slog.Error("TG→MAX edit photo replacement failed", "err", err, "tgChat", msg.Chat.ID)
				return
			}
			m.AddPhoto(uploaded)
		case "video", "animation", "video_note":
			name := state.FileName
			if name == "" {
				name = "video.mp4"
			}
			uploaded, err := b.uploadTgMediaToMax(ctx, state.FileID, maxschemes.VIDEO, name)
			if err != nil {
				slog.Error("TG→MAX edit video replacement failed", "err", err, "tgChat", msg.Chat.ID)
				return
			}
			m.AddVideo(uploaded)
		case "document":
			name, uploadType, _ := tgDocumentMaxSpec(state.FileName, state.MimeType)
			uploaded, err := b.uploadTgMediaToMax(ctx, state.FileID, uploadType, name)
			if err != nil {
				slog.Error("TG→MAX edit document replacement failed", "err", err, "tgChat", msg.Chat.ID)
				return
			}
			m.AddFile(uploaded)
		case "audio":
			name := state.FileName
			if name == "" {
				name = "audio.mp3"
			}
			uploaded, err := b.uploadTgMediaToMax(ctx, state.FileID, maxschemes.FILE, name)
			if err != nil {
				slog.Error("TG→MAX edit audio replacement failed", "err", err, "tgChat", msg.Chat.ID)
				return
			}
			m.AddFile(uploaded)
		case "voice":
			uploaded, err := b.uploadTgMediaToMax(ctx, state.FileID, maxschemes.AUDIO, "voice.ogg")
			if err != nil {
				slog.Error("TG→MAX edit voice replacement failed", "err", err, "tgChat", msg.Chat.ID)
				return
			}
			m.AddAudio(uploaded)
		default:
			slog.Error("TG→MAX unsupported media replacement", "kind", state.Kind, "tgChat", msg.Chat.ID)
			return
		}
	}
	if len(states) == 0 {
		slog.Error("TG→MAX empty media replacement", "tgChat", msg.Chat.ID)
		return
	}
	if err := b.maxClientFor(ctx, maxChatID).Messages.EditMessage(ctx, maxMsgID, m); err != nil {
		slog.Error("TG→MAX crosspost media edit failed", "err", err, "tgChat", msg.Chat.ID, "maxMsgID", maxMsgID)
		return
	}
	for _, state := range states {
		b.repo.SaveTgMediaState(msg.Chat.ID, state)
	}
	slog.Info("TG→MAX crosspost media replaced", "mid", maxMsgID, "items", len(states),
		"tgChat", msg.Chat.ID, "tgMsg", msg.MessageID)
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
