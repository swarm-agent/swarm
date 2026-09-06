package run

import (
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: approved shorthand must never select a future, paused, terminal or
// stale-attempt checkpoint. Exercise canonical plan storage and resolver, with
// no program creation or child launch on each rejection.
func TestTaskProgramPlannedStartRejectsNonRunnableCheckpoint(t *testing.T) {
	for _, status := range []string{"pending", "paused", "completed", "blocked", "failed"} {
		t.Run(status, func(t *testing.T) {
			svc, sessions, cleanup := newPlanManageRunTestService(t)
			defer cleanup()
			id := createPlanManageTestSession(t, sessions)
			program := pebblestore.TaskProgramDefinition{ID: "program", Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "job", StageID: "build", AgentType: "coder", Title: "Scoped job", MetaPrompt: "Implement scoped change", Deliverable: "commit", OwnedScope: []string{"source.go"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "ready"}}}
			doc := &pebblestore.SessionPlanDocument{ID: "plan", Title: "Plan", Status: "approved", Info: pebblestore.SessionPlanInfo{Goal: "build"}, ActiveCheckpointID: "cp", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp", Title: "Build", Status: status, Order: 1, AcceptanceCriteria: []string{"done"}, TaskProgram: &program}}}
			if _, _, err := sessions.SavePlanWithMetadata(id, "plan", "Plan", "# Plan", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: doc}); err != nil {
				t.Fatal(err)
			}
			parsed, err := parseTaskCallArguments(`{"action":"start"}`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.resolveApprovedCheckpointTaskProgram(id, parsed); err == nil {
				t.Fatal("non-runnable checkpoint accepted")
			}
			if _, ok, err := sessions.GetTaskProgram(id, "program"); err != nil || ok {
				t.Fatalf("rejection created program: %v %v", ok, err)
			}
		})
	}
}

// Purpose: rejected durable transitions must preserve the last known program
// and successful child handoff, rather than zeroing state used for recovery.
func TestTaskProgramTransitionFailurePreservesSnapshot(t *testing.T) {
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	record := pebblestore.TaskProgramRecord{ParentSessionID: id, ProgramID: "missing", Revision: 3, State: "running", Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "done", State: "handoff_ready", ChildSessionID: "child", ChildHead: "head"}}}
	p := taskProgramScheduler{service: svc, record: record}
	got, changed, err := p.transition(id, "missing", pebblestore.TaskProgramTransition{ExpectedRevision: 3, MutationID: "failure"})
	if err == nil || changed || got.ProgramID != record.ProgramID || got.Revision != 3 || got.Jobs[0].ChildHead != "head" {
		t.Fatalf("lost recovery snapshot: %+v %v %v", got, changed, err)
	}
}

// Purpose: a terminal owning V3 run cannot leave phantom executing jobs. Status
// reconciliation preserves committed siblings and blocks only unfinished jobs.
func TestTaskProgramEndedOwnerPreservesCommittedSibling(t *testing.T) {
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, _, err := svc.sessions.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	fixture := pebblestore.TaskProgramRecord{ParentSessionID: id, ProgramID: "interrupted", DefinitionHash: "hash", ReservationRunID: "owner-run", State: "running", ActiveStageID: "build", Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "done", StageID: "build", AgentType: "coder"}, {ID: "unfinished", StageID: "build", AgentType: "coder"}}}, Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "done", StageID: "build", State: "handoff_ready", ChildSessionID: "done-child", ChildHead: "committed-head"}, {JobID: "unfinished", StageID: "build", State: "running", ChildSessionID: "unfinished-child"}}}
	if _, _, err := svc.sessions.CreateTaskProgram(fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: id, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: "pending-owner", IdempotencyKey: "pending-owner", PayloadHash: "pending-owner", RequestHash: "pending-owner", RunIntent: &pebblestore.V3SessionRunIntent{SessionID: id, RunID: "owner-run", Status: pebblestore.V3RunIntentPendingExecutor}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: id, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: "ended-owner", IdempotencyKey: "ended-owner", PayloadHash: "ended-owner", RequestHash: "ended-owner", RunIntent: &pebblestore.V3SessionRunIntent{SessionID: id, RunID: "owner-run", Status: "failed"}}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := svc.sessions.GetTaskProgram(id, "interrupted")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.State != "blocked" || got.Jobs[0].State != "handoff_ready" || got.Jobs[0].ChildHead != "committed-head" || got.Jobs[1].State != "blocked" || got.Jobs[1].ChildSessionID != "unfinished-child" {
		t.Fatalf("reconciliation: %+v", got)
	}
	again, _, err := svc.sessions.GetTaskProgram(id, "interrupted")
	if err != nil || again.Revision != got.Revision {
		t.Fatal("reconciliation repeated")
	}
}
