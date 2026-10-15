package run

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/tool"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// settledCoderJob dispatches a direct Coder job through the production
// dispatch path and settles it the way the executor and reconcile loop do: its
// run intent completes and ReconcileWorker records the terminal receipt. When
// blocked is set the worker first declares a blocker with task_progress.
func settledCoderJob(t *testing.T, runs *Service, ss *sessions.Service, execution *WorkerExecutionService, workspaceID, key string, blocked bool) (store.WorkerRecord, store.WorkerRunRecord) {
	t.Helper()
	ws := ss.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "coder " + key, Role: store.WorkerRoleCoder, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	r, err := execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Add a script"}, IdempotencyKey: key})
	if err != nil || r.Status != "running" {
		t.Fatalf("dispatch: %+v %v", r, err)
	}
	if blocked {
		call := tool.Call{CallID: "blocked", Name: "task_progress", Arguments: `{"action":"blocked","reason":"needs a decision"}`}
		if _, err = runs.executeTaskProgressTool(r.SessionID, call, ss.ApplySessionMutation, planLifecycleRunContext{RunID: r.ID}); err != nil {
			t.Fatal(err)
		}
	}
	intent, _, err := ss.GetSessionRunIntent(r.SessionID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent.Status = sessions.RunIntentCompleted
	if _, err = ss.ApplySessionMutation(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: "owner", AccountScopeID: "account", Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: "completed-" + key, IdempotencyKey: "completed-" + key, PayloadHash: "completed-" + key, RequestHash: "completed-" + key, EventType: "session.run_intent.updated", RunIntent: &intent, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err = execution.ReconcileWorker(context.Background(), "account", w.ID); err != nil {
		t.Fatal(err)
	}
	settled, _, err := ws.GetWorkerRun("account", w.ID, r.ID)
	if err != nil || settled.Status != "succeeded" {
		t.Fatalf("job did not settle: %+v %v", settled, err)
	}
	if blocked {
		// The API's run-completion projection marks the declared blocker on the
		// task; mirror it so the follow-up starts from a blocked job.
		if _, err = ss.Store().UpdateProjectTask("account", "proj_fixture", "task_"+r.ID, func(task *store.ProjectTaskRecord) error {
			task.Status = "blocked"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return w, settled
}

// appendFollowup appends a user message with its pending run intent through
// the canonical V3 message mutation, as POST /v3/sessions/{id}/messages does
// after WorkerExecutionService.PrepareFollowup.
func appendFollowup(ss *sessions.Service, sessionID, key string) (sessions.SessionMutationResult, error) {
	now := time.Now().UnixMilli()
	message := store.MessageSnapshot{ID: "msg-" + key, SessionID: sessionID, UserID: "owner", AccountScopeID: "account", Role: "user", Content: "Also add a test", CreatedAt: now}
	intent := store.V3SessionRunIntent{RunID: "run-" + key, SourceMessageID: message.ID, Status: sessions.RunIntentPendingExecutor}
	return ss.ApplySessionMutation(sessions.SessionMutationInput{SessionID: sessionID, UserID: "owner", AccountScopeID: "account", ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessions.SessionMutationAppendMessage, Message: &message, RunIntent: &intent, NowUnixMs: now})
}

// otherThinking returns a thinking level the catalog resolves for the
// assignment's model, different from its current one.
func otherThinking(t *testing.T, runs *Service, a store.AgentModelAssignment) string {
	t.Helper()
	for _, level := range []string{"low", "medium", "high", "xhigh", "minimal"} {
		if level == a.Thinking {
			continue
		}
		resolved, err := runs.model.ResolvePreference(store.ModelPreference{Provider: a.Provider, Model: a.Model, Thinking: level})
		if err == nil && resolved.CatalogPresent && resolved.Preference.Thinking == level {
			return level
		}
	}
	t.Fatalf("no second thinking level for %s/%s", a.Provider, a.Model)
	return ""
}

// Purpose: a follow-up sent into a finished worker job's session (the job
// settled for review, or settled after the worker declared a blocker, or the
// worker's revision and role model changed since the job started) must pass
// the worker execution gate that every run in a worker-execution session
// crosses at start (Service.automationPolicy -> validateWorkerExecution) and
// at each tool call, and must run on the worker's current model. Threat: the
// gate admitted only admitted/running receipts, so every follow-up on a
// settled job failed at start with "worker revision or state conflict" while
// swarm_message_job reported it accepted. The job's own run must still never
// regain execution once settled. Owners: WorkerExecutionService.PrepareFollowup
// and validateWorkerExecution. Real Pebble, Git, worker dispatch and V3
// session mutations without a provider are the narrowest layer that holds the
// receipt, the session and the gate together.
func TestWorkerFollowupPassesExecutionGateAfterJobSettles(t *testing.T) {
	for _, tc := range []struct {
		name            string
		blocked         bool
		reviseWorker    bool
		wantTaskBlocked bool
	}{
		{name: "needs_review"},
		{name: "blocked", blocked: true, wantTaskBlocked: true},
		{name: "worker revision changed", reviseWorker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
			w, r := settledCoderJob(t, runs, ss, execution, workspaceID, "followup", tc.blocked)
			before, _, err := ss.GetSession(r.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			wantThinking := before.Preference.Thinking
			if tc.reviseWorker {
				// The role's model changes and the worker is revised after the job started.
				settings := store.NewAgentModelSettingsStore(ss.Store().Underlying())
				record, _, err := settings.GetForAccount("account")
				if err != nil {
					t.Fatal(err)
				}
				wantThinking = otherThinking(t, runs, record.SystemAgents.Coder)
				record.SystemAgents.Coder.Thinking = wantThinking
				if _, err = settings.PutForAccount(record); err != nil {
					t.Fatal(err)
				}
				current, _, err := ss.Store().WorkerStore().GetWorker("account", w.ID)
				if err != nil {
					t.Fatal(err)
				}
				description := "revised after the job started"
				revised, err := ss.Store().WorkerStore().UpdateWorker("account", "owner", w.ID, current.Revision, store.UpdateWorkerRequest{Description: &description}, nil)
				if err != nil || revised.Revision == r.WorkerRevision {
					t.Fatalf("revise worker: %+v %v", revised, err)
				}
			}
			// The settled job's own run cannot run again.
			if err := runs.validateWorkerExecutionForTest(r.SessionID); !errors.Is(err, store.ErrWorkerConflict) {
				t.Fatalf("settled job admitted without a follow-up: %v", err)
			}
			if err := execution.PrepareFollowup("account", "owner", r.SessionID); err != nil {
				t.Fatalf("follow-up refused before sending: %v", err)
			}
			if _, err := appendFollowup(ss, r.SessionID, "first"); err != nil {
				t.Fatal(err)
			}
			if _, err := runs.automationPolicy(r.SessionID); err != nil {
				t.Fatalf("follow-up refused at run start: %v", err)
			}
			if err := runs.enforceAutomationTool(r.SessionID, "bash"); err != nil {
				t.Fatalf("follow-up refused at tool call: %v", err)
			}
			after, _, err := ss.GetSession(r.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Preference.Thinking != wantThinking || after.WorktreeRootPath != before.WorktreeRootPath || after.WorktreeBranch != before.WorktreeBranch {
				t.Fatalf("follow-up model or lane: thinking %q want %q; lane %q/%q want %q/%q", after.Preference.Thinking, wantThinking, after.WorktreeRootPath, after.WorktreeBranch, before.WorktreeRootPath, before.WorktreeBranch)
			}
			if task, _, err := ss.Store().GetProjectTask("account", "proj_fixture", "task_"+r.ID); err != nil || task == nil || (task.Status == "blocked") != tc.wantTaskBlocked {
				t.Fatalf("task: %+v %v", task, err)
			}
		})
	}
}

// Purpose: real conflicts stay refused cleanly. A second follow-up while one
// is pending, a cancelled job and a job whose worker was archived are refused
// by WorkerExecutionService.PrepareFollowup with ErrWorkerConflict, and a
// racing second message is refused atomically by the V3 run-intent store;
// neither refusal changes the session (no message, no run, no model change).
// Threat: opening the gate for settled jobs lets duplicate or revoked work run.
// The same real store fixture is the narrowest layer holding all three owners.
func TestWorkerFollowupRefusesRealConflicts(t *testing.T) {
	runs, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	w, r := settledCoderJob(t, runs, ss, execution, workspaceID, "concurrent", false)
	if err := execution.PrepareFollowup("account", "owner", r.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := appendFollowup(ss, r.SessionID, "first"); err != nil {
		t.Fatal(err)
	}
	countMessages := func(sessionID string) int {
		messages, err := ss.ListSessionMessages(sessionID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(messages)
	}
	messages := countMessages(r.SessionID)
	err := execution.PrepareFollowup("account", "owner", r.SessionID)
	if !errors.Is(err, store.ErrWorkerConflict) || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second follow-up while one is pending: %v", err)
	}
	if _, err = appendFollowup(ss, r.SessionID, "second"); err == nil {
		t.Fatal("racing second follow-up appended a second active run")
	}
	if got := countMessages(r.SessionID); got != messages {
		t.Fatalf("refused follow-up changed the session: %d messages, want %d", got, messages)
	}
	if active, ok, err := ss.GetSessionActiveRunIntent(r.SessionID); err != nil || !ok || active.RunID != "run-first" {
		t.Fatalf("active run replaced: %+v %v %v", active, ok, err)
	}

	// A job cancelled before it settled takes no follow-up.
	ws := ss.Store().WorkerStore()
	cancelled, err := execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Another change"}, IdempotencyKey: "cancelled"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = execution.CancelRun(context.Background(), "account", w.ID, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if err = execution.PrepareFollowup("account", "owner", cancelled.SessionID); !errors.Is(err, store.ErrWorkerConflict) || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancelled job: %v", err)
	}

	// A settled job of an archived worker takes no follow-up.
	other, settled := settledCoderJob(t, runs, ss, execution, workspaceID, "archived", false)
	current, _, err := ws.GetWorker("account", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = execution.Stop(context.Background(), "account", "owner", other.ID, current.Revision, store.WorkerLifecycleStateArchived); err != nil {
		t.Fatal(err)
	}
	before, _, err := ss.GetSession(settled.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err = execution.PrepareFollowup("account", "owner", settled.SessionID); !errors.Is(err, store.ErrWorkerConflict) || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("archived worker: %v", err)
	}
	if after, _, err := ss.GetSession(settled.SessionID); err != nil || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("refusal mutated the session: %v", err)
	}
	// Even a follow-up appended past the refusal cannot run for an archived worker.
	if _, err = appendFollowup(ss, settled.SessionID, "late"); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.automationPolicy(settled.SessionID); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("archived worker's follow-up ran: %v", err)
	}
}

// validateWorkerExecutionForTest runs the gate against the session as stored.
func (s *Service) validateWorkerExecutionForTest(sessionID string) error {
	snapshot, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("session missing")
	}
	return s.validateWorkerExecution(snapshot)
}

// Purpose: PrepareFollowup must register a durable queue and the execution gate
// must refuse the appended pending intent until a box slot is reserved. A real
// session/store fixture proves neither follow-up nor normal work bypasses it.
func TestWorkerFollowupQueuesBeforeExecution(t *testing.T) {
	runs, ss, execution, workspace := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
	w, r := settledCoderJob(t, runs, ss, execution, workspace, "capacity", false)
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeOff, UID: 10001, GID: 10001, Memory: "1g", MaxWorkerRuns: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := sandbox.Default()
	sandbox.SetDefault(m)
	t.Cleanup(func() { sandbox.SetDefault(old) })
	ws := ss.Store().WorkerStore()
	blocker, err := ws.RecordWorkerRun("account", store.WorkerRunRecord{WorkerID: w.ID, Status: "preparing"})
	if err != nil {
		t.Fatal(err)
	}
	if err = execution.PrepareFollowup("account", "owner", r.SessionID); err != nil {
		t.Fatal(err)
	}
	queued, _, _ := ws.GetWorkerRun("account", w.ID, r.ID)
	if queued.FollowupStatus != "queued" || queued.QueueReason == "" {
		t.Fatalf("not queued: %+v", queued)
	}
	if _, err = appendFollowup(ss, r.SessionID, "capacity"); err != nil {
		t.Fatal(err)
	}
	intent, found, err := ss.GetSessionActiveRunIntent(r.SessionID)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if ok, err := ws.ReserveWorkerFollowup("account", w.ID, r.ID, intent.RunID, 1); err != nil || ok {
		t.Fatal("overloaded follow-up", ok, err)
	}
	if err = runs.validateWorkerExecutionForTest(r.SessionID); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("queued intent executable: %v", err)
	}
	blocker.Status = "failed"
	if _, err = ws.RecordWorkerRun("account", blocker); err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerFollowup("account", w.ID, r.ID, intent.RunID, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = runs.validateWorkerExecutionForTest(r.SessionID); err != nil {
		t.Fatal(err)
	}
	if err = ws.ReleaseWorkerFollowup("account", w.ID, r.ID, intent.RunID); err != nil {
		t.Fatal(err)
	}
}
