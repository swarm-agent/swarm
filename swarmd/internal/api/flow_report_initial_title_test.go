package api

import (
	"testing"
	"time"

	"swarm/packages/swarmd/internal/flow"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRunAcceptedFlowNowInitialSessionSnapshotUsesFlowNameTitle(t *testing.T) {
	server, flows := newFlowPeerTestServer(t)
	ensureFlowMemoryAgentRunnable(t, server)
	runner := &fakeFlowRunService{}
	server.runner = runner
	assignment := testAPIFlowAssignment("flow-summary-created-title", 1)
	assignment.Intent.Tasks = []flow.TaskStep{{ID: "task", Title: "Run smoke prompt", Action: "write"}}
	accepted, err := flows.PutAcceptedAssignment(flow.AcceptedAssignment{Assignment: assignment})
	if err != nil {
		t.Fatalf("put accepted assignment: %v", err)
	}
	if accepted.Assignment.Name != "Memory sweep" || flowRunSessionTitle(accepted.Assignment) != "Memory sweep" {
		t.Fatalf("accepted assignment title source = name %q title %q, want flow name", accepted.Assignment.Name, flowRunSessionTitle(accepted.Assignment))
	}
	start := flow.RunStart{
		FlowID:      assignment.FlowID,
		Revision:    assignment.Revision,
		ScheduledAt: time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC),
		SessionID:   "session-summary-created-title",
		RunID:       "run-summary-created-title",
	}
	session, _, err := server.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     start.SessionID,
		Title:         flowRunSessionTitle(accepted.Assignment),
		WorkspacePath: accepted.Assignment.Workspace.WorkspacePath,
		WorkspaceName: "project",
		Mode:          "auto",
		Preference:    &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
		Metadata:      flowRunMetadata(accepted.Assignment, resolvedFlowRunAgent{RuntimeTargetKind: flow.RuntimeTargetKindForProfileMode(accepted.Assignment.Agent.ProfileMode), RuntimeTargetName: accepted.Assignment.Agent.ProfileName}, start.ScheduledAt, false),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if runner.callCount() != 0 {
		t.Fatalf("creating initial flow session should not need runner correction")
	}
	if session.Title != "Memory sweep" {
		t.Fatalf("created title = %q, want flow name", session.Title)
	}
	startedAt := start.ScheduledAt.Add(time.Second)
	if _, err := flows.PutTargetRun(pebblestore.FlowRunSummaryRecord{
		RunID:       start.RunID,
		FlowID:      start.FlowID,
		Revision:    start.Revision,
		ScheduledAt: start.ScheduledAt,
		StartedAt:   startedAt,
		Status:      pebblestore.FlowRunStatusRunning,
		SessionID:   start.SessionID,
	}); err != nil {
		t.Fatalf("put target run: %v", err)
	}
	reportedSession, _, err := server.flowRunReportSessionPayload(startedRunSummary(t, flows, start.RunID))
	if err != nil {
		t.Fatalf("report payload: %v", err)
	}
	if reportedSession == nil {
		t.Fatal("reported session is nil")
	}
	if reportedSession.Title != "Memory sweep" {
		t.Fatalf("reported session = %+v, want flow name from initial durable session", reportedSession)
	}
	createdPayload := requireSessionCreatedPayload(t, server, start.SessionID)
	if createdPayload.Title != "Memory sweep" {
		t.Fatalf("created payload = %+v, want flow name from initial durable session", createdPayload)
	}
	if reportedSession.Metadata["title_pending"] != false || reportedSession.Metadata["title_locked"] != true || reportedSession.Metadata["title_source"] != flowSessionTitleSourceTask {
		t.Fatalf("reported title metadata = %+v", reportedSession.Metadata)
	}
}

func startedRunSummary(t *testing.T, flows *pebblestore.FlowStore, runID string) pebblestore.FlowRunSummaryRecord {
	t.Helper()
	summary, ok, err := flows.GetTargetRun(runID)
	if err != nil || !ok {
		t.Fatalf("get target run ok=%v err=%v", ok, err)
	}
	return summary
}
