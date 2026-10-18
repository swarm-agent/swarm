package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
)

type managedCommandFixture struct {
	*managedExecFixture
	root   string
	before func()
}

func (f *managedCommandFixture) ExecTaskLease(ctx context.Context, _, _, _ string, _ environments.OperationAttribution, req provider.ExecRequest) (*provider.ExecResult, error) {
	f.calls++
	if f.failure != nil {
		return nil, f.failure
	}
	if f.before != nil {
		f.before()
	}
	c := exec.CommandContext(ctx, req.Command[0], req.Command[1:]...)
	c.Dir = f.root
	out, err := c.CombinedOutput()
	code := 0
	if err != nil {
		var e *exec.ExitError
		if !errors.As(err, &e) {
			return nil, err
		}
		code = e.ExitCode()
	}
	return &provider.ExecResult{Stdout: string(out), ExitCode: code, OperationID: "deterministic-command"}, nil
}

// Requirement: managed file tools edit the assigned remote checkout only and
// obey existing edit semantics, scopes and stale-content rejection. Threat:
// host fallback, traversal/symlinks, lost updates and invalid lease execution.
// Runtime dispatch and managedCommand own this boundary. Real Bash in a temp
// directory behind a recording lifecycle double isolates file semantics; this
// is not evidence of live Podman, provider or agent execution.
func TestManagedCheckoutFileTools(t *testing.T) {
	for _, tc := range []string{"edit", "write", "read", "traversal", "symlink", "scope", "expired", "replaced_content", "missing_checkout"} {
		t.Run(tc, func(t *testing.T) {
			r, scope, _, d := newManagedExecutionFixture(t)
			scope.WorktreeEnabled = true
			scope.MutationScopes = []string{"fix.txt"}
			d.lease.TaskBinding.ParentLeaseID = "parent"
			remote := t.TempDir()
			checkout := filepath.Join(remote, managedCheckoutName(d.lease))
			if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{filepath.Join(checkout, ".git/swarm-ready"): "base", filepath.Join(checkout, "fix.txt"): "broken\n", filepath.Join(scope.PrimaryPath, "fix.txt"): "host unchanged\n"} {
				if err := os.WriteFile(name, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &managedCommandFixture{managedExecFixture: d, root: remote}
			r.deploymentManager = runner
			args := map[string]any{"path": "fix.txt", "old_string": "broken", "new_string": "fixed"}
			name := "edit"
			switch tc {
			case "write":
				name = "write"
				args["content"] = "fixed\n"
			case "read":
				name = "read"
			case "traversal":
				args["path"] = "../fix.txt"
			case "symlink":
				if err := os.Symlink(filepath.Join(scope.PrimaryPath, "fix.txt"), filepath.Join(checkout, "link")); err != nil {
					t.Fatal(err)
				}
				name = "read"
				args["path"] = "link"
			case "scope":
				args["path"] = "elsewhere"
			case "expired":
				d.lease.Active = false
			case "replaced_content":
				runner.before = func() {
					if d.calls == 2 {
						_ = os.WriteFile(filepath.Join(checkout, "fix.txt"), []byte("concurrent change\n"), 0600)
					}
				}
			case "missing_checkout":
				if err := os.Remove(filepath.Join(checkout, ".git/swarm-ready")); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(args)
			out, err := r.executeOne(context.Background(), scope, Call{Name: name, Arguments: string(raw)}, nil)
			pass := tc == "edit" || tc == "write" || tc == "read"
			if pass && err != nil {
				t.Fatal(err)
			}
			if !pass && err == nil {
				t.Fatalf("unsafe operation accepted: %s", out)
			}
			local, _ := os.ReadFile(filepath.Join(scope.PrimaryPath, "fix.txt"))
			if string(local) != "host unchanged\n" {
				t.Fatal("host changed")
			}
			got, _ := os.ReadFile(filepath.Join(checkout, "fix.txt"))
			want := "broken\n"
			if tc == "edit" || tc == "write" {
				want = "fixed\n"
			}
			if tc == "replaced_content" {
				want = "concurrent change\n"
			}
			if string(got) != want {
				t.Fatalf("remote content %q want %q", got, want)
			}
			if pass && (!strings.Contains(out, `"execution_mode":"managed"`) || strings.Contains(out, scope.PrimaryPath)) {
				t.Fatalf("wrong managed result %s", out)
			}
			if (tc == "traversal" || tc == "scope" || tc == "expired") && d.calls != 0 {
				t.Fatal("rejected operation reached provider")
			}
		})
	}
}
