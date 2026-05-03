package pebblestore

import (
	"path/filepath"
	"strings"
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
	if loaded.Assignment.Agent.ProfileName != "memory" || loaded.Assignment.Agent.ProfileMode != "background" {
		t.Fatalf("loaded durable agent = %+v", loaded.Assignment.Agent)
	}
	if loaded.Assignment.Agent.TargetKind != "background" || loaded.Assignment.Agent.TargetName != "memory" {
		t.Fatalf("loaded derived agent = %+v", loaded.Assignment.Agent)
	}
	if len(loaded.Assignment.Schedule.Times) != 1 || loaded.Assignment.Schedule.Times[0] != "09:00" {
		t.Fatalf("loaded normalized schedule times = %+v", loaded.Assignment.Schedule.Times)
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

func TestFlowStoreNormalizesPersistedScheduleTimes(t *testing.T) {
	store := openFlowTestStore(t)
	flows := NewFlowStore(store)
	assignment := testFlowAssignment("flow-times", 1)
	assignment.Schedule.Time = "17:00"
	assignment.Schedule.Times = []string{"17:00", "09:00", "17:00"}

	definition, err := flows.PutDefinition(FlowDefinitionRecord{Assignment: assignment})
	if err != nil {
		t.Fatalf("put definition: %v", err)
	}
	if len(definition.Assignment.Schedule.Times) != 2 || definition.Assignment.Schedule.Times[0] != "09:00" || definition.Assignment.Schedule.Times[1] != "17:00" {
		t.Fatalf("definition normalized schedule times = %+v", definition.Assignment.Schedule.Times)
	}
	if definition.Assignment.Schedule.Time != "09:00" {
		t.Fatalf("definition normalized schedule time = %q", definition.Assignment.Schedule.Time)
	}
	loaded, ok, err := flows.GetDefinition(definition.FlowID)
	if err != nil || !ok {
		t.Fatalf("get definition ok=%v err=%v", ok, err)
	}
	if len(loaded.Assignment.Schedule.Times) != 2 || loaded.Assignment.Schedule.Times[0] != "09:00" || loaded.Assignment.Schedule.Times[1] != "17:00" {
		t.Fatalf("loaded normalized schedule times = %+v", loaded.Assignment.Schedule.Times)
	}
	if loaded.Assignment.Schedule.Time != "09:00" {
		t.Fatalf("loaded normalized schedule time = %q", loaded.Assignment.Schedule.Time)
	}
}

func TestFlowStoreRejectsRuntimeAgentFieldsInDefinitionJSON(t *testing.T) {
	store := openFlowTestStore(t)
	flows := NewFlowStore(store)
	assignment := testFlowAssignment("flow-runtime-agent", 1)
	assignment.Agent = flow.AgentSelection{ProfileName: "memory", ProfileMode: "background", TargetKind: "background", TargetName: "memory"}

	definition, err := flows.PutDefinition(FlowDefinitionRecord{Assignment: assignment})
	if err != nil {
		t.Fatalf("put definition: %v", err)
	}
	loaded, ok, err := flows.GetDefinition(definition.FlowID)
	if err != nil || !ok {
		t.Fatalf("get definition ok=%v err=%v", ok, err)
	}
	if loaded.Assignment.Agent.ProfileName != "memory" || loaded.Assignment.Agent.ProfileMode != "background" {
		t.Fatalf("loaded durable agent = %+v", loaded.Assignment.Agent)
	}
	if loaded.Assignment.Agent.TargetKind != "background" || loaded.Assignment.Agent.TargetName != "memory" {
		t.Fatalf("loaded derived agent = %+v", loaded.Assignment.Agent)
	}
	payload, ok, err := store.GetBytes(KeyFlowDefinition(definition.FlowID))
	if err != nil || !ok {
		t.Fatalf("get raw definition ok=%v err=%v", ok, err)
	}
	if string(payload) == "" {
		t.Fatal("raw definition payload was empty")
	}
	if strings.Contains(string(payload), "target_kind") || strings.Contains(string(payload), "target_name") {
		t.Fatalf("raw definition leaked runtime agent fields: %s", payload)
	}
	if !strings.Contains(string(payload), `"profile_name":"memory"`) || !strings.Contains(string(payload), `"profile_mode":"background"`) {
		t.Fatalf("raw definition missing durable profile selector: %s", payload)
	}
	badJSON := []byte(`{"flow_id":"bad-flow","revision":1,"assignment":{"flow_id":"bad-flow","revision":1,"name":"Bad","enabled":true,"target":{"swarm_id":"target-1","kind":"remote"},"agent":{"target_kind":"background","target_name":"memory"},"workspace":{"workspace_path":"workspace/project"},"schedule":{"cadence":"on_demand","timezone":"UTC"},"catch_up_policy":{"mode":"once"},"intent":{"prompt":"x"}}}`)
	if err := store.PutBytes(KeyFlowDefinition("bad-flow"), badJSON); err != nil {
		t.Fatalf("put raw bad definition: %v", err)
	}
	loadedBad, ok, err := flows.GetDefinition("bad-flow")
	if err != nil || !ok {
		t.Fatalf("get repaired bad definition ok=%v err=%v", ok, err)
	}
	if loadedBad.Assignment.Agent.ProfileName != "memory" || loadedBad.Assignment.Agent.ProfileMode != "background" {
		t.Fatalf("repaired runtime-only bad definition agent = %+v", loadedBad.Assignment.Agent)
	}
}

func TestFlowStoreRepairsLegacyAgentPayloadWithRuntimeAndModelFields(t *testing.T) {
	store := openFlowTestStore(t)
	flows := NewFlowStore(store)
	legacyJSON := []byte(`{"flow_id":"legacy-flow","revision":3,"assignment":{"flow_id":"legacy-flow","revision":3,"name":"Legacy","enabled":true,"target":{"swarm_id":"target-1","kind":"remote","name":"Laptop"},"agent":{"target_kind":"background","target_name":"memory","model":"gpt-5","service_tier":"priority"},"workspace":{"workspace_path":"workspace/project"},"schedule":{"cadence":"on_demand","timezone":"UTC"},"catch_up_policy":{"mode":"once"},"intent":{"prompt":"repair me"}},"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}`)
	if err := store.PutBytes(KeyFlowDefinition("legacy-flow"), legacyJSON); err != nil {
		t.Fatalf("put raw legacy definition: %v", err)
	}
	loaded, ok, err := flows.GetDefinition("legacy-flow")
	if err != nil || !ok {
		t.Fatalf("get repaired definition ok=%v err=%v", ok, err)
	}
	if loaded.Assignment.Agent.ProfileName != "memory" || loaded.Assignment.Agent.ProfileMode != "background" {
		t.Fatalf("repaired agent = %+v", loaded.Assignment.Agent)
	}
	payload, ok, err := store.GetBytes(KeyFlowDefinition("legacy-flow"))
	if err != nil || !ok {
		t.Fatalf("get repaired payload ok=%v err=%v", ok, err)
	}
	assertFlowAgentPayloadRepaired(t, payload)
}

func TestFlowStoreRepairsLegacyOutboxAgentPayloadWithRuntimeFields(t *testing.T) {
	store := openFlowTestStore(t)
	flows := NewFlowStore(store)
	legacyJSON := []byte(`{"command_id":"flow-6699b3e358c33c74-1-install-0cb0a20bc6e2","flow_id":"flow-6699b3e358c33c74","revision":1,"target_swarm_id":"target-1","target":{"swarm_id":"target-1","kind":"remote"},"command":{"command_id":"flow-6699b3e358c33c74-1-install-0cb0a20bc6e2","flow_id":"flow-6699b3e358c33c74","revision":1,"action":"install","created_at":"2025-01-01T00:00:00Z","assignment":{"flow_id":"flow-6699b3e358c33c74","revision":1,"name":"Legacy outbox","enabled":true,"target":{"swarm_id":"target-1","kind":"remote"},"agent":{"target_kind":"background","target_name":"memory","model":"gpt-5","service_tier":"priority"},"workspace":{"workspace_path":"workspace/project"},"schedule":{"cadence":"on_demand","timezone":"UTC"},"catch_up_policy":{"mode":"once"},"intent":{"prompt":"repair outbox"}}},"status":"pending","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}`)
	commandID := "flow-6699b3e358c33c74-1-install-0cb0a20bc6e2"
	if err := store.PutBytes(KeyFlowOutbox(commandID), legacyJSON); err != nil {
		t.Fatalf("put raw legacy outbox: %v", err)
	}
	if err := store.PutBytes(KeyFlowOutboxStatus(FlowOutboxStatusPending, 0, commandID), []byte(KeyFlowOutbox(commandID))); err != nil {
		t.Fatalf("put outbox status index: %v", err)
	}
	loaded, ok, err := flows.GetOutboxCommand(commandID)
	if err != nil || !ok {
		t.Fatalf("get repaired outbox ok=%v err=%v", ok, err)
	}
	if loaded.Command.Assignment.Agent.ProfileName != "memory" || loaded.Command.Assignment.Agent.ProfileMode != "background" {
		t.Fatalf("repaired outbox agent = %+v", loaded.Command.Assignment.Agent)
	}
	payload, ok, err := store.GetBytes(KeyFlowOutbox(commandID))
	if err != nil || !ok {
		t.Fatalf("get repaired outbox payload ok=%v err=%v", ok, err)
	}
	assertFlowAgentPayloadRepaired(t, payload)
	listed, err := flows.ListOutboxCommands(FlowOutboxStatusPending, 10)
	if err != nil {
		t.Fatalf("list repaired outbox: %v", err)
	}
	if len(listed) != 1 || listed[0].CommandID != commandID || listed[0].Command.Assignment.Agent.ProfileName != "memory" {
		t.Fatalf("listed repaired outbox = %+v", listed)
	}
}

func assertFlowAgentPayloadRepaired(t *testing.T, payload []byte) {
	t.Helper()
	if strings.Contains(string(payload), "target_kind") || strings.Contains(string(payload), "target_name") || strings.Contains(string(payload), "service_tier") || strings.Contains(string(payload), "model") {
		t.Fatalf("repaired payload still contains legacy agent fields: %s", payload)
	}
	if !strings.Contains(string(payload), `"profile_name":"memory"`) || !strings.Contains(string(payload), `"profile_mode":"background"`) {
		t.Fatalf("repaired payload missing durable agent selector: %s", payload)
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
		{RunID: "run-old", FlowID: "flow-1", Revision: 7, ScheduledAt: due1.DueAt, StartedAt: time.Date(2025, 1, 2, 10, 1, 0, 0, time.UTC), FinishedAt: time.Date(2025, 1, 2, 10, 2, 0, 0, time.UTC), Status: FlowRunStatusSuccess},
		{RunID: "run-new", FlowID: "flow-1", Revision: 7, ScheduledAt: due3.DueAt, StartedAt: time.Date(2025, 1, 3, 9, 1, 0, 0, time.UTC), FinishedAt: time.Date(2025, 1, 3, 9, 2, 0, 0, time.UTC), Status: FlowRunStatusFailed},
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
	pendingReports, err := flows.ListPendingTargetRunReports(time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatalf("list pending target run reports: %v", err)
	}
	if len(pendingReports) != 2 || pendingReports[0].RunID != "run-new" || pendingReports[1].RunID != "run-old" {
		t.Fatalf("pending reports = %+v", pendingReports)
	}
	pendingReports[0].ReportedAt = time.Date(2025, 1, 4, 1, 0, 0, 0, time.UTC)
	if _, err := flows.PutTargetRun(pendingReports[0]); err != nil {
		t.Fatalf("mark reported: %v", err)
	}
	pendingReports, err = flows.ListPendingTargetRunReports(time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatalf("list pending target run reports after reported: %v", err)
	}
	if len(pendingReports) != 1 || pendingReports[0].RunID != "run-old" {
		t.Fatalf("pending reports after reported = %+v", pendingReports)
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
		Agent: flow.AgentSelection{ProfileName: "memory", ProfileMode: "background"},
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
