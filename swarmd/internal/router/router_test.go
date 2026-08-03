package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPromptAndSchemaAreWorktreeNamingOnly(t *testing.T) {
	prompt := Prompt()
	lower := strings.ToLower(prompt)
	for _, forbidden := range []string{"workspace_id", "advertised workspace", "managed_worktree_allowed", "worktree as true", "plan mode"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("Router prompt contains forbidden routing material %q: %s", forbidden, prompt)
		}
	}
	if !strings.Contains(prompt, "worktree_name") || !strings.Contains(prompt, "3-5 word title") {
		t.Fatalf("Router prompt omitted naming contract: %s", prompt)
	}
	schema := ResultSchema()
	encoded, _ := json.Marshal(schema)
	properties := schema["properties"].(map[string]any)
	if len(properties) != 2 || properties["title"] == nil || properties["worktree_name"] == nil {
		t.Fatalf("Router schema properties = %#v", properties)
	}
	for _, forbidden := range []string{"workspace_id", `"worktree":`, "mode"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("Router schema contains forbidden field %q: %s", forbidden, encoded)
		}
	}
	if schema["additionalProperties"] != false {
		t.Fatal("Router schema allows additional properties")
	}
}

func TestDecodeResultStrict(t *testing.T) {
	got, err := DecodeResult(`{"title":" Route work ","worktree_name":" router-core "}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Route work" || got.WorktreeName == nil || *got.WorktreeName != "router-core" {
		t.Fatalf("decoded result = %+v", got)
	}

	bad := []string{
		`{"title":"x","worktree_name":"x","mode":"plan"}`,
		`{"title":"x","worktree_name":"x","workspace_id":"ws"}`,
		`{"title":"x","worktree_name":"x","worktree":true}`,
		`{"title":"x","worktree_name":"x"}{}`,
		`{}`,
		`{"title":"x"}`,
		`{"title":" ","worktree_name":"x"}`,
		`{"title":"x","worktree_name":" "}`,
		`{"title":"` + strings.Repeat("x", MaxTitleRunes+1) + `","worktree_name":"x"}`,
	}
	for _, raw := range bad {
		if _, err := DecodeResult(raw); err == nil {
			t.Fatalf("DecodeResult(%s) succeeded", raw)
		}
	}
}

func TestValidateRequestRequiresInput(t *testing.T) {
	if err := ValidateRequest(Request{Input: " "}); err == nil {
		t.Fatal("empty request input accepted")
	}
}
