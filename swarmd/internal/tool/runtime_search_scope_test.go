package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Purpose: search/find must index the narrowest authorized containing workspace,
// not a saved ancestor such as home or filesystem root. selectResidentSearchScope
// owns this choice before SearchCoordinator starts native FFF. This unit layer
// proves root choice, order independence, exact-file scope, and sibling exclusion
// without scanning any real home directory or filesystem root.
func TestResidentSearchScopeChoosesNarrowestWorkspace(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	nested := filepath.Join(project, "nested")
	sibling := filepath.Join(parent, "project-other")
	for _, tc := range []struct {
		name   string
		scope  WorkspaceScope
		target searchTarget
		root   string
		path   string
	}{
		{"project before ancestor", WorkspaceScope{PrimaryPath: project, Roots: []string{project, parent}}, searchTarget{Root: nested}, project, nested},
		{"ancestor before project", WorkspaceScope{PrimaryPath: parent, Roots: []string{parent, project}}, searchTarget{Root: nested}, project, nested},
		{"filesystem root ancestor", WorkspaceScope{PrimaryPath: project, Roots: []string{string(filepath.Separator), parent}}, searchTarget{Root: nested}, project, nested},
		{"nested workspace", WorkspaceScope{PrimaryPath: project, Roots: []string{parent, nested}}, searchTarget{Root: nested}, nested, nested},
		{"single file", WorkspaceScope{PrimaryPath: project, Roots: []string{parent}}, searchTarget{Root: nested, FileName: "needle.go"}, project, filepath.Join(nested, "needle.go")},
		{"sibling prefix is not containment", WorkspaceScope{PrimaryPath: project, Roots: []string{parent}}, searchTarget{Root: sibling}, parent, sibling},
		{"empty roots do not become cwd", WorkspaceScope{Roots: []string{"", " "}}, searchTarget{Root: "nested"}, "nested", "nested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := selectResidentSearchScope(tc.scope, tc.target)
			if root != tc.root || path != tc.path {
				t.Fatalf("scope = (%q, %q), want (%q, %q)", root, path, tc.root, tc.path)
			}
		})
	}
}

// Purpose: reproduce the reported initialization failure using a synthetic HOME
// ancestor and the actual runtime -> coordinator -> native helper path. Both
// tools must return the requested fixture only; an out-of-scope request must
// fail without starting another worker. This hermetic integration layer proves
// that root selection fixes native initialization without disabling FFF guards.
func TestResidentSearchToolsWithHomeAncestor(t *testing.T) {
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelBuild()
	helper := filepath.Join(t.TempDir(), "swarm-fff-search")
	cmd := exec.CommandContext(buildCtx, "go", "build", "-p=2", "-o", helper, "../../cmd/swarm-fff-search")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native helper: %v\n%s", err, output)
	}
	t.Setenv("SWARM_FFF_SEARCH_HELPER", helper)
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	subdir := filepath.Join(project, "src")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(subdir, "needle.txt"), filepath.Join(project, "needle-sibling.txt"), filepath.Join(home, "needle-outside.txt")} {
		if err := os.WriteFile(path, []byte("unique_scope_needle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runtime := NewRuntime(1)
	t.Cleanup(func() { _ = runtime.Close() })
	scope := WorkspaceScope{PrimaryPath: project, Roots: []string{home, project}}
	for _, toolName := range []string{"search", "find"} {
		t.Run(toolName, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			query := "needle"
			if toolName == "search" {
				query = "unique_scope_needle"
			}
			args, err := json.Marshal(map[string]any{"query": query, "path": subdir, "max_results": 10, "timeout_ms": 10000})
			if err != nil {
				t.Fatal(err)
			}
			output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{Name: toolName, Arguments: string(args)})
			if err != nil {
				t.Fatalf("%s: %v\n%s", toolName, err, output)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(output), &payload); err != nil {
				t.Fatal(err)
			}
			if payload[toolName+"_errors"] != nil || payload[toolName+"_warnings"] != nil || payload["timed_out"] == true {
				t.Fatalf("incomplete %s: %s", toolName, output)
			}
			results, ok := payload["results"].([]any)
			if !ok || len(results) != 1 {
				t.Fatalf("expected one scoped result: %s", output)
			}
			paths := searchDecodedResultPaths(results)
			// Single-directory results are relative to the requested directory.
			if len(paths) != 1 || paths[0] != "needle.txt" {
				t.Fatalf("unexpected scoped paths %v: %s", paths, output)
			}
			if payload["path"] != subdir || payload["total_files"] != float64(2) {
				t.Fatalf("index widened beyond the two project files: %s", output)
			}
		})
	}
	before := runtime.searchCoordinator.Snapshot()
	if before.ColdStarts != 1 || before.ResidentRoots != 1 {
		t.Fatalf("expected one shared project worker: %+v", before)
	}
	outside := t.TempDir()
	for _, toolName := range []string{"search", "find"} {
		args, _ := json.Marshal(map[string]any{"query": "needle", "path": outside})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{Name: toolName, Arguments: string(args)})
		cancel()
		if err == nil {
			t.Fatalf("%s accepted unauthorized path", toolName)
		}
	}
	after := runtime.searchCoordinator.Snapshot()
	if after.ColdStarts != before.ColdStarts || after.NativeExecutions != before.NativeExecutions {
		t.Fatalf("unauthorized request reached native helper: before=%+v after=%+v", before, after)
	}
}
