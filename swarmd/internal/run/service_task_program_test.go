package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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

func TestParseTaskProgramPreservesLegacyRegularPath(t *testing.T) {
	parsed, err := parseTaskCallArguments(mustJSON(t, map[string]any{"prompt": "inspect", "agent": "finder", "role": "map files"}))
	if err != nil {
		t.Fatalf("parse legacy task: %v", err)
	}
	if parsed.Action != "spawn" || parsed.Program != nil || parsed.Swarm != nil || parsed.Mode != taskModeRegular || len(parsed.Launches) != 1 || parsed.Launches[0].RequestedSubagentType != "finder" {
		t.Fatalf("legacy parse changed: %#v", parsed)
	}
}

func TestParseTaskProgramPreservesLegacySplitWave(t *testing.T) {
	parsed, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "regular", "description": "split audit", "prompt": "Inspect independent surfaces.",
		"launches": []any{
			map[string]any{"subagent_type": "finder", "title": "Backend Map", "meta_prompt": "Map backend files.", "deliverable": "Backend map", "dependency_evidence": "Independent read-only scope."},
			map[string]any{"subagent_type": "designer", "title": "Visual Variant", "meta_prompt": "Create the requested visual variant.", "deliverable": "Reusable variant", "owned_scope": []any{"web/src/variants/program-compat.tsx"}, "dependency_evidence": "Distinct output target."},
		},
	}))
	if err != nil {
		t.Fatalf("parse legacy split task: %v", err)
	}
	if parsed.Action != "spawn" || parsed.Program != nil || parsed.Swarm != nil || len(parsed.Launches) != 2 || parsed.Launches[0].RequestedSubagentType != "finder" || parsed.Launches[1].RequestedSubagentType != "designer" {
		t.Fatalf("legacy split parse changed: %#v", parsed)
	}
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

func TestParseTaskProgramLeavesConcurrencyUncappedByDefault(t *testing.T) {
	parsed, err := parseTaskCallArguments(mustJSON(t, taskProgramFixture(nil)))
	if err != nil {
		t.Fatalf("parse uncapped program: %v", err)
	}
	if parsed.Program.MaxConcurrency != nil {
		t.Fatalf("unexpected default cap: %v", *parsed.Program.MaxConcurrency)
	}
	capacity := taskProgramEffectiveCapacity(len(parsed.Program.Jobs), 2, 7, nil)
	if capacity.TotalJobs != 3 || capacity.ReadyJobs != 2 || capacity.ActiveAccountCapacity != 7 || capacity.EffectiveCapacity != 2 {
		t.Fatalf("capacity = %#v", capacity)
	}
	cap := 1
	capacity = taskProgramEffectiveCapacity(3, 2, 7, &cap)
	if capacity.EffectiveCapacity != 1 || capacity.ExplicitLowerCap == nil {
		t.Fatalf("capped capacity = %#v", capacity)
	}
}

func TestTaskProgramSixJobCapacityUsesExplicitTwoActiveCap(t *testing.T) {
	capacity := taskProgramEffectiveCapacity(6, 6, 10, intPointer(2))
	if capacity.TotalJobs != 6 || capacity.ReadyJobs != 6 || capacity.ActiveAccountCapacity != 10 || capacity.ExplicitLowerCap == nil || *capacity.ExplicitLowerCap != 2 || capacity.EffectiveCapacity != 2 {
		t.Fatalf("explicit six-job capacity = %#v", capacity)
	}
	if remaining := capacity.TotalJobs - capacity.EffectiveCapacity; remaining != 4 {
		t.Fatalf("explicit cap should retain four declared jobs for later cohorts, got %d", remaining)
	}
}

func TestTaskProgramUncappedCapacityUsesAllUsefulAvailableCapacity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		totalJobs  int
		readyJobs  int
		accountCap int
		want       int
	}{
		{name: "all six ready with capacity", totalJobs: 6, readyJobs: 6, accountCap: 10, want: 6},
		{name: "account capacity is ceiling", totalJobs: 9, readyJobs: 9, accountCap: 4, want: 4},
		{name: "ready work is ceiling", totalJobs: 20, readyJobs: 3, accountCap: 10, want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capacity := taskProgramEffectiveCapacity(tc.totalJobs, tc.readyJobs, tc.accountCap, nil)
			if capacity.ExplicitLowerCap != nil || capacity.EffectiveCapacity != tc.want {
				t.Fatalf("uncapped capacity = %#v, want effective %d", capacity, tc.want)
			}
		})
	}
}

func intPointer(value int) *int { return &value }

func TestParseTaskProgramAcceptsArbitraryBoundedProgramSize(t *testing.T) {
	const jobCount = 17
	jobs := make([]any, 0, jobCount)
	for i := 0; i < jobCount; i++ {
		jobs = append(jobs, map[string]any{
			"id": fmt.Sprintf("job_%02d", i), "stage_id": "inspect", "agent_type": "finder",
			"title": fmt.Sprintf("Inspect %02d", i), "meta_prompt": fmt.Sprintf("Inspect independent scope %02d.", i),
			"deliverable": fmt.Sprintf("Evidence %02d", i), "owned_scope": []any{fmt.Sprintf("scope/%02d", i)},
			"acceptance_criteria": []any{fmt.Sprintf("Scope %02d is mapped", i)}, "dependency_evidence": "Independent read-only inspection.",
		})
	}
	parsed, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"action": "start", "mode": "regular", "description": "arbitrary inspection", "prompt": "Run the declared inspections.",
		"program": map[string]any{"id": "arbitrary_program", "stages": []any{map[string]any{"id": "inspect", "dependency_evidence": "All inspections are dependency-ready."}}, "jobs": jobs},
	}))
	if err != nil {
		t.Fatalf("parse arbitrary program: %v", err)
	}
	if len(parsed.Program.Jobs) != jobCount || len(parsed.Launches) != jobCount || parsed.Program.MaxConcurrency != nil {
		t.Fatalf("arbitrary program shape = jobs %d launches %d cap %#v", len(parsed.Program.Jobs), len(parsed.Launches), parsed.Program.MaxConcurrency)
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

func TestTaskProgramStatusProjectionIsBoundedAndComplete(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ParentSessionID: "parent", ProgramID: "release", Revision: 4, ResumeGeneration: 2,
		State: pebblestore.TaskProgramStateRunning, ActiveStageID: "build", NextAction: "integrate_handoff_ready_jobs",
		Jobs: []pebblestore.TaskProgramJobRecord{
			{JobID: "declared", StageID: "build", State: pebblestore.TaskProgramJobDeclared},
			{JobID: "running", StageID: "build", State: pebblestore.TaskProgramJobRunning, ChildSessionID: "child"},
			{JobID: "handoff", StageID: "build", State: pebblestore.TaskProgramJobHandoffReady, ImmutableStageBase: "base", ChildHead: "head", IntegrationState: "pending"},
			{JobID: "integrated", StageID: "build", State: pebblestore.TaskProgramJobIntegrated},
			{JobID: "blocked", StageID: "fix", State: pebblestore.TaskProgramJobBlocked},
			{JobID: "failed", StageID: "fix", State: pebblestore.TaskProgramJobFailed},
			{JobID: "cancelled", StageID: "fix", State: pebblestore.TaskProgramJobCancelled},
			{JobID: "completed", StageID: "fix", State: pebblestore.TaskProgramJobCompleted},
		},
	}
	payload := taskProgramStatusPayload(record, false)
	counts := payload["counts"].(map[string]int)
	for _, state := range []string{"declared", "running", "handoff_ready", "integrated", "blocked", "failed", "cancelled", "completed"} {
		if counts[state] != 1 {
			t.Fatalf("count %s = %d", state, counts[state])
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "report") || strings.Contains(string(raw), "transcript") {
		t.Fatalf("projection leaked unbounded content: %s", raw)
	}
}

func TestTaskProgramSchedulerRetainsQueuedJobsAcrossCapacityCohorts(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ActiveStageID: "build",
		Definition: pebblestore.TaskProgramDefinition{
			Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}},
			Jobs: []pebblestore.TaskProgramJobSpec{
				{ID: "one", StageID: "build"}, {ID: "two", StageID: "build"}, {ID: "three", StageID: "build"},
			},
		},
		Jobs: []pebblestore.TaskProgramJobRecord{
			{JobID: "one", StageID: "build", State: pebblestore.TaskProgramJobDeclared},
			{JobID: "two", StageID: "build", State: pebblestore.TaskProgramJobRunning},
			{JobID: "three", StageID: "build", State: pebblestore.TaskProgramJobDeclared},
		},
	}
	if got := taskProgramReadyJobIndexes(record, 0); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("ready indexes = %v", got)
	}
	if !taskProgramStageHasRunningOrDeclared(record, 0) {
		t.Fatal("stage should remain active while queued or running jobs exist")
	}
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

func TestTaskProgramStatusIncludesCoherentParentHead(t *testing.T) {
	record := pebblestore.TaskProgramRecord{ParentSessionID: "parent", ProgramID: "release", ParentHead: strings.Repeat("a", 40), Revision: 2, ResumeGeneration: 1, State: pebblestore.TaskProgramStateCompleted}
	payload := taskProgramStatusPayload(record, false)
	if payload["parent_head"] != record.ParentHead {
		t.Fatalf("parent head = %#v", payload["parent_head"])
	}
}

func TestTaskProgramStructuredBlockerPreservesExactRepairState(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ProgramID: "release", Revision: 7, ResumeGeneration: 2, ActiveStageID: "build", ParentHead: strings.Repeat("a", 40),
		Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{{ID: "api", StageID: "build", AgentType: "coder"}}},
		Jobs:       []pebblestore.TaskProgramJobRecord{{JobID: "api", StageID: "build", State: pebblestore.TaskProgramJobHandoffReady, AttemptNumber: 1, ChildSessionID: "child", WorkspacePath: "worktree", WorktreeBranch: "agent/api", ParentBranch: "dev", ImmutableStageBase: strings.Repeat("a", 40), ChildHead: strings.Repeat("b", 40), IntegrationState: "pending"}},
	}
	scheduler := taskProgramScheduler{record: record, barrierJobID: "api", expectedParentHead: record.ParentHead}
	blocker := scheduler.structuredBlocker("integration_conflict", errors.New("cherry-pick conflict"), "repair_integration_then_resume", "api")
	if blocker.ProgramID != "release" || blocker.ProgramRevision != 8 || blocker.ResumeGeneration != 2 || blocker.StageID != "build" || blocker.JobID != "api" || blocker.AttemptNumber != 1 || blocker.ExpectedParentHead != record.ParentHead {
		t.Fatalf("blocker identity = %#v", blocker)
	}
	if len(blocker.PreservedChildren) != 1 || blocker.PreservedChildren[0].ChildSessionID != "child" || blocker.PreservedChildren[0].ChildHead != strings.Repeat("b", 40) {
		t.Fatalf("preserved handoff = %#v", blocker.PreservedChildren)
	}
}

func TestTaskProgramSpecReconstructionDoesNotInventJobs(t *testing.T) {
	record := pebblestore.TaskProgramRecord{ProgramID: "release", Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "api", StageID: "build", AgentType: "coder", Title: "API", MetaPrompt: "implement", Deliverable: "commit", OwnedScope: []string{"api/**"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "ready"}}}}
	spec := taskProgramSpecFromRecord(record)
	launches := taskProgramLaunchesFromSpec(spec)
	if spec.ID != "release" || len(spec.Jobs) != 1 || len(launches) != 1 || launches[0].SourceArguments["program_job_id"] != "api" {
		t.Fatalf("reconstructed spec=%#v launches=%#v", spec, launches)
	}
}

func TestTaskProgramErrorCodesAreActionable(t *testing.T) {
	cases := map[string]string{
		"parent worktree is dirty":             "dirty_worktree",
		"stale parent HEAD":                    "stale_base",
		"permission denied":                    "permission_denied",
		"integration conflict":                 "integration_conflict",
		"handoff HEAD does not descend":        "invalid_handoff",
		"failed to allocate subagent worktree": "worktree_creation_failed",
		"undeclared job required":              "planning_required",
	}
	for message, want := range cases {
		if got := taskProgramErrorCode(errors.New(message)); got != want {
			t.Fatalf("code for %q = %q, want %q", message, got, want)
		}
	}
}

func TestParseTaskProgramLifecycleGuards(t *testing.T) {
	status, err := parseTaskCallArguments(`{"action":"status","program_id":"release_program"}`)
	if err != nil || status.ProgramID != "release_program" || len(status.Launches) != 0 {
		t.Fatalf("status = %#v err=%v", status, err)
	}
	resume, err := parseTaskCallArguments(`{"action":"resume","program_id":"release_program","expected_revision":3,"expected_generation":2}`)
	if err != nil || resume.ExpectedRevision != 3 || resume.ExpectedGeneration != 2 {
		t.Fatalf("resume = %#v err=%v", resume, err)
	}
	if _, err := parseTaskCallArguments(`{"action":"resume","program_id":"release_program"}`); err == nil || !strings.Contains(err.Error(), "requires positive") {
		t.Fatalf("unguarded resume error = %v", err)
	}
}
