package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: production permission binding must reject denied or mismatched reads
// before acceptance, including innocent filenames. Real Runtime hydration,
// permission records and executeDesign prove source bytes reach the adapter only
// after approval; this hermetic service boundary is not a live provider test.
func TestDesignSourceProductionPermission(t *testing.T) {
	for _, decision := range []string{"approve", "deny", "foreign", "missing", "scope"} {
		t.Run(decision, func(t *testing.T) {
			s, p, parent, runner := designExecutionFixture(t)
			root := t.TempDir()
			for path, text := range map[string]string{"TaskCard.tsx": "export const TaskCard = () => 'task';", "card.css": "article { color: rebeccapurple; }"} {
				if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: p.AccountID, UserID: p.PrincipalID, SessionID: parent.ParentSessionID}
			scope := tool.WorkspaceScope{PrimaryPath: root, Roots: []string{root}, SessionID: parent.ParentSessionID, Principal: principal}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if decision == "foreign" {
				principal.UserID = "foreign"
			}
			if decision != "missing" {
				ctx = identity.ContextWithPrincipal(ctx, principal)
			}
			emit := func(e StreamEvent) {
				if e.Type == StreamEventPermissionReq {
					if e.Permission.Requirement != "design_source_sensitive_read" || !strings.Contains(e.Permission.ToolArguments, `"critical":true`) {
						t.Error("not a separate sensitive permission")
					}
					action := permission.ActionAllowOnce
					if decision == "deny" {
						action = permission.ActionDenyOnce
					}
					if _, err := s.permissions.Resolve(parent.ParentSessionID, e.Permission.ID, action, ""); err != nil {
						t.Error(err)
					}
				}
			}
			ctx = s.withDesignSourcePermission(ctx, scope, parent.ParentRunID, emit)
			if decision == "scope" {
				scope.Roots = append(scope.Roots, t.TempDir())
			}
			ctx = tool.WithWorkspaceScope(ctx, scope)
			ctx = tool.WithArtifactRunContext(ctx, tool.ArtifactRunContext{SessionID: parent.ParentSessionID, RunID: parent.ParentRunID})
			s.tools.SetManageSessionService(s.sessions)
			before, _, err := s.sessions.DesignStore().ScanDesignPending("")
			if err != nil {
				t.Fatal(err)
			}
			results := s.tools.ExecuteBatchStreamingWithProgress(ctx, root, []tool.Call{{Name: "manage_design", CallID: "source", Arguments: `{"action":"submit","idempotency_key":"source","files":[{"path":"TaskCard.tsx"},{"path":"card.css"}],"candidates":[{"kind":"html","operation":"generate","brief":"style task"}]}`}}, nil, nil)
			if len(results) != 1 {
				t.Fatal("missing result")
			}
			after, _, err := s.sessions.DesignStore().ScanDesignPending("")
			if err != nil {
				t.Fatal(err)
			}
			if decision != "approve" {
				if results[0].Error == "" || len(after) != len(before) || len(runner.requests) != 0 {
					t.Fatalf("unauthorized acceptance: %+v", results)
				}
				return
			}
			if results[0].Error != "" || len(after) != len(before)+1 {
				t.Fatalf("approval failed: %+v", results)
			}
			var accepted store.DesignRequest
			for _, r := range after {
				if r.ID != parent.ID {
					accepted = r
				}
			}
			s.executeDesign(ctx, p, accepted.ID, 0)
			if len(runner.requests) != 1 {
				t.Fatal("approved request did not execute")
			}
			raw, _ := json.Marshal(runner.requests[0].Input)
			if !strings.Contains(string(raw), "export const TaskCard") || !strings.Contains(string(raw), "rebeccapurple") {
				t.Fatal("actual source bytes missing")
			}
		})
	}
}

// Purpose: unavailable account models must terminate visibly without fabricated
// children or attempts. executeDesign and real store state prove this negative
// allocation path more narrowly than a full daemon deployment.
func TestDesignExecutionUnavailableModelTerminal(t *testing.T) {
	s, p, r, runner := designExecutionFixture(t)
	s.model = nil
	s.executeDesign(context.Background(), p, r.ID, 0)
	got, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if err != nil || got.State != store.DesignFailed || got.Candidates[0].FailureReason != "model_unavailable" || got.Candidates[0].RouterAlert == "" || len(got.Candidates[0].Attempts) != 0 || len(runner.requests) != 0 {
		t.Fatalf("silent or fabricated failure: %+v %v", got, err)
	}
	if _, ok, err := s.sessions.GetSession(store.DesignChildID(p, r.ID, 0, 1)); err != nil || ok {
		t.Fatal("fabricated child", err)
	}
}

// Purpose: canonical Designer assignment, not the account default, determines
// the actual provider request. Account settings + allocation + adapter request
// is the narrowest observable model authority boundary.
func TestDesignExecutionConfiguredDesigner(t *testing.T) {
	s, p, r, runner := designExecutionFixture(t)
	db := s.sessions.DesignStore()
	if err := store.NewModelCatalogStore(db).SetRecord(store.ModelCatalogRecord{Provider: "test", Model: "designer-choice"}); err != nil {
		t.Fatal(err)
	}
	assignment := store.AgentModelAssignment{Provider: "test", Model: "designer-choice", Thinking: "low"}
	settings := store.NewAgentModelSettingsStore(db)
	if _, err := settings.PutForAccount(store.AgentModelSettingsRecord{AccountScopeID: p.AccountID, Swarm: store.SwarmAgentModelAssignments{Action: assignment, Plan: assignment}, SystemAgents: store.SystemAgentModelAssignments{Compact: assignment, Finder: assignment, Coder: assignment, Designer: assignment, Router: assignment}}); err != nil {
		t.Fatal(err)
	}
	s.agentModelSettings = agentmodelsettings.NewService(settings)
	if err := s.agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	s.executeDesign(context.Background(), p, r.ID, 0)
	got, err := db.GetDesignRequest(p, r.ID)
	if err != nil || len(runner.requests) != 1 || runner.requests[0].Model != "designer-choice" || got.Candidates[0].Attempts[0].RouterAlert != "" {
		t.Fatalf("Designer authority lost: %+v %v", got, err)
	}
}

// Purpose: daemon workers must truly overlap candidate calls while retaining
// success beside failure. Blocking channels prove concurrency without timing
// simulation; real queue/admission/CAS proves independent durable outcomes.
func TestDesignDispatcherConcurrentBatch(t *testing.T) {
	s, p, parent, runner := designExecutionFixture(t)
	if _, err := s.permissions.UpdateActiveExecutionLimitForAccount(p.AccountID, 2); err != nil {
		t.Fatal(err)
	}
	// Remove the fixture's unrelated queued candidate.
	if _, err := s.sessions.DesignStore().FailQueuedDesign(p, parent.ID, parent.Revision, 0, "allocation_unavailable"); err != nil {
		t.Fatal(err)
	}
	r := acceptDesignFixture(t, s, p, parent, "parallel", []store.DesignCandidateSpec{{ArtifactID: "parallel-one", Kind: store.DesignHTML, Operation: store.DesignGenerate, Brief: "success"}, {ArtifactID: "parallel-two", Kind: store.DesignHTML, Operation: store.DesignGenerate, Brief: "failure"}}, nil)
	entered := make(chan string, 2)
	release := make(chan struct{})
	runner.call = func(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
		entered <- req.SessionID
		select {
		case <-release:
		case <-ctx.Done():
			return provideriface.Response{}, ctx.Err()
		}
		raw, _ := json.Marshal(req.Input)
		if strings.Contains(string(raw), "failure") {
			return provideriface.Response{}, errors.New("expected adapter failure")
		}
		return provideriface.Response{Text: "<!doctype html><html><body>retained</body></html>"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := s.StartDesignDispatcher(ctx)
	defer d.Close()
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case id := <-entered:
			ids[id] = true
		case <-ctx.Done():
			t.Fatal("candidates did not overlap")
		}
	}
	if len(ids) != 2 {
		t.Fatal("shared child identity")
	}
	close(release)
	capacityBoundaryAwait(t, func() bool {
		got, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
		return err == nil && got.State == store.DesignPartial
	})
	got, _ := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if got.Candidates[0].Attempts[0].Result == nil || got.Candidates[1].State != store.DesignFailed {
		t.Fatal("lost sibling outcome")
	}
}

// Purpose: a cancellation racing after canonical completion must not rewrite
// success as interrupted. Real canonical intent and publication CAS boundaries
// prove the ordering without timing-dependent goroutine scheduling.
func TestDesignExecutionCompletedWinsLateCancellation(t *testing.T) {
	s, p, r, _ := designExecutionFixture(t)
	a, lease, err := s.AllocateDesignChild(context.Background(), p, r.ID, r.Revision, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	// Completion must now be preceded by retained output and browser evidence.
	if _, err = s.retainAndValidateDesign(context.Background(), p, r.ID, 0, provideriface.Response{Text: "<!doctype html><html></html>"}, nil, false); err != nil {
		t.Fatal(err)
	}
	if err = s.designRunState(p, a, store.V3RunIntentPendingExecutor, store.V3RunIntentCompleted); err != nil {
		t.Fatal(err)
	}
	r, _ = s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if _, err = s.sessions.DesignStore().RecordDesignAttempt(p, r.ID, store.DesignAttemptMutation{IdempotencyKey: "late-cancel", ExpectedRevision: r.Revision, Candidate: 0, ChildSessionID: a.ChildSessionID, RunID: a.RunID, State: store.DesignCancelRequested}); err != nil {
		t.Fatal(err)
	}
	if err = s.finishDesign(p, r.ID, 0, []byte("<!doctype html><html></html>"), store.DesignSucceeded); err != nil {
		t.Fatal(err)
	}
	got, _ := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	intent, _, _ := s.sessions.Store().GetV3SessionRunIntent(a.ChildSessionID, a.RunID)
	if got.State != store.DesignSucceeded || got.Candidates[0].Attempts[0].Result == nil || intent.Status != store.V3RunIntentCompleted {
		t.Fatal("completion overwritten")
	}
}

// Purpose: cancellation persisted before a daemon restart must prevent adapter
// submission even for an allocated pending child. A fresh probe ensures recovery
// really executes; the narrow dispatcher/store test needs no live provider.
func TestDesignDispatcherPendingCancellationRestart(t *testing.T) {
	s, p, r, runner := designExecutionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, lease, err := s.AllocateDesignChild(ctx, p, r.ID, r.Revision, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	r, _ = s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if _, err = s.sessions.DesignStore().RecordDesignAttempt(p, r.ID, store.DesignAttemptMutation{IdempotencyKey: "pending-stop", ExpectedRevision: r.Revision, Candidate: 0, ChildSessionID: a.ChildSessionID, RunID: a.RunID, State: store.DesignCancelRequested}); err != nil {
		t.Fatal(err)
	}
	probe := acceptDesignFixture(t, s, p, r, "after-restart", []store.DesignCandidateSpec{{ArtifactID: "after-restart-artifact", Kind: store.DesignHTML, Operation: store.DesignGenerate, Brief: "probe"}}, nil)
	d := s.StartDesignDispatcher(ctx)
	defer d.Close()
	capacityBoundaryAwait(t, func() bool {
		got, err := s.sessions.DesignStore().GetDesignRequest(p, probe.ID)
		cancelled, _ := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
		return err == nil && got.State == store.DesignSucceeded && cancelled.State == store.DesignCancelled
	})
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.requests) != 1 || runner.requests[0].SessionID == a.ChildSessionID {
		t.Fatal("cancelled child submitted")
	}
	intent, _, _ := s.sessions.Store().GetV3SessionRunIntent(a.ChildSessionID, a.RunID)
	if intent.Status != store.V3RunIntentCancelled {
		t.Fatal("canonical cancellation missing")
	}
}

// Purpose: a crash after canonical provider completion but before retaining bytes
// must terminate the artifact attempt honestly rather than queue forever or replay.
// finishDesign + canonical store receipts are the narrow recovery boundary;
// the successful sibling and completed run receipt must remain unchanged.
func TestDesignExecutionCompletedWithoutOutputRecovery(t *testing.T) {
	s, p, parent, runner := designExecutionFixture(t)
	r := acceptDesignFixture(t, s, p, parent, "lost-output", []store.DesignCandidateSpec{{ArtifactID: "lost", Kind: store.DesignHTML, Operation: store.DesignGenerate, Brief: "lost"}, {ArtifactID: "kept", Kind: store.DesignHTML, Operation: store.DesignGenerate, Brief: "kept"}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.executeDesign(ctx, p, r.ID, 1)
	r, _ = s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	kept := *r.Candidates[1].Attempts[0].Result
	a, lease, err := s.AllocateDesignChild(ctx, p, r.ID, r.Revision, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if err = s.designRunState(p, a, store.V3RunIntentPendingExecutor, store.V3RunIntentCompleted); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err = s.finishDesign(p, r.ID, 0, nil, store.DesignInterrupted); err != nil {
			t.Fatal(err)
		}
	}
	s.executeDesign(ctx, p, r.ID, 0)
	got, err := s.sessions.DesignStore().GetDesignRequest(p, r.ID)
	if err != nil || got.State != store.DesignPartial || got.Candidates[0].State != store.DesignInterrupted || got.Candidates[0].Attempts[0].ReasonCode != "completed_output_unavailable" || got.Candidates[0].Attempts[0].Result != nil || *got.Candidates[1].Attempts[0].Result != kept {
		t.Fatalf("dishonest recovery: %+v %v", got, err)
	}
	intent, ok, err := s.sessions.Store().GetV3SessionRunIntent(a.ChildSessionID, a.RunID)
	if err != nil || !ok || intent.Status != store.V3RunIntentCompleted || len(runner.requests) != 1 {
		t.Fatal("rewritten receipt or replay")
	}
	history, err := s.sessions.DesignStore().DesignHistory(p, "lost", 0, 10)
	if err != nil || len(history) != 0 {
		t.Fatal("fabricated output", err)
	}
}
