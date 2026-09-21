package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// mockSupervisedProvider implements provider.DeploymentProvider and provider.OperationCanceler.
type mockSupervisedProvider struct {
	mockProvider

	cancelCalls int32
	cancelFunc  func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.CancelExecRequest) (*provider.CancelExecResult, error)
	execFn      func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error)
}

func newMockSupervisedProvider(kind environments.ConnectionKind) *mockSupervisedProvider {
	return &mockSupervisedProvider{
		mockProvider: mockProvider{kind: kind},
	}
}

func (m *mockSupervisedProvider) CancelExec(
	ctx context.Context,
	conn *environments.Connection,
	dep *environments.Deployment,
	req provider.CancelExecRequest,
) (*provider.CancelExecResult, error) {
	atomic.AddInt32(&m.cancelCalls, 1)
	if m.cancelFunc != nil {
		return m.cancelFunc(ctx, conn, dep, req)
	}
	return &provider.CancelExecResult{
		OperationID: req.OperationID,
		Terminated:  true,
		ObservedAt:  time.Now(),
		SignalSent:  "SIGTERM",
	}, nil
}

func (m *mockSupervisedProvider) Exec(
	ctx context.Context,
	conn *environments.Connection,
	dep *environments.Deployment,
	req provider.ExecRequest,
) (*provider.ExecResult, error) {
	atomic.AddInt32(&m.execCalls, 1)
	if m.execFn != nil {
		return m.execFn(ctx, conn, dep, req)
	}
	return m.mockProvider.Exec(ctx, conn, dep, req)
}

type supervisedTestHarness struct {
	testHarness
	supervisedProv *mockSupervisedProvider
	opStore        *pebblestore.EnvironmentOperationStore
}

func setupSupervisedHarness(t *testing.T, opts ...DeploymentManagerOption) *supervisedTestHarness {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "supervised_test.pebble")
	store, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	cs := pebblestore.NewConnectionStore(store)
	es := pebblestore.NewEnvironmentStore(store)
	ds := pebblestore.NewDeploymentStore(store)
	ws := pebblestore.NewWorkspaceStore(store)
	ops := ds.Operations()

	prov := newMockSupervisedProvider(environments.ConnectionKindLocalDocker)
	reg := provider.NewRegistry()
	reg.Register(prov)

	allOpts := append([]DeploymentManagerOption{
		WithOperationStore(ops),
		WithHeartbeatInterval(20 * time.Millisecond),
		WithCleanupTimeout(100 * time.Millisecond),
		WithProbeTimeout(100 * time.Millisecond),
	}, opts...)

	mgr := NewDeploymentManager(cs, es, ds, ws, reg, allOpts...)
	t.Cleanup(func() {
		_ = mgr.Close()
	})

	return &supervisedTestHarness{
		testHarness: testHarness{
			store:        store,
			connections:  cs,
			environments: es,
			deployments:  ds,
			workspaces:   ws,
			mockProv:     &prov.mockProvider,
			manager:      mgr,
		},
		supervisedProv: prov,
		opStore:        ops,
	}
}

// 1. Test: provider hangs ignoring cancellation -> bounded cleanup timeout transitions to cleanup_failed/unknown
// and blocks conflicting reuse.
func TestSupervisedOperation_ProviderHangsIgnoringCancellation(t *testing.T) {
	h := setupSupervisedHarness(t, WithCleanupTimeout(50*time.Millisecond))
	ctx := context.Background()
	accountScope := "acc-hang"
	workspaceID := "ws-hang"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-hang", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-hang", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-hang",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	// Provider hangs on Exec and ignores cancellation
	execStarted := make(chan struct{})
	h.supervisedProv.execFn = func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
		close(execStarted)
		<-ctx.Done()
		// Provider hangs even after context is cancelled
		time.Sleep(500 * time.Millisecond)
		return nil, ctx.Err()
	}

	// CancelExec also fails/hangs
	h.supervisedProv.cancelFunc = func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.CancelExecRequest) (*provider.CancelExecResult, error) {
		return &provider.CancelExecResult{
			OperationID:  req.OperationID,
			Terminated:   false,
			ErrorMessage: "container unresponsive",
		}, provider.ErrOperationCleanupFailed
	}

	op, err := h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		Command:        []string{"sleep", "60"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-hang",
		},
	})
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	<-execStarted

	// Cancel the operation
	_, err = h.manager.Cancel(ctx, CancelOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		OperationID:    op.OperationID,
		Reason:         "user cancelled hanging command",
	})
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	// Wait for cleanup timeout to elapse
	time.Sleep(150 * time.Millisecond)

	finalOp, found, err := h.manager.Get(ctx, accountScope, workspaceID, op.OperationID)
	if err != nil || !found {
		t.Fatalf("Get operation failed: %v", err)
	}

	if finalOp.Status != environments.OperationStatusCleanupFailed && finalOp.Status != environments.OperationStatusUnknown {
		t.Errorf("expected cleanup_failed or unknown, got status %q", finalOp.Status)
	}

	// Verify conflicting reuse is blocked
	_, err = h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		Command:        []string{"echo", "next"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-hang",
		},
	})
	if !errors.Is(err, environments.ErrDeploymentOperationBlocked) {
		t.Errorf("expected ErrDeploymentOperationBlocked when deployment has unresolved operation, got %v", err)
	}
}

// 2. Test: silent heartbeat -> Activity and ObservedAt update even without provider progress events.
func TestSupervisedOperation_SilentHeartbeat(t *testing.T) {
	h := setupSupervisedHarness(t, WithHeartbeatInterval(25*time.Millisecond))
	ctx := context.Background()
	accountScope := "acc-silent"
	workspaceID := "ws-silent"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-silent", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-silent", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-silent",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	// Exec runs for 150ms without calling OnProgress
	h.supervisedProv.execFn = func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
		time.Sleep(150 * time.Millisecond)
		return &provider.ExecResult{ExitCode: 0, Stdout: "silent done"}, nil
	}

	op, err := h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		Command:        []string{"sleep", "1"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-silent",
		},
	})
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	// Wait for several heartbeats to fire
	time.Sleep(80 * time.Millisecond)

	runningOp, found, err := h.manager.Get(ctx, accountScope, workspaceID, op.OperationID)
	if err != nil || !found {
		t.Fatalf("Get failed: %v", err)
	}

	if runningOp.Activity.HeartbeatSeq <= 1 {
		t.Errorf("expected HeartbeatSeq > 1 from silent ticker, got %d", runningOp.Activity.HeartbeatSeq)
	}
	if runningOp.ObservedAt <= op.ObservedAt {
		t.Errorf("expected ObservedAt to advance, got %d vs %d", runningOp.ObservedAt, op.ObservedAt)
	}

	// Wait for completion
	time.Sleep(120 * time.Millisecond)
	termOp, _, _ := h.manager.Get(ctx, accountScope, workspaceID, op.OperationID)
	if termOp.Status != environments.OperationStatusSucceeded {
		t.Errorf("expected succeeded, got %q", termOp.Status)
	}
}

// 3. Test: duplicate admission -> idempotent reuse returns same record; conflicting parameters or overlapping operations fail.
func TestSupervisedOperation_DuplicateAdmission(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx := context.Background()
	accountScope := "acc-dup"
	workspaceID := "ws-dup"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-dup", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-dup", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-dup",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	execBlock := make(chan struct{})
	execStarted := make(chan struct{}, 1)
	var execRunCount int32
	h.supervisedProv.execFn = func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
		atomic.AddInt32(&execRunCount, 1)
		select {
		case execStarted <- struct{}{}:
		default:
		}
		<-execBlock
		return &provider.ExecResult{ExitCode: 0}, nil
	}

	// Submit 1 with idempotency key
	op1, err := h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		IdempotencyKey: "idem-dup-key",
		Command:        []string{"long-task"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-dup",
		},
	})
	if err != nil {
		t.Fatalf("Submit op1 failed: %v", err)
	}

	// Verify that op1 ACTUALLY launched supervisor and reached provider (did not mistakenly treat first admission as duplicate)
	select {
	case <-execStarted:
		// execution launched successfully
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("CRITICAL: op1 with idempotency key never launched provider execution (first admission masked as duplicate)")
	}

	// Submit 2: duplicate idempotency key with identical parameters returns op1 without second launch
	op2, err := h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		IdempotencyKey: "idem-dup-key",
		Command:        []string{"long-task"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-dup",
		},
	})
	if err != nil {
		t.Fatalf("Submit op2 failed: %v", err)
	}
	if op2.OperationID != op1.OperationID {
		t.Errorf("expected idempotent reuse of %q, got %q", op1.OperationID, op2.OperationID)
	}
	if atomic.LoadInt32(&execRunCount) != 1 {
		t.Errorf("expected exactly 1 provider execution across duplicate idempotent submits, got %d", execRunCount)
	}

	// Submit 3: duplicate idempotency key with different command (payload hash conflict) fails
	_, err = h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		IdempotencyKey: "idem-dup-key",
		Command:        []string{"different-task"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-dup",
		},
	})
	if !errors.Is(err, environments.ErrIdempotencyConflict) {
		t.Errorf("expected ErrIdempotencyConflict for altered command payload, got %v", err)
	}

	// Submit 3b: duplicate idempotency key with different action fails
	_, err = h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionStop,
		DeploymentID:   res.Deployment.ID,
		IdempotencyKey: "idem-dup-key",
		Attribution: environments.OperationAttribution{
			SessionID: "sess-dup",
		},
	})
	if !errors.Is(err, environments.ErrIdempotencyConflict) {
		t.Errorf("expected ErrIdempotencyConflict for altered action, got %v", err)
	}

	// Submit 4: new operation without idempotency key while op1 is still running on same deployment fails
	_, err = h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		IdempotencyKey: "different-key",
		Command:        []string{"another-task"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-dup",
		},
	})
	if !errors.Is(err, environments.ErrDeploymentOperationConflict) {
		t.Errorf("expected ErrDeploymentOperationConflict, got %v", err)
	}

	close(execBlock)
}

// 4. Test: cancelled lock wait -> context cancellation releases lock wait immediately without hanging.
func TestSupervisedOperation_CancelledLockWait(t *testing.T) {
	h := setupSupervisedHarness(t)
	accountScope := "acc-lock"
	workspaceID := "ws-lock"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-lock", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-lock", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	envLock := h.manager.getEnvLock(accountScope, workspaceID, env.ID)
	// Hold the environment lock intentionally
	if err := envLock.Lock(context.Background()); err != nil {
		t.Fatalf("acquire lock: %v", err)
	}

	cancelCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := h.manager.EnsureDeployment(cancelCtx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-lock",
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected error on cancelled lock wait, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected DeadlineExceeded or Canceled, got: %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("lock wait took %v; expected to abort within timeout", elapsed)
	}

	envLock.Unlock()
}

// 5. Test: wrong/expired lease -> rejected at admission.
func TestSupervisedOperation_WrongOrExpiredLease(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx := context.Background()
	accountScope := "acc-lease"
	workspaceID := "ws-lease"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-lease", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-lease", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-owner-1",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	// Case 1: Non-existent lease ID
	_, err = h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        "lease-non-existent",
		Command:        []string{"ls"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-owner-1",
		},
	})
	if !errors.Is(err, ErrLeaseNotFound) {
		t.Errorf("expected ErrLeaseNotFound, got %v", err)
	}

	// Case 2: Wrong session caller (lease held by sess-owner-1, caller is sess-intruder)
	_, err = h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		Command:        []string{"ls"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-intruder",
		},
	})
	if !errors.Is(err, ErrDeploymentLeaseHeld) {
		t.Errorf("expected ErrDeploymentLeaseHeld for wrong caller, got %v", err)
	}

	// Case 3: Release lease and attempt exec
	_, err = h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		LeaseID:        res.Lease.ID,
	})
	if err != nil {
		t.Fatalf("ReleaseDeployment failed: %v", err)
	}

	_, err = h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		Command:        []string{"ls"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-owner-1",
		},
	})
	if !errors.Is(err, ErrLeaseAlreadyReleased) {
		t.Errorf("expected ErrLeaseAlreadyReleased, got %v", err)
	}
}

// 6. Test: failed cleanup / no state loss -> operation history and deployment records retained after failed cleanup.
func TestSupervisedOperation_FailedCleanupNoStateLoss(t *testing.T) {
	h := setupSupervisedHarness(t, WithCleanupTimeout(50*time.Millisecond))
	ctx := context.Background()
	accountScope := "acc-cleanup"
	workspaceID := "ws-cleanup"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-cleanup", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-cleanup", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-cleanup",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	started := make(chan struct{})
	h.supervisedProv.execFn = func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	h.supervisedProv.cancelFunc = func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.CancelExecRequest) (*provider.CancelExecResult, error) {
		return nil, errors.New("cleanup error: container locked")
	}

	op, err := h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		Command:        []string{"work"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-cleanup",
		},
	})
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	<-started

	_, err = h.manager.Cancel(ctx, CancelOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		OperationID:    op.OperationID,
	})
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// State should NOT be lost: record must be present
	savedOp, found, err := h.manager.Get(ctx, accountScope, workspaceID, op.OperationID)
	if err != nil || !found {
		t.Fatalf("operation record lost: %v", err)
	}
	if savedOp.Status != environments.OperationStatusCleanupFailed {
		t.Errorf("expected cleanup_failed, got %q", savedOp.Status)
	}

	// Deployment record must be retained
	dep, foundDep, err := h.manager.GetDeployment(accountScope, workspaceID, res.Deployment.ID)
	if err != nil || !foundDep {
		t.Fatalf("deployment record lost: %v", err)
	}
	if dep.Health != environments.HealthStatusUnhealthy {
		t.Errorf("expected deployment health unhealthy after failed cleanup, got %q", dep.Health)
	}
}

// 7. Test: late success rejection -> provider returning 0 after cancel does not transition operation to succeeded.
func TestSupervisedOperation_LateSuccess(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx := context.Background()
	accountScope := "acc-late"
	workspaceID := "ws-late"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-late", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-late", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-late",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	started := make(chan struct{})
	cancelRequested := make(chan struct{})

	h.supervisedProv.execFn = func(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, req provider.ExecRequest) (*provider.ExecResult, error) {
		close(started)
		<-cancelRequested
		// Wait a bit to simulate late completion with exit code 0
		time.Sleep(30 * time.Millisecond)
		return &provider.ExecResult{ExitCode: 0, Stdout: "late exit 0"}, nil
	}

	op, err := h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		LeaseID:        res.Lease.ID,
		Command:        []string{"work"},
		Attribution: environments.OperationAttribution{
			SessionID: "sess-late",
		},
	})
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	<-started

	// Initiate user stop
	_, err = h.manager.Cancel(ctx, CancelOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		OperationID:    op.OperationID,
		Reason:         "user stop",
	})
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	close(cancelRequested)

	time.Sleep(100 * time.Millisecond)

	finalOp, found, err := h.manager.Get(ctx, accountScope, workspaceID, op.OperationID)
	if err != nil || !found {
		t.Fatalf("Get failed: %v", err)
	}

	if finalOp.Status == environments.OperationStatusSucceeded {
		t.Errorf("CRITICAL: late success erroneously accepted! Status is %q", finalOp.Status)
	}
	if finalOp.Status != environments.OperationStatusCancelled {
		t.Errorf("expected status cancelled, got %q", finalOp.Status)
	}
}

// 8. Test: restart reconciliation -> nonterminal operations reconciled without replaying commands.
func TestSupervisedOperation_RestartReconciliation(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx := context.Background()
	accountScope := "acc-reconcile"
	workspaceID := "ws-reconcile"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-rec", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-rec", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-rec",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	now := time.Now().UnixMilli()

	// 1. Manually insert queued op (simulating crash before supervisor started)
	queuedOp := environments.EnvironmentOperation{
		OperationID:    "op-queued-crash",
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		Status:         environments.OperationStatusQueued,
		Revision:       1,
		CreatedAt:      now,
		ObservedAt:     now,
		Deadline:       now + 60000,
		Attribution: environments.OperationAttribution{
			SessionID: "sess-rec",
		},
	}
	_, _, err = h.opStore.AdmitOperation(queuedOp)
	if err != nil {
		t.Fatalf("Admit queued op: %v", err)
	}

	// 2. Call Recover on startup
	execCallsBefore := atomic.LoadInt32(&h.supervisedProv.execCalls)
	err = h.manager.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover failed: %v", err)
	}

	// Assert NO commands replayed
	if atomic.LoadInt32(&h.supervisedProv.execCalls) != execCallsBefore {
		t.Errorf("expected no commands to be replayed during Recover")
	}

	// Assert queued op was failed
	reconciledOp, found, err := h.manager.Get(ctx, accountScope, workspaceID, queuedOp.OperationID)
	if err != nil || !found {
		t.Fatalf("Get reconciled op: %v", err)
	}
	if reconciledOp.Status != environments.OperationStatusFailed {
		t.Errorf("expected queued op to be failed on recovery, got %q", reconciledOp.Status)
	}
	if reconciledOp.Result.FailureKind != "daemon_restart" {
		t.Errorf("expected failure_kind=daemon_restart, got %q", reconciledOp.Result.FailureKind)
	}
}

// 9. Test: store failures -> errors are propagated, not swallowed, without misleading success.
func TestSupervisedOperation_StoreFailures(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store_fail.pebble")
	store, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cs := pebblestore.NewConnectionStore(store)
	es := pebblestore.NewEnvironmentStore(store)
	ds := pebblestore.NewDeploymentStore(store)
	ws := pebblestore.NewWorkspaceStore(store)

	reg := provider.NewRegistry()
	prov := newMockSupervisedProvider(environments.ConnectionKindLocalDocker)
	reg.Register(prov)

	mgr := NewDeploymentManager(cs, es, ds, ws, reg)

	// Close store prematurely to induce store failures
	_ = store.Close()

	ctx := context.Background()
	_, err = mgr.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: "acc-fail",
		WorkspaceID:    "ws-fail",
		Action:         environments.OperationActionEnsure,
		EnvironmentID:  "env-fail",
		Attribution: environments.OperationAttribution{
			SessionID: "sess-fail",
		},
	})
	if err == nil {
		t.Errorf("expected error when store is closed, got nil")
	}

	err = mgr.DestroyDeployment(ctx, DestroyDeploymentRequest{
		AccountScopeID: "acc-fail",
		WorkspaceID:    "ws-fail",
		DeploymentID:   "dep-fail",
	})
	if err == nil {
		t.Errorf("expected error when store is closed, got nil")
	}
}

// 10. Test: restart from reopened store -> nonterminal operations reconciled, summary updated, no commands replayed.
func TestSupervisedOperation_RestartFromReopenedStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reopen_test.pebble")
	store, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open initial store: %v", err)
	}

	cs := pebblestore.NewConnectionStore(store)
	es := pebblestore.NewEnvironmentStore(store)
	ds := pebblestore.NewDeploymentStore(store)
	ws := pebblestore.NewWorkspaceStore(store)
	ops := ds.Operations()

	prov := newMockSupervisedProvider(environments.ConnectionKindLocalDocker)
	reg := provider.NewRegistry()
	reg.Register(prov)

	mgr := NewDeploymentManager(cs, es, ds, ws, reg, WithOperationStore(ops))

	ctx := context.Background()
	accountScope := "acc-reopen"
	workspaceID := "ws-reopen"

	conn := createTestConnection(t, cs, accountScope, workspaceID, "conn-reopen", "Docker")
	env := createTestEnvironment(t, es, accountScope, workspaceID, "env-reopen", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := mgr.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-reopen",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	// Admit a running operation directly in store before shutdown
	now := time.Now().UnixMilli()
	opRunning := environments.EnvironmentOperation{
		OperationID:    "op-crashed-running",
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionExec,
		DeploymentID:   res.Deployment.ID,
		Status:         environments.OperationStatusQueued,
		CreatedAt:      now,
		Deadline:       now + 60000,
		Attribution: environments.OperationAttribution{
			SessionID: "sess-reopen",
		},
	}
	admittedRunning, _, err := ops.AdmitOperation(opRunning)
	if err != nil {
		t.Fatalf("admit opRunning: %v", err)
	}
	_, err = ops.TransitionOperation(pebblestore.OperationTransitionInput{
		AccountScopeID:   accountScope,
		WorkspaceID:      workspaceID,
		OperationID:      admittedRunning.OperationID,
		ExpectedRevision: admittedRunning.Revision,
		TargetStatus:     environments.OperationStatusRunning,
		ObservedAt:       now,
	})
	if err != nil {
		t.Fatalf("transition opRunning to running: %v", err)
	}

	// Close manager and store
	_ = mgr.Close()
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopen store from disk
	reopenedStore, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopenedStore.Close() }()

	reopenedCS := pebblestore.NewConnectionStore(reopenedStore)
	reopenedES := pebblestore.NewEnvironmentStore(reopenedStore)
	reopenedDS := pebblestore.NewDeploymentStore(reopenedStore)
	reopenedWS := pebblestore.NewWorkspaceStore(reopenedStore)
	reopenedOps := reopenedDS.Operations()

	reopenedProv := newMockSupervisedProvider(environments.ConnectionKindLocalDocker)
	reopenedReg := provider.NewRegistry()
	reopenedReg.Register(reopenedProv)

	reopenedMgr := NewDeploymentManager(reopenedCS, reopenedES, reopenedDS, reopenedWS, reopenedReg, WithOperationStore(reopenedOps))
	defer func() { _ = reopenedMgr.Close() }()

	execCallsBefore := atomic.LoadInt32(&reopenedProv.execCalls)

	// Execute Recover on reopened manager
	if err := reopenedMgr.Recover(ctx); err != nil {
		t.Fatalf("Recover on reopened store failed: %v", err)
	}

	// Verify no commands replayed
	if atomic.LoadInt32(&reopenedProv.execCalls) != execCallsBefore {
		t.Errorf("expected 0 commands replayed upon recover from reopened store")
	}

	// Verify operation reconciled to cancelled
	recOp, found, err := reopenedMgr.Get(ctx, accountScope, workspaceID, admittedRunning.OperationID)
	if err != nil || !found {
		t.Fatalf("Get reconciled op from reopened store: %v", err)
	}
	if recOp.Status != environments.OperationStatusCancelled {
		t.Errorf("expected cancelled status after recover, got %q", recOp.Status)
	}

	// Verify summary recalculation updated active counts correctly
	sum, err := reopenedMgr.Summary(ctx, accountScope, workspaceID)
	if err != nil {
		t.Fatalf("Summary failed: %v", err)
	}
	if sum.RunningOps != 0 {
		t.Errorf("expected 0 running ops after recovery, got %d", sum.RunningOps)
	}
	if sum.CancelledOps < 1 {
		t.Errorf("expected at least 1 cancelled op in summary, got %d", sum.CancelledOps)
	}
}

// 11. Test: deploy failure maintains atomic state and preserves resource reference.
func TestSupervisedOperation_DeployFailureNoResourceLoss(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx := context.Background()
	accountScope := "acc-deploy-fail"
	workspaceID := "ws-deploy-fail"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-dfail", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-dfail", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	// Simulate provider failure during deploy
	h.supervisedProv.deployErr = errors.New("docker daemon out of disk space")

	op, err := h.manager.Submit(ctx, SubmitOperationRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		Action:         environments.OperationActionDeploy,
		EnvironmentID:  env.ID,
		Attribution: environments.OperationAttribution{
			SessionID: "sess-dfail",
		},
	})
	if err != nil {
		t.Fatalf("Submit deploy operation failed: %v", err)
	}

	// Wait for execution to finish
	time.Sleep(100 * time.Millisecond)

	finalOp, found, err := h.manager.Get(ctx, accountScope, workspaceID, op.OperationID)
	if err != nil || !found {
		t.Fatalf("operation lost: %v", err)
	}
	if finalOp.Status != environments.OperationStatusFailed {
		t.Errorf("expected failed status, got %q", finalOp.Status)
	}
	// DeploymentID must be preserved on the operation record
	if finalOp.DeploymentID == "" {
		t.Errorf("expected deployment_id to be preserved on failed deploy operation")
	}

	// Deployment record must be retained in store with failed/unhealthy status
	dep, foundDep, err := h.manager.GetDeployment(accountScope, workspaceID, finalOp.DeploymentID)
	if err != nil || !foundDep {
		t.Fatalf("deployment record lost from store on deploy failure: %v", err)
	}
	if dep.Status != environments.DeploymentStatusFailed {
		t.Errorf("expected deployment status failed, got %q", dep.Status)
	}
	if dep.Health != environments.HealthStatusUnhealthy {
		t.Errorf("expected deployment health unhealthy, got %q", dep.Health)
	}
}

// 12. Test: inspect on unreachable provider sets health unhealthy/unknown, never fake healthy.
func TestSupervisedOperation_InspectUnreachableProvider(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx := context.Background()
	accountScope := "acc-inspect"
	workspaceID := "ws-inspect"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-inspect", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-inspect", conn.ID, true, 2, environments.ReleaseBehaviorNone)

	res, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-inspect",
	})
	if err != nil {
		t.Fatalf("EnsureDeployment failed: %v", err)
	}

	// Provider inspect returns error (simulating container crashed or docker daemon unreachable)
	h.supervisedProv.inspectErr = errors.New("cannot connect to docker daemon: connection refused")

	inspected, err := h.manager.InspectDeployment(ctx, accountScope, workspaceID, res.Deployment.ID)
	if err == nil {
		t.Fatalf("expected error from InspectDeployment on unreachable provider, got nil")
	}
	if inspected == nil {
		t.Fatalf("expected inspected deployment record returned, got nil")
	}
	if inspected.Health == environments.HealthStatusHealthy {
		t.Errorf("CRITICAL: unreachable provider must not report healthy! got health=%q", inspected.Health)
	}
	if inspected.Health != environments.HealthStatusUnhealthy && inspected.Health != environments.HealthStatusUnknown {
		t.Errorf("expected unhealthy or unknown, got %q", inspected.Health)
	}
}

// 13. Test: automatic lease release on reuse of expired deployment.
func TestSupervisedOperation_ExpiredLeaseReleasedBeforeReuse(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx := context.Background()
	accountScope := "acc-exp-reuse"
	workspaceID := "ws-exp-reuse"

	conn := createTestConnection(t, h.connections, accountScope, workspaceID, "conn-exp", "Docker")
	env := createTestEnvironment(t, h.environments, accountScope, workspaceID, "env-exp", conn.ID, true, 1, environments.ReleaseBehaviorNone)

	// Ensure deployment with short 10ms lease
	res1, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-first",
		TTLMillis:      10,
	})
	if err != nil {
		t.Fatalf("EnsureDeployment 1 failed: %v", err)
	}

	// Wait for lease to expire
	time.Sleep(25 * time.Millisecond)

	// Ensure deployment with second consumer: must automatically release expired lease and acquire fresh lease for sess-second
	res2, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{
		AccountScopeID: accountScope,
		WorkspaceID:    workspaceID,
		EnvironmentID:  env.ID,
		ConsumerType:   environments.ConsumerTypeSession,
		ConsumerID:     "sess-second",
		TTLMillis:      60000,
	})
	if err != nil {
		t.Fatalf("EnsureDeployment 2 failed: %v", err)
	}
	if res2.Deployment.ID != res1.Deployment.ID {
		t.Errorf("expected reuse of deployment %q, got %q", res1.Deployment.ID, res2.Deployment.ID)
	}
	if res2.Lease.ConsumerID != "sess-second" {
		t.Errorf("expected new lease for sess-second, got consumer_id=%q", res2.Lease.ConsumerID)
	}
	if res2.Lease.ID == res1.Lease.ID {
		t.Errorf("expected fresh lease ID, got same ID %q", res2.Lease.ID)
	}
}
