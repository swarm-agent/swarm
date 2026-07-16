package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestManageSessionsPermissionModalOpensBeforeApprovedArgumentsBackfill(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
	record := ChatPermissionRecord{
		ID: "perm-archive", SessionID: "session-1", RunID: "run-1", ToolName: "manage_sessions",
		Requirement: "session_archive", Status: "pending", ToolArguments: `{"action":"archive","sessions":[{"session_id":"child-1","title":"Child"}]}`,
	}
	page.ApplyPermissionRecords([]ChatPermissionRecord{record})
	if !page.manageSessionsPermissionModalActive() {
		t.Fatal("missing approved_arguments hid the pending manage-sessions permission")
	}
	if got := page.manageSessionsPermissionApprovedArguments(); got != "" {
		t.Fatalf("approval unexpectedly available before canonical backfill: %q", got)
	}
	record.UpdatedAt = 20
	record.ApprovedArguments = `{"action":"archive","session_ids":["child-1"]}`
	page.ApplyPermissionRecords([]ChatPermissionRecord{record})
	if got := page.manageSessionsPermissionApprovedArguments(); got == "" {
		t.Fatal("visible modal did not adopt later canonical approved arguments")
	}
}

func TestManageSessionsPermissionModalRoutesAllCanonicalRequirements(t *testing.T) {
	for _, requirement := range []string{"session_deploy", "session_commit", "session_archive", "session_unarchive"} {
		t.Run(requirement, func(t *testing.T) {
			page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
			record := manageSessionsPermissionTestRecord("perm-"+requirement, requirement)
			page.permissions = []ChatPermissionRecord{record}
			page.rebuildToolLifecycleViews()
			if !page.manageSessionsPermissionModalActive() || page.manageSessionsPermission != record.ID {
				t.Fatalf("manage-sessions modal = active %v permission %q, want %q", page.manageSessionsPermissionModalActive(), page.manageSessionsPermission, record.ID)
			}
			if page.OrdinaryPermissionComposerVisible() || page.planPermissionModalActive() {
				t.Fatal("manage-sessions permission activated another permission surface")
			}
		})
	}
}

func TestManageSessionsPermissionModalRendersActionSpecificSections(t *testing.T) {
	tests := []struct {
		requirement string
		want        []string
		notWant     []string
	}{
		{requirement: "session_deploy", want: []string{"Proposal 1: Primary work", "Prompt: Ship the primary task", "Mode: auto", "Agent: swarm primary", "Workspace: Workspace", "Worktree: Managed worktree"}, notWant: []string{`"proposals"`}},
		{requirement: "session_commit", want: []string{"Commit 1", "Message: Ship exact files", "Repository: /workspace", "Files: web/src/app.tsx"}, notWant: []string{"fingerprint", "secret"}},
		{requirement: "session_archive", want: []string{"Session 1: Review search results", "Workspace: swarm-go", "State: needs review", "Updated:"}, notWant: []string{"opaque-session-id", "expected_updated_at_by_id"}},
		{requirement: "session_unarchive", want: []string{"Session 1: Restore session", "State: archived"}, notWant: []string{"opaque-session-id"}},
	}
	for _, test := range tests {
		t.Run(test.requirement, func(t *testing.T) {
			page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
			record := manageSessionsPermissionTestRecord("perm-"+test.requirement, test.requirement)
			if !page.OpenManageSessionsPermissionModal(record) {
				t.Fatal("OpenManageSessionsPermissionModal returned false")
			}
			lines, _ := page.manageSessionsPermissionModalLines(record, 120)
			joined := renderLineTexts(lines)
			for _, want := range test.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("rendered content %q missing %q", joined, want)
				}
			}
			for _, notWant := range test.notWant {
				if strings.Contains(joined, notWant) {
					t.Fatalf("rendered content %q unexpectedly contains %q", joined, notWant)
				}
			}
		})
	}
}

func TestManageSessionsDeployDefaultsFirstValidAndOverlaysOnlySelection(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
	record := manageSessionsPermissionTestRecord("perm-deploy", "session_deploy")
	page.OpenManageSessionsPermissionModal(record)
	if got := page.manageSessionsSelectedCount(); got != 1 || len(page.manageSessionsSelected) != 2 || !page.manageSessionsSelected[0] || page.manageSessionsSelected[1] {
		t.Fatalf("default selected state = %#v count %d, want first only", page.manageSessionsSelected, got)
	}
	assertManageSessionsArguments(t, page.manageSessionsPermissionApprovedArguments(), "proposal-1")

	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	if got := page.manageSessionsSelectedCount(); got != 2 {
		t.Fatalf("selected count = %d, want 2", got)
	}
	assertManageSessionsArguments(t, page.manageSessionsPermissionApprovedArguments(), "proposal-1", "proposal-2")
}

func TestManageSessionsNonDeployPreservesCanonicalArgumentsExactly(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
	page.OpenManageSessionsPermissionModal(manageSessionsPermissionTestRecord("perm-commit", "session_commit"))
	var got map[string]any
	if err := json.Unmarshal([]byte(page.manageSessionsPermissionApprovedArguments()), &got); err != nil {
		t.Fatal(err)
	}
	if got["keep"] != "canonical" || len(got) != 2 {
		t.Fatalf("non-deploy arguments changed: %#v", got)
	}
	if _, exists := got["selected_proposal_ids"]; exists {
		t.Fatalf("non-deploy arguments gained selection overlay: %#v", got)
	}
}

func TestManageSessionsPermissionUpdatedClosesAndAdvancesQueue(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
	first := manageSessionsPermissionTestRecord("perm-first", "session_archive")
	second := manageSessionsPermissionTestRecord("perm-second", "session_commit")
	page.permissions = []ChatPermissionRecord{first, second}
	page.rebuildToolLifecycleViews()
	if page.manageSessionsPermission != first.ID {
		t.Fatalf("opened %q, want %q", page.manageSessionsPermission, first.ID)
	}
	first.Status = "approved"
	first.UpdatedAt = 20
	page.permissions = mergePermissionHistory(page.permissions, []ChatPermissionRecord{first})
	page.rebuildToolLifecycleViews()
	if page.manageSessionsPermission != second.ID {
		t.Fatalf("advanced to %q, want %q", page.manageSessionsPermission, second.ID)
	}
}

func TestManageSessionsPermissionRealtimeRoutesAndDismisses(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
	record := manageSessionsPermissionTestRecord("perm-realtime", "session_archive")
	page.ApplySharedStreamEvent(ChatRunStreamEvent{Type: "permission.requested", SessionID: "session-1", RunID: "run-1", Permission: &record}, 10)
	if !page.manageSessionsPermissionModalActive() {
		t.Fatal("realtime manage-sessions permission did not open dedicated modal")
	}
	record.Status = "approved"
	record.UpdatedAt = 20
	page.ApplySharedStreamEvent(ChatRunStreamEvent{Type: "permission.updated", SessionID: "session-1", RunID: "run-1", Permission: &record}, 20)
	if page.manageSessionsPermissionModalActive() {
		t.Fatal("resolved realtime permission did not dismiss dedicated modal")
	}
}

func assertManageSessionsArguments(t *testing.T, raw string, selected ...string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("invalid approved arguments: %v", err)
	}
	if got["keep"] != "canonical" {
		t.Fatalf("canonical arguments not preserved: %#v", got)
	}
	ids := jsonStringSlice(got, "selected_proposal_ids")
	if strings.Join(ids, ",") != strings.Join(selected, ",") {
		t.Fatalf("selected IDs = %#v, want %#v", ids, selected)
	}
	if len(got) != 3 {
		t.Fatalf("unexpected client-authored argument fields: %#v", got)
	}
}

func manageSessionsPermissionTestRecord(id, requirement string) ChatPermissionRecord {
	action := manageSessionsActionForRequirement(requirement)
	payload := map[string]any{
		"action":             action,
		"approved_arguments": map[string]any{"action": action, "keep": "canonical"},
	}
	switch requirement {
	case "session_deploy":
		payload["proposals"] = []any{
			map[string]any{"id": "proposal-1", "title": "Primary work", "prompt": "Ship the primary task", "mode": "auto", "agent_name": "swarm", "agent_mode": "primary", "workspace_path": "/workspace", "workspace_name": "Workspace", "managed_worktree": true, "worktree_base_branch": "dev", "worktree_branch": "agent/work"},
			map[string]any{"id": "proposal-2", "title": "Extra work", "prompt": "Investigate an extra task", "mode": "plan", "agent_name": "explorer", "agent_mode": "subagent", "workspace_path": "/workspace", "workspace_name": "Workspace"},
		}
	case "session_commit":
		payload["manifest"] = map[string]any{"commits": []any{map[string]any{"message": "Ship exact files", "repository": "/workspace", "files": []any{map[string]any{"path": "web/src/app.tsx", "fingerprint": "secret"}}}}}
	case "session_archive":
		payload["sessions"] = []any{map[string]any{"title": "Review search results", "workspace_name": "swarm-go", "state": "needs_review", "updated_at": float64(1783764535576)}}
		payload["approved_arguments"] = map[string]any{"action": action, "keep": "canonical", "session_ids": []any{"opaque-session-id"}, "expected_updated_at_by_id": map[string]any{"opaque-session-id": float64(1783764535576)}}
	case "session_unarchive":
		payload["sessions"] = []any{map[string]any{"title": "Restore session", "workspace_name": "swarm-go", "state": "archived", "updated_at": float64(1783764535576)}}
	}
	raw, _ := json.Marshal(payload)
	return ChatPermissionRecord{ID: id, SessionID: "session-1", RunID: "run-1", ToolName: "functions.manage-sessions", Requirement: requirement, Status: "pending", CreatedAt: 10, UpdatedAt: 10, ToolArguments: string(raw)}
}
