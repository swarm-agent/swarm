package api

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	// sessionV3TitleSourceFirstMessage marks a provisional title taken from the
	// first user message. It is not locked: the Compact/Router title flow may
	// still replace it, while a manual rename (title_source=manual) locks it.
	sessionV3TitleSourceFirstMessage   = "first_message"
	sessionV3FirstMessageTitleMaxRunes = 60
	// A word-boundary cut is used only when it keeps at least this many runes.
	sessionV3FirstMessageTitleMinCutRunes = 30
)

// sessionV3PlaceholderTitles are titles clients send when they have no real
// one. Earlier SDK releases sent "Chat Session" and "Orchestrator AI" by default;
// treating them as placeholders lets those conversations still be titled.
var sessionV3PlaceholderTitles = []string{
	sessionV3TitleDefault,
	"Automation conversation",
	"Automation session",
	"Chat Session",
	"Orchestrator AI",
	"New conversation",
}

func sessionV3TitleIsPlaceholder(title string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return true
	}
	for _, placeholder := range sessionV3PlaceholderTitles {
		if strings.EqualFold(title, placeholder) {
			return true
		}
	}
	return false
}

// firstMessageSessionTitle derives a short, deterministic title from a user
// message: its first non-empty line, leading Markdown markers removed,
// whitespace collapsed and cut to sessionV3FirstMessageTitleMaxRunes at a word
// boundary when possible. No model is involved.
func firstMessageSessionTitle(content string) string {
	line := ""
	for _, candidate := range strings.Split(content, "\n") {
		candidate = strings.TrimLeft(strings.TrimSpace(candidate), "#>*-+ \t")
		if candidate = strings.Join(strings.Fields(candidate), " "); candidate != "" {
			line = candidate
			break
		}
	}
	runes := []rune(line)
	if len(runes) <= sessionV3FirstMessageTitleMaxRunes {
		return line
	}
	cut := string(runes[:sessionV3FirstMessageTitleMaxRunes])
	if space := strings.LastIndex(cut, " "); space > 0 && len([]rune(cut[:space])) >= sessionV3FirstMessageTitleMinCutRunes {
		cut = cut[:space]
	}
	return strings.TrimRight(cut, " ,;:.-") + "…"
}

func sessionV3FirstUserMessageContent(messages []pebblestore.MessageSnapshot) string {
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			return message.Content
		}
	}
	return ""
}

// applySessionV3FirstMessageTitle gives a still-untitled session an immediate
// title from its first user message, through the canonical V3 title mutation.
// The caller has already checked that title generation is allowed (not locked,
// not a delegated/background session). Client-given titles are never replaced.
func (e *sessionV3Executor) applySessionV3FirstMessageTitle(job sessionV3ExecutorJob, session pebblestore.SessionSnapshot, messages []pebblestore.MessageSnapshot) {
	if e == nil || e.server == nil || !sessionV3TitleIsPlaceholder(session.Title) {
		return
	}
	title := firstMessageSessionTitle(sessionV3FirstUserMessageContent(messages))
	if title == "" {
		return
	}
	// Re-read right before the snapshot write so concurrent session updates
	// (for example a rename) are not overwritten by an older snapshot.
	current, ok, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil || !ok || !shouldGenerateSessionV3Title(current) || !sessionV3TitleIsPlaceholder(current.Title) {
		return
	}
	session = current
	now := time.Now().UnixMilli()
	next := session
	next.Title = title
	next.UpdatedAt = now
	next.Metadata = cloneSessionsV3Metadata(session.Metadata)
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata["title_source"] = sessionV3TitleSourceFirstMessage
	payload, err := json.Marshal(map[string]any{
		"session_id": job.SessionID,
		"title":      title,
		"stage":      "provisional",
		"updated_at": now,
		"session":    next,
	})
	if err != nil {
		log.Printf("warning: marshal v3 first-message title for session %q: %v", job.SessionID, err)
		return
	}
	payloadHash, err := sessionV3TitlePayloadHash(job.SessionID, job.RunID, title)
	if err != nil {
		log.Printf("warning: hash v3 first-message title for session %q: %v", job.SessionID, err)
		return
	}
	clientRequestID := sessionV3ExecutorClientRequestID("session.title.first_message", job.RunID)
	if _, err := e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       job.SessionID,
		UserID:          job.Principal.UserID,
		AccountScopeID:  job.Principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateTitle,
		EventType:       "session.title.updated",
		EventPayload:    payload,
		Session:         &next,
		NowUnixMs:       now,
	}); err != nil {
		log.Printf("warning: apply v3 first-message title for session %q: %v", job.SessionID, err)
	}
}
