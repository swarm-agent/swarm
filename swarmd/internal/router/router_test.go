package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func boundContext(plan bool) Context {
	return Context{PlanEnabled: plan, ServerBoundWorkspaceID: "ws-1", Workspaces: []Workspace{{ID: "ws-1", Name: "Core", Definition: "untrusted"}}}
}

func multipleContext(plan bool) Context {
	return Context{PlanEnabled: plan, WorktreeRequested: true, Workspaces: []Workspace{{ID: "ws-1", Name: "Core"}, {ID: "ws-2", Name: "Web"}}}
}

func TestPromptAndSchemaOmitDisabledPlan(t *testing.T) {
	prompt, err := Prompt(boundContext(false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, ModePlan) || !strings.Contains(prompt, `["auto"]`) {
		t.Fatalf("disabled mode leaked into prompt: %s", prompt)
	}
	schema, err := ResultSchema(boundContext(false))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(schema)
	if strings.Contains(string(encoded), ModePlan) {
		t.Fatalf("disabled mode leaked into schema: %s", encoded)
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["workspace_id"]; ok {
		t.Fatal("server-bound workspace schema advertised workspace_id")
	}
	if schema["additionalProperties"] != false {
		t.Fatal("schema allows additional properties")
	}
}

func TestPromptAndSchemaAdvertiseConditionalChoices(t *testing.T) {
	prompt, err := Prompt(multipleContext(true))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{ModeAuto, ModePlan, "workspace_id is required", "ws-1", "ws-2"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
	schema, err := ResultSchema(multipleContext(true))
	if err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if worktree := properties["worktree"].(map[string]any); worktree["const"] != true {
		t.Fatalf("schema did not lock worktree to per-session intent: %+v", worktree)
	}
	if _, ok := properties["workspace_id"]; !ok {
		t.Fatal("multiple-workspace schema omitted workspace_id")
	}
	if _, ok := properties["worktree_name"]; !ok {
		t.Fatal("schema omitted conditional worktree_name")
	}
	if _, ok := schema["allOf"]; !ok {
		t.Fatal("schema omitted worktree conditional")
	}
}

func TestDecodeResultStrictAndContextual(t *testing.T) {
	name := "router-core"
	got, err := DecodeResult(`{"title":" Route work ","mode":"plan","workspace_id":"ws-2","worktree":true,"worktree_name":" router-core "}`, multipleContext(true))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Route work" || got.Mode != ModePlan || got.WorkspaceID == nil || *got.WorkspaceID != "ws-2" || got.WorktreeName == nil || *got.WorktreeName != name {
		t.Fatalf("decoded result = %+v", got)
	}

	bad := []struct {
		name string
		raw  string
		ctx  Context
	}{
		{"unknown field", `{"title":"x","mode":"auto","worktree":false,"escape":true}`, boundContext(false)},
		{"trailing object", `{"title":"x","mode":"auto","worktree":false}{}`, boundContext(false)},
		{"missing required", `{"title":"x","mode":"auto"}`, boundContext(false)},
		{"disabled plan", `{"title":"x","mode":"plan","worktree":false}`, boundContext(false)},
		{"mode is case sensitive", `{"title":"x","mode":"AUTO","worktree":false}`, boundContext(false)},
		{"bound workspace id", `{"title":"x","mode":"auto","workspace_id":"ws-1","worktree":false}`, boundContext(false)},
		{"null bound workspace id", `{"title":"x","mode":"auto","workspace_id":null,"worktree":false}`, boundContext(false)},
		{"missing workspace id", `{"title":"x","mode":"auto","worktree":false}`, multipleContext(false)},
		{"unadvertised workspace", `{"title":"x","mode":"auto","workspace_id":"ws-x","worktree":false}`, multipleContext(false)},
		{"missing worktree name", `{"title":"x","mode":"auto","workspace_id":"ws-1","worktree":true}`, multipleContext(false)},
		{"forbidden worktree name", `{"title":"x","mode":"auto","worktree":false,"worktree_name":"x"}`, boundContext(false)},
		{"worktree intent mismatch", `{"title":"x","mode":"auto","workspace_id":"ws-1","worktree":false}`, multipleContext(false)},
		{"empty title", `{"title":" ","mode":"auto","worktree":false}`, boundContext(false)},
		{"long title", `{"title":"` + strings.Repeat("x", MaxTitleRunes+1) + `","mode":"auto","worktree":false}`, boundContext(false)},
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
	if err := ValidateRequest(Request{Input: " ", Context: boundContext(false)}); err == nil {
		t.Fatal("empty request input accepted")
	}
}
