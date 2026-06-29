package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type chatPendingHydrationBackendStub struct {
	recent         []ChatPermissionRecord
	pending        []ChatPermissionRecord
	listCalls      int
	listLimit      int
	pendingCalls   int
	pendingLimit   int
	permissionsErr error
	pendingErr     error
}

func (s *chatPendingHydrationBackendStub) LoadMessages(context.Context, string, uint64, int) ([]ChatMessageRecord, error) {
	return nil, nil
}

func (s *chatPendingHydrationBackendStub) GetSessionUsageSummary(context.Context, string) (*ChatUsageSummary, error) {
	return nil, nil
}

func (s *chatPendingHydrationBackendStub) GetSessionMode(context.Context, string) (string, error) {
	return "auto", nil
}

func (s *chatPendingHydrationBackendStub) SetSessionMode(context.Context, string, string) (string, error) {
	return "auto", nil
}

func (s *chatPendingHydrationBackendStub) GetSessionPreference(context.Context, string) (string, string, string, string, string, int, error) {
	return "", "", "", "", "", 0, nil
}

func (s *chatPendingHydrationBackendStub) SetSessionPreference(context.Context, string, string, string, string, string, string) (string, string, string, string, string, int, error) {
	return "", "", "", "", "", 0, nil
}

func (s *chatPendingHydrationBackendStub) GetActiveSessionPlan(context.Context, string) (ChatSessionPlan, bool, error) {
	return ChatSessionPlan{}, false, nil
}

func (s *chatPendingHydrationBackendStub) SaveSessionPlan(_ context.Context, _ string, plan ChatSessionPlan) (ChatSessionPlan, error) {
	return plan, nil
}

func (s *chatPendingHydrationBackendStub) ListPermissions(_ context.Context, _ string, limit int) ([]ChatPermissionRecord, error) {
	s.listCalls++
	s.listLimit = limit
	return append([]ChatPermissionRecord(nil), s.recent...), s.permissionsErr
}

func (s *chatPendingHydrationBackendStub) ListPendingPermissions(_ context.Context, _ string, limit int) ([]ChatPermissionRecord, error) {
	s.pendingCalls++
	s.pendingLimit = limit
	return append([]ChatPermissionRecord(nil), s.pending...), s.pendingErr
}

func (s *chatPendingHydrationBackendStub) ResolvePermission(context.Context, string, string, string, string) (ChatPermissionRecord, error) {
	return ChatPermissionRecord{}, nil
}

func (s *chatPendingHydrationBackendStub) ResolvePermissionWithArguments(context.Context, string, string, string, string, string) (ChatPermissionRecord, error) {
	return ChatPermissionRecord{}, nil
}

func (s *chatPendingHydrationBackendStub) ResolveAllPermissions(context.Context, string, string, string) ([]ChatPermissionRecord, error) {
	return nil, nil
}

func (s *chatPendingHydrationBackendStub) GetPermissionPolicy(context.Context) (ChatPermissionPolicy, error) {
	return ChatPermissionPolicy{}, nil
}

func (s *chatPendingHydrationBackendStub) AddPermissionRule(context.Context, ChatPermissionRule) (ChatPermissionRule, error) {
	return ChatPermissionRule{}, nil
}

func (s *chatPendingHydrationBackendStub) RemovePermissionRule(context.Context, string) (bool, error) {
	return false, nil
}

func (s *chatPendingHydrationBackendStub) ResetPermissionPolicy(context.Context) (ChatPermissionPolicy, error) {
	return ChatPermissionPolicy{}, nil
}

func (s *chatPendingHydrationBackendStub) ExplainPermission(context.Context, string, string, string) (ChatPermissionExplain, error) {
	return ChatPermissionExplain{}, nil
}

func (s *chatPendingHydrationBackendStub) StopRun(context.Context, string, string) error {
	return nil
}

func TestChatPageInitialHydrationMergesDedicatedPendingPermissionsIntoConversation(t *testing.T) {
	base := time.Now().Add(-10 * time.Minute).UnixMilli()
	recent := make([]ChatPermissionRecord, 0, 200)
	for i := 0; i < 200; i++ {
		recent = append(recent, ChatPermissionRecord{
			ID:                    fmt.Sprintf("resolved-%03d", i),
			SessionID:             "session-1",
			ToolName:              "read",
			CallID:                fmt.Sprintf("call-resolved-%03d", i),
			Status:                "approved",
			ExecutionStatus:       "completed",
			PermissionRequestedAt: base + int64(i),
			CreatedAt:             base + int64(i),
			ResolvedAt:            base + int64(i) + 1,
		})
	}
	pending := ChatPermissionRecord{
		ID:                    "pending-critical",
		SessionID:             "session-1",
		RunID:                 "run-1",
		CallID:                "call-pending-critical",
		ToolName:              "bash",
		ToolArguments:         `{"command":"deploy production"}`,
		Status:                "pending",
		ExecutionStatus:       "waiting_approval",
		PermissionRequestedAt: base - 1000,
		CreatedAt:             base - 1000,
	}
	backend := &chatPendingHydrationBackendStub{
		recent:  recent,
		pending: []ChatPermissionRecord{pending},
	}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		AuthConfigured: true,
		SessionMode:    "auto",
	})

	deadline := time.Now().Add(2 * time.Second)
	for page.permissionsLoading && time.Now().Before(deadline) {
		page.drainPermissionLoads()
		time.Sleep(5 * time.Millisecond)
	}
	if page.permissionsLoading {
		t.Fatalf("permissions never loaded")
	}
	page.drainPermissionLoads()

	if backend.listCalls != 1 {
		t.Fatalf("ListPermissions calls = %d, want 1", backend.listCalls)
	}
	if backend.pendingCalls != 1 {
		t.Fatalf("ListPendingPermissions calls = %d, want 1", backend.pendingCalls)
	}
	if backend.pendingLimit != 200 {
		t.Fatalf("ListPendingPermissions limit = %d, want 200", backend.pendingLimit)
	}
	if len(page.pendingPerms) != 1 {
		t.Fatalf("pendingPerms len = %d, want 1", len(page.pendingPerms))
	}
	if page.pendingPerms[0].ID != pending.ID {
		t.Fatalf("pendingPerms[0].ID = %q, want %q", page.pendingPerms[0].ID, pending.ID)
	}

	foundTimeline := false
	for _, item := range page.timeline {
		if strings.Contains(item.Text, "deploy production") || strings.Contains(item.Text, "bash") {
			foundTimeline = true
			break
		}
	}
	if !foundTimeline {
		t.Fatalf("pending permission was not rebuilt into the conversation timeline; timeline=%#v", page.timeline)
	}
}

func TestChatPageInitialHydrationUsesPendingPermissionsWhenHistoryFails(t *testing.T) {
	now := time.Now().UnixMilli()
	pending := ChatPermissionRecord{
		ID:                    "pending-critical",
		SessionID:             "session-1",
		CallID:                "call-pending-critical",
		ToolName:              "bash",
		ToolArguments:         `{"command":"approve me"}`,
		Status:                "pending",
		ExecutionStatus:       "waiting_approval",
		PermissionRequestedAt: now,
		CreatedAt:             now,
	}
	backend := &chatPendingHydrationBackendStub{
		pending:        []ChatPermissionRecord{pending},
		permissionsErr: errors.New("history unavailable"),
	}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		AuthConfigured: true,
		SessionMode:    "auto",
	})

	deadline := time.Now().Add(2 * time.Second)
	for page.permissionsLoading && time.Now().Before(deadline) {
		page.drainPermissionLoads()
		time.Sleep(5 * time.Millisecond)
	}
	if page.permissionsLoading {
		t.Fatalf("permissions never loaded")
	}
	if len(page.pendingPerms) != 1 || page.pendingPerms[0].ID != pending.ID {
		t.Fatalf("pendingPerms = %#v, want dedicated pending permission", page.pendingPerms)
	}
	if strings.Contains(page.statusLine, "unavailable") {
		t.Fatalf("statusLine = %q, should not mark permissions unavailable when dedicated pending load succeeded", page.statusLine)
	}
}
