package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
)

// Purpose: superviseOperation/executeAction and the real Pebble operation store
// must preserve both capped streams and actual nonzero status, and settle a
// deadline/cancellation even if Exec and CancelExec ignore context. This service
// layer proves persistence, blocked reuse and capacity postconditions without SSH.
func TestManagedExecDurableDiagnostics(t *testing.T) {
	for _, mode := range []string{"success", "failure", "bounded", "deadline", "cancel", "cleanup-hang", "cancel-cleanup-hang"} {
		t.Run(mode, func(t *testing.T) {
			h := setupSupervisedHarness(t, WithCleanupTimeout(30*time.Millisecond))
			conn := createTestConnection(t, h.connections, "account", "workspace", "docker", "Docker")
			env := createTestEnvironment(t, h.environments, "account", "workspace", "env", conn.ID, true, 2, environments.ReleaseBehaviorNone)
			dep, err := h.manager.EnsureDeployment(context.Background(), EnsureDeploymentRequest{AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "consumer"})
			if err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			execDone, cleanupDone := make(chan struct{}), make(chan struct{})
			defer func() {
				close(release)
				select {
				case <-started:
					select {
					case <-execDone:
					case <-time.After(time.Second):
						t.Error("fixture exec goroutine did not return")
					}
				default:
				}
				if (mode == "cleanup-hang" || mode == "cancel-cleanup-hang") && atomic.LoadInt32(&h.supervisedProv.cancelCalls) != 0 {
					select {
					case <-cleanupDone:
					case <-time.After(time.Second):
						t.Error("fixture cleanup goroutine did not return")
					}
				}
			}()
			h.supervisedProv.execFn = func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
				defer close(execDone)
				req.OnProgress(provider.ExecProgress{Stream: "stdout", Data: []byte("partial assertion\n"), Timestamp: time.Now()})
				req.OnProgress(provider.ExecProgress{Stream: "stderr", Data: []byte("partial error\n"), Timestamp: time.Now()})
				close(started)
				if mode == "deadline" || mode == "cancel" || mode == "cleanup-hang" || mode == "cancel-cleanup-hang" {
					<-release
					return nil, ctx.Err()
				}
				code := 0
				if mode == "failure" {
					code = 7
				}
				return &provider.ExecResult{ExitCode: code, Stdout: strings.Repeat("x", 2500) + "FINAL ASSERTION\n", Stderr: "failure detail\nAuthorization: Bearer fixture-value\n"}, nil
			}
			if mode == "cleanup-hang" || mode == "cancel-cleanup-hang" {
				h.supervisedProv.cancelFunc = func(context.Context, *environments.Connection, *environments.Deployment, provider.CancelExecRequest) (*provider.CancelExecResult, error) {
					defer close(cleanupDone)
					<-release
					return nil, provider.ErrOperationNotConfirmed
				}
			}
			req := SubmitOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace", Action: "exec", DeploymentID: dep.Deployment.ID, LeaseID: dep.Lease.ID, Command: []string{"true"}, MaxOutput: 4096, Timeout: 80 * time.Millisecond, Attribution: environments.OperationAttribution{SessionID: "consumer"}}
			if mode == "bounded" {
				req.MaxOutput = 32
			}
			op, err := h.manager.Submit(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("exec did not start")
			}
			if mode == "cancel" || mode == "cancel-cleanup-hang" {
				if _, err := h.manager.Cancel(context.Background(), CancelOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace", OperationID: op.OperationID}); err != nil {
					t.Fatal(err)
				}
			}
			final := awaitDiagnosticOperation(t, h, op.OperationID)
			want := environments.OperationStatusSucceeded
			switch mode {
			case "failure":
				want = environments.OperationStatusFailed
			case "deadline":
				want = environments.OperationStatusTimedOut
			case "cancel":
				want = environments.OperationStatusCancelled
			case "cleanup-hang", "cancel-cleanup-hang":
				want = environments.OperationStatusUnknown
			}
			if final.Status != want || final.IsActive() {
				t.Fatalf("unsettled outcome: %+v", final)
			}
			if strings.Contains(final.Result.Stderr, "fixture-value") {
				t.Fatal("credential persisted")
			}
			if mode == "bounded" {
				if len(final.Result.Stdout) > 32 || len(final.Result.Stderr) > 32 || !final.Result.Truncated {
					t.Fatalf("durable output cap lost: %+v", final.Result)
				}
			} else if mode == "success" || mode == "failure" {
				if !strings.Contains(final.Result.Stdout, "FINAL ASSERTION") || !strings.Contains(final.Result.Stderr, "failure detail") {
					t.Fatalf("durable diagnostics lost: %+v", final.Result)
				}
				if mode == "failure" && final.Result.ExitCode != 7 {
					t.Fatal("actual exit status lost")
				}
			} else {
				if final.Result.Stdout != "partial assertion\n" || final.Result.Stderr != "partial error\n" {
					t.Fatalf("partial output lost: %+v", final.Result)
				}
				if mode == "cleanup-hang" || mode == "cancel-cleanup-hang" {
					if !strings.Contains(final.Result.ErrorMessage, "restore connection access") {
						t.Fatal("no actionable cleanup evidence")
					}
					if _, err := h.manager.Submit(context.Background(), req); !errors.Is(err, environments.ErrDeploymentOperationBlocked) {
						t.Fatalf("unconfirmed remote work reusable: %v", err)
					}
				} else {
					// Confirmed cleanup must not retain capacity behind a hung output reader.
					deadline := time.Now().Add(time.Second)
					for len(h.manager.opSem) != 0 && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if len(h.manager.opSem) != 0 {
						t.Fatal("confirmed cleanup retained admission capacity")
					}
				}
			}
		})
	}
}

func awaitDiagnosticOperation(t *testing.T, h *supervisedTestHarness, id string) *environments.EnvironmentOperation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		op, found, err := h.manager.Get(context.Background(), "account", "workspace", id)
		if err != nil || !found {
			t.Fatalf("get: %v", err)
		}
		if !op.IsActive() {
			return &op
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operation did not settle within bounded cleanup window")
	return nil
}

// Purpose: durable admission deadlines include semaphore waiting, not just
// provider execution. superviseOperation plus real store proves an exhausted
// manager cannot leave queued deployment admission locks indefinitely.
func TestManagedExecQueuedDeadline(t *testing.T) {
	h := setupSupervisedHarness(t)
	for i := 0; i < cap(h.manager.opSem); i++ {
		h.manager.opSem <- struct{}{}
	}
	t.Cleanup(func() {
		for len(h.manager.opSem) > 0 {
			<-h.manager.opSem
		}
	})
	now := time.Now().UnixMilli()
	op, _, err := h.opStore.AdmitOperation(environments.EnvironmentOperation{OperationID: "op_queued_deadline", AccountScopeID: "account", WorkspaceID: "workspace", Action: "exec", Status: environments.OperationStatusQueued, CreatedAt: now, ObservedAt: now, Deadline: now + 30})
	if err != nil {
		t.Fatal(err)
	}
	h.manager.activeOpsMu.Lock()
	state := h.manager.launchOperationLocked(op, SubmitOperationRequest{Action: "exec"}, nil, nil, nil)
	h.manager.activeOpsMu.Unlock()
	// Join the actual supervisor, not merely a visible terminal projection:
	// publication and deferred bookkeeping may still be in flight then.
	t.Cleanup(func() {
		state.cancel()
		select {
		case <-state.done:
		case <-time.After(time.Second):
			t.Fatal("queued supervisor did not join before store teardown")
		}
	})
	final := awaitDiagnosticOperation(t, h, op.OperationID)
	if final.Status != environments.OperationStatusTimedOut || final.CompletedAt == 0 {
		t.Fatalf("queued deadline ignored: %+v", final)
	}
	select {
	case <-state.done:
	case <-time.After(time.Second):
		t.Fatal("queued deadline settled without joining supervisor")
	}
	if final.StartedAt != 0 || !strings.Contains(final.Result.ErrorMessage, "provider was not started") {
		t.Fatalf("queued deadline claimed execution or cleanup: %+v", final)
	}
}

// Purpose: Submit/launchOperationLocked must register queued supervision before
// capacity is available; Close must settle and join it before storage teardown.
// Cancellation of queued work must not invoke remote cleanup for a process that
// was never started. Real Pebble plus an injected provider is the narrowest
// layer proving the durable admission lock and goroutine-lifetime postconditions.
func TestManagedExecQueuedShutdown(t *testing.T) {
	for _, mode := range []string{"close", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			h := setupSupervisedHarness(t, WithMaxConcurrentOps(1))
			conn := createTestConnection(t, h.connections, "account", "workspace", "docker", "Docker")
			env := createTestEnvironment(t, h.environments, "account", "workspace", "env", conn.ID, true, 1, environments.ReleaseBehaviorNone)
			dep, err := h.manager.EnsureDeployment(context.Background(), EnsureDeploymentRequest{
				AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID,
				ConsumerType: environments.ConsumerTypeSession, ConsumerID: "consumer",
			})
			if err != nil {
				t.Fatal(err)
			}
			h.manager.opSem <- struct{}{}
			defer func() { <-h.manager.opSem }()
			req := SubmitOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace", Action: "exec",
				DeploymentID: dep.Deployment.ID, LeaseID: dep.Lease.ID, Command: []string{"true"},
				Timeout: time.Minute, Attribution: environments.OperationAttribution{SessionID: "consumer"}}
			op, err := h.manager.Submit(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			h.manager.activeOpsMu.RLock()
			state := h.manager.activeOps[op.OperationID]
			h.manager.activeOpsMu.RUnlock()
			if state == nil {
				t.Fatal("Submit returned before queued supervision registration")
			}
			if mode == "cancel" {
				if _, err := h.manager.Cancel(context.Background(), CancelOperationRequest{
					AccountScopeID: "account", WorkspaceID: "workspace", OperationID: op.OperationID,
				}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-state.done:
				case <-time.After(time.Second):
					t.Fatal("queued cancel did not settle and join")
				}
			}
			if err := h.manager.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-state.done:
			default:
				t.Fatal("Close returned before queued supervisor joined")
			}
			final := awaitDiagnosticOperation(t, h, op.OperationID)
			if final.Status != environments.OperationStatusCancelled || final.StartedAt != 0 || final.CompletedAt == 0 {
				t.Fatalf("queued work not cancelled before Close returned: %+v", final)
			}
			if atomic.LoadInt32(&h.supervisedProv.execCalls) != 0 || atomic.LoadInt32(&h.supervisedProv.cancelCalls) != 0 {
				t.Fatal("queued cancellation reached provider")
			}
			h.manager.activeOpsMu.RLock()
			remaining := len(h.manager.activeOps)
			h.manager.activeOpsMu.RUnlock()
			if remaining != 0 || len(h.manager.opSem) != 1 {
				t.Fatal("supervision or capacity bookkeeping leaked")
			}
			active, found, err := h.opStore.GetActiveOperationForDeployment("account", "workspace", dep.Deployment.ID)
			if err != nil || found {
				t.Fatalf("queued admission lock retained: %+v %v", active, err)
			}
			if _, err := h.manager.Submit(context.Background(), req); err == nil {
				t.Fatal("operation admitted after Close")
			}
		})
	}
}

// Purpose: Close must join durable cancellation/heartbeat supervision even when
// provider Exec or CancelExec ignores context. The bounded cleanup window must
// preserve uncertainty and partial output, not release unconfirmed capacity or
// claim process termination. Real stores and releasable blocked provider calls
// prove shutdown ordering without leaving permanent fixture goroutines.
func TestManagedExecRunningShutdown(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprintf("confirmed=%v", confirmed), func(t *testing.T) {
			h := setupSupervisedHarness(t, WithMaxConcurrentOps(1), WithCleanupTimeout(30*time.Millisecond))
			conn := createTestConnection(t, h.connections, "account", "workspace", "docker", "Docker")
			env := createTestEnvironment(t, h.environments, "account", "workspace", "env", conn.ID, true, 1, environments.ReleaseBehaviorNone)
			dep, err := h.manager.EnsureDeployment(context.Background(), EnsureDeploymentRequest{
				AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID,
				ConsumerType: environments.ConsumerTypeSession, ConsumerID: "consumer",
			})
			if err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			execDone, cleanupDone := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() {
				close(release)
				select {
				case <-execDone:
				case <-time.After(time.Second):
					t.Error("blocked exec fixture did not return")
				}
				if !confirmed && atomic.LoadInt32(&h.supervisedProv.cancelCalls) != 0 {
					select {
					case <-cleanupDone:
					case <-time.After(time.Second):
						t.Error("blocked cleanup fixture did not return")
					}
				}
			})
			h.supervisedProv.execFn = func(ctx context.Context, _ *environments.Connection, _ *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
				defer close(execDone)
				req.OnProgress(provider.ExecProgress{Stream: "stdout", Data: []byte("before shutdown\n"), Timestamp: time.Now()})
				close(started)
				<-release
				// A late callback must not touch storage after the supervisor joins.
				req.OnProgress(provider.ExecProgress{Stream: "stdout", Data: []byte("late success\n"), Timestamp: time.Now()})
				return nil, ctx.Err()
			}
			if !confirmed {
				h.supervisedProv.cancelFunc = func(context.Context, *environments.Connection, *environments.Deployment, provider.CancelExecRequest) (*provider.CancelExecResult, error) {
					defer close(cleanupDone)
					<-release
					return nil, provider.ErrOperationNotConfirmed
				}
			}
			op, err := h.manager.Submit(context.Background(), SubmitOperationRequest{
				AccountScopeID: "account", WorkspaceID: "workspace", Action: "exec", DeploymentID: dep.Deployment.ID,
				LeaseID: dep.Lease.ID, Command: []string{"true"}, Timeout: time.Minute,
				Attribution: environments.OperationAttribution{SessionID: "consumer"},
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("exec did not start")
			}
			h.manager.activeOpsMu.RLock()
			state := h.manager.activeOps[op.OperationID]
			h.manager.activeOpsMu.RUnlock()
			if state == nil {
				t.Fatal("running supervision missing")
			}
			if err := h.manager.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-state.done:
			default:
				t.Fatal("Close returned before running supervisor joined")
			}
			final := awaitDiagnosticOperation(t, h, op.OperationID)
			want, capacity := environments.OperationStatusCancelled, 0
			if !confirmed {
				want, capacity = environments.OperationStatusUnknown, 1
			}
			if final.Status != want || final.Result.Stdout != "before shutdown\n" || len(h.manager.opSem) != capacity {
				t.Fatalf("shutdown lost cleanup/output/capacity contract: %+v capacity=%d", final, len(h.manager.opSem))
			}
		})
	}
}
