package tool

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestScopeExpansionChecksEveryBatchedPath(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	arguments, err := json.Marshal(map[string]any{"query": "needle", "paths": []string{workspace, outside}})
	if err != nil {
		t.Fatal(err)
	}
	request, needsApproval, err := ScopeExpansionForCall(WorkspaceScope{PrimaryPath: workspace, Roots: []string{workspace}}, Call{Name: "search", Arguments: string(arguments)})
	if err != nil || !needsApproval {
		t.Fatalf("batched search expansion: needed=%t request=%#v err=%v", needsApproval, request, err)
	}
	if request.ArgumentName != "paths" || request.RequestedPath != outside {
		t.Fatalf("batched search request = %#v, want outside path", request)
	}
}

func TestScopeExpansionChecksEveryTaskWorkspaceTarget(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	arguments, err := json.Marshal(map[string]any{
		"prompt": "work",
		"launches": []any{
			map[string]any{"workspace_path": parent, "subagent_type": "coder", "meta_prompt": "first"},
			map[string]any{"workspace_path": outside, "subagent_type": "finder", "meta_prompt": "second"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, needsApproval, err := ScopeExpansionForCall(WorkspaceScope{PrimaryPath: parent, Roots: []string{parent}}, Call{Name: "task", Arguments: string(arguments)})
	if err != nil || !needsApproval {
		t.Fatalf("batched task expansion: needed=%t request=%#v err=%v", needsApproval, request, err)
	}
	if request.ArgumentName != "workspace_path" || request.RequestedPath != outside {
		t.Fatalf("batched task request = %#v, want outside target", request)
	}
}

func TestScopeExpansionChecksEveryTaskProgramWorkspaceTarget(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	arguments, err := json.Marshal(map[string]any{
		"prompt":         "work",
		"workspace_path": parent,
		"program": map[string]any{
			"id": "program-1",
			"jobs": []any{
				map[string]any{"id": "inside", "workspace_path": parent, "subagent_type": "coder"},
				map[string]any{"id": "outside", "workspace_path": outside, "subagent_type": "finder"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, needsApproval, err := ScopeExpansionForCall(WorkspaceScope{PrimaryPath: parent, Roots: []string{parent}}, Call{Name: "task", Arguments: string(arguments)})
	if err != nil || !needsApproval {
		t.Fatalf("task program expansion: needed=%t request=%#v err=%v", needsApproval, request, err)
	}
	if request.ArgumentName != "workspace_path" || request.RequestedPath != outside {
		t.Fatalf("task program request = %#v, want outside job target", request)
	}
}

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
