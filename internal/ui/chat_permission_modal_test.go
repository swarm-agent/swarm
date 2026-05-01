package ui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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
