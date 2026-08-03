package router

import (
	"strings"
	"testing"
)

func TestDecodeResultRejectsAdversarialOutputShapes(t *testing.T) {
	tests := []string{
		`{"title":"x"`,
		`{"title":"x","worktree_name":"x"} {"title":"escape","worktree_name":"escape"}`,
		`{"title":"x","worktree_name":"x","account_scope_id":"other"}`,
		`{"title":null,"worktree_name":"x"}`,
		`{"title":"x","worktree_name":null}`,
		`{"title":"x","worktree_name":"x","workspace_id":null}`,
		`{"title":"x","worktree_name":"x","worktree":true}`,
		`{"title":" \t\n ","worktree_name":"x"}`,
		`{"title":"x","worktree_name":"  "}`,
		`{"title":"` + strings.Repeat("界", MaxTitleRunes+1) + `","worktree_name":"x"}`,
	}
	for _, raw := range tests {
		if result, err := DecodeResult(raw); err == nil {
			t.Fatalf("DecodeResult accepted adversarial output %+v from %q", result, raw)
		}
	}
}
