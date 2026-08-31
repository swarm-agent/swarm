package tool

import (
	"encoding/json"
	"errors"
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

func TestScopeExpansionRejectsImmutableCoderWorktreeEscape(t *testing.T) {
	worktree := t.TempDir()
	outside := t.TempDir()
	scope := WorkspaceScope{
		PrimaryPath: worktree, Roots: []string{worktree}, MutationScopes: []string{"internal/**"}, RejectScopeExpansion: true,
	}
	for _, test := range []struct {
		name          string
		call          Call
		requestedPath string
	}{
		{name: "outside worktree", call: Call{Name: "search", Arguments: mustWorkspaceScopeJSON(t, map[string]any{"path": outside, "query": "needle"})}, requestedPath: outside},
		{name: "outside owned scope", call: Call{Name: "edit", Arguments: mustWorkspaceScopeJSON(t, map[string]any{"path": filepath.Join(worktree, "README.md"), "old_string": "a", "new_string": "b"})}, requestedPath: filepath.Join(worktree, "README.md")},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, needsApproval, err := ScopeExpansionForCall(scope, test.call)
			if !errors.Is(err, ErrWorkspaceScopeExpansionRejected) || needsApproval {
				t.Fatalf("immutable Coder expansion: needed=%t request=%#v err=%v", needsApproval, request, err)
			}
			if request.RequestedPath != test.requestedPath {
				t.Fatalf("immutable Coder rejection lost requested path: %#v", request)
			}
		})
	}
}

func TestScopeExpansionAllowsImmutableCoderWorktreeAccess(t *testing.T) {
	worktree := t.TempDir()
	arguments, err := json.Marshal(map[string]any{"path": filepath.Join(worktree, "internal", "inside.txt")})
	if err != nil {
		t.Fatal(err)
	}
	request, needsApproval, err := ScopeExpansionForCall(WorkspaceScope{
		PrimaryPath: worktree, Roots: []string{worktree}, MutationScopes: []string{"internal/**"}, RejectScopeExpansion: true,
	}, Call{Name: "write", Arguments: string(arguments)})
	if err != nil || needsApproval {
		t.Fatalf("immutable Coder owned-scope access: needed=%t request=%#v err=%v", needsApproval, request, err)
	}
}

func mustWorkspaceScopeJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
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

func TestScopeExpansionForTaskChecksEveryLaunchWorkspace(t *testing.T) {
	parent := t.TempDir()
	external := t.TempDir()
	arguments, err := json.Marshal(map[string]any{
		"launches": []any{
			map[string]any{"workspace_path": parent, "subagent_type": "coder"},
			map[string]any{"workspace_path": external, "subagent_type": "coder"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, needsApproval, err := ScopeExpansionForCall(WorkspaceScope{PrimaryPath: parent, Roots: []string{parent}}, Call{Name: "task", Arguments: string(arguments)})
	if err != nil || !needsApproval {
		t.Fatalf("task workspace expansion: needed=%t request=%#v err=%v", needsApproval, request, err)
	}
	if request.ArgumentName != "workspace_path" || request.DirectoryPath != filepath.Clean(external) {
		t.Fatalf("task workspace request = %#v", request)
	}
}

func TestScopeExpansionChecksEverySearchPath(t *testing.T) {
	parent := t.TempDir()
	external := t.TempDir()
	arguments, err := json.Marshal(map[string]any{"paths": []string{parent, external}})
	if err != nil {
		t.Fatal(err)
	}
	request, needsApproval, err := ScopeExpansionForCall(WorkspaceScope{PrimaryPath: parent, Roots: []string{parent}}, Call{Name: "search", Arguments: string(arguments)})
	if err != nil || !needsApproval {
		t.Fatalf("search workspace expansion: needed=%t request=%#v err=%v", needsApproval, request, err)
	}
	if request.ArgumentName != "paths" || request.DirectoryPath != filepath.Clean(external) {
		t.Fatalf("search workspace request = %#v", request)
	}
}
