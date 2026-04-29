package pebblestore

import (
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/flow"
)

func TestFlowStoreControllerRecordsRoundTripAndOrdering(t *testing.T) {
	store := openFlowTestStore(t)
	flows := NewFlowStore(store)

	assignment := testFlowAssignment("flow-1", 1)
	definition, err := flows.PutDefinition(FlowDefinitionRecord{
		Assignment: assignment,
		NextDueAt:  time.Date(2025, 1, 2, 14, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("put definition: %v", err)
	}
	if definition.FlowID != "flow-1" || definition.Revision != 1 {
		t.Fatalf("definition identity = %s/%d", definition.FlowID, definition.Revision)
	}

	loaded, ok, err := flows.GetDefinition("flow-1")
	if err != nil || !ok {
		t.Fatalf("get definition ok=%v err=%v", ok, err)
	}
	if loaded.Assignment.Agent.TargetKind != "background" || loaded.Assignment.Agent.TargetName != "memory" {
		t.Fatalf("loaded agent = %+v", loaded.Assignment.Agent)
	}

	if _, err := flows.PutAssignmentStatus(FlowAssignmentStatusRecord{
		FlowID:           "flow-1",
		TargetSwarmID:    "target-1",
		DesiredRevision:  1,
		AcceptedRevision: 1,
		Status:           flow.AssignmentAccepted,
	}); err != nil {
		t.Fatalf("put assignment status: %v", err)
	}
	statuses, err := flows.ListAssignmentStatuses("flow-1", 10)
	if err != nil {
		t.Fatalf("list assignment statuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != flow.AssignmentAccepted || statuses[0].PendingSync {
		t.Fatalf("statuses = %+v", statuses)
	}

	command1 := testAssignmentCommand("cmd-1", assignment)
	outbox1, err := flows.PutOutboxCommand(FlowOutboxCommandRecord{
		CommandID:     "cmd-1",
		FlowID:        "flow-1",
		Revision:      1,
		TargetSwarmID: "target-1",
		Command:       command1,
		Status:        FlowOutboxStatusPending,
		NextAttemptAt: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC),
	}, nil)
	if err != nil {
		t.Fatalf("put outbox1: %v", err)
	}
	command2 := testAssignmentCommand("cmd-2", assignment)
	if _, err := flows.PutOutboxCommand(FlowOutboxCommandRecord{
		CommandID:     "cmd-2",
		FlowID:        "flow-1",
		Revision:      1,
		TargetSwarmID: "target-1",
		Command:       command2,
		Status:        FlowOutboxStatusPending,
		NextAttemptAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
	}, nil); err != nil {
		t.Fatalf("put outbox2: %v", err)
	}
	pending, err := flows.ListOutboxCommands(FlowOutboxStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending outbox: %v", err)
	}
	if len(pending) != 2 || pending[0].CommandID != "cmd-2" || pending[1].CommandID != "cmd-1" {
		t.Fatalf("pending order = %+v", pending)
	}

	previousOutbox1 := outbox1
	outbox1.Status = FlowOutboxStatusDelivered
	if _, err := flows.PutOutboxCommand(outbox1, &previousOutbox1); err != nil {
		t.Fatalf("update outbox1: %v", err)
	}
	pending, err = flows.ListOutboxCommands(FlowOutboxStatusPending, 10)
	if err != nil {
		t.Fatalf("list pending after update: %v", err)
	}
	if len(pending) != 1 || pending[0].CommandID != "cmd-2" {
		t.Fatalf("pending after update = %+v", pending)
	}

	if err := flows.DeleteDefinition("flow-1"); err != nil {
		t.Fatalf("delete definition: %v", err)
	}
	if _, ok, err := flows.GetDefinition("flow-1"); err != nil || ok {
		t.Fatalf("definition after delete ok=%v err=%v", ok, err)
	}
}

func TestFlowSchedulerStoreListsAcceptedDueAndSchedulesNext(t *testing.T) {
	store := openFlowTestStore(t)
	flows := NewFlowStore(store)
	assignment := testFlowAssignment("flow-scheduler", 3)
	accepted, err := flows.PutAcceptedAssignment(flow.AcceptedAssignment{Assignment: assignment})
	if err != nil {
		t.Fatalf("put accepted: %v", err)
	}
	dueAt := time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC)
	if _, err := flows.PutDue(FlowDueRecord{FlowID: accepted.FlowID, Revision: accepted.Revision, DueAt: dueAt}); err != nil {
		t.Fatalf("put due: %v", err)
	}

	schedulerStore := NewFlowSchedulerStore(flows)
	due, err := schedulerStore.ListDue(t.Context(), time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatalf("list scheduler due: %v", err)
	}
	if len(due) != 1 || due[0].Assignment.FlowID != "flow-scheduler" || !due[0].ScheduledAt.Equal(dueAt) {
		t.Fatalf("scheduler due = %+v", due)
	}

	claim, inserted, err := schedulerStore.ClaimRun(t.Context(), flow.RunClaim{FlowID: accepted.FlowID, Revision: accepted.Revision, ScheduledAt: dueAt, RunID: "run-1"})
	if err != nil || !inserted || claim.RunID != "run-1" {
		t.Fatalf("claim inserted=%v claim=%+v err=%v", inserted, claim, err)
	}
	if err := schedulerStore.DeleteDue(t.Context(), accepted.FlowID, accepted.Revision, dueAt); err != nil {
		t.Fatalf("delete due: %v", err)
	}
	if _, ok, err := schedulerStore.ScheduleNext(t.Context(), accepted, dueAt); err != nil || !ok {
		t.Fatalf("schedule next ok=%v err=%v", ok, err)
	}
	nextDue, err := flows.ListDue(time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatalf("list next due: %v", err)
	}
	if len(nextDue) != 1 || !nextDue[0].DueAt.Equal(time.Date(2025, 1, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("next due = %+v", nextDue)
	}
}

func TestFlowStoreTargetIdempotencyDueClaimsAndRunHistory(t *testing.T) {
	store := openFlowTestStore(t)
	flows := NewFlowStore(store)
	assignment := testFlowAssignment("flow-1", 7)

	accepted, err := flows.PutAcceptedAssignment(flow.AcceptedAssignment{Assignment: assignment})
	if err != nil {
		t.Fatalf("put accepted: %v", err)
	}
	if accepted.AcceptedAt.IsZero() {
		t.Fatalf("accepted_at was not filled")
	}
	loadedAccepted, ok, err := flows.GetAcceptedAssignment("flow-1")
	if err != nil || !ok {
		t.Fatalf("get accepted ok=%v err=%v", ok, err)
	}
	if loadedAccepted.Revision != 7 {
		t.Fatalf("accepted revision = %d", loadedAccepted.Revision)
	}

	ledger := FlowCommandLedgerRecord{
		CommandID: "cmd-1",
		FlowID:    "flow-1",
		Revision:  7,
		Action:    flow.CommandInstall,
		Status:    flow.AssignmentAccepted,
		Ack:       flow.AssignmentAck{CommandID: "cmd-1", FlowID: "flow-1", AcceptedRevision: 7, Status: flow.AssignmentAccepted},
	}
	first, inserted, err := flows.PutCommandLedger(ledger)
	if err != nil || !inserted {
		t.Fatalf("put ledger inserted=%v err=%v", inserted, err)
	}
	ledger.Status = flow.AssignmentRejected
	second, inserted, err := flows.PutCommandLedger(ledger)
	if err != nil {
		t.Fatalf("put duplicate ledger: %v", err)
	}
	if inserted || second.Status != first.Status {
		t.Fatalf("duplicate ledger inserted=%v second=%+v first=%+v", inserted, second, first)
	}

	due1 := FlowDueRecord{FlowID: "flow-1", Revision: 7, DueAt: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)}
	due2 := FlowDueRecord{FlowID: "flow-1", Revision: 7, DueAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC)}
	due3 := FlowDueRecord{FlowID: "flow-1", Revision: 7, DueAt: time.Date(2025, 1, 3, 9, 0, 0, 0, time.UTC)}
	for _, due := range []FlowDueRecord{due1, due2, due3} {
		if _, err := flows.PutDue(due); err != nil {
			t.Fatalf("put due %+v: %v", due, err)
		}
	}
	due, err := flows.ListDue(time.Date(2025, 1, 2, 11, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 2 || !due[0].DueAt.Equal(due2.DueAt) || !due[1].DueAt.Equal(due1.DueAt) {
		t.Fatalf("due order = %+v", due)
	}

	claim := FlowRunClaimRecord{FlowID: "flow-1", Revision: 7, ScheduledAt: due2.DueAt, RunID: "run-1"}
	storedClaim, claimed, err := flows.ClaimRun(claim)
	if err != nil || !claimed {
		t.Fatalf("claim run claimed=%v err=%v", claimed, err)
	}
	claim.RunID = "run-2"
	duplicateClaim, claimed, err := flows.ClaimRun(claim)
	if err != nil {
		t.Fatalf("duplicate claim: %v", err)
	}
	if claimed || duplicateClaim.RunID != storedClaim.RunID {
		t.Fatalf("duplicate claim claimed=%v duplicate=%+v stored=%+v", claimed, duplicateClaim, storedClaim)
	}

	runs := []FlowRunSummaryRecord{
		{RunID: "run-old", FlowID: "flow-1", Revision: 7, ScheduledAt: due1.DueAt, StartedAt: time.Date(2025, 1, 2, 10, 1, 0, 0, time.UTC), Status: FlowRunStatusSuccess},
		{RunID: "run-new", FlowID: "flow-1", Revision: 7, ScheduledAt: due3.DueAt, StartedAt: time.Date(2025, 1, 3, 9, 1, 0, 0, time.UTC), Status: FlowRunStatusFailed},
	}
	for _, run := range runs {
		if _, err := flows.PutTargetRun(run); err != nil {
			t.Fatalf("put target run %+v: %v", run, err)
		}
	}
	history, err := flows.ListTargetRuns("flow-1", 10)
	if err != nil {
		t.Fatalf("list target runs: %v", err)
	}
	if len(history) != 2 || history[0].RunID != "run-new" || history[1].RunID != "run-old" {
		t.Fatalf("history order = %+v", history)
	}
}

func openFlowTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "flows.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testFlowAssignment(flowID string, revision int64) flow.Assignment {
	return flow.Assignment{
		FlowID:   flowID,
		Revision: revision,
		Name:     "Memory sweep",
		Enabled:  true,
		Target: flow.TargetSelection{
			SwarmID: "target-1",
			Kind:    "remote",
			Name:    "Laptop",
		},
		Agent: flow.AgentSelection{TargetKind: "background", TargetName: "memory"},
		Workspace: flow.WorkspaceContext{
			WorkspacePath: filepath.Join("workspace", "project"),
		},
		Schedule: flow.ScheduleSpec{
			Cadence:  "Daily",
			Time:     "09:00",
			Timezone: "UTC",
		},
		CatchUpPolicy: flow.CatchUpPolicy{Mode: flow.CatchUpOnce},
		Intent: flow.PromptIntent{
			Prompt: "Summarize outstanding work.",
			Tasks:  []flow.TaskStep{{ID: "read", Title: "Read", Action: "read"}},
		},
	}
}

func testAssignmentCommand(commandID string, assignment flow.Assignment) flow.AssignmentCommand {
	return flow.AssignmentCommand{
		CommandID:  commandID,
		FlowID:     assignment.FlowID,
		Revision:   assignment.Revision,
		Action:     flow.CommandInstall,
		CreatedAt:  time.Date(2025, 1, 2, 8, 0, 0, 0, time.UTC),
		Assignment: assignment,
	}
}
