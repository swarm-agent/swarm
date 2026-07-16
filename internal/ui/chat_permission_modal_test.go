package ui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClassifyChatPermissionRoutesCanonicalDestinations(t *testing.T) {
	tests := []struct {
		name   string
		record ChatPermissionRecord
		want   chatPermissionDestination
	}{
		{name: "bash", record: ChatPermissionRecord{ToolName: "functions.bash", Requirement: "bash"}, want: chatPermissionDestinationOrdinaryInline},
		{name: "bash cannot spoof plan", record: ChatPermissionRecord{ToolName: "bash", Requirement: "plan_new_request"}, want: chatPermissionDestinationOrdinaryInline},
		{name: "exit plan", record: ChatPermissionRecord{ToolName: "exit-plan-mode", Requirement: "tool"}, want: chatPermissionDestinationPlanModal},
		{name: "plan lifecycle", record: ChatPermissionRecord{ToolName: "plan_manage", Requirement: "plan_revision_request"}, want: chatPermissionDestinationPlanModal},
		{name: "manage sessions", record: ChatPermissionRecord{ToolName: "functions.manage-sessions", Requirement: "session_deploy"}, want: chatPermissionDestinationManageSessionsModal},
		{name: "manage sessions read action", record: ChatPermissionRecord{ToolName: "manage_sessions", Requirement: "manage_sessions"}, want: chatPermissionDestinationOrdinaryInline},
		{name: "task launch", record: ChatPermissionRecord{ToolName: "task", Requirement: "task_launch"}, want: chatPermissionDestinationSpecialized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyChatPermission(tt.record); got != tt.want {
				t.Fatalf("classifyChatPermission(%#v) = %v, want %v", tt.record, got, tt.want)
			}
		})
	}
}

func TestOrdinaryPermissionIndexesExcludePlanAndManageSessions(t *testing.T) {
	page := &ChatPage{pendingPerms: []ChatPermissionRecord{
		{ID: "bash", ToolName: "bash", Requirement: "bash", Status: "pending"},
		{ID: "plan", ToolName: "plan_manage", Requirement: "plan_new_request", Status: "pending"},
		{ID: "sessions", ToolName: "manage_sessions", Requirement: "session_archive", Status: "pending"},
		{ID: "read", ToolName: "read", Requirement: "read", Status: "pending"},
	}}
	if got, want := page.ordinaryPermissionIndexes(), []int{0, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinaryPermissionIndexes() = %#v, want %#v", got, want)
	}
}

func TestFilterPermissionArgumentFieldsDropsBashCommandWhenRequestSummaryRendered(t *testing.T) {
	fields := []permissionArgumentField{
		{Key: "command", Value: "go test ./..."},
		{Key: "timeout_ms", Value: 120000},
	}
	summaries := []chatRenderLine{{Text: "request: bash go test ./..."}}

	got := filterPermissionArgumentFields("bash", fields, summaries)
	if len(got) != 1 {
		t.Fatalf("filtered fields length = %d, want 1", len(got))
	}
	if got[0].Key != "timeout_ms" {
		t.Fatalf("filtered field key = %q, want timeout_ms", got[0].Key)
	}
}

func TestBashPermissionPreviewPrefixUsesRealPrefix(t *testing.T) {
	for _, tc := range []struct {
		preview string
		want    string
	}{
		{preview: "allow bash prefix: go", want: "go"},
		{preview: "allow bash command prefix: ls", want: "ls"},
	} {
		t.Run(tc.preview, func(t *testing.T) {
			if got := bashPermissionPreviewPrefix(tc.preview); strings.TrimSpace(got) != tc.want {
				t.Fatalf("bashPermissionPreviewPrefix(%q) = %q, want %q", tc.preview, got, tc.want)
			}
		})
	}
}

func TestBashPermissionRequestSummaryKeepsFullCommand(t *testing.T) {
	command := strings.Repeat("echo critical-permission-command; ", 12) + "printf 'done'"
	payload := map[string]any{"command": command}

	summary := permissionPrimaryRequestSummary("bash", payload)
	if !strings.Contains(summary, command) {
		t.Fatalf("summary = %q, want full command %q", summary, command)
	}
	if strings.Contains(summary, "...") {
		t.Fatalf("summary = %q, must not include truncation ellipsis", summary)
	}

	page := &ChatPage{theme: NordTheme()}
	lines := page.permissionArgumentRenderLines(ChatPermissionRecord{
		ToolName:      "bash",
		ToolArguments: `{"command":"` + command + `"}`,
	}, 72)
	joined := renderLineTexts(lines)
	if !strings.Contains(joined, command) {
		t.Fatalf("rendered lines = %q, want full command %q", joined, command)
	}
}

func renderLineTexts(lines []chatRenderLine) string {
	var out strings.Builder
	for _, line := range lines {
		text := strings.TrimSpace(line.Text)
		if strings.HasPrefix(text, "request:") {
			text = strings.TrimSpace(strings.TrimPrefix(text, "request:"))
		}
		out.WriteString(text)
		if text != "" && !strings.HasSuffix(text, " ") {
			out.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func TestBashPermissionRequestSummaryWrapsInsteadOfTruncating(t *testing.T) {
	command := strings.Repeat("0123456789 ", 18)
	page := &ChatPage{theme: NordTheme()}

	lines := page.permissionArgumentRenderLines(ChatPermissionRecord{
		ToolName:      "bash",
		ToolArguments: `{"command":"` + command + `"}`,
	}, 40)
	if len(lines) < 2 {
		t.Fatalf("rendered %d line(s), want wrapped growth", len(lines))
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line.Text) > 40 {
			t.Fatalf("line %q has width %d, want <= 40", line.Text, utf8.RuneCountInString(line.Text))
		}
	}
	if strings.Contains(renderLineTexts(lines), "...") {
		t.Fatalf("rendered lines = %q, must not include truncation ellipsis", renderLineTexts(lines))
	}
}
