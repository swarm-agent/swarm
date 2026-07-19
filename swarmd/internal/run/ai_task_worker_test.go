package run

import (
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBuildAITaskPreparationPromptIncludesFullAuthorizedOriginConversation(t *testing.T) {
	svc, sessions, cleanup := newPlanManageTestService(t)
	defer cleanup()

	origin, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "origin-ai-task", UserID: "user-origin", AccountScopeID: "account-origin",
		Title: "Origin", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "off"},
	})
	if err != nil {
		t.Fatalf("create origin session: %v", err)
	}
	for _, message := range []struct{ role, content string }{{"user", "First request"}, {"assistant", "First answer"}, {"tool", "Tool result"}, {"user", "Latest detail"}} {
		if _, _, _, err := sessions.AppendMessage(origin.ID, message.role, message.content, nil); err != nil {
			t.Fatalf("append origin message: %v", err)
		}
	}
	before, err := sessions.ListSessionMessages(origin.ID, 0, 100)
	if err != nil {
		t.Fatalf("list origin before assembly: %v", err)
	}

	prompt, err := svc.buildAITaskPreparationPrompt(pebblestore.WorkspaceTodoItem{
		OriginSessionID: origin.ID, AccountScopeID: origin.AccountScopeID, UserID: origin.UserID, WorkspacePath: origin.WorkspacePath, AIRequest: "  Implement the queued change  ",
	})
	if err != nil {
		t.Fatalf("build preparation prompt: %v", err)
	}
	want := aiTaskOriginContextOpen + "\n\n[user]\nFirst request\n\n[assistant]\nFirst answer\n\n[tool]\nTool result\n\n[user]\nLatest detail\n\n" + aiTaskOriginContextEnd + "\n\n" + aiTaskRequestOpen + "\nImplement the queued change\n" + aiTaskRequestEnd
	if prompt != want {
		t.Fatalf("preparation prompt mismatch\nwant:\n%s\n\ngot:\n%s", want, prompt)
	}
	if strings.Index(prompt, "First request") > strings.Index(prompt, "Latest detail") || strings.Index(prompt, aiTaskOriginContextEnd) > strings.Index(prompt, aiTaskRequestOpen) {
		t.Fatalf("preparation prompt ordering is not deterministic: %s", prompt)
	}

	after, err := sessions.ListSessionMessages(origin.ID, 0, 100)
	if err != nil {
		t.Fatalf("list origin after assembly: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("origin message count changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Role != after[i].Role || before[i].Content != after[i].Content || before[i].GlobalSeq != after[i].GlobalSeq {
			t.Fatalf("origin message %d mutated: before=%#v after=%#v", i, before[i], after[i])
		}
	}
}

func TestListAllAITaskOriginMessagesPaginatesEntireDurableTranscript(t *testing.T) {
	svc, sessions, cleanup := newPlanManageTestService(t)
	defer cleanup()

	origin, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "paged-origin-ai-task", UserID: "user-origin", AccountScopeID: "account-origin",
		Title: "Origin", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "off"},
	})
	if err != nil {
		t.Fatalf("create paged origin: %v", err)
	}
	for i := 0; i < aiTaskMessagePageLimit+1; i++ {
		if _, _, _, err := sessions.AppendMessage(origin.ID, "user", "message", nil); err != nil {
			t.Fatalf("append paged origin message %d: %v", i, err)
		}
	}

	messages, err := svc.listAllAITaskOriginMessages(origin.ID)
	if err != nil {
		t.Fatalf("list entire origin transcript: %v", err)
	}
	if len(messages) != aiTaskMessagePageLimit+1 {
		t.Fatalf("origin transcript length = %d, want %d", len(messages), aiTaskMessagePageLimit+1)
	}
	for i := 1; i < len(messages); i++ {
		if messages[i].GlobalSeq <= messages[i-1].GlobalSeq {
			t.Fatalf("origin transcript order did not advance at %d: %#v then %#v", i, messages[i-1], messages[i])
		}
	}
}

func TestBuildAITaskPreparationPromptStandaloneAndAuthorization(t *testing.T) {
	svc, sessions, cleanup := newPlanManageTestService(t)
	defer cleanup()

	standalone, err := svc.buildAITaskPreparationPrompt(pebblestore.WorkspaceTodoItem{AIRequest: "  Standalone task  "})
	if err != nil || standalone != "Standalone task" {
		t.Fatalf("standalone prompt = %q, err=%v", standalone, err)
	}

	origin, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "foreign-origin-ai-task", UserID: "user-owner", AccountScopeID: "account-owner",
		Title: "Origin", WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "test", Model: "test", Thinking: "off"},
	})
	if err != nil {
		t.Fatalf("create foreign origin: %v", err)
	}
	_, err = svc.buildAITaskPreparationPrompt(pebblestore.WorkspaceTodoItem{OriginSessionID: origin.ID, AccountScopeID: origin.AccountScopeID, UserID: "different-user", WorkspacePath: origin.WorkspacePath, AIRequest: "task"})
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("authorization error = %v", err)
	}
}
