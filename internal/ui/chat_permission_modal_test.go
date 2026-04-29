package ui

import (
	"strings"
	"testing"
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
