package router

import (
	"strings"
	"testing"
)

func TestDecodeResultRejectsAdversarialOutputShapes(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		context Context
	}{
		{name: "malformed JSON", raw: `{"title":"x"`, context: boundContext()},
		{name: "trailing JSON", raw: `{"title":"x"} {"title":"escape"}`, context: boundContext()},
		{name: "unknown extra field", raw: `{"title":"x","account_scope_id":"other"}`, context: boundContext()},
		{name: "null title", raw: `{"title":null}`, context: boundContext()},
		{name: "mode authority", raw: `{"title":"x","mode":"auto"}`, context: boundContext()},
		{name: "null worktree", raw: `{"title":"x","worktree":null}`, context: withManagedWorktree(boundContext())},
		{name: "null workspace id", raw: `{"title":"x","workspace_id":null}`, context: multipleContext()},
		{name: "null worktree name", raw: `{"title":"x","worktree":true,"worktree_name":null}`, context: withManagedWorktree(boundContext())},
		{name: "omitted title", raw: `{}`, context: boundContext()},
		{name: "whitespace title", raw: `{"title":" \t\n "}`, context: boundContext()},
		{name: "overlong Unicode title", raw: `{"title":"` + strings.Repeat("界", MaxTitleRunes+1) + `"}`, context: boundContext()},
		{name: "multiple choices missing workspace", raw: `{"title":"x"}`, context: multipleContext()},
		{name: "multiple choices empty workspace", raw: `{"title":"x","workspace_id":" "}`, context: multipleContext()},
		{name: "multiple choices unknown workspace", raw: `{"title":"x","workspace_id":"ws-unoffered"}`, context: multipleContext()},
		{name: "server-bound workspace supplied", raw: `{"title":"x","workspace_id":"ws-1"}`, context: boundContext()},
		{name: "worktree missing name", raw: `{"title":"x","worktree":true}`, context: withManagedWorktree(boundContext())},
		{name: "worktree empty name", raw: `{"title":"x","worktree":true,"worktree_name":"  "}`, context: withManagedWorktree(boundContext())},
		{name: "unauthorized worktree", raw: `{"title":"x","worktree":true,"worktree_name":"escape"}`, context: boundContext()},
		{name: "unauthorized null worktree name", raw: `{"title":"x","worktree_name":null}`, context: boundContext()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if result, err := DecodeResult(test.raw, test.context); err == nil {
				t.Fatalf("DecodeResult accepted adversarial output %+v from %q", result, test.raw)
			}
		})
	}
}
