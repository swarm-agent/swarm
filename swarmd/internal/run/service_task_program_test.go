package run

import (
	"errors"

	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func taskProgramFixture(maxConcurrency any) map[string]any {
	program := map[string]any{
		"id": "release_program",
		"stages": []any{
			map[string]any{"id": "build", "dependency_evidence": "The complete independent build assignments are ready."},
			map[string]any{"id": "fix", "depends_on": []any{"build"}, "dependency_evidence": "The fixer consumes only integrated build outputs."},
		},
		"jobs": []any{
			map[string]any{"id": "api", "stage_id": "build", "agent_type": "coder", "title": "API Work", "meta_prompt": "Implement only the API contract.", "deliverable": "Committed API change", "owned_scope": []any{"swarmd/internal/api/**"}, "acceptance_criteria": []any{"API contract is complete"}, "dependency_evidence": "No unfinished job is required."},
			map[string]any{"id": "web", "stage_id": "build", "agent_type": "coder", "title": "Web Work", "meta_prompt": "Implement only the web contract.", "deliverable": "Committed web change", "owned_scope": []any{"web/src/**"}, "acceptance_criteria": []any{"Web contract is complete"}, "dependency_evidence": "No unfinished job is required."},
			map[string]any{"id": "fixer", "stage_id": "fix", "depends_on": []any{"api", "web"}, "agent_type": "coder", "title": "Integration Fixer", "meta_prompt": "Fix only integration defects after the prior stage is integrated.", "deliverable": "Committed integration fix", "owned_scope": []any{"swarmd/internal/integration/**"}, "acceptance_criteria": []any{"Integrated state is coherent"}, "dependency_evidence": "Requires both integrated build jobs."},
		},
	}
	if maxConcurrency != nil {
		program["max_concurrency"] = maxConcurrency
	}
	return map[string]any{"action": "start", "mode": "regular", "description": "release work", "prompt": "Run the fully declared release program.", "program": program}
}

func TestParseTaskProgramValidatesCompleteStagedContract(t *testing.T) {
	parsed, err := parseTaskCallArguments(mustJSON(t, taskProgramFixture(float64(2))))
	if err != nil {
		t.Fatalf("parse program: %v", err)
	}
	if parsed.Action != taskProgramActionStart || parsed.Program == nil || parsed.Program.ID != "release_program" || len(parsed.Program.Stages) != 2 || len(parsed.Program.Jobs) != 3 || len(parsed.Launches) != 3 {
		t.Fatalf("unexpected program: %#v", parsed)
	}
	if parsed.Program.MaxConcurrency == nil || *parsed.Program.MaxConcurrency != 2 {
		t.Fatalf("max concurrency = %#v", parsed.Program.MaxConcurrency)
	}
	if got := parsed.Launches[2].SourceArguments["program_job_id"]; got != "fixer" {
		t.Fatalf("program job identity = %#v", got)
	}
}

func TestParseTaskProgramRejectsInvalidGraphAndScopesBeforeLaunch(t *testing.T) {
	tests := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"missing acceptance", func(program map[string]any) {
			delete(program["jobs"].([]any)[0].(map[string]any), "acceptance_criteria")
		}, "acceptance_criteria"},
		{"copied broad assignment", func(program map[string]any) {
			first := program["jobs"].([]any)[0].(map[string]any)
			second := program["jobs"].([]any)[1].(map[string]any)
			second["title"], second["meta_prompt"], second["deliverable"] = first["title"], first["meta_prompt"], first["deliverable"]
		}, "copies the reviewable assignment"},
		{"future stage dependency", func(program map[string]any) {
			program["stages"].([]any)[0].(map[string]any)["depends_on"] = []any{"fix"}
		}, "earlier stage"},
		{"overlapping coder scope", func(program map[string]any) {
			program["jobs"].([]any)[1].(map[string]any)["owned_scope"] = []any{"swarmd/internal/api/handlers/**"}
		}, "owned scopes overlap"},
		{"unsupported agent", func(program map[string]any) { program["jobs"].([]any)[0].(map[string]any)["agent_type"] = "idea" }, "coder, finder, or designer"},
		{"cap exceeds jobs", func(program map[string]any) { program["max_concurrency"] = float64(4) }, "cannot exceed total job count"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := taskProgramFixture(nil)
			tc.edit(args["program"].(map[string]any))
			_, err := parseTaskCallArguments(mustJSON(t, args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

type taskProgramCleanupWorktreeStub struct {
	taskLaunchWorktreeStub
	cleanupParentPaths []string
}

func (s *taskProgramCleanupWorktreeStub) RemoveIntegratedTaskWorkspace(parentPath, childPath, sessionID, branchName, baseCommit, headCommit string) error {
	s.cleanupParentPaths = append(s.cleanupParentPaths, parentPath)
	return s.taskLaunchWorktreeStub.RemoveIntegratedTaskWorkspace(parentPath, childPath, sessionID, branchName, baseCommit, headCommit)
}

func (s *taskProgramCleanupWorktreeStub) PrepareTaskIntegration(_ string, expectedParentBranch, expectedParentHead string, children []worktreeruntime.TaskIntegrationChild) (worktreeruntime.TaskIntegrationPlan, error) {
	return worktreeruntime.TaskIntegrationPlan{ParentBranch: expectedParentBranch, ParentHead: expectedParentHead}, nil
}

func (s *taskProgramCleanupWorktreeStub) ApplyTaskIntegration(_ string, plan worktreeruntime.TaskIntegrationPlan) (worktreeruntime.TaskIntegrationResult, error) {
	return worktreeruntime.TaskIntegrationResult{TaskIntegrationPlan: plan, ResultingParentHead: plan.ParentHead}, nil
}

func TestTaskProgramSchedulerUnlocksOnlyIntegratedDependencies(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ActiveStageID: "fix",
		Definition: pebblestore.TaskProgramDefinition{
			Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}, {ID: "fix", DependsOn: []string{"build"}}},
			Jobs: []pebblestore.TaskProgramJobSpec{
				{ID: "api", StageID: "build"},
				{ID: "web", StageID: "build"},
				{ID: "fixer", StageID: "fix", DependsOn: []string{"api", "web"}},
			},
		},
		Jobs: []pebblestore.TaskProgramJobRecord{
			{JobID: "api", StageID: "build", State: pebblestore.TaskProgramJobHandoffReady},
			{JobID: "web", StageID: "build", State: pebblestore.TaskProgramJobHandoffReady},
			{JobID: "fixer", StageID: "fix", State: pebblestore.TaskProgramJobDeclared},
		},
	}
	if got := taskProgramReadyJobIndexes(record, 1); len(got) != 0 {
		t.Fatalf("unintegrated dependencies unlocked fixer: %v", got)
	}
	record.Jobs[0].State = pebblestore.TaskProgramJobIntegrated
	if got := taskProgramReadyJobIndexes(record, 1); len(got) != 0 {
		t.Fatalf("partially integrated stage unlocked fixer: %v", got)
	}
	record.Jobs[1].State = pebblestore.TaskProgramJobIntegrated
	if got := taskProgramReadyJobIndexes(record, 1); len(got) != 1 || got[0] != 2 {
		t.Fatalf("fully integrated parallel stage did not unlock fixer: %v", got)
	}
	record.Jobs[2].State = pebblestore.TaskProgramJobCompleted
	if taskProgramStageHasRunningOrDeclared(record, 1) {
		t.Fatal("completed dependent stage should be terminal")
	}
}

func TestTaskProgramStructuredBlockerPreservesExactRepairState(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ProgramID: "release", Revision: 7, ActiveStageID: "build", ParentHead: strings.Repeat("a", 40),
		Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{{ID: "api", StageID: "build", AgentType: "coder"}}},
		Jobs:       []pebblestore.TaskProgramJobRecord{{JobID: "api", StageID: "build", State: pebblestore.TaskProgramJobHandoffReady, AttemptNumber: 1, ChildSessionID: "child", WorkspacePath: "worktree", WorktreeBranch: "agent/api", ParentBranch: "dev", ImmutableStageBase: strings.Repeat("a", 40), ChildHead: strings.Repeat("b", 40), IntegrationState: "pending"}},
	}
	scheduler := taskProgramScheduler{record: record, barrierJobID: "api", expectedParentHead: record.ParentHead}
	blocker := scheduler.structuredBlocker("integration_conflict", errors.New("cherry-pick conflict"), "", "api")
	if blocker.ProgramID != "release" || blocker.ProgramRevision != 8 || blocker.StageID != "build" || blocker.JobID != "api" || blocker.AttemptNumber != 1 || blocker.ExpectedParentHead != record.ParentHead || blocker.RepairAction != "resolve_integration_conflict" || blocker.NextAction != "resolve_integration_conflict_then_author_new_program_for_remaining_work" {
		t.Fatalf("blocker identity = %#v", blocker)
	}
	if len(blocker.PreservedChildren) != 1 || blocker.PreservedChildren[0].ChildSessionID != "child" || blocker.PreservedChildren[0].ChildHead != strings.Repeat("b", 40) {
		t.Fatalf("preserved handoff = %#v", blocker.PreservedChildren)
	}
}

func TestTaskProgramBlockedJobIsTerminalAndNeverRescheduled(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ActiveStageID: "build",
		Definition:    pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "blocked", StageID: "build"}, {ID: "pending", StageID: "build"}}},
		Jobs:          []pebblestore.TaskProgramJobRecord{{JobID: "blocked", StageID: "build", State: pebblestore.TaskProgramJobBlocked, AttemptNumber: 1}, {JobID: "pending", StageID: "build", State: pebblestore.TaskProgramJobDeclared}},
	}
	ready := taskProgramReadyJobIndexes(record, 0)
	if len(ready) != 1 || ready[0] != 1 {
		t.Fatalf("blocked child was rescheduled: %v", ready)
	}
	record.Jobs[1].State = pebblestore.TaskProgramJobCompleted
	if taskProgramStageHasRunningOrDeclared(record, 0) {
		t.Fatal("terminal blocked child kept the stage schedulable")
	}
}

func TestTaskProgramFinderHandoffHydratesDependentCoderWithVerificationWarning(t *testing.T) {
	svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentID)
	if err != nil || !ok {
		t.Fatalf("load parent: ok=%v err=%v", ok, err)
	}
	mutation, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: parentID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID,
		ClientRequestID: "finder-handoff", IdempotencyKey: "finder-handoff", PayloadHash: "finder-handoff", RequestHash: "finder-handoff",
		Kind: sessionruntime.SessionMutationAppendMessage, Message: &pebblestore.MessageSnapshot{ID: "finder-handoff", Role: "assistant", Content: "Inspect service.go:42 before editing."},
	})
	if err != nil || mutation.Message == nil {
		t.Fatalf("persist V3 Finder handoff: message=%#v err=%v", mutation.Message, err)
	}
	message := *mutation.Message
	transitiveMutation, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: parentID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID,
		ClientRequestID: "transitive-finder-handoff", IdempotencyKey: "transitive-finder-handoff", PayloadHash: "transitive-finder-handoff", RequestHash: "transitive-finder-handoff",
		Kind: sessionruntime.SessionMutationAppendMessage, Message: &pebblestore.MessageSnapshot{ID: "transitive-finder-handoff", Role: "assistant", Content: "Also inspect task_program_store.go:407."},
	})
	if err != nil || transitiveMutation.Message == nil {
		t.Fatalf("persist transitive V3 Finder handoff: message=%#v err=%v", transitiveMutation.Message, err)
	}
	transitiveMessage := *transitiveMutation.Message
	record := pebblestore.TaskProgramRecord{
		Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{
			{ID: "inspect", StageID: "research", AgentType: "finder"},
			{ID: "audit", StageID: "audit", AgentType: "finder", DependsOn: []string{"inspect"}},
			{ID: "implement", StageID: "build", AgentType: "coder", DependsOn: []string{"audit"}},
		}},
		Jobs: []pebblestore.TaskProgramJobRecord{
			{JobID: "inspect", StageID: "research", State: pebblestore.TaskProgramJobCompleted, ChildSessionID: message.SessionID, HandoffRef: &pebblestore.TaskProgramHandoffRef{SessionID: message.SessionID, MessageID: message.ID, GlobalSeq: message.GlobalSeq}},
			{JobID: "audit", StageID: "audit", State: pebblestore.TaskProgramJobCompleted, ChildSessionID: transitiveMessage.SessionID, HandoffRef: &pebblestore.TaskProgramHandoffRef{SessionID: transitiveMessage.SessionID, MessageID: transitiveMessage.ID, GlobalSeq: transitiveMessage.GlobalSeq}},
			{JobID: "implement", StageID: "build", State: pebblestore.TaskProgramJobDeclared},
		},
	}
	scheduler := taskProgramScheduler{service: svc, parentSession: pebblestore.SessionSnapshot{ID: parentID}, record: record}
	for _, consumer := range []string{"finder", "designer", "coder"} {
		scheduler.record.Definition.Jobs[2].AgentType = consumer
		if text, err := scheduler.finderHandoffsForJob(2); err != nil || !strings.Contains(text, "Inspect service.go:42") {
			t.Fatalf("%s handoff: %q %v", consumer, text, err)
		}
	}
	handoff, err := scheduler.finderHandoffsForJob(2)
	if err != nil {
		t.Fatalf("hydrate Finder handoff: %v", err)
	}
	for _, required := range []string{"Inspect service.go:42 before editing.", "Also inspect task_program_store.go:407.", "Finder agents can make mistakes", "Independently verify every relevant claim"} {
		if !strings.Contains(handoff, required) {
			t.Fatalf("Finder handoff missing %q: %s", required, handoff)
		}
	}
}

func TestTaskProgramFinderHandoffLookupFailsClosedForMissingV3Message(t *testing.T) {
	svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	record := pebblestore.TaskProgramRecord{
		Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{
			{ID: "inspect", StageID: "research", AgentType: "finder"},
			{ID: "implement", StageID: "build", AgentType: "coder", DependsOn: []string{"inspect"}},
		}},
		Jobs: []pebblestore.TaskProgramJobRecord{
			{JobID: "inspect", StageID: "research", State: pebblestore.TaskProgramJobCompleted, ChildSessionID: parentID, HandoffRef: &pebblestore.TaskProgramHandoffRef{SessionID: parentID, MessageID: "missing", GlobalSeq: 999}},
			{JobID: "implement", StageID: "build", State: pebblestore.TaskProgramJobDeclared},
		},
	}
	scheduler := taskProgramScheduler{service: svc, parentSession: pebblestore.SessionSnapshot{ID: parentID}, record: record}
	if _, err := scheduler.finderHandoffsForJob(1); err == nil || !strings.Contains(err.Error(), "no longer resolves") {
		t.Fatalf("missing V3 handoff error = %v", err)
	}
}

func TestTaskProgramManagedArtifactValidationRejectsLineageMismatch(t *testing.T) {
	svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentID)
	if err != nil || !ok {
		t.Fatalf("load parent: ok=%v err=%v", ok, err)
	}
	programID, jobID, callID, childID := "managed_program", "design", "call-managed-program", "child-design"
	spec := taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, SourceArguments: map[string]any{"program_id": programID, "program_job_id": jobID}}
	if _, err := svc.ensureManagedDesignerArtifactCollection(parent, callID, []taskLaunchSpec{spec}, nil); err != nil {
		t.Fatalf("allocate managed collection: %v", err)
	}
	run := managedDesignerArtifactContext(parent, callID, spec, 1)
	run.ChildSessionID = childID
	svc.markManagedDesignerArtifactFailed(parent, run, childID, "managed_output_missing")
	reference := &taskArtifactReference{SessionID: parent.ID, ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 1, TurnID: "turn", CandidateID: "candidate", Status: pebblestore.SessionArtifactStatusReady}
	scheduler := taskProgramScheduler{service: svc, parentSession: parent, record: pebblestore.TaskProgramRecord{ParentSessionID: parent.ID, ProgramID: programID, ReservationCallID: callID}}
	failedReference := *reference
	failedReference.Status = pebblestore.SessionArtifactStatusFailed
	if err := scheduler.validateManagedDesignerArtifact(jobID, childID, &failedReference); err == nil || !strings.Contains(err.Error(), "malformed or mismatched") {
		t.Fatalf("non-ready artifact rejection = %v", err)
	}
	if err := scheduler.validateManagedDesignerArtifact(jobID, "different-child", reference); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("lineage mismatch rejection = %v", err)
	}
}

func TestParseTaskProgramStartWithoutDefinitionSelectsApprovedCheckpointProgram(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"action":"start","workspace_path":"/shared/repo"}`)
	if err != nil || !parsed.PlannedProgram || parsed.Program != nil || parsed.Action != taskProgramActionStart || parsed.ProgramWorkspacePath != "/shared/repo" {
		t.Fatalf("planned start = %#v err=%v", parsed, err)
	}
}

func TestParseTaskProgramLifecycleRejectsAllExistingProgramContinuation(t *testing.T) {
	status, err := parseTaskCallArguments(`{"action":"status","program_id":"release_program"}`)
	if err != nil || status.ProgramID != "release_program" || len(status.Launches) != 0 {
		t.Fatalf("status = %#v err=%v", status, err)
	}
	if _, err := parseTaskCallArguments(`{"action":"start","program_id":"release_program"}`); err == nil || !strings.Contains(err.Error(), "new program definition") {
		t.Fatalf("existing program start error = %v", err)
	}
	if _, err := parseTaskCallArguments(`{"action":"resume","program_id":"release_program"}`); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("retired resume error = %v", err)
	}
}
