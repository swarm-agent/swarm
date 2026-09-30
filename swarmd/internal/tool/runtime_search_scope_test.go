package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/tool/searchipc"
)

// Purpose: search/find must index the narrowest authorized containing workspace,
// not a saved ancestor such as home or filesystem root. selectResidentSearchScope
// owns this choice before SearchCoordinator starts native FFF. This unit layer
// proves root choice, order independence, exact-file scope, and sibling exclusion
// without scanning any real home directory or filesystem root.
func TestResidentSearchScopeChoosesNarrowestWorkspace(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("HOME", parent)
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
		{"sibling prefix is not containment", WorkspaceScope{PrimaryPath: project, Roots: []string{parent}}, searchTarget{Root: sibling}, sibling, sibling},
		{"home-only coordination directory", WorkspaceScope{PrimaryPath: parent, Roots: []string{parent}}, searchTarget{Root: project}, project, project},
		{"home-only coordination file", WorkspaceScope{PrimaryPath: parent}, searchTarget{Root: nested, FileName: "needle.go"}, nested, filepath.Join(nested, "needle.go")},
		{"root-only coordination directory", WorkspaceScope{PrimaryPath: string(filepath.Separator)}, searchTarget{Root: project}, project, project},
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
// ancestor and home-only coordination scope through the actual runtime ->
// coordinator -> native helper path. Both tools must return requested content
// and paths only, including exact files; an out-of-scope request must
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

	// Unlike the registered-project case above, this is the reported home-only
	// coordination scope: no project root is present in the authorized roots.
	homeScope := WorkspaceScope{PrimaryPath: home, Roots: []string{home}}
	for _, target := range []string{project, filepath.Join(subdir, "needle.txt")} {
		for _, toolName := range []string{"search", "find"} {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			query := "needle"
			if toolName == "search" {
				query = "unique_scope_needle"
			}
			args, _ := json.Marshal(map[string]any{"query": query, "content_mode": "literal", "path": target, "max_results": 10, "timeout_ms": 10000})
			output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, homeScope, Call{Name: toolName, Arguments: string(args)})
			cancel()
			if err != nil {
				t.Fatalf("home-only %s: %v\n%s", toolName, err, output)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(output), &payload); err != nil {
				t.Fatal(err)
			}
			if payload[toolName+"_errors"] != nil || payload[toolName+"_warnings"] != nil || payload["timed_out"] == true {
				t.Fatalf("incomplete home-only %s: %s", toolName, output)
			}
			results, ok := payload["results"].([]any)
			want := 1
			if target == project {
				want = 2
			}
			if !ok || len(results) != want || payload["total_files"] != float64(want) {
				t.Fatalf("home-only index escaped requested directory: %s", output)
			}
			paths := searchDecodedResultPaths(results)
			if len(paths) != want || !searchPathContains(paths, "needle.txt") || (want == 2 && !searchPathContains(paths, "needle-sibling.txt")) {
				t.Fatalf("missing requested fixture paths %v: %s", paths, output)
			}
			if strings.Contains(output, "needle-outside.txt") || (want == 1 && strings.Contains(output, "needle-sibling.txt")) {
				t.Fatalf("home-only results escaped target: %s", output)
			}
			if toolName == "search" && !strings.Contains(output, "unique_scope_needle") {
				t.Fatalf("content search did not return fixture text: %s", output)
			}
		}
	}
	shared := runtime.searchCoordinator.Snapshot()
	if shared.ColdStarts != 2 || shared.ResidentRoots != 2 {
		t.Fatalf("expected project reuse and one exact-file directory index: %+v", shared)
	}
	for _, toolName := range []string{"search", "find"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, homeScope, Call{Name: toolName, Arguments: `{"query":"needle"}`})
		cancel()
		var payload map[string]any
		if err != nil {
			if !strings.Contains(err.Error(), "narrower project directory or file") {
				t.Fatalf("missing broad-root guidance: %v", err)
			}
		} else if json.Unmarshal([]byte(output), &payload) != nil || payload[toolName+"_errors"] == nil || !strings.Contains(output, "narrower project directory or file") {
			t.Fatalf("broad HOME appeared successful: %s", output)
		}
	}
	final := runtime.searchCoordinator.Snapshot()
	if final.ColdStarts != shared.ColdStarts || final.NativeExecutions != shared.NativeExecutions || final.ResidentRoots != shared.ResidentRoots {
		t.Fatalf("broad requests reached helpers: before=%+v after=%+v", shared, final)
	}
}

// Purpose: canonicalSearchScope must reject HOME/root (including aliases)
// before resident allocation, while retaining canonical cache identity for
// narrow directories. This coordinator layer proves no helper is resolved or
// executed on rejection; the native library guard remains an independent gate.
func TestResidentSearchRejectsBroadRootsAndAliases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "home-alias")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	coordinator := NewSearchCoordinator(1)
	defer coordinator.Close()
	coordinator.resolve = func() (string, error) {
		t.Error("broad root reached helper resolution")
		return "", nil
	}
	for _, root := range []string{home, alias, string(filepath.Separator)} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := coordinator.Execute(ctx, searchipc.Request{IndexRoot: root, TargetPath: root, Operation: "content", Queries: []string{"needle"}})
		cancel()
		if err == nil || !strings.Contains(err.Error(), "narrower project directory or file") {
			t.Fatalf("broad root did not fail with guidance: %v", err)
		}
	}
	stats := coordinator.Snapshot()
	if stats.ResidentRoots != 0 || stats.ColdStarts != 0 || stats.NativeExecutions != 0 || stats.Inflight != 0 {
		t.Fatalf("rejected roots changed coordinator state: %+v", stats)
	}
	root, target := selectResidentSearchScope(WorkspaceScope{PrimaryPath: alias}, searchTarget{Root: filepath.Join(alias, "project")})
	canonicalRoot, canonicalTarget, err := canonicalSearchScope(searchipc.Request{IndexRoot: root, TargetPath: target})
	if err != nil || canonicalRoot != project || canonicalTarget != project {
		t.Fatalf("alias did not keep narrow canonical identity: (%q, %q, %v)", canonicalRoot, canonicalTarget, err)
	}
}

// Purpose: home coordination must not change openRootedWorkspacePath's
// authorization or symlink containment. Runtime rejection must occur before
// helper allocation, not turn into empty successful search/find output.
func TestResidentSearchHomeScopeRejectsUnauthorizedTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	outside := t.TempDir()
	link := filepath.Join(home, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	defer runtime.Close()
	scope := WorkspaceScope{PrimaryPath: home, Roots: []string{home}}
	for _, path := range []string{outside, link} {
		for _, toolName := range []string{"search", "find"} {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			args, _ := json.Marshal(map[string]any{"path": path, "query": "needle"})
			_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{Name: toolName, Arguments: string(args)})
			cancel()
			if err == nil {
				t.Fatalf("%s accepted unauthorized target %q", toolName, path)
			}
		}
	}
	stats := runtime.searchCoordinator.Snapshot()
	if stats.ResidentRoots != 0 || stats.ColdStarts != 0 || stats.NativeExecutions != 0 {
		t.Fatalf("unauthorized targets reached helpers: %+v", stats)
	}
}
