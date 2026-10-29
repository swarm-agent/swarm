package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type managedSessionFixture struct {
	manageSessionService
	snapshot pebblestore.SessionSnapshot
}

func (f *managedSessionFixture) GetSession(string) (pebblestore.SessionSnapshot, bool, error) {
	return f.snapshot, true, nil
}

type managedExecFixture struct {
	manageDeploymentLifecycleService
	lease   environments.DeploymentLease
	calls   int
	request provider.ExecRequest
	failure error
	stdout  string
}

func (f *managedExecFixture) GetLease(string, string, string) (environments.DeploymentLease, bool, error) {
	return f.lease, true, nil
}
func (f *managedExecFixture) ExecTaskLease(_ context.Context, _, _, _ string, _ environments.OperationAttribution, req provider.ExecRequest) (*provider.ExecResult, error) {
	f.calls++
	f.request = req
	if f.failure != nil {
		return nil, f.failure
	}
	if f.stdout != "" {
		return &provider.ExecResult{Stdout: f.stdout}, nil
	}
	return &provider.ExecResult{Stdout: "managed-only", ExitCode: 7}, nil
}
func newManagedExecutionFixture(t *testing.T) (*Runtime, WorkspaceScope, *managedSessionFixture, *managedExecFixture) {
	t.Helper()
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account", SessionID: "child"}
	s := &managedSessionFixture{snapshot: pebblestore.SessionSnapshot{ID: "child", UserID: p.UserID, AccountScopeID: p.AccountScopeID, Metadata: map[string]any{ManagedExecutionMetadataKey: ManagedExecutionBinding{WorkspaceID: "workspace", LeaseID: "receipt"}, "project_id": "project", "task_id": "task", "task_attempt_id": "attempt"}}}
	d := &managedExecFixture{lease: environments.DeploymentLease{ID: "receipt", AccountScopeID: p.AccountScopeID, WorkspaceID: "workspace", DeploymentID: "deployment", ConsumerType: environments.ConsumerTypeSession, ConsumerID: "child", Active: true, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), TaskBinding: &environments.TaskLeaseBinding{ProjectID: "project", TaskID: "task", AttemptID: "attempt", UserID: p.UserID}, PreparedSource: &environments.PreparedDeploymentSource{}}}
	r := NewRuntime(0)
	r.sessions, r.deploymentManager = s, d
	return r, WorkspaceScope{PrimaryPath: t.TempDir(), SessionID: "child", Principal: p}, s, d
}

// Requirement: explicit managed selection overrides all local sandbox choices.
// Threat: invalid/foreign/stale receipts or cwd/source overrides executing on the
// host. Production executeOne/managedExecutionBinding/executeManagedBash own this
// dispatch boundary. A recording lifecycle double is the narrowest test of that
// boundary; it does NOT qualify Podman/provider execution or remote file editing.
func TestManagedExecutionDispatch(t *testing.T) {
	for _, name := range []string{"success", "wrong_session", "wrong_account", "wrong_attempt", "wrong_task", "expired", "released", "malformed", "provider_rejected", "override", "host_write", "host_git", "host_artifact", "custom"} {
		t.Run(name, func(t *testing.T) {
			r, scope, sessions, deployments := newManagedExecutionFixture(t)
			marker := filepath.Join(scope.PrimaryPath, "host-marker")
			args, _ := json.Marshal(map[string]any{"command": "touch " + marker, "category": "write", "critical": false, "explanation": []string{"Create the isolated test marker"}})
			call := Call{Name: "bash", Arguments: string(args)}
			switch name {
			case "wrong_session":
				deployments.lease.ConsumerID = "another-child"
			case "wrong_account":
				deployments.lease.AccountScopeID = "another-account"
			case "wrong_attempt":
				deployments.lease.TaskBinding.AttemptID = "previous"
			case "wrong_task":
				deployments.lease.TaskBinding.TaskID = "other"
			case "expired":
				deployments.lease.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
			case "released":
				deployments.lease.Active = false
			case "malformed":
				sessions.snapshot.Metadata[ManagedExecutionMetadataKey] = "receipt"
			case "provider_rejected":
				deployments.failure = errors.New("attachment detached")
			case "override":
				call.Arguments = `{"command":"pwd","workspace_path":"."}`
			case "host_write":
				call.Name = "write"
				call.Arguments = `{"path":"host-marker","content":"unauthorized"}`
			case "host_git":
				call.Name = "git_init"
				call.Arguments = `{}`
			case "custom":
				call.Name = "custom-shell"
			case "host_artifact":
				call.Name = "artifact_v3_author"
			}
			output, err := r.executeOne(context.Background(), scope, call, nil)
			if name == "success" {
				if err != nil {
					t.Fatal(err)
				}
				var result map[string]any
				if json.Unmarshal([]byte(output), &result) != nil || result["execution_mode"] != "managed" || result["lease_id"] != "receipt" || result["exit_code"] != float64(7) || result["output"] != "managed-only" {
					t.Fatalf("wrong managed output: %s", output)
				}
				if deployments.calls != 1 || deployments.request.WorkingDir != "" || deployments.request.Timeout <= 0 {
					t.Fatalf("wrong managed request: %#v", deployments.request)
				}
			} else if err == nil {
				t.Fatalf("expected rejection, got %s", output)
			}
			if name != "success" && name != "provider_rejected" && deployments.calls != 0 {
				t.Fatal("rejected request reached provider")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("host marker changed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(scope.PrimaryPath, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("host git changed: %v", err)
			}
		})
	}
}

// Requirement: managed execution refuses only a request to background a
// command (background:true or a background_id); background:false is an
// ordinary foreground command and runs in the managed environment.
// Threat: the refusal checked only that the key was present, so an agent
// spelling out the default (background:false) could not run anything there.
// Owner: executeOne's managed bash branch. The recording lease double is the
// narrowest layer that observes whether the command reached the lease.
func TestManagedExecutionBackgroundFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		extra   map[string]any
		refused bool
	}{
		{"background_false_runs", map[string]any{"background": false}, false},
		{"background_true_refused", map[string]any{"background": true}, true},
		{"background_id_refused", map[string]any{"background_id": "bg_x", "command": "status"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, scope, _, deployments := newManagedExecutionFixture(t)
			args := map[string]any{"command": "true", "category": "read", "critical": false, "explanation": []string{"Run a no-op"}}
			for k, v := range tc.extra {
				args[k] = v
			}
			raw, _ := json.Marshal(args)
			_, err := r.executeOne(context.Background(), scope, Call{Name: "bash", Arguments: string(raw)}, nil)
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "not available in managed execution") {
					t.Fatalf("err = %v, want managed background refusal", err)
				}
				if deployments.calls != 0 {
					t.Fatalf("refused request reached the lease %d times", deployments.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("background:false refused: %v", err)
			}
			if deployments.calls != 1 {
				t.Fatalf("lease calls = %d, want 1", deployments.calls)
			}
		})
	}
}

// Requirement: managed Bash output over the tool output cap keeps its head
// and tail, says plainly that the full output is unavailable, and offers no
// saved path; nothing is written to the host child worktree.
// Threat: the marker and full_output_path point the agent at a host file
// that its managed read (which runs in the lease checkout) and search
// (refused in managed mode) cannot reach.
// Owner: executeManagedBash with bashResponseCapped/capToolOutputTextUnsaved.
// The recording lease double is the narrowest layer that drives the managed
// branch; it does not qualify Podman execution.
func TestManagedBashCappedOutputNotSavedOnHost(t *testing.T) {
	r, scope, _, deployments := newManagedExecutionFixture(t)
	deployments.stdout = "HEAD-MARKER\n" + strings.Repeat("managed output line\n", 4*ToolOutputCapBytes()/20) + "TAIL-MARKER\n"
	raw, _ := json.Marshal(map[string]any{"command": "go test ./...", "category": "read", "critical": false, "explanation": []string{"Run tests"}})
	output, err := r.executeOne(context.Background(), scope, Call{Name: "bash", Arguments: string(raw)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	text, _ := result["output"].(string)
	if result["execution_mode"] != "managed" || result["output_capped"] != true || len(text) > ToolOutputCapBytes() {
		t.Fatalf("managed output not capped: mode=%v capped=%v len=%d", result["execution_mode"], result["output_capped"], len(text))
	}
	if !strings.HasPrefix(text, "HEAD-MARKER") || !strings.HasSuffix(strings.TrimSpace(text), "TAIL-MARKER") {
		t.Fatal("head or tail not kept")
	}
	if !strings.Contains(text, "bytes omitted; full output unavailable: ") || strings.Contains(text, "saved to") || strings.Contains(text, "read it with read/search") {
		t.Fatalf("marker does not say the full output is unavailable: %q", text)
	}
	if _, ok := result["full_output_path"]; ok {
		t.Fatalf("managed result offers a saved path: %v", result["full_output_path"])
	}
	if result["full_output_error"] != managedFullOutputUnavailable {
		t.Fatalf("full_output_error = %v", result["full_output_error"])
	}
	if _, err := os.Stat(filepath.Join(scope.PrimaryPath, ".swarm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed output written to the host worktree: %v", err)
	}
}
