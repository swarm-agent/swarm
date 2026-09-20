package run

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type capacityBoundaryRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (*capacityBoundaryRunner) ID() string { return "capacity-fake" }
func (r *capacityBoundaryRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *capacityBoundaryRunner) CreateResponseStreaming(ctx context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	select {
	case r.entered <- struct{}{}:
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	}
	select {
	case <-r.release:
		return provideriface.Response{Text: "done"}, nil
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	}
}
func capacityBoundaryFixture(t *testing.T) (*Service, pebblestore.SessionSnapshot, RunOptions, *capacityBoundaryRunner) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "capacity"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	sess, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{UserID: "user-cap", AccountScopeID: "account-cap", WorkspacePath: t.TempDir(), Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "capacity-fake", Model: "test", Thinking: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	perms := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	perms.SetSessionResolver(sessions)
	if _, err = perms.UpdateActiveExecutionLimitForAccount(sess.AccountScopeID, 1); err != nil {
		t.Fatal(err)
	}
	runner := &capacityBoundaryRunner{entered: make(chan struct{}, 4), release: make(chan struct{})}
	providers := registry.New()
	providers.RegisterRunner(runner)
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	svc := NewService(sessions, model.NewService(pebblestore.NewModelStore(store), events, nil), providers, tool.NewRuntime(1), perms, agents, nil, events)
	profile := agentruntime.CoderAgentProfileForParent(pebblestore.AgentProfile{Provider: "capacity-fake", Model: "test", Thinking: "off"})
	options := RunOptions{Prompt: "bounded task", RunID: "child-run", TargetKind: RunTargetKindSubagent, TargetName: profile.Name, AgentName: profile.Name, AllowSubagent: true, TrustedAgentProfile: &profile, Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: sess.UserID, AccountScopeID: sess.AccountScopeID}}
	return svc, sess, options, runner
}
func capacityBoundaryAwait(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("capacity boundary did not settle")
		case <-tick.C:
		}
	}
}

// Purpose: Service.runTurn must use the same permission-owned admission pool as
// V3, before lifecycle/message writes. A parked parent at cap=1 must allow an
// actual delegated provider turn; a queued stop must never reach the provider.
// This hermetic service test exercises RunTurnWithOptions, not a mock admission.
func TestCapacityBoundaryDelegatedAdmissionAndStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent_yields", true: "stop_pending"}[stop], func(t *testing.T) {
			svc, sess, options, runner := capacityBoundaryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			parent, err := svc.permissions.AdmitExecution(ctx, executioncapacity.AcquireRequest{AccountScopeID: sess.AccountScopeID, SessionID: "parent", RunID: "parent-run"})
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Release()
			done := make(chan error, 1)
			joined := false
			go func() {
				_, err := svc.RunTurnWithOptions(executioncapacity.WithLease(ctx, parent), sess.ID, options)
				done <- err
			}()
			defer func() {
				cancel()
				if joined {
					return
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("child did not stop")
				}
			}()
			capacityBoundaryAwait(t, func() bool { return svc.ExecutionCapacitySnapshot(sess.AccountScopeID).Pending == 1 })
			msgs, err := svc.sessions.ListMessages(sess.ID, 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 0 {
				t.Fatalf("queued turn wrote messages: %+v", msgs)
			}
			if stop {
				if err := svc.StopSessionRun(sess.ID, options.RunID, "cancel pending"); err != nil {
					t.Fatal(err)
				}
				capacityBoundaryAwait(t, func() bool { return svc.ExecutionCapacitySnapshot(sess.AccountScopeID).Pending == 0 })
				select {
				case err := <-done:
					joined = true
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("stopped admission result: %v", err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case <-runner.entered:
					t.Fatal("cancelled queued run entered provider")
				default:
				}
				return
			}
			if err := parent.Park(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-runner.entered:
			case err := <-done:
				t.Fatalf("child failed before provider: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if snap := svc.ExecutionCapacitySnapshot(sess.AccountScopeID); snap.TotalActive != 1 {
				t.Fatalf("not using shared pool: %+v", snap)
			}
			close(runner.release)
			select {
			case err := <-done:
				joined = true
				if err != nil {
					t.Fatalf("delegated completion: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			capacityBoundaryAwait(t, func() bool { return svc.ExecutionCapacitySnapshot(sess.AccountScopeID).TotalActive == 0 })
			if err := parent.Reacquire(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Purpose: internal compaction may borrow only an active same-account/session
// context lease even though it uses a new run ID; standalone compaction still
// queues. RunTurnWithOptions reaches the real compact implementation (missing
// compact settings deliberately fail after admission, never by timing out).
func TestCapacityBoundaryCompactionAdmission(t *testing.T) {
	svc, sess, options, _ := capacityBoundaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	owner, err := svc.permissions.AdmitExecution(ctx, executioncapacity.AcquireRequest{AccountScopeID: sess.AccountScopeID, SessionID: sess.ID, RunID: "outer"})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	options.Compact = true
	options.RunID = "internal-compact"
	_, err = svc.RunTurnWithOptions(executioncapacity.WithLease(ctx, owner), sess.ID, options)
	if errors.Is(err, context.DeadlineExceeded) || err == nil || !strings.Contains(err.Error(), "compact") {
		t.Fatalf("internal compact did not reach compact boundary: %v", err)
	}
	if snap := svc.ExecutionCapacitySnapshot(sess.AccountScopeID); snap.TotalActive != 1 || !owner.IsActive() {
		t.Fatalf("internal compact released/duplicated owner: %+v", snap)
	}
	options.RunID = "standalone-compact"
	standalone, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	_, err = svc.RunTurnWithOptions(standalone, sess.ID, options)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("standalone compact bypassed slot: %v", err)
	}
}

// Purpose: the direct V3 manual compact entry point is distinct from runTurn.
// It must not bypass the account ceiling or use another principal's session.
// RunManualCompaction is the narrowest layer before any compact provider/write.
func TestCapacityBoundaryDirectManualCompact(t *testing.T) {
	svc, sess, options, _ := capacityBoundaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	owner, err := svc.permissions.AdmitExecution(ctx, executioncapacity.AcquireRequest{AccountScopeID: sess.AccountScopeID, SessionID: sess.ID, RunID: "outer"})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	input := ManualCompactionInput{RunID: "compact", Principal: options.Principal, ApplySessionMutation: svc.sessions.ApplySessionMutation}
	queued, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if _, err := svc.RunManualCompaction(queued, sess.ID, input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("direct compact bypassed ceiling: %v", err)
	}
	if _, err := svc.RunManualCompaction(executioncapacity.WithLease(ctx, owner), sess.ID, input); err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "compact") {
		t.Fatalf("borrowed compact did not reach compact boundary: %v", err)
	}
	input.Principal.AccountScopeID = "foreign"
	if _, err := svc.RunManualCompaction(ctx, sess.ID, input); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("foreign compact: %v", err)
	}
	if snap := svc.ExecutionCapacitySnapshot(sess.AccountScopeID); snap.TotalActive != 1 || snap.Pending != 0 {
		t.Fatalf("compact leaked owner: %+v", snap)
	}
}

// Purpose: usage can change while an admitted child waits for capacity. The
// post-admission check in runTurn must reject before any provider or message.
func TestCapacityBoundaryUsageChangedWhileQueued(t *testing.T) {
	svc, sess, options, runner := capacityBoundaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	owner, err := svc.permissions.AdmitExecution(ctx, executioncapacity.AcquireRequest{AccountScopeID: sess.AccountScopeID, SessionID: "other", RunID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	done := make(chan error, 1)
	go func() { _, err := svc.RunTurnWithOptions(ctx, sess.ID, options); done <- err }()
	capacityBoundaryAwait(t, func() bool { return svc.ExecutionCapacitySnapshot(sess.AccountScopeID).Pending == 1 })
	if _, err := svc.sessions.SetUsageLimit(sess.AccountScopeID, 0.01, 0, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, _, _, err := svc.sessions.RecordTurnUsage(sess.ID, pebblestore.SessionTurnUsageSnapshot{SessionID: sess.ID, UserID: sess.UserID, AccountScopeID: sess.AccountScopeID, RunID: "previous", Provider: "capacity-fake", Model: "test", EstimatedCostUSD: 0.02, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "daily usage limit") {
			t.Fatalf("queued usage refusal=%v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-runner.entered:
		t.Fatal("usage refused run reached provider")
	default:
	}
	msgs, err := svc.sessions.ListMessages(sess.ID, 0, 20)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("usage refusal wrote messages=%+v err=%v", msgs, err)
	}
	if s := svc.ExecutionCapacitySnapshot(sess.AccountScopeID); s.TotalActive != 0 || s.Pending != 0 {
		t.Fatalf("usage refusal leaked %+v", s)
	}
}

// Purpose: runtime instructions must disclose the account ceiling and real
// counts, never invent availability on authority failure. ComposeRuntimeInstructions
// uses this formatter for ordinary V3 and delegated model requests.
func TestCapacityBoundaryRuntimeFacts(t *testing.T) {
	text := executionCapacityInstructions(executioncapacity.Snapshot{EffectiveLimit: 7, TotalActive: 4, DeployedActive: 2, Pending: 1, Available: 3, DeploymentBatchBound: 8})
	for _, want := range []string{"effective_overall_cap: 7", "total_active: 4", "deployed_active: 2", "available_slots: 3", "deployment_batch_bound: 8", "saved_session_quota: null", "point-in-time"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	unavailable := executionCapacityInstructions(executioncapacity.Snapshot{Unavailable: true})
	if !strings.Contains(unavailable, "unavailable") || strings.Contains(unavailable, "slots available") {
		t.Fatalf("fabricated facts %q", unavailable)
	}
}
