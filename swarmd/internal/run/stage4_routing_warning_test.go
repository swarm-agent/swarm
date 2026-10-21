package run

import (
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const stage4RoutingWarning = "system-coder model resolution failed: configured model metadata unavailable. Using the owning account's configured Swarm Action model fixture-provider/fallback; compiled role and tool permissions are unchanged."

func stage4WarningStore(t *testing.T) (string, *pebblestore.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routing-warning.pebble")
	store, err := pebblestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, store
}

func TestStage4RoutingWarningChildRestartAndReceipts(t *testing.T) {
	// Requirement: a fallback warning remains visible after durable child restart.
	// Threat: rebuilding task receipts from a stored child loses model disclosure.
	// Owners: SessionStore, buildTaskLaunchOutcome and task stream/patch projection;
	// real close/reopen plus production receipt functions is the narrowest proof.
	path, db := stage4WarningStore(t)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	sessions := pebblestore.NewSessionStore(db)
	for _, key := range []string{"router_alert", "model_resolution_warning"} {
		child := pebblestore.SessionSnapshot{ID: "fixture-child-" + key, AccountScopeID: "fixture-account", UserID: "fixture-user", Metadata: map[string]any{key: stage4RoutingWarning, "parent_session_id": "fixture-parent"}}
		if err := sessions.CreateSession(child); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	db, err = pebblestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions = pebblestore.NewSessionStore(db)
	for _, key := range []string{"router_alert", "model_resolution_warning"} {
		t.Run(key, func(t *testing.T) {
			child, found, err := sessions.GetSession("fixture-child-" + key)
			if err != nil || !found {
				t.Fatalf("read restarted child: found=%t error=%v", found, err)
			}
			if child.Metadata[key] != stage4RoutingWarning {
				t.Fatal("stored model warning changed on restart")
			}
			// Deliberately omit in-memory RouterAlert: restart must use the child.
			outcome := buildTaskLaunchOutcome(taskLaunchPrepared{ChildSession: child, RequestedSubagent: "coder"})
			if outcome.RouterAlert != stage4RoutingWarning {
				t.Fatal("durable warning absent from reconstructed outcome")
			}
			payload := buildTaskStreamPayload("fixture-parent", "start", "fixture task", 1, outcome, "completed", "fixture result")
			if payload["router_alert"] != stage4RoutingWarning {
				t.Fatal("user-visible task stream lost warning")
			}
			rows := taskProgramLaunchRows(payload)
			if len(rows) != 1 || rows[0]["router_alert"] != stage4RoutingWarning {
				t.Fatal("user-visible child row lost warning")
			}
			patch := buildTaskStreamLaunchPatchPayload(outcome, "ok", "completed", true)
			if patch["router_alert"] != stage4RoutingWarning {
				t.Fatal("task patch lost warning")
			}
			parsed := taskProgramOutcomesFromPayload(payload, 1)
			if len(parsed) != 1 || parsed[0].RouterAlert != stage4RoutingWarning {
				t.Fatal("task outcome parser dropped warning")
			}
		})
	}
}

func TestStage4RoutingWarningProgramTransitionRestartAndStatus(t *testing.T) {
	// Requirement: fallback disclosure persists through canonical program progress
	// and remains visible in status even when the job has no committed child HEAD.
	// Threat: transition, replay, stale updates or presentation silently drop it.
	// Owners: taskProgramOutcomeTransitions, SessionStore.TransitionTaskProgram,
	// and taskProgramStatusPayload; real Pebble restart proves durable continuity.
	path, db := stage4WarningStore(t)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	sessions := pebblestore.NewSessionStore(db)
	fixture := pebblestore.TaskProgramRecord{
		ParentSessionID: "fixture-parent", ProgramID: "fixture-program", DefinitionHash: "fixture-definition",
		Definition:    pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "job", StageID: "build", AgentType: "coder", Title: "fixture", MetaPrompt: "implement assignment", Deliverable: "change", OwnedScope: []string{"fixture/**"}, AcceptanceCriteria: []string{"assigned behavior"}, DependencyEvidence: "ready"}}},
		ActiveStageID: "build", State: pebblestore.TaskProgramStateDeclared, NextAction: "launch_ready_jobs", Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "job", StageID: "build", State: pebblestore.TaskProgramJobDeclared}},
	}
	created, fresh, err := sessions.CreateTaskProgram(fixture)
	if err != nil || !fresh {
		t.Fatalf("create fixture: fresh=%t error=%v", fresh, err)
	}
	state := pebblestore.TaskProgramStateRunning
	running, _, err := sessions.TransitionTaskProgram(fixture.ParentSessionID, fixture.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: created.Revision, MutationID: "fixture-start", State: &state, Jobs: []pebblestore.TaskProgramJobTransition{{JobID: "job", ExpectedState: pebblestore.TaskProgramJobDeclared, State: pebblestore.TaskProgramJobRunning, ChildSessionID: "fixture-child"}}})
	if err != nil {
		t.Fatal(err)
	}
	payload := buildTaskStreamPayload(fixture.ParentSessionID, "start", "fixture task", 1, taskLaunchOutcome{RouterAlert: stage4RoutingWarning, ChildSessionID: "fixture-child", Phase: "completed"}, "completed", "fixture result")
	outcomes := taskProgramOutcomesFromPayload(payload, 1)
	updates := taskProgramOutcomeTransitions(&taskProgramSpec{Jobs: []taskProgramJob{{ID: "job", RequestedSubagentType: "coder"}}}, outcomes, nil)
	if len(updates) != 1 || updates[0].RouterAlert != stage4RoutingWarning {
		t.Fatal("outcome transition dropped warning")
	}
	transition := pebblestore.TaskProgramTransition{ExpectedRevision: running.Revision, MutationID: "fixture-outcome", Jobs: updates}
	updated, changed, err := sessions.TransitionTaskProgram(fixture.ParentSessionID, fixture.ProgramID, transition)
	if err != nil || !changed || updated.Jobs[0].RouterAlert != stage4RoutingWarning {
		t.Fatalf("persist warning transition: changed=%t error=%v", changed, err)
	}
	duplicate, changed, err := sessions.TransitionTaskProgram(fixture.ParentSessionID, fixture.ProgramID, transition)
	if err != nil || changed || duplicate.Revision != updated.Revision || duplicate.Jobs[0].RouterAlert != stage4RoutingWarning {
		t.Fatal("idempotent outcome replay changed warning")
	}
	stale := pebblestore.TaskProgramTransition{ExpectedRevision: created.Revision, MutationID: "fixture-stale", Jobs: []pebblestore.TaskProgramJobTransition{{JobID: "job", RouterAlert: "incorrect replacement"}}}
	if _, _, err := sessions.TransitionTaskProgram(fixture.ParentSessionID, fixture.ProgramID, stale); err == nil || !strings.Contains(err.Error(), "revision mismatch") {
		t.Fatalf("stale update was not rejected: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = pebblestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions = pebblestore.NewSessionStore(db)
	reopened, found, err := sessions.GetTaskProgram(fixture.ParentSessionID, fixture.ProgramID)
	if err != nil || !found || reopened.Jobs[0].RouterAlert != stage4RoutingWarning || reopened.Revision != updated.Revision {
		t.Fatal("restart or rejected mutation changed durable warning")
	}
	if reopened.Jobs[0].ChildHead != "" {
		t.Fatal("fixture unexpectedly has child HEAD")
	}
	status := taskProgramStatusPayload(reopened, false)
	jobs, ok := status["jobs"].([]map[string]any)
	if !ok || len(jobs) != 1 || jobs[0]["router_alert"] != stage4RoutingWarning {
		t.Fatal("user-visible program status lost no-HEAD warning")
	}
	if _, found, err := sessions.GetTaskProgram("other-parent", fixture.ProgramID); err != nil || found {
		t.Fatal("warning leaked through cross-parent program lookup")
	}
}
