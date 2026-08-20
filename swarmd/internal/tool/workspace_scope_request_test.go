package tool

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestScopeExpansionForTaskWorkspaceTarget(t *testing.T) {
	parent := t.TempDir()
	shared := t.TempDir()
	arguments, err := json.Marshal(map[string]any{
		"prompt":   "work",
		"launches": []any{map[string]any{"workspace_path": shared, "subagent_type": "coder", "meta_prompt": "implement"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, needsApproval, err := ScopeExpansionForCall(WorkspaceScope{PrimaryPath: parent, Roots: []string{parent}}, Call{Name: "task", Arguments: string(arguments)})
	if err != nil || !needsApproval {
		t.Fatalf("task workspace expansion: needed=%t request=%#v err=%v", needsApproval, request, err)
	}
	if request.ArgumentName != "workspace_path" || request.DirectoryPath != filepath.Clean(shared) {
		t.Fatalf("task workspace request = %#v", request)
	}
}
