package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// companionWatchSession creates an owned session with a running run; marked
// sessions carry the protected Companion marker.
func (f *taskWaitFixture) companionWatchSession(id, run string, marked bool) {
	f.t.Helper()
	metadata := map[string]any{"agent_name": "swarm"}
	if marked {
		metadata[SessionCompanionMetadataKey] = true
	}
	f.apply(V3SessionMutationInput{SessionID: id, Kind: V3SessionMutationCreateSession, ClientRequestID: "create-" + id, Session: &SessionSnapshot{ID: id, Mode: "auto", Metadata: metadata}})
	f.apply(V3SessionMutationInput{SessionID: id, Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "pending-" + id, RunIntent: &V3SessionRunIntent{RunID: run, Status: V3RunIntentPendingExecutor}})
	f.apply(V3SessionMutationInput{SessionID: id, Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "run-" + id, RunIntent: &V3SessionRunIntent{RunID: run, Status: V3RunIntentRunning}})
}

func companionLocalWatch(session, run, project string, ids ...string) V3SessionMutationInput {
	return V3SessionMutationInput{SessionID: session, Kind: V3SessionMutationAppendMessage, EventType: "session.task_watch.started", ClientRequestID: "task-watch:" + session + ":" + run + ":" + project, TaskWait: &V3ProjectTaskWaitMutation{RunID: run, ProjectID: project, TaskIDs: ids}}
}

// Purpose: the Companion follows the work it delegates. Requirement: the
// account user's current Companion (protected marker, same owner, named by
// their durable CompanionIdentity) may register a watch on the local tasks of
// any existing project of the account, although it is not a project
// conversation; the watch survives a daemon restart and its outcome is
// delivered to the Companion through the session wake (source task_watch),
// after which the watch ends. A session that is neither a project
// conversation nor the current Companion (unmarked, a marked session the
// identity does not name, or the Companion after the person deleted it) is
// refused and records no watch, and a Companion watch on a project that does
// not exist is refused. Threat: the Companion's contract tells it to follow
// delegated work with watches, but the store refused every local watch from a
// non-project session, so research results never reached it; widening that
// rule to arbitrary sessions or forged markers. Boundary:
// prepareProjectTaskWait (registration), prepareProjectTaskWatchWake
// (delivery), SessionStore.IsCurrentCompanion/companionWatchesProject.
// Narrowest layer: ApplyV3SessionMutation over a real Pebble store, the one
// authority both the native tool and the reconcile path use.
func TestCompanionWatchesItsDelegatedLocalTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	f, progress := newSettledWaitFixture(t, path, "task")
	identity, _, err := f.db.EnsureCompanion("account", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	companion := identity.SessionID
	f.companionWatchSession(companion, "c-run", true)
	f.companionWatchSession("plain", "p-run", false)
	f.companionWatchSession("forged", "f-run", true)

	for _, refused := range []V3SessionMutationInput{
		companionLocalWatch("plain", "p-run", "project", "task"),
		companionLocalWatch("forged", "f-run", "project", "task"),
		companionLocalWatch(companion, "c-run", "missing", "task"),
	} {
		if _, err := f.try(refused); err == nil {
			t.Fatalf("watch registered for %s on %s", refused.SessionID, refused.TaskWait.ProjectID)
		}
		if n := len(f.watches(refused.SessionID)); n != 0 {
			t.Fatalf("refused watch left %d watches on %s", n, refused.SessionID)
		}
	}

	off := false
	in := companionLocalWatch(companion, "c-run", "project", "task")
	in.TaskWait.Milestones = &off
	f.apply(in)
	f.endRun(companion, "c-run")
	if w := f.onlyWatch(companion); w.Wait.ProjectID != "project" || len(w.Wait.Tasks) != 1 || w.Wait.Tasks[0].TaskID != "task" {
		t.Fatalf("companion watch: %+v", w)
	}

	// Restart: the watch is durable and still the Companion's.
	if err := f.db.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f.db = NewSessionStore(store)
	progress.install(store)
	watch := f.onlyWatch(companion)

	f.setTask("task", func(t *ProjectTaskRecord) { t.Status = "needs_review" })
	progress.set("task", map[string]any{"state": "not_integrated", "integration": map[string]any{"state": "not_integrated"}})
	result, err := f.try(ProjectTaskWatchWakeInput(watch, nil, nil))
	if err != nil {
		t.Fatalf("companion watch outcome: %v", err)
	}
	if out := taskWaitOutcome(t, result.Message); outcomeRow(t, out, 0)["task_id"] != "task" {
		t.Fatalf("outcome: %v", out)
	}
	if result.Message.Metadata["task_watch_id"] != watch.WatchID {
		t.Fatalf("outcome not tied to the watch: %+v", result.Message.Metadata)
	}
	if n := len(f.watches(companion)); n != 0 {
		t.Fatalf("delivered watch kept: %d", n)
	}
	if result.RunIntent == nil || result.RunIntent.RunID != SessionWakeRunID(companion, watch.WatchID+":outcome") {
		t.Fatalf("the outcome did not start a Companion turn: %+v", result.RunIntent)
	}
	f.apply(V3SessionMutationInput{SessionID: companion, Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "claim-wake", RunIntent: &V3SessionRunIntent{RunID: result.RunIntent.RunID, Status: V3RunIntentRunning}})
	f.endRun(companion, result.RunIntent.RunID)

	// After the person deletes the Companion, its old session no longer
	// qualifies: a later watch from it is refused.
	if _, err := f.db.RetireCompanion("account", "user", companion, 2); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.db.IsCurrentCompanion(mustSession(t, f.db, companion), "account", "user"); err != nil || ok {
		t.Fatalf("retired companion still current: %v %v", ok, err)
	}
	f.apply(V3SessionMutationInput{SessionID: companion, Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "pending-c-run-2", RunIntent: &V3SessionRunIntent{RunID: "c-run-2", Status: V3RunIntentPendingExecutor}})
	f.apply(V3SessionMutationInput{SessionID: companion, Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "run-c-run-2", RunIntent: &V3SessionRunIntent{RunID: "c-run-2", Status: V3RunIntentRunning}})
	if _, err := f.try(companionLocalWatch(companion, "c-run-2", "project", "task")); err == nil || errors.Is(err, ErrProjectTaskWaitNotReady) {
		t.Fatalf("retired companion registered a watch: %v", err)
	}
	if n := len(f.watches(companion)); n != 0 {
		t.Fatalf("retired companion has %d watches", n)
	}
}

func mustSession(t *testing.T, db *SessionStore, id string) SessionSnapshot {
	t.Helper()
	session, ok, err := db.GetSession(id)
	if err != nil || !ok {
		t.Fatalf("session %s: %v %v", id, ok, err)
	}
	return session
}
