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
		{name: "malformed JSON", raw: `{"title":"x"`, context: boundContext(false)},
		{name: "trailing JSON", raw: `{"title":"x","mode":"auto","worktree":false} {"title":"escape"}`, context: boundContext(false)},
		{name: "unknown extra field", raw: `{"title":"x","mode":"auto","worktree":false,"account_scope_id":"other"}`, context: boundContext(false)},
		{name: "null title", raw: `{"title":null,"mode":"auto","worktree":false}`, context: boundContext(false)},
		{name: "null mode", raw: `{"title":"x","mode":null,"worktree":false}`, context: boundContext(false)},
		{name: "null worktree", raw: `{"title":"x","mode":"auto","worktree":null}`, context: boundContext(false)},
		{name: "null workspace id", raw: `{"title":"x","mode":"auto","workspace_id":null,"worktree":false}`, context: multipleContext(false)},
		{name: "null worktree name", raw: `{"title":"x","mode":"auto","worktree":true,"worktree_name":null}`, context: boundContext(false)},
		{name: "omitted title", raw: `{"mode":"auto","worktree":false}`, context: boundContext(false)},
		{name: "omitted mode", raw: `{"title":"x","worktree":false}`, context: boundContext(false)},
		{name: "omitted worktree", raw: `{"title":"x","mode":"auto"}`, context: boundContext(false)},
		{name: "whitespace title", raw: `{"title":" \t\n ","mode":"auto","worktree":false}`, context: boundContext(false)},
		{name: "overlong Unicode title", raw: `{"title":"` + strings.Repeat("界", MaxTitleRunes+1) + `","mode":"auto","worktree":false}`, context: boundContext(false)},
		{name: "unsupported mode", raw: `{"title":"x","mode":"chat","worktree":false}`, context: boundContext(true)},
		{name: "Plan disabled", raw: `{"title":"x","mode":"plan","worktree":false}`, context: boundContext(false)},
		{name: "multiple choices missing workspace", raw: `{"title":"x","mode":"auto","worktree":false}`, context: multipleContext(false)},
		{name: "multiple choices empty workspace", raw: `{"title":"x","mode":"auto","workspace_id":" ","worktree":false}`, context: multipleContext(false)},
		{name: "multiple choices unknown workspace", raw: `{"title":"x","mode":"auto","workspace_id":"ws-unoffered","worktree":false}`, context: multipleContext(false)},
		{name: "server-bound workspace supplied", raw: `{"title":"x","mode":"auto","workspace_id":"ws-1","worktree":false}`, context: boundContext(false)},
		{name: "worktree missing name", raw: `{"title":"x","mode":"auto","worktree":true}`, context: boundContext(false)},
		{name: "worktree empty name", raw: `{"title":"x","mode":"auto","worktree":true,"worktree_name":"  "}`, context: boundContext(false)},
		{name: "no worktree with name", raw: `{"title":"x","mode":"auto","worktree":false,"worktree_name":"escape"}`, context: boundContext(false)},
		{name: "no worktree with null name", raw: `{"title":"x","mode":"auto","worktree":false,"worktree_name":null}`, context: boundContext(false)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if result, err := DecodeResult(test.raw, test.context); err == nil {
				t.Fatalf("DecodeResult accepted adversarial output %+v from %q", result, test.raw)
			}
		})
	}
}
