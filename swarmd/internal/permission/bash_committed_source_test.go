package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Requirement: committed-source Bash must follow normal command approval for
// its actual child cwd. Threat: auto-approving a benign parent-relative file and
// then reading a child symlink outside that scope. Owners: parseBashSimpleCommand,
// BashCommandReadsWorkspacePaths and explainPolicyInScope. This policy-layer test
// proves the decision before any command can run, including malformed retargets.
func TestCommittedSourceBashCannotInheritParentReadApproval(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(root, "child")
	for _, dir := range []string{parent, child} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(parent, "input.txt"), []byte("benign parent fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(child, "input.txt")); err != nil {
		t.Fatal(err)
	}
	scope := &tool.WorkspaceScope{PrimaryPath: parent, Roots: []string{parent}}
	args := map[string]any{"command": "cat input.txt", "explanation": []string{"Inspect input"}, "category": "read", "critical": false}
	encode := func() string {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	ordinary := encode()
	if !BashCommandReadsWorkspacePaths(ordinary) {
		t.Fatal("ordinary scoped read no longer resolves its cwd")
	}
	if got := explainPolicyInScope("auto", "bash", ordinary, DefaultPolicy(), scope); got.Decision != PolicyDecisionAllow || got.Source != "builtin" {
		t.Fatalf("ordinary scoped read changed: %#v", got)
	}
	for name, value := range map[string]any{"valid": map[string]any{"task_call_id": "call-test", "child_session_id": "child-test", "head_commit": "0123456789012345678901234567890123456789"}, "null": nil, "empty": map[string]any{}, "wrong-type": "child-test"} {
		t.Run(name, func(t *testing.T) {
			args["committed_source"] = value
			raw := encode()
			if BashCommandReadsWorkspacePaths(raw) {
				t.Fatal("retarget selected parent permission scope")
			}
			got := explainPolicyInScope("auto", "bash", raw, DefaultPolicy(), scope)
			if got.Decision != PolicyDecisionAsk {
				t.Fatalf("retarget bypassed approval: %#v", got)
			}
			if got := explainPolicyInScope("read", "bash", raw, DefaultPolicy(), scope); got.Decision != PolicyDecisionDeny {
				t.Fatalf("restricted role gained Bash: %#v", got)
			}
		})
	}
	// Policy evaluation must leave both the benign parent and child symlink intact.
	if got, err := os.ReadFile(filepath.Join(parent, "input.txt")); err != nil || string(got) != "benign parent fixture" {
		t.Fatalf("parent changed: %q %v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(child, "input.txt")); err != nil || got != outside {
		t.Fatalf("child changed: %q %v", got, err)
	}
}
