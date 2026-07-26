package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

type chatMentionBackendStub struct {
	lastReq ChatRunRequest
}

func (s *chatMentionBackendStub) LoadMessages(context.Context, string, uint64, int) ([]ChatMessageRecord, error) {
	return nil, nil
}

func (s *chatMentionBackendStub) GetSessionUsageSummary(context.Context, string) (*ChatUsageSummary, error) {
	return nil, nil
}

func (s *chatMentionBackendStub) GetSessionMode(context.Context, string) (string, error) {
	return "auto", nil
}

func (s *chatMentionBackendStub) SetSessionMode(context.Context, string, string) (string, error) {
	return "auto", nil
}

func (s *chatMentionBackendStub) GetSessionPreference(context.Context, string) (string, string, string, string, string, int, error) {
	return "", "", "", "", "", 0, nil
}

func (s *chatMentionBackendStub) SetSessionPreference(context.Context, string, string, string, string, string, string) (string, string, string, string, string, int, error) {
	return "", "", "", "", "", 0, nil
}

func (s *chatMentionBackendStub) GetActiveSessionPlan(context.Context, string) (ChatSessionPlan, bool, error) {
	return ChatSessionPlan{}, false, nil
}

func (s *chatMentionBackendStub) SaveSessionPlan(context.Context, string, ChatSessionPlan) (ChatSessionPlan, error) {
	return ChatSessionPlan{}, nil
}

func (s *chatMentionBackendStub) ListPermissions(context.Context, string, int) ([]ChatPermissionRecord, error) {
	return nil, nil
}

func (s *chatMentionBackendStub) ListPendingPermissions(context.Context, string, int) ([]ChatPermissionRecord, error) {
	return nil, nil
}

func (s *chatMentionBackendStub) ResolvePermission(context.Context, string, string, string, string) (ChatPermissionRecord, error) {
	return ChatPermissionRecord{}, nil
}

func (s *chatMentionBackendStub) ResolvePermissionWithArguments(context.Context, string, string, string, string, string) (ChatPermissionRecord, error) {
	return ChatPermissionRecord{}, nil
}

func (s *chatMentionBackendStub) ResolveAllPermissions(context.Context, string, string, string) ([]ChatPermissionRecord, error) {
	return nil, nil
}

func (s *chatMentionBackendStub) GetPermissionPolicy(context.Context) (ChatPermissionPolicy, error) {
	return ChatPermissionPolicy{}, nil
}

func (s *chatMentionBackendStub) AddPermissionRule(context.Context, ChatPermissionRule) (ChatPermissionRule, error) {
	return ChatPermissionRule{}, nil
}

func (s *chatMentionBackendStub) RemovePermissionRule(context.Context, string) (bool, error) {
	return false, nil
}

func (s *chatMentionBackendStub) ResetPermissionPolicy(context.Context) (ChatPermissionPolicy, error) {
	return ChatPermissionPolicy{}, nil
}

func (s *chatMentionBackendStub) ExplainPermission(context.Context, string, string, string) (ChatPermissionExplain, error) {
	return ChatPermissionExplain{}, nil
}

func (s *chatMentionBackendStub) StopRun(context.Context, string, string) error {
	return nil
}

func (s *chatMentionBackendStub) RunTurn(context.Context, string, ChatRunRequest) (ChatRunResponse, error) {
	return ChatRunResponse{}, nil
}

func (s *chatMentionBackendStub) RunTurnStream(_ context.Context, _ string, req ChatRunRequest, _ func(ChatRunStreamEvent)) (ChatRunResponse, error) {
	s.lastReq = req
	return ChatRunResponse{}, nil
}

func TestParseTargetedSubagentPrompt(t *testing.T) {
	subagent, task, ok := parseTargetedSubagentPrompt("@finder inspect src", []string{"memory", "finder"})
	if !ok {
		t.Fatalf("expected targeted subagent prompt to parse")
	}
	if subagent != "finder" {
		t.Fatalf("subagent = %q, want finder", subagent)
	}
	if task != "inspect src" {
		t.Fatalf("task = %q, want inspect src", task)
	}
}

func TestChatMentionPaletteCompletesSubagent(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", AuthConfigured: true, Meta: ChatSessionMeta{Subagents: []string{"memory", "finder"}}})
	page.input = "@ex"
	if !page.mentionPaletteActive() {
		t.Fatalf("mentionPaletteActive() = false, want true")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	if got := page.input; got != "@finder " {
		t.Fatalf("input = %q, want @finder ", got)
	}
}

func TestChatMentionCandidatesSorted(t *testing.T) {
	got := chatMentionCandidates("", []string{"memory", "finder", "clone"})
	want := []string{"clone", "finder", "memory"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("chatMentionCandidates() = %v, want %v", got, want)
	}
}

func TestChatSubmitRoutesTargetedSubagentRun(t *testing.T) {
	backend := &chatMentionBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		AuthConfigured: true,
		Meta:           ChatSessionMeta{Agent: "swarm", Subagents: []string{"memory", "finder"}},
	})
	page.input = "@finder inspect src"
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if backend.lastReq.TargetKind != "subagent" {
		t.Fatalf("TargetKind = %q, want subagent", backend.lastReq.TargetKind)
	}
	if backend.lastReq.TargetName != "finder" {
		t.Fatalf("TargetName = %q, want finder", backend.lastReq.TargetName)
	}
	if backend.lastReq.Prompt != "inspect src" {
		t.Fatalf("Prompt = %q, want inspect src", backend.lastReq.Prompt)
	}
	if backend.lastReq.AgentName != "" {
		t.Fatalf("AgentName = %q, want empty for targeted subagent", backend.lastReq.AgentName)
	}
}
