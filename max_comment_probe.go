package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxCommentProbeTrackedTTL = 30 * time.Minute
	maxCommentProbeMaxTracked = 64
	maxCommentProbeMaxPaths   = 256
	maxCommentProbeMaxBody    = 2 << 20
	maxCommentProbeMaxDepth   = 32
	maxCommentProbeMaxPathLen = 256
)

// maxCommentProbe is a temporary, opt-in webhook diagnostic for native MAX
// comments. It never logs message text, names, URLs, attachments or raw JSON.
// A random marker narrows collection to one controlled test conversation.
type maxCommentProbe struct {
	marker string

	mu      sync.Mutex
	tracked map[string]time.Time
}

func newMaxCommentProbe(marker string) *maxCommentProbe {
	marker = strings.TrimSpace(marker)
	if len(marker) < 12 || len(marker) > 128 {
		marker = ""
	}
	return &maxCommentProbe{
		marker:  marker,
		tracked: make(map[string]time.Time),
	}
}

func (p *maxCommentProbe) Enabled() bool {
	return p != nil && p.marker != ""
}

func (p *maxCommentProbe) Wrap(next http.HandlerFunc) http.HandlerFunc {
	if !p.Enabled() {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			original := r.Body
			body, err := io.ReadAll(io.LimitReader(original, maxCommentProbeMaxBody+1))
			if err == nil && len(body) <= maxCommentProbeMaxBody {
				_ = original.Close()
				r.Body = io.NopCloser(bytes.NewReader(body))
				updateType := p.inspect(body)
				if updateType != "" && !knownMaxProbeUpdateType(updateType) {
					// The SDK rejects unknown update types with 400. During the
					// opt-in probe we acknowledge them after structural capture,
					// otherwise MAX retries the same event indefinitely.
					w.WriteHeader(http.StatusOK)
					return
				}
			} else {
				// Preserve bytes already consumed before a read error or the probe
				// limit, so the normal SDK handler still sees the original body.
				r.Body = &probeReplayBody{
					Reader: io.MultiReader(bytes.NewReader(body), original),
					Closer: original,
				}
			}
		}
		next(w, r)
	}
}

type probeReplayBody struct {
	io.Reader
	io.Closer
}

type maxCommentProbeSummary struct {
	UpdateType string
	MatchedBy  string
	Paths      []string
	IDs        map[string]string
}

func (p *maxCommentProbe) inspect(body []byte) string {
	var root any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&root) != nil {
		return ""
	}

	summary := summarizeMaxCommentProbe(root)
	markerMatch := p.marker != "" && jsonContainsProbeMarker(root, p.marker, 0)
	trackedMatch := p.matchesTracked(summary.IDs)
	if !markerMatch && !trackedMatch {
		return summary.UpdateType
	}
	if markerMatch {
		summary.MatchedBy = "marker"
	} else {
		summary.MatchedBy = "tracked_id"
	}
	p.track(summary.IDs)

	idsJSON, _ := json.Marshal(summary.IDs)
	slog.Info("MAX native-comment probe",
		"updateType", summary.UpdateType,
		"matchedBy", summary.MatchedBy,
		"paths", strings.Join(summary.Paths, ","),
		"ids", string(idsJSON))
	return summary.UpdateType
}

func knownMaxProbeUpdateType(updateType string) bool {
	switch updateType {
	case "message_created", "message_edited", "message_removed",
		"message_callback", "bot_added", "bot_removed", "bot_stopped",
		"dialog_removed", "dialog_cleared", "user_added", "user_removed",
		"bot_started", "chat_title_changed":
		return true
	default:
		return false
	}
}

func summarizeMaxCommentProbe(root any) maxCommentProbeSummary {
	summary := maxCommentProbeSummary{IDs: make(map[string]string)}
	pathSet := make(map[string]struct{})
	collectMaxCommentProbe(root, "", 0, pathSet, summary.IDs, &summary.UpdateType)

	summary.Paths = make([]string, 0, len(pathSet))
	for path := range pathSet {
		summary.Paths = append(summary.Paths, path)
	}
	sort.Strings(summary.Paths)
	if len(summary.Paths) > maxCommentProbeMaxPaths {
		summary.Paths = summary.Paths[:maxCommentProbeMaxPaths]
	}
	return summary
}

func collectMaxCommentProbe(value any, path string, depth int, paths map[string]struct{}, ids map[string]string, updateType *string) {
	if depth >= maxCommentProbeMaxDepth {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if len(childPath) > maxCommentProbeMaxPathLen {
				childPath = childPath[:maxCommentProbeMaxPathLen]
			}
			child := typed[key]
			paths[childPath+":"+probeJSONType(child)] = struct{}{}
			if scalar, ok := probeSafeScalar(child); ok && isProbeSafeValueKey(key) {
				ids[childPath] = scalar
				if key == "update_type" {
					*updateType = scalar
				}
			}
			collectMaxCommentProbe(child, childPath, depth+1, paths, ids, updateType)
		}
	case []any:
		for _, child := range typed {
			childPath := path + "[]"
			paths[childPath+":"+probeJSONType(child)] = struct{}{}
			collectMaxCommentProbe(child, childPath, depth+1, paths, ids, updateType)
		}
	}
}

func probeJSONType(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case json.Number, float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

func probeSafeScalar(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		if len(typed) > 256 {
			return "", false
		}
		return typed, true
	case json.Number:
		return typed.String(), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return "", false
	}
}

func isProbeSafeValueKey(key string) bool {
	key = strings.ToLower(key)
	if strings.Contains(key, "token") || strings.Contains(key, "secret") ||
		strings.Contains(key, "url") || strings.Contains(key, "text") ||
		strings.Contains(key, "name") {
		return false
	}
	switch key {
	case "update_type", "chat_type", "mid", "seq", "reply_to", "type":
		return true
	}
	return strings.HasSuffix(key, "_id") ||
		strings.Contains(key, "post") ||
		strings.Contains(key, "thread") ||
		strings.Contains(key, "comment")
}

func jsonContainsProbeMarker(value any, marker string, depth int) bool {
	if depth >= maxCommentProbeMaxDepth {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.EqualFold(key, "text") {
				if text, ok := child.(string); ok && strings.Contains(text, marker) {
					return true
				}
			}
			if jsonContainsProbeMarker(child, marker, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if jsonContainsProbeMarker(child, marker, depth+1) {
				return true
			}
		}
	}
	return false
}

func (p *maxCommentProbe) matchesTracked(ids map[string]string) bool {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked(now)
	for path, value := range ids {
		if !isProbeCorrelationPath(path) {
			continue
		}
		if _, ok := p.tracked[value]; ok {
			return true
		}
	}
	return false
}

func (p *maxCommentProbe) track(ids map[string]string) {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked(now)
	for path, value := range ids {
		if value != "" && isProbeCorrelationPath(path) {
			p.tracked[value] = now
		}
	}
	for len(p.tracked) > maxCommentProbeMaxTracked {
		var oldestKey string
		var oldestTime time.Time
		for key, created := range p.tracked {
			if oldestKey == "" || created.Before(oldestTime) {
				oldestKey, oldestTime = key, created
			}
		}
		delete(p.tracked, oldestKey)
	}
}

func (p *maxCommentProbe) expireLocked(now time.Time) {
	for key, created := range p.tracked {
		if now.Sub(created) > maxCommentProbeTrackedTTL {
			delete(p.tracked, key)
		}
	}
}

func isProbeCorrelationPath(path string) bool {
	key := path
	if idx := strings.LastIndexByte(path, '.'); idx >= 0 {
		key = path[idx+1:]
	}
	key = strings.ToLower(key)
	switch key {
	case "mid", "message_id", "reply_to", "post_id", "thread_id", "comment_id":
		return true
	default:
		return false
	}
}
