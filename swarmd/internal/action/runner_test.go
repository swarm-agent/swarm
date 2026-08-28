package action

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestResolveActionEntrypointRejectsWorkspaceEscape(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(workspace, "scripts", "run")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveActionEntrypoint(workspace, "scripts/run")
	if err != nil {
		t.Fatalf("resolve contained entrypoint: %v", err)
	}
	if resolved != inside {
		t.Fatalf("resolved entrypoint = %q, want %q", resolved, inside)
	}

	outsideDir := filepath.Join(root, "outside-dir")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(outsideDir, "outside")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveActionEntrypoint(workspace, filepath.Join("..", "outside-dir", "outside")); err == nil {
		t.Fatal("accepted relative entrypoint that escapes the workspace")
	}

	link := filepath.Join(workspace, "scripts", "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := resolveActionEntrypoint(workspace, "scripts/outside-link"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("symlink escape error = %v, want workspace escape rejection", err)
	}
}

func TestResolveActionEntrypointFailsClosedForInvalidTargets(t *testing.T) {
	workspace := t.TempDir()
	if _, err := resolveActionEntrypoint("relative/workspace", "run"); err == nil {
		t.Fatal("accepted non-absolute workspace")
	}
	if _, err := resolveActionEntrypoint(workspace, "missing"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing entrypoint error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveActionEntrypoint(workspace, "directory"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory entrypoint error = %v", err)
	}
}

func TestAssembleActionArgumentsPreservesStructuredArgv(t *testing.T) {
	action := pebblestore.WorkspaceAction{
		Arguments: []string{"--fixed", "$(touch should-not-run)"},
		Inputs: []pebblestore.WorkspaceActionInput{
			{ID: "name", Required: true, Arguments: []string{"--name"}},
			{ID: "mode", Default: "safe", Arguments: []string{"--mode"}},
		},
	}

	argv, err := assembleActionArguments(action, map[string]string{"name": "; touch still-not-run"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--fixed", "$(touch should-not-run)", "--name", "; touch still-not-run", "--mode", "safe"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}

	if _, err := assembleActionArguments(action, map[string]string{"unknown": "value", "name": "ok"}); err == nil {
		t.Fatal("accepted unknown action input")
	}
	if _, err := assembleActionArguments(action, nil); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing required input error = %v", err)
	}
	if _, err := assembleActionArguments(action, map[string]string{"name": "bad\x00value"}); err == nil || !strings.Contains(err.Error(), "null byte") {
		t.Fatalf("null-byte input error = %v", err)
	}
}

func TestRunnerSnapshotsAreExactScopeBound(t *testing.T) {
	run := &actionRun{
		scope: Scope{
			AccountScopeID: "account-a",
			WorkspaceID:    "workspace-a",
			WorkspacePath:  "/workspace/a",
		},
		snapshot: RunSnapshot{ID: "run-a", Status: ActionRunStatusSucceeded},
		output:   newCappedOutput(32),
	}
	runner := &Runner{runs: map[string]*actionRun{"run-a": run}}

	allowed := run.scope
	if _, found, err := runner.Get(allowed, "run-a"); err != nil || !found {
		t.Fatalf("same-scope lookup = found:%v err:%v", found, err)
	}
	for name, scope := range map[string]Scope{
		"account":   {AccountScopeID: "account-b", WorkspaceID: "workspace-a", WorkspacePath: "/workspace/a"},
		"workspace": {AccountScopeID: "account-a", WorkspaceID: "workspace-b", WorkspacePath: "/workspace/a"},
		"path":      {AccountScopeID: "account-a", WorkspaceID: "workspace-a", WorkspacePath: "/workspace/b"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, found, err := runner.Get(scope, "run-a"); err != nil || found {
				t.Fatalf("cross-scope lookup = found:%v err:%v", found, err)
			}
		})
	}

	if _, _, err := runner.Get(Scope{}, "run-a"); err == nil {
		t.Fatal("accepted empty action scope")
	}
}
