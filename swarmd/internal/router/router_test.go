package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func boundContext() Context {
	return Context{ServerBoundWorkspaceID: "ws-1", Workspaces: []Workspace{{ID: "ws-1", Name: "Core", Definition: "untrusted"}}}
}

func multipleContext() Context {
	return Context{Workspaces: []Workspace{{ID: "ws-1", Name: "Core"}, {ID: "ws-2", Name: "Web"}}}
}

func withManagedWorktree(context Context) Context {
	context.ManagedWorktreeAllowed = true
	return context
}

func TestPromptAndSchemaContainNoModeChoice(t *testing.T) {
	prompt, err := Prompt(boundContext())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(prompt), "mode") || strings.Contains(prompt, "plan") || strings.Contains(prompt, "auto") {
		t.Fatalf("mode authority leaked into prompt: %s", prompt)
	}
	if strings.Contains(prompt, "worktree") {
		t.Fatalf("unauthorized worktree instructions leaked into prompt: %s", prompt)
	}
	if !strings.Contains(prompt, "Prefer a concise 3-5 word title") || !strings.Contains(prompt, "guidance rather than a hard word-count restriction") {
		t.Fatalf("prompt does not express the advisory 3-5 word title preference: %s", prompt)
	}
	schema, err := ResultSchema(boundContext())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(schema)
	if strings.Contains(strings.ToLower(string(encoded)), "mode") || strings.Contains(string(encoded), "plan") || strings.Contains(string(encoded), "auto") {
		t.Fatalf("mode authority leaked into schema: %s", encoded)
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["workspace_id"]; ok {
		t.Fatal("server-bound workspace schema advertised workspace_id")
	}
	if _, ok := properties["worktree"]; ok {
		t.Fatal("unauthorized schema advertised worktree")
	}
	if _, ok := properties["worktree_name"]; ok {
		t.Fatal("unauthorized schema advertised worktree_name")
	}
	if schema["additionalProperties"] != false {
		t.Fatal("schema allows additional properties")
	}
}

func TestPromptAndSchemaAdvertiseConditionalChoices(t *testing.T) {
	context := withManagedWorktree(multipleContext())
	prompt, err := Prompt(context)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"workspace_id is required", "ws-1", "ws-2"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
	schema, err := ResultSchema(context)
	if err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["workspace_id"]; !ok {
		t.Fatal("multiple-workspace schema omitted workspace_id")
	}
	if _, ok := properties["worktree_name"]; !ok {
		t.Fatal("schema omitted conditional worktree_name")
	}
	if worktree := properties["worktree"].(map[string]any); worktree["const"] != true {
		t.Fatal("authorized schema did not require worktree true")
	}
}

func TestDecodeResultStrictAndContextual(t *testing.T) {
	name := "router-core"
	got, err := DecodeResult(`{"title":" Route work ","workspace_id":"ws-2","worktree":true,"worktree_name":" router-core "}`, withManagedWorktree(multipleContext()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Route work" || got.WorkspaceID == nil || *got.WorkspaceID != "ws-2" || got.WorktreeName == nil || *got.WorktreeName != name {
		t.Fatalf("decoded result = %+v", got)
	}

	bad := []struct {
		name string
		raw  string
		ctx  Context
	}{
		{"mode is forbidden", `{"title":"x","mode":"plan"}`, boundContext()},
		{"unknown field", `{"title":"x","escape":true}`, boundContext()},
		{"trailing object", `{"title":"x"}{}`, boundContext()},
		{"missing required", `{}`, boundContext()},
		{"bound workspace id", `{"title":"x","workspace_id":"ws-1"}`, boundContext()},
		{"null bound workspace id", `{"title":"x","workspace_id":null}`, boundContext()},
		{"missing workspace id", `{"title":"x"}`, multipleContext()},
		{"unadvertised workspace", `{"title":"x","workspace_id":"ws-x"}`, multipleContext()},
		{"unauthorized worktree", `{"title":"x","worktree":true,"worktree_name":"x"}`, boundContext()},
		{"authorized missing worktree name", `{"title":"x","worktree":true}`, withManagedWorktree(boundContext())},
		{"empty title", `{"title":" "}`, boundContext()},
		{"long title", `{"title":"` + strings.Repeat("x", MaxTitleRunes+1) + `"}`, boundContext()},
	}
	for _, test := range bad {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeResult(test.raw, test.ctx); err == nil {
				t.Fatalf("DecodeResult(%s) succeeded", test.raw)
			}
		})
	}
}

func TestValidateContextRequiresUnambiguousWorkspaceShape(t *testing.T) {
	bad := []Context{
		{},
		{Workspaces: []Workspace{{ID: "ws-1"}}},
		{ServerBoundWorkspaceID: "ws-1", Workspaces: []Workspace{{ID: "ws-2"}}},
		{Workspaces: []Workspace{{ID: "ws-1"}, {ID: "ws-1"}}},
	}
	for _, context := range bad {
		if err := ValidateContext(context); err == nil {
			t.Fatalf("ValidateContext(%+v) succeeded", context)
		}
	}
	if err := ValidateRequest(Request{Input: " ", Context: boundContext()}); err == nil {
		t.Fatal("empty request input accepted")
	}
}
