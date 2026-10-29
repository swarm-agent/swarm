package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the person can see and stop a conversation's task watches, and a
// watching conversation is never pending. Requirement: while the goal watches
// task-1 its run state is idle (no active run, not waiting_tasks) and GET
// task-watches lists the watch; POST task-watches/stop ends it through the
// canonical mutation with one "Stopped watching" line (ended_by stop), the
// list is then empty and a second stop finds nothing (404, no new line); the
// stopped watch never fires when the task settles. Threat: a watch the person
// cannot end, a stop that silently succeeds on nothing, or a stopped watch
// that wakes the conversation later. Authority: handleSessionV3TaskWatches /
// handleSessionV3TaskWatchesStop over the real store and
// prepareProjectTaskWatchCancel; the handler over a real daemon fixture is the
// narrowest layer holding the route, the store and reconciliation.
func TestPersonStopsTaskWatches(t *testing.T) {
	w := newWaitWorkflowFixture(t)
	w.localWait(t)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: w.f.user, AccountScopeID: w.f.account}
	if intent, ok, err := w.f.s.sessions.GetSessionActiveRunIntent("goal"); err != nil {
		t.Fatal(err)
	} else if ok && sessionV3RunIntentStatusActive(intent.Status) {
		t.Fatalf("a watching conversation has an active run: %+v", intent)
	}
	if state, _, _ := w.f.s.sessions.Store().GetV3SessionRunStateWithWatches("goal"); state.Status == pebblestore.V3RunIntentWaitingTasks || state.Active || len(state.TaskWatches) != 1 {
		t.Fatalf("watching run state: %+v", state)
	}
	list := func() []any {
		t.Helper()
		rec := httptest.NewRecorder()
		w.f.s.handleSessionV3TaskWatches(rec, httptest.NewRequest(http.MethodGet, "/v3/sessions/goal/task-watches", nil), principal, "goal")
		var out map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		watches, _ := out["task_watches"].([]any)
		return watches
	}
	if got := list(); len(got) != 1 || got[0].(map[string]any)["watch_id"] != w.watchID {
		t.Fatalf("listed watches: %v", got)
	}
	stop := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		w.f.s.handleSessionV3TaskWatchesStop(rec, httptest.NewRequest(http.MethodPost, "/v3/sessions/goal/task-watches/stop", strings.NewReader(`{}`)), principal, "goal")
		return rec
	}
	if rec := stop(); rec.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("watches after stop: %v", got)
	}
	stopLines := func() int {
		messages, err := w.f.s.sessions.Store().ListMessages("goal", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, m := range messages {
			if pebblestore.IsProjectTaskWaitNoticeMessage(m) && strings.HasPrefix(m.Content, "Stopped watching:") && m.Metadata["ended_by"] == pebblestore.ProjectTaskWaitEndedByStop {
				n++
			}
		}
		return n
	}
	if n := stopLines(); n != 1 {
		t.Fatalf("stop lines = %d", n)
	}
	if rec := stop(); rec.Code != http.StatusNotFound {
		t.Fatalf("second stop: %d %s", rec.Code, rec.Body.String())
	}
	if n := stopLines(); n != 1 {
		t.Fatalf("second stop recorded a line: %d", n)
	}
	w.finishTask(t)
	w.f.s.reconcileProjectTaskWaits("", "")
	if w.woke(t) {
		t.Fatal("a stopped watch fired")
	}
}

// Purpose: outcomes that settle while a run is active are delivered right
// after that run, by the executor itself. Requirement: with the goal working
// (run-2 running, e.g. the person talking), task-1 settling fires nothing (the
// watch stays); once run-2 has ended, the executor's finish of that job
// (sessionV3Executor.finish -> Server.deliverSessionTaskWatches) starts the
// outcome turn exactly once. Threat: an outcome dropped because the person
// was talking, or a watch stuck until some unrelated task change. Authority:
// sessionV3Executor.finish over the real store; the daemon fixture is the
// narrowest layer holding the executor and the store together.
func TestExecutorDeliversDeferredWatchAfterTheRun(t *testing.T) {
	w := newWaitWorkflowFixture(t)
	w.localWait(t)
	if w.f.s.v3SessionExecutor == nil {
		t.Fatal("fixture has no v3 executor")
	}
	db := w.f.s.sessions.Store()
	record := func(key, status string) {
		t.Helper()
		in := pebblestore.V3SessionMutationInput{SessionID: "goal", AccountScopeID: w.f.account, UserID: w.f.user, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: key, PayloadHash: key, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-2", Status: status}}
		if _, err := db.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	record("run-2-pending", pebblestore.V3RunIntentPendingExecutor)
	record("run-2-running", pebblestore.V3RunIntentRunning)
	w.finishTask(t)
	w.f.s.reconcileProjectTaskWaits("", "")
	if w.woke(t) {
		t.Fatal("outcomes started a turn while a run was active")
	}
	if watches, err := db.ListV3SessionTaskWatches("goal"); err != nil || len(watches) != 1 {
		t.Fatalf("deferred watch: %+v %v", watches, err)
	}
	record("run-2-done", pebblestore.V3RunIntentCompleted)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: w.f.user, AccountScopeID: w.f.account}
	w.f.s.v3SessionExecutor.finish(sessionV3ExecutorJob{Principal: principal, SessionID: "goal", RunID: "run-2"})
	if !w.woke(t) {
		t.Fatal("the run's end did not deliver the deferred outcomes")
	}
	if watches, err := db.ListV3SessionTaskWatches("goal"); err != nil || len(watches) != 0 {
		t.Fatalf("watch after delivery: %+v %v", watches, err)
	}
	messages, err := db.ListMessages("goal", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := 0
	for _, m := range messages {
		if strings.HasPrefix(m.Content, "Delegated project task outcomes:") {
			outcomes++
		}
	}
	if outcomes != 1 {
		t.Fatalf("outcome messages: %d", outcomes)
	}
}
