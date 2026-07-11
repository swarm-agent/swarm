package permission

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPermissionNotificationSummarizesPlanApproval(t *testing.T) {
	record := pebblestore.PermissionRecord{
		ToolName:          "exit_plan_mode",
		ToolCallArguments: `{"title":"Plan: improve push notifications","document":{"info":{"goal":"secret fallback"}}}`,
		Status:            pebblestore.PermissionStatusPending,
	}

	title := permissionNotificationTitleFromRecord(record)
	body := permissionNotificationBodyFromRecord(record)
	if title != "Plan approval requested: Plan: improve push notifications" {
		t.Fatalf("title = %q", title)
	}
	if body != "Review and approve the plan: Plan: improve push notifications." {
		t.Fatalf("body = %q", body)
	}
	if strings.Contains(title+body, "exit_plan_mode") || strings.Contains(title+body, "secret fallback") {
		t.Fatalf("notification exposed raw or lower-priority arguments: %q / %q", title, body)
	}
}

func TestPermissionNotificationSummariesAreSafeAndBounded(t *testing.T) {
	tests := []struct {
		name       string
		record     pebblestore.PermissionRecord
		wantTitle  string
		wantBody   string
		notContain string
	}{
		{
			name:       "plan checkpoint",
			record:     pebblestore.PermissionRecord{ToolName: "plan_manage", ToolArguments: `{"action":"request_followup_checkpoint","checkpoint_title":"Review focused diffs","change_request":"full private payload"}`, Status: pebblestore.PermissionStatusPending},
			wantTitle:  "Plan checkpoint approval requested: Review focused diffs",
			wantBody:   "Review and approve: Review focused diffs.",
			notContain: "full private payload",
		},
		{
			name:       "command",
			record:     pebblestore.PermissionRecord{ToolName: "bash", ToolArguments: `{"command":"git push https://token@example.invalid/repo"}`, Status: pebblestore.PermissionStatusPending},
			wantTitle:  "Command approval requested: git",
			wantBody:   "Approve running a git command.",
			notContain: "token",
		},
		{
			name:       "file",
			record:     pebblestore.PermissionRecord{ToolName: "write", ToolArguments: `{"path":"/private/workspace/settings.json","content":"api_key=secret"}`, Status: pebblestore.PermissionStatusPending},
			wantTitle:  "Write file: settings.json",
			wantBody:   "Approve access to settings.json.",
			notContain: "api_key",
		},
		{
			name:       "generic action",
			record:     pebblestore.PermissionRecord{ToolName: "manage_theme", ToolArguments: `{"action":"set","content":{"palette":{"secret":"value"}}}`, Status: pebblestore.PermissionStatusPending},
			wantTitle:  "Permission requested: Manage theme: Set",
			wantBody:   "Approve the requested manage theme: set operation.",
			notContain: "palette",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			title := permissionNotificationTitleFromRecord(tc.record)
			body := permissionNotificationBodyFromRecord(tc.record)
			if title != tc.wantTitle || body != tc.wantBody {
				t.Fatalf("notification = %q / %q, want %q / %q", title, body, tc.wantTitle, tc.wantBody)
			}
			if strings.Contains(title+body, tc.notContain) {
				t.Fatalf("notification exposed unsafe argument content: %q / %q", title, body)
			}
			if len([]rune(title)) > 180 || len([]rune(body)) > 180 {
				t.Fatalf("notification is unbounded: %d / %d", len([]rune(title)), len([]rune(body)))
			}
		})
	}
}
