package ui

import (
	"strings"
	"testing"
)

func TestChatPermissionRealtimeUpdateDismissesResolvedSpecialModal(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "plan", AuthConfigured: true})
	requested := ChatPermissionRecord{
		ID:        "perm-exit",
		SessionID: "session-1",
		RunID:     "run-1",
		ToolName:  "exit_plan_mode",
		Status:    "pending",
		CreatedAt: 10,
		UpdatedAt: 10,
	}

	if !page.ApplySharedStreamEvent(ChatRunStreamEvent{Type: "permission.requested", SessionID: "session-1", RunID: "run-1", Permission: &requested}, 10) {
		t.Fatal("permission.requested was not applied")
	}
	if !page.planExitModalActive() {
		t.Fatal("exit plan permission modal did not open for pending realtime permission")
	}
	if len(page.pendingPerms) != 1 || page.pendingPerms[0].ID != requested.ID {
		t.Fatalf("pending permissions = %#v, want requested permission", page.pendingPerms)
	}

	updated := requested
	updated.Status = "approved"
	updated.Decision = "allow_once"
	updated.ResolvedAt = 20
	updated.UpdatedAt = 20
	if !page.ApplySharedStreamEvent(ChatRunStreamEvent{Type: "permission.updated", SessionID: "session-1", RunID: "run-1", Permission: &updated}, 20) {
		t.Fatal("permission.updated was not applied")
	}
	if page.planExitModalActive() {
		t.Fatal("resolved permission did not dismiss exit plan permission modal")
	}
	if len(page.pendingPerms) != 0 {
		t.Fatalf("pending permissions = %#v, want none after resolved update", page.pendingPerms)
	}
	if got := page.statusLine; got != "exit plan mode approved" {
		t.Fatalf("statusLine = %q, want exit plan mode approved", got)
	}
}

func TestChatRealtimeSessionModeUpdateSwitchesFooterOutOfPlanMode(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		SessionMode:    "plan",
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Agent:                 "swarm",
			AgentExecutionSetting: "readwrite",
			AgentExitPlanMode:     true,
			AgentRuntimeKnown:     true,
		},
	})
	if got := page.footerSettingsLine(1000); !strings.Contains(got, "plan") {
		t.Fatalf("footer before mode update = %q, want plan", got)
	}

	if !page.ApplySharedStreamEvent(ChatRunStreamEvent{Type: "session.mode.updated", SessionID: "session-1", SessionMode: "auto"}, 20) {
		t.Fatal("session.mode.updated was not applied")
	}

	if got := page.SessionMode(); got != "auto" {
		t.Fatalf("SessionMode = %q, want auto", got)
	}
	if got := page.footerSettingsLine(1000); !strings.Contains(got, "auto") || strings.Contains(got, "readwrite") {
		t.Fatalf("footer after mode update = %q, want auto and no stale direct runtime mode", got)
	}
}

func TestChatPermissionRealtimeUpdateDismissesGenericModal(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
	requested := ChatPermissionRecord{
		ID:        "perm-tool",
		SessionID: "session-1",
		RunID:     "run-1",
		ToolName:  "bash",
		Status:    "pending",
		CreatedAt: 10,
		UpdatedAt: 10,
	}

	page.ApplySharedStreamEvent(ChatRunStreamEvent{Type: "permission.requested", SessionID: "session-1", RunID: "run-1", Permission: &requested}, 10)
	if !page.PermissionModalVisible() {
		t.Fatal("generic permission modal did not open for pending realtime permission")
	}

	updated := requested
	updated.Status = "denied"
	updated.Decision = "deny_once"
	updated.ResolvedAt = 20
	updated.UpdatedAt = 20
	page.ApplySharedStreamEvent(ChatRunStreamEvent{Type: "permission.updated", SessionID: "session-1", RunID: "run-1", Permission: &updated}, 20)
	if page.PermissionModalVisible() {
		t.Fatal("resolved generic permission modal stayed visible")
	}
	if len(page.pendingPerms) != 0 {
		t.Fatalf("pending permissions = %#v, want none after denied update", page.pendingPerms)
	}
}
