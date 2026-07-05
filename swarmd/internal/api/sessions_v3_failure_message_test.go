package api

import (
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestRecordRunFailureSystemMessageSanitizesAndPersistsUserVisibleMessage(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	sessions := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessions, events)
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, nil, events, stream.NewHub(events))
	principal := accountTestPrincipal()
	created, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-provider-failure-message",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Title:          "Provider failure",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Preference: &pebblestore.ModelPreference{
			Provider: "anthropic",
			Model:    "claude-sonnet-5",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	exec := &sessionV3Executor{server: server}
	reason := `POST "https://api.anthropic.com/v1/messages?api_key=anthropic-test-secret": 400 Bad Request {"type":"error","error":{"type":"invalid_request_error","message":"\"thinking.type.enabled\" is not supported for this model."},"api_key":"anthropic-test-secret"}`
	if _, err := exec.recordRunFailureSystemMessage(sessionV3ExecutorJob{Principal: principal, SessionID: created.ID, RunID: "run-failed-1"}, reason); err != nil {
		t.Fatalf("record failure message: %v", err)
	}

	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	message := messages[0]
	if message.Role != "system" {
		t.Fatalf("message role = %q, want system", message.Role)
	}
	if !strings.Contains(message.Content, "[run-failed]") || !strings.Contains(message.Content, "thinking.type.enabled") {
		t.Fatalf("failure message missing provider detail: %q", message.Content)
	}
	if strings.Contains(message.Content, "anthropic-test-secret") || strings.Contains(message.Content, "api_key=anthropic-test-secret") {
		t.Fatalf("failure message leaked API key: %q", message.Content)
	}
}
