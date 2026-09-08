package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: filesystem consumers must retain explicit root authority without
// promoting linked roots or granting mutation through read-only access. These
// direct executor tests exercise openRootedWorkspacePath/workspaceMutationAllowed
// at the narrowest real-filesystem layer, including unchanged rejected targets.
func TestWorkspaceTargetFilesystemAuthority(t *testing.T) {
	primary, linked, reference, outside := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, root := range []string{primary, linked, reference, outside} {
		if err := os.WriteFile(filepath.Join(root, "item.txt"), []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scope := normalizeWorkspaceScope(primary, []string{linked})
	scope.ReadOnlyRoots = []string{reference}
	for _, path := range []string{"item.txt", filepath.Join(linked, "item.txt"), filepath.Join(reference, "item.txt")} {
		if output, err := executeRead(scope, map[string]any{"path": path}); err != nil || !strings.Contains(output, "original") {
			t.Fatalf("read %s: %s, %v", path, output, err)
		}
	}
	for _, path := range []string{"", linked, reference} {
		if output, err := executeList(scope, map[string]any{"path": path}); err != nil || !strings.Contains(output, "item.txt") {
			t.Fatalf("list %s: %s, %v", path, output, err)
		}
	}
	for _, path := range []string{"item.txt", filepath.Join(linked, "item.txt")} {
		if _, err := executeWrite(scope, map[string]any{"path": path, "content": "written"}); err != nil {
			t.Fatal(err)
		}
		if _, err := executeEdit(scope, map[string]any{"path": path, "old_string": "written", "new_string": "edited"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []string{reference, outside} {
		path := filepath.Join(root, "item.txt")
		if _, err := executeWrite(scope, map[string]any{"path": path, "content": "bad"}); err == nil {
			t.Fatalf("write authorized %s", path)
		}
		if _, err := executeEdit(scope, map[string]any{"path": path, "old_string": "original", "new_string": "bad"}); err == nil {
			t.Fatalf("edit authorized %s", path)
		}
		if _, err := executeWrite(scope, map[string]any{"path": filepath.Join(root, "new", "item.txt"), "content": "bad"}); err == nil {
			t.Fatalf("creation authorized %s", root)
		}
		if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
			t.Fatalf("rejected write created parent: %v", err)
		}
	}
	for _, root := range []string{primary, linked, reference, outside} {
		want := "original"
		if root == primary || root == linked {
			want = "edited"
		}
		if data, err := os.ReadFile(filepath.Join(root, "item.txt")); err != nil || string(data) != want {
			t.Fatalf("postcondition %s: %q, %v", root, data, err)
		}
	}
	if _, err := executeRead(scope, map[string]any{"path": filepath.Join(outside, "item.txt")}); err == nil {
		t.Fatal("read escaped authorized roots")
	}
	scope.MutationScopes = []string{"owned/**"}
	if _, err := executeWrite(scope, map[string]any{"path": "owned/new.txt", "content": "owned"}); err != nil {
		t.Fatal(err)
	}
	if _, err := executeWrite(scope, map[string]any{"path": "item.txt", "content": "bad"}); err == nil {
		t.Fatal("write escaped Coder ownership")
	}
	if data, err := os.ReadFile(filepath.Join(primary, "item.txt")); err != nil || string(data) != "edited" {
		t.Fatalf("unowned file changed: %q, %v", data, err)
	}
}

// Purpose: unrelated Git administration must not be exposed through broad source
// read roots. Real read/list and the shared search/find resolver exercise the
// workspaceGitAdminAllowed boundary, including the explicitly granted own admin.
func TestWorkspaceTargetGitAdministration(t *testing.T) {
	primary, source := t.TempDir(), t.TempDir()
	own := filepath.Join(source, ".git", "worktrees", "own")
	other := filepath.Join(source, ".git", "worktrees", "other")
	for _, dir := range []string{own, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("admin"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scope := normalizeWorkspaceScope(primary, nil)
	scope.ReadOnlyRoots = []string{source, own}
	scope.RejectScopeExpansion = true
	if _, err := executeRead(scope, map[string]any{"path": filepath.Join(own, "HEAD")}); err != nil {
		t.Fatalf("own admin denied: %v", err)
	}
	if _, err := executeRead(scope, map[string]any{"path": filepath.Join(other, "HEAD")}); err == nil {
		t.Fatal("unrelated admin read allowed")
	}
	if _, err := executeList(scope, map[string]any{"path": other}); err == nil {
		t.Fatal("unrelated admin list allowed")
	}
	if targets, err := resolveSearchTargets(scope, map[string]any{"path": other}); err == nil {
		closeSearchTargets(targets)
		t.Fatal("unrelated admin discovery allowed")
	}
}

// Purpose: search/find must select the same explicit/default target and reject
// conflicting selectors before permission expansion. Testing their shared
// resolver avoids indexing/provider dependencies while asserting exact roots.
func TestWorkspaceTargetSearchSelection(t *testing.T) {
	primary, linked, outside := t.TempDir(), t.TempDir(), t.TempDir()
	scope := normalizeWorkspaceScope(primary, []string{linked})
	for _, tc := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{}, []string{primary}},
		{map[string]any{"path": "."}, []string{primary}},
		{map[string]any{"path": linked}, []string{linked}},
		{map[string]any{"paths": []string{".", linked}}, []string{primary, linked}},
	} {
		targets, err := resolveSearchTargets(scope, tc.args)
		if err != nil {
			t.Fatal(err)
		}
		closeSearchTargets(targets)
		if len(targets) != len(tc.want) {
			t.Fatalf("targets=%v want=%v", targets, tc.want)
		}
		for i, want := range tc.want {
			if targets[i].Root != want {
				t.Fatalf("root=%s want=%s", targets[i].Root, want)
			}
		}
	}
	for _, args := range []map[string]any{
		{"path": primary, "paths": []string{linked}},
		{"paths": []string{primary, outside}},
	} {
		if targets, err := resolveSearchTargets(scope, args); err == nil {
			closeSearchTargets(targets)
			t.Fatalf("invalid target accepted: %v", args)
		}
	}
	for _, name := range []string{"search", "find"} {
		raw, err := json.Marshal(map[string]any{"path": primary, "paths": []string{outside}})
		if err != nil {
			t.Fatal(err)
		}
		if _, needed, err := ScopeExpansionForCall(scope, Call{Name: name, Arguments: string(raw)}); err == nil || needed {
			t.Fatalf("%s ambiguous request must fail without expansion: needed=%v err=%v", name, needed, err)
		}
	}
}

// Purpose: linked roots are access grants, not implicit execution defaults.
// normalizeWorkspaceScope and the Bash executor must reject missing primary
// authority before spawning or creating command output; no shell is needed.
func TestWorkspaceTargetMissingPrimary(t *testing.T) {
	linked := t.TempDir()
	scope := normalizeWorkspaceScope("", []string{linked})
	if scope.PrimaryPath != "" || len(scope.Roots) != 1 || scope.Roots[0] != linked {
		t.Fatalf("normalization promoted or lost linked authority: %+v", scope)
	}
	if _, err := executeBashCommand(context.Background(), scope, map[string]any{}, "printf bad > marker", nil); err == nil || !strings.Contains(err.Error(), "workspace path is required") {
		t.Fatalf("missing-primary Bash error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(linked, "marker")); !os.IsNotExist(err) {
		t.Fatalf("Bash executed in linked root: %v", err)
	}
	if targets, err := resolveSearchTargets(scope, map[string]any{}); err == nil {
		closeSearchTargets(targets)
		t.Fatal("missing-primary default search accepted")
	}
}
