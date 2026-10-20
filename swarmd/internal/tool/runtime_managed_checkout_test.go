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

// Requirement: parsed managed checkout results (file receipt, staging path)
// come only from the command's own payload. Threat: the managed wrapper merges
// stderr into stdout and runs a login shell, so profile/logout output, BASH_ENV
// output and locale warnings (for example in a sandbox whose user has no passwd
// entry) corrupted the mktemp staging path and file receipts. Owners:
// managedCommandOutput, managedFramedOutput, executeManagedFile. Real Bash with
// injected shell-startup noise behind the recording lifecycle double is the
// narrowest layer that exercises the actual wrapper; it is not evidence of live
// Podman or provider execution.
func TestManagedCheckoutParsingIgnoresShellNoise(t *testing.T) {
	home := t.TempDir()
	noise := "printf '%s\\n' '.git/swarm-file.decoy' present >&2; printf '%s\\n' 'noise on stdout' absent\n"
	for name, content := range map[string]string{".bash_profile": noise, ".bash_logout": noise, "bash_env": noise} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("BASH_ENV", filepath.Join(home, "bash_env"))
	t.Setenv("LC_ALL", "swarm_INVALID.UTF-8")

	for _, name := range []string{"write", "edit"} {
		t.Run(name, func(t *testing.T) {
			r, scope, _, d := newManagedExecutionFixture(t)
			scope.WorktreeEnabled = true
			scope.MutationScopes = []string{"fix.txt"}
			d.lease.TaskBinding.ParentLeaseID = "parent"
			remote := t.TempDir()
			checkout := filepath.Join(remote, managedCheckoutName(d.lease))
			if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			for path, content := range map[string]string{filepath.Join(checkout, ".git/swarm-ready"): "base", filepath.Join(checkout, "fix.txt"): "broken\n", filepath.Join(scope.PrimaryPath, "fix.txt"): "host unchanged\n"} {
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r.deploymentManager = &managedCommandFixture{managedExecFixture: d, root: remote}

			binding, err := r.managedExecutionBinding(scope)
			if err != nil || binding == nil {
				t.Fatalf("managed binding: %v", err)
			}
			// The injected noise is really present in the raw combined output.
			raw, err := r.managedCommand(context.Background(), scope, *binding, "mktemp .git/swarm-file.XXXXXXXX", true)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(raw.Stdout, ".git/swarm-file.decoy") || !strings.Contains(raw.Stdout, "noise on stdout") {
				t.Fatalf("shell noise was not injected: %q", raw.Stdout)
			}
			staged, err := r.managedCommandOutput(context.Background(), scope, *binding, "mktemp .git/swarm-file.XXXXXXXX")
			if err != nil {
				t.Fatal(err)
			}
			staging := strings.TrimSpace(staged)
			if filepath.Dir(staging) != ".git" || !strings.HasPrefix(staging, ".git/swarm-file.") || staging == ".git/swarm-file.decoy" {
				t.Fatalf("wrong staging path %q", staging)
			}
			if info, err := os.Stat(filepath.Join(checkout, staging)); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("staging path %q not created in checkout: %v", staging, err)
			}

			args := map[string]any{"path": "fix.txt", "old_string": "broken", "new_string": "fixed"}
			if name == "write" {
				args["content"] = "fixed\n"
			}
			encoded, _ := json.Marshal(args)
			out, err := r.executeOne(context.Background(), scope, Call{Name: name, Arguments: string(encoded)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, `"execution_mode":"managed"`) {
				t.Fatalf("wrong managed result %s", out)
			}
			got, _ := os.ReadFile(filepath.Join(checkout, "fix.txt"))
			if string(got) != "fixed\n" {
				t.Fatalf("remote content %q", got)
			}
			if local, _ := os.ReadFile(filepath.Join(scope.PrimaryPath, "fix.txt")); string(local) != "host unchanged\n" {
				t.Fatal("host changed")
			}
		})
	}
}

// Requirement: a framed managed result fails closed unless exactly one frame
// with the per-call markers is present. Threat: noise that omits, repeats or
// forges a frame being accepted as payload. Owner: managedFramedOutput.
func TestManagedFramedOutputRejectsMissingOrRepeatedFrames(t *testing.T) {
	begin, end := managedFrameMarkers("abc")
	frame := func(payload string) string { return "\n" + begin + "\n" + payload + "\n" + end + "\n" }
	got, err := managedFramedOutput("login noise\n"+frame(".git/swarm-file.X1")+"logout noise\n", begin, end)
	if err != nil || got != ".git/swarm-file.X1" {
		t.Fatalf("framed payload %q err %v", got, err)
	}
	otherBegin, otherEnd := managedFrameMarkers("other")
	for _, stdout := range []string{
		"",
		".git/swarm-file.X1\n",
		"\n" + begin + "\n.git/swarm-file.X1\n",
		frame("a") + frame("b"),
		"\n" + otherBegin + "\nforged\n" + otherEnd + "\n",
		"\n" + end + "\nx\n" + begin + "\n",
	} {
		if got, err := managedFramedOutput(stdout, begin, end); err == nil {
			t.Fatalf("accepted %q as %q", stdout, got)
		}
	}
}
