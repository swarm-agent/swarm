package session

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSetModeToPlanAppendsSystemReentryNotification(t *testing.T) {
	svc, cleanup := newModeReentryTestService(t)
	defer cleanup()
	sessionID := createModeReentryTestSession(t, svc, ModeAuto)

	updated, event, err := svc.SetMode(sessionID, ModePlan)
	if err != nil {
		t.Fatalf("set mode to plan: %v", err)
	}
	if event == nil {
		t.Fatal("expected mode update event")
	}
	if updated.Mode != ModePlan {
		t.Fatalf("updated mode = %q, want %q", updated.Mode, ModePlan)
	}
	if updated.MessageCount != 1 {
		t.Fatalf("message count = %d, want 1", updated.MessageCount)
	}

	messages, err := svc.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	assertModeTransitionMessage(t, messages[0], planModeReentrySystemMessage, ModeAuto, ModePlan)
	if messages[0].GlobalSeq <= event.GlobalSeq {
		t.Fatalf("notification seq = %d, want after mode update seq %d", messages[0].GlobalSeq, event.GlobalSeq)
	}

	if _, _, _, err := svc.AppendMessage(sessionID, "user", "next turn", nil); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	messages, err = svc.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after user append: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("message count after user append = %d, want 2", len(messages))
	}
	assertModeTransitionMessage(t, messages[0], planModeReentrySystemMessage, ModeAuto, ModePlan)
	if messages[1].Role != "user" || messages[1].Content != "next turn" {
		t.Fatalf("second message = (%q, %q), want user next turn", messages[1].Role, messages[1].Content)
	}
}

func TestSetModeToPlanDoesNotDuplicateWhenAlreadyPlan(t *testing.T) {
	svc, cleanup := newModeReentryTestService(t)
	defer cleanup()
	sessionID := createModeReentryTestSession(t, svc, ModeAuto)

	if _, _, err := svc.SetMode(sessionID, ModePlan); err != nil {
		t.Fatalf("set mode to plan: %v", err)
	}
	if _, event, err := svc.SetMode(sessionID, ModePlan); err != nil {
		t.Fatalf("set mode to same plan: %v", err)
	} else if event != nil {
		t.Fatalf("same-mode set returned event: %#v", event)
	}

	messages, err := svc.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want exactly one plan notification", len(messages))
	}
	assertModeTransitionMessage(t, messages[0], planModeReentrySystemMessage, ModeAuto, ModePlan)
}

func TestSetModeFromPlanToAutoAppendsSystemReentryNotification(t *testing.T) {
	svc, cleanup := newModeReentryTestService(t)
	defer cleanup()
	sessionID := createModeReentryTestSession(t, svc, ModePlan)

	updated, event, err := svc.SetMode(sessionID, ModeAuto)
	if err != nil {
		t.Fatalf("set mode to auto: %v", err)
	}
	if event == nil {
		t.Fatal("expected mode update event")
	}
	if updated.Mode != ModeAuto {
		t.Fatalf("updated mode = %q, want %q", updated.Mode, ModeAuto)
	}
	if updated.MessageCount != 1 {
		t.Fatalf("message count = %d, want 1", updated.MessageCount)
	}

	messages, err := svc.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	assertModeTransitionMessage(t, messages[0], autoModeReentrySystemMessage, ModePlan, ModeAuto)
	if messages[0].GlobalSeq <= event.GlobalSeq {
		t.Fatalf("notification seq = %d, want after mode update seq %d", messages[0].GlobalSeq, event.GlobalSeq)
	}

	if _, _, _, err := svc.AppendMessage(sessionID, "user", "next auto turn", nil); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	messages, err = svc.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after user append: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("message count after user append = %d, want 2", len(messages))
	}
	assertModeTransitionMessage(t, messages[0], autoModeReentrySystemMessage, ModePlan, ModeAuto)
	if messages[1].Role != "user" || messages[1].Content != "next auto turn" {
		t.Fatalf("second message = (%q, %q), want user next auto turn", messages[1].Role, messages[1].Content)
	}
}

func TestSetModeToAutoDoesNotDuplicateWhenAlreadyAuto(t *testing.T) {
	svc, cleanup := newModeReentryTestService(t)
	defer cleanup()
	sessionID := createModeReentryTestSession(t, svc, ModePlan)

	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set mode to auto: %v", err)
	}
	if _, event, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set mode to same auto: %v", err)
	} else if event != nil {
		t.Fatalf("same-mode set returned event: %#v", event)
	}

	messages, err := svc.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want exactly one auto notification", len(messages))
	}
	assertModeTransitionMessage(t, messages[0], autoModeReentrySystemMessage, ModePlan, ModeAuto)
}

func assertModeTransitionMessage(t *testing.T, message pebblestore.MessageSnapshot, content, previousMode, mode string) {
	t.Helper()
	if message.Role != "system" {
		t.Fatalf("notification role = %q, want system", message.Role)
	}
	if message.Content != content {
		t.Fatalf("notification content = %q, want %q", message.Content, content)
	}
	if got := message.Metadata["source"]; got != "session_mode_transition" {
		t.Fatalf("notification source metadata = %#v, want session_mode_transition", got)
	}
	if got := message.Metadata["previous_mode"]; got != previousMode {
		t.Fatalf("notification previous_mode metadata = %#v, want %q", got, previousMode)
	}
	if got := message.Metadata["mode"]; got != mode {
		t.Fatalf("notification mode metadata = %#v, want %q", got, mode)
	}
}

func newModeReentryTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	sessions := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("open event log: %v", err)
	}
	return NewService(sessions, events), func() { _ = store.Close() }
}

func createModeReentryTestSession(t *testing.T, svc *Service, mode string) string {
	t.Helper()
	snapshot, _, err := svc.CreateSessionWithOptions(CreateSessionOptions{
		SessionID:     "session-mode-reentry-test",
		Title:         "Mode Reentry Test",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Mode:          mode,
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return snapshot.ID
}
