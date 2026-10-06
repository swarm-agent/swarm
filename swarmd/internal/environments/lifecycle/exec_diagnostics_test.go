package lifecycle

import (
	"context"
	"errors"
	"strings"
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
			if err != nil { t.Fatal(err) }
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			h.supervisedProv.execFn = func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
				req.OnProgress(provider.ExecProgress{Stream: "stdout", Data: []byte("partial assertion\n"), Timestamp: time.Now()})
				req.OnProgress(provider.ExecProgress{Stream: "stderr", Data: []byte("partial error\n"), Timestamp: time.Now()})
				close(started)
				if mode == "deadline" || mode == "cancel" || mode == "cleanup-hang" || mode == "cancel-cleanup-hang" { <-release; return nil, ctx.Err() }
				code := 0
				if mode == "failure" { code = 7 }
				return &provider.ExecResult{ExitCode: code, Stdout: strings.Repeat("x", 2500)+"FINAL ASSERTION\n", Stderr: "failure detail\nAuthorization: Bearer fixture-value\n"}, nil
			}
			if mode == "cleanup-hang" || mode == "cancel-cleanup-hang" {
				h.supervisedProv.cancelFunc = func(context.Context, *environments.Connection, *environments.Deployment, provider.CancelExecRequest) (*provider.CancelExecResult, error) { <-release; return nil, provider.ErrOperationNotConfirmed }
			}
			req := SubmitOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace", Action: "exec", DeploymentID: dep.Deployment.ID, LeaseID: dep.Lease.ID, Command: []string{"true"}, MaxOutput: 4096, Timeout: 80*time.Millisecond, Attribution: environments.OperationAttribution{SessionID: "consumer"}}
			if mode == "bounded" { req.MaxOutput = 32 }
			op, err := h.manager.Submit(context.Background(), req)
			if err != nil { t.Fatal(err) }
			select { case <-started: case <-time.After(time.Second): t.Fatal("exec did not start") }
			if mode == "cancel" || mode == "cancel-cleanup-hang" {
				if _, err := h.manager.Cancel(context.Background(), CancelOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace", OperationID: op.OperationID}); err != nil { t.Fatal(err) }
			}
			final := awaitDiagnosticOperation(t, h, op.OperationID)
			want := environments.OperationStatusSucceeded
			switch mode {
			case "failure": want = environments.OperationStatusFailed
			case "deadline": want = environments.OperationStatusTimedOut
			case "cancel": want = environments.OperationStatusCancelled
			case "cleanup-hang", "cancel-cleanup-hang": want = environments.OperationStatusUnknown
			}
			if final.Status != want || final.IsActive() { t.Fatalf("unsettled outcome: %+v", final) }
			if strings.Contains(final.Result.Stderr, "fixture-value") { t.Fatal("credential persisted") }
			if mode == "bounded" {
				if len(final.Result.Stdout) > 32 || len(final.Result.Stderr) > 32 || !final.Result.Truncated { t.Fatalf("durable output cap lost: %+v", final.Result) }
			} else if mode == "success" || mode == "failure" {
				if !strings.Contains(final.Result.Stdout, "FINAL ASSERTION") || !strings.Contains(final.Result.Stderr, "failure detail") { t.Fatalf("durable diagnostics lost: %+v", final.Result) }
				if mode == "failure" && final.Result.ExitCode != 7 { t.Fatal("actual exit status lost") }
			} else {
				if final.Result.Stdout != "partial assertion\n" || final.Result.Stderr != "partial error\n" { t.Fatalf("partial output lost: %+v", final.Result) }
				if mode == "cleanup-hang" || mode == "cancel-cleanup-hang" {
					if !strings.Contains(final.Result.ErrorMessage, "restore connection access") { t.Fatal("no actionable cleanup evidence") }
					if _, err := h.manager.Submit(context.Background(), req); !errors.Is(err, environments.ErrDeploymentOperationBlocked) { t.Fatalf("unconfirmed remote work reusable: %v", err) }
				} else {
					// Confirmed cleanup must not retain capacity behind a hung output reader.
					deadline := time.Now().Add(time.Second)
					for len(h.manager.opSem) != 0 && time.Now().Before(deadline) { time.Sleep(time.Millisecond) }
					if len(h.manager.opSem) != 0 { t.Fatal("confirmed cleanup retained admission capacity") }
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
		if err != nil || !found { t.Fatalf("get: %v", err) }
		if !op.IsActive() { return &op }
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
	for i := 0; i < cap(h.manager.opSem); i++ { h.manager.opSem <- struct{}{} }
	defer func() { for len(h.manager.opSem) > 0 { <-h.manager.opSem } }()
	now := time.Now().UnixMilli()
	op, _, err := h.opStore.AdmitOperation(environments.EnvironmentOperation{OperationID: "op_queued_deadline", AccountScopeID: "account", WorkspaceID: "workspace", Action: "exec", Status: environments.OperationStatusQueued, CreatedAt: now, ObservedAt: now, Deadline: now+30})
	if err != nil { t.Fatal(err) }
	go h.manager.superviseOperation(op, SubmitOperationRequest{Action: "exec"}, nil, nil, nil)
	final := awaitDiagnosticOperation(t, h, op.OperationID)
	if final.Status != environments.OperationStatusTimedOut || final.CompletedAt == 0 { t.Fatalf("queued deadline ignored: %+v", final) }
}
