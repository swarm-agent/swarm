package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
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

func TestParseTaskProgramPreservesRegularWorkspaceSplitWave(t *testing.T) {
	parsed, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "regular", "description": "split audit", "prompt": "Inspect independent surfaces.",
		"launches": []any{
			map[string]any{"subagent_type": "finder", "title": "Backend Map", "meta_prompt": "Map backend files.", "deliverable": "Backend map", "dependency_evidence": "Independent read-only scope."},
			map[string]any{"subagent_type": "designer", "title": "Visual Variant", "meta_prompt": "Create the requested visual variant.", "deliverable": "Reusable variant", "output_mode": "workspace", "owned_scope": []any{"web/src/variants/program-compat.tsx"}, "dependency_evidence": "Distinct output target."},
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

func TestTaskProgramPresentationGroupsOrderedStagesAndJobsWithoutPrivateFields(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ParentSessionID: "parent", ProgramID: "release", ReservationCallID: "call-release",
		Revision: 6, ResumeGeneration: 2, State: pebblestore.TaskProgramStateRunning, ActiveStageID: "build",
		Definition: pebblestore.TaskProgramDefinition{
			Stages: []pebblestore.TaskProgramStageSpec{
				{ID: "build", DependencyEvidence: "Independent work is ready."},
				{ID: "fix", DependsOn: []string{"build"}, DependencyEvidence: "Requires integrated build outputs."},
			},
			Jobs: []pebblestore.TaskProgramJobSpec{
				{ID: "api", StageID: "build", AgentType: "coder", Title: "API Work", MetaPrompt: "private prompt", OwnedScope: []string{"private/**"}, DependencyEvidence: "Ready."},
				{ID: "fixer", StageID: "fix", DependsOn: []string{"api"}, AgentType: "finder", Title: "Verify Integration", MetaPrompt: "private verification prompt", DependencyEvidence: "Waits for API."},
			},
		},
		Jobs: []pebblestore.TaskProgramJobRecord{
			{JobID: "api", StageID: "build", State: pebblestore.TaskProgramJobRunning, AttemptNumber: 1, ChildSessionID: "child-api"},
			{JobID: "fixer", StageID: "fix", State: pebblestore.TaskProgramJobDeclared},
		},
	}

	presentation := taskProgramPresentationPayload(record)
	if presentation["kind"] != "task_program" || presentation["program_id"] != "release" || presentation["task_call_id"] != "call-release" || presentation["active_stage_id"] != "build" {
		t.Fatalf("presentation identity = %#v", presentation)
	}
	stages := presentation["stages"].([]map[string]any)
	if len(stages) != 2 || stages[0]["stage_id"] != "build" || stages[0]["order"] != 1 || stages[0]["state"] != "running" || stages[1]["stage_id"] != "fix" || stages[1]["state"] != "waiting" {
		t.Fatalf("stages = %#v", stages)
	}
	buildJobs := stages[0]["jobs"].([]map[string]any)
	fixJobs := stages[1]["jobs"].([]map[string]any)
	if len(buildJobs) != 1 || buildJobs[0]["job_id"] != "api" || buildJobs[0]["title"] != "API Work" || buildJobs[0]["agent_type"] != "coder" || buildJobs[0]["child_session_id"] != "child-api" {
		t.Fatalf("build jobs = %#v", buildJobs)
	}
	if len(fixJobs) != 1 || fixJobs[0]["job_id"] != "fixer" || fixJobs[0]["dependency_state"] != "waiting" {
		t.Fatalf("fix jobs = %#v", fixJobs)
	}
	raw, err := json.Marshal(presentation)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private prompt", "private verification prompt", "private/**", "meta_prompt", "owned_scope", "workspace_path", "report"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("presentation leaked %q: %s", forbidden, raw)
		}
	}
}

func TestTaskProgramStatusCarriesStableGroupingIdentityAndPresentation(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ParentSessionID: "parent", ProgramID: "release", ReservationCallID: "call-release",
		Revision: 3, ResumeGeneration: 1, State: pebblestore.TaskProgramStateRunning, ActiveStageID: "build",
		Definition: pebblestore.TaskProgramDefinition{
			Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}},
			Jobs:   []pebblestore.TaskProgramJobSpec{{ID: "api", StageID: "build", AgentType: "coder", Title: "API", DependencyEvidence: "ready"}},
		},
		Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "api", StageID: "build", State: pebblestore.TaskProgramJobRunning}},
	}
	payload := taskProgramStatusPayload(record, false)
	presentation, ok := payload["program_presentation"].(map[string]any)
	if !ok || payload["task_call_id"] != "call-release" || presentation["task_call_id"] != "call-release" || presentation["program_id"] != "release" {
		t.Fatalf("status grouping contract = %#v", payload)
	}
}

func TestTaskProgramProgressStreamCarriesCanonicalSnapshotAndStableCallID(t *testing.T) {
	var event StreamEvent
	record := pebblestore.TaskProgramRecord{
		ParentSessionID: "parent", ProgramID: "release", ReservationCallID: "call-release",
		Revision: 2, ResumeGeneration: 1, State: pebblestore.TaskProgramStateRunning, ActiveStageID: "build",
		Definition: pebblestore.TaskProgramDefinition{
			Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}},
			Jobs:   []pebblestore.TaskProgramJobSpec{{ID: "api", StageID: "build", AgentType: "coder", Title: "API", MetaPrompt: "private", DependencyEvidence: "ready"}},
		},
		Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "api", StageID: "build", State: pebblestore.TaskProgramJobRunning}},
	}
	scheduler := taskProgramScheduler{
		step: 4, call: tool.Call{CallID: "unstable-cohort"}, emit: func(got StreamEvent) { event = got },
		parentSession: pebblestore.SessionSnapshot{ID: "parent"}, parsed: taskCallArguments{Action: taskProgramActionStart}, record: record, description: "release work",
	}
	scheduler.emitProgramProgress("stage.running", "Build is running")
	if event.Type != StreamEventToolDelta || event.CallID != "call-release" {
		t.Fatalf("stream identity = %#v", event)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.Output), &payload); err != nil {
		t.Fatal(err)
	}
	presentation, ok := payload["program_presentation"].(map[string]any)
	if !ok || payload["path_id"] != taskStreamPathIDV2 || payload["stream_version"] != float64(2) || payload["program_id"] != "release" || payload["task_call_id"] != "call-release" || presentation["active_stage_id"] != "build" {
		t.Fatalf("stream contract = %#v", payload)
	}
	program, programOK := payload["program"].(map[string]any)
	status, statusOK := payload["program_status"].(map[string]any)
	if !programOK || !statusOK || program["id"] != "release" || status["active_stage_id"] != "build" {
		t.Fatalf("task stream v2 program metadata = %#v", payload)
	}
	if strings.Contains(event.Output, "private") {
		t.Fatalf("stream leaked private definition fields: %s", event.Output)
	}
}

func TestTaskProgramStreamMetadataIsClientCompatibleAndPrivacyBounded(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ProgramID: "release", State: pebblestore.TaskProgramStateRunning, ActiveStageID: "build", NextAction: "await_running_jobs", Revision: 4,
		Definition: pebblestore.TaskProgramDefinition{
			Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}},
			Jobs:   []pebblestore.TaskProgramJobSpec{{ID: "api", StageID: "build", AgentType: "coder", Title: "API", MetaPrompt: "private prompt", OwnedScope: []string{"private/**"}, DependencyEvidence: "ready"}},
		},
		Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "api", StageID: "build", State: pebblestore.TaskProgramJobRunning, ChildSessionID: "child-api", WorkspacePath: "/private/worktree"}},
	}
	program, status := taskProgramStreamMetadata(record)
	if program["id"] != "release" || status["program_state"] != pebblestore.TaskProgramStateRunning || status["next_action"] != "await_running_jobs" {
		t.Fatalf("stream metadata = program:%#v status:%#v", program, status)
	}
	raw, err := json.Marshal(map[string]any{"program": program, "program_status": status})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private prompt", "private/**", "/private/worktree", "meta_prompt", "owned_scope", "workspace_path"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("stream metadata leaked %q: %s", forbidden, raw)
		}
	}
}

func TestTaskProgramLaunchPatchPreservesGranularAgentModelAndToolMetadata(t *testing.T) {
	launch := map[string]any{
		"agent_type":           "coder",
		"subagent_provider":    "codex",
		"subagent_model":       "gpt-5.6-codex",
		"child_session_id":     "child-api",
		"current_tool":         "edit",
		"current_tool_display": "edit x2",
		"tool_order":           []string{"search", "read", "edit", "edit"},
	}
	patch := taskProgramLaunchPatch(launch, "release", "api", "build", "tool.started")
	if patch["agent_type"] != "coder" || patch["subagent_model"] != "gpt-5.6-codex" || patch["program_id"] != "release" || patch["program_job_id"] != "api" || patch["program_stage_id"] != "build" {
		t.Fatalf("program launch patch lost identity/model metadata: %#v", patch)
	}
	order, ok := patch["tool_order"].([]string)
	if !ok || strings.Join(order, ",") != "search,read,edit,edit" {
		t.Fatalf("program launch patch lost granular tools: %#v", patch["tool_order"])
	}
}

func TestTaskProgramCohortProgressUsesDeclaredJobIdentity(t *testing.T) {
	scheduler := taskProgramScheduler{record: pebblestore.TaskProgramRecord{Jobs: []pebblestore.TaskProgramJobRecord{
		{JobID: "api", StageID: "build"}, {JobID: "web", StageID: "build"}, {JobID: "fixer", StageID: "fix"},
	}}}
	if got := scheduler.taskProgramCohortJobID([]int{0, 2}, map[string]any{"launch_index": float64(2)}); got != "fixer" {
		t.Fatalf("cohort job identity = %q, want fixer", got)
	}
	if got := taskProgramPresentationJobState("tool.started"); got != pebblestore.TaskProgramJobRunning {
		t.Fatalf("tool phase state = %q", got)
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

func TestTaskProgramResumeLaunchesPreserveChildIdentityAndRecoveryContext(t *testing.T) {
	prior := pebblestore.TaskProgramRecord{
		ProgramID: "release",
		Blocker:   &pebblestore.TaskProgramBlocker{JobID: "api", Message: "user stopped subagent"},
		Definition: pebblestore.TaskProgramDefinition{
			Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}},
			Jobs:   []pebblestore.TaskProgramJobSpec{{ID: "api", StageID: "build", AgentType: "coder", Title: "API", MetaPrompt: "implement API", Deliverable: "commit", AcceptanceCriteria: []string{"done"}}},
		},
		Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "api", StageID: "build", State: pebblestore.TaskProgramJobCancelled, AttemptNumber: 2, ChildSessionID: "child-api", WorkspacePath: "/worktree/api", WorktreeBranch: "agent/api", ImmutableStageBase: "base"}},
	}
	prepared := prior
	prepared.Jobs = append([]pebblestore.TaskProgramJobRecord(nil), prior.Jobs...)
	prepared.Jobs[0].State = pebblestore.TaskProgramJobDeclared
	spec := taskProgramSpecFromRecord(prepared)
	launches := taskProgramResumeLaunches(prepared, prior, spec)
	if len(launches) != 1 {
		t.Fatalf("launch count = %d", len(launches))
	}
	launch := launches[0]
	if launch.ResumeChildSessionID != "child-api" || launch.ResumeWorkspacePath != "/worktree/api" || launch.ResumeWorktreeBranch != "agent/api" || launch.ResumeImmutableBase != "base" || launch.ResumeAttemptNumber != 2 || launch.ResumeReason != "user stopped subagent" {
		t.Fatalf("resume launch lost durable identity: %#v", launch)
	}
	prompt := taskProgramRecoveryPrompt(launch)
	for _, want := range []string{"existing child session", "inspect the durable conversation", "Preserve valid progress", "user stopped subagent", "implement API"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("recovery prompt missing %q: %s", want, prompt)
		}
	}
}

func TestPrepareResumedTaskProgramLaunchUsesOriginalSessionAgentAndModel(t *testing.T) {
	svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentID)
	if err != nil || !ok {
		t.Fatalf("get parent: ok=%t err=%v", ok, err)
	}
	profile := pebblestore.AgentProfile{Name: "finder", Mode: "subagent", Enabled: true, Prompt: "original finder prompt"}
	child, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         parent.UserID,
		AccountScopeID: parent.AccountScopeID,
		Title:          "Interrupted Finder",
		WorkspacePath:  parent.WorkspacePath,
		WorkspaceName:  parent.WorkspaceName,
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "original-provider", Model: "original-model", Thinking: "high"},
		Metadata: map[string]any{
			"parent_session_id":  parent.ID,
			"requested_subagent": "finder",
			"source_agent_name":  "finder",
			"agent_profile":      profile,
		},
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	launch, err := svc.prepareResumedTaskProgramLaunch(parent, taskLaunchPrepared{LaunchIndex: 1, RequestedSubagent: "finder"}, taskLaunchSpec{
		RequestedSubagentType: "finder", MetaPrompt: "inspect repository", AssignmentLabel: "Resume Finder", ResumeChildSessionID: child.ID, ResumeAttemptNumber: 1, ResumeReason: "daemon restart",
	})
	if err != nil {
		t.Fatalf("prepare resumed launch: %v", err)
	}
	if launch.ChildSession.ID != child.ID || launch.SubagentProfile.Prompt != "original finder prompt" || launch.SubagentProvider != "original-provider" || launch.SubagentModel != "original-model" {
		t.Fatalf("resumed launch drifted from original identity: %#v", launch)
	}
	if !strings.Contains(launch.MetaPrompt, "daemon restart") || !strings.Contains(launch.MetaPrompt, "inspect repository") {
		t.Fatalf("resumed launch lacks bounded recovery reprompt: %q", launch.MetaPrompt)
	}
}

func TestPrepareTaskProgramResumeReconcilesCompletedChildWithoutReplay(t *testing.T) {
	svc, parentID, permissions, cleanup := newTaskLaunchPermissionServiceWithPermissions(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentID)
	if err != nil || !ok {
		t.Fatalf("get parent: ok=%t err=%v", ok, err)
	}
	profile := pebblestore.AgentProfile{Name: "finder", Mode: "subagent", Enabled: true, Prompt: "finder"}
	child, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		AccountScopeID: parent.AccountScopeID, Title: "Completed Finder", WorkspacePath: parent.WorkspacePath, WorkspaceName: parent.WorkspaceName, Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "original-provider", Model: "original-model", Thinking: "high"},
		Metadata:   map[string]any{"parent_session_id": parent.ID, "requested_subagent": "finder", "agent_profile": profile},
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := svc.sessions.UpsertLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: child.ID, RunID: "child-run", Active: false, Phase: lifecyclePhaseCompleted, Generation: 1}); err != nil {
		t.Fatalf("persist completed child lifecycle: %v", err)
	}
	definition := pebblestore.TaskProgramDefinition{
		Stages: []pebblestore.TaskProgramStageSpec{{ID: "inspect"}},
		Jobs:   []pebblestore.TaskProgramJobSpec{{ID: "finder", StageID: "inspect", AgentType: "finder", Title: "Find", MetaPrompt: "inspect", Deliverable: "report", AcceptanceCriteria: []string{"done"}}},
	}
	record := pebblestore.TaskProgramRecord{
		ParentSessionID: parent.ID, ProgramID: "resume_completed", DefinitionHash: "hash", Definition: definition,
		ReservationRunID: "parent-run", ReservationCallID: "task-call", State: pebblestore.TaskProgramStateRunning, ActiveStageID: "inspect", NextAction: "await_running_jobs",
		Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "finder", StageID: "inspect", State: pebblestore.TaskProgramJobRunning, AttemptNumber: 1, ChildSessionID: child.ID}},
	}
	record, _, err = svc.sessions.CreateTaskProgram(record)
	if err != nil {
		t.Fatalf("create task program: %v", err)
	}
	reserved, err := permissions.ReserveSubagentWave(permission.SubagentReservationRequest{
		SessionID: parent.ID, AccountScopeID: parent.AccountScopeID, RunID: record.ReservationRunID, CallID: record.ReservationCallID,
		ManifestHash: "manifest", LaunchCount: 1, Program: true, ReadyCount: 1,
	})
	if err != nil || reserved.Decision != permission.SubagentReservationApprove {
		t.Fatalf("reserve program: decision=%s err=%v", reserved.Decision, err)
	}
	resumed, readyCount, err := svc.prepareTaskProgramResume(parent, record, taskCallArguments{ExpectedRevision: record.Revision, ExpectedGeneration: record.ResumeGeneration})
	if err != nil {
		t.Fatalf("resume program: %v", err)
	}
	if readyCount != 1 || resumed.State != pebblestore.TaskProgramStateRunning || resumed.Jobs[0].State != pebblestore.TaskProgramJobCompleted || resumed.ResumeGeneration != record.ResumeGeneration+1 {
		t.Fatalf("completed child reconciliation = ready:%d record:%#v", readyCount, resumed)
	}
}

func TestGateTaskProgramResumeReusesReservationWithoutApproval(t *testing.T) {
	svc, parentID, permissions, cleanup := newTaskLaunchPermissionServiceWithPermissions(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentID)
	if err != nil || !ok {
		t.Fatalf("get parent: ok=%t err=%v", ok, err)
	}
	definition := pebblestore.TaskProgramDefinition{
		Stages: []pebblestore.TaskProgramStageSpec{{ID: "inspect"}},
		Jobs:   []pebblestore.TaskProgramJobSpec{{ID: "finder", StageID: "inspect", AgentType: "finder", Title: "Find", MetaPrompt: "inspect", Deliverable: "report", AcceptanceCriteria: []string{"done"}}},
	}
	record, _, err := svc.sessions.CreateTaskProgram(pebblestore.TaskProgramRecord{
		ParentSessionID: parent.ID, ProgramID: "resume_no_approval", DefinitionHash: "hash", Definition: definition,
		ReservationRunID: "parent-run", ReservationCallID: "original-task-call", State: pebblestore.TaskProgramStateBlocked, ActiveStageID: "inspect", NextAction: "repair_failed_child_then_resume",
		Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "finder", StageID: "inspect", State: pebblestore.TaskProgramJobBlocked, AttemptNumber: 1}},
	})
	if err != nil {
		t.Fatalf("create task program: %v", err)
	}
	reserved, err := permissions.ReserveSubagentWave(permission.SubagentReservationRequest{
		SessionID: parent.ID, AccountScopeID: parent.AccountScopeID, RunID: record.ReservationRunID, CallID: record.ReservationCallID,
		ManifestHash: "manifest", LaunchCount: 1, Program: true, ReadyCount: 1,
	})
	if err != nil || reserved.Decision != permission.SubagentReservationApprove {
		t.Fatalf("reserve program: decision=%s err=%v", reserved.Decision, err)
	}
	statusCall := tool.Call{CallID: "status-lifecycle-call", Name: "task", Arguments: mustJSON(t, map[string]any{
		"action": "status", "program_id": record.ProgramID,
	})}
	statusResults, statusApproved, _, statusMask, _, err := svc.gateToolCalls(context.Background(), parent.ID, "status-parent-run", 1, sessionruntime.ModeAuto, []tool.Call{statusCall}, nil, nil)
	if err != nil || len(statusResults) != 1 || len(statusApproved) != 1 || len(statusMask) != 1 || !statusMask[0] {
		t.Fatalf("read-only status was not approved inline: results=%#v approved=%d mask=%v err=%v", statusResults, len(statusApproved), statusMask, err)
	}

	call := tool.Call{CallID: "resume-lifecycle-call", Name: "task", Arguments: mustJSON(t, map[string]any{
		"action": "resume", "program_id": record.ProgramID, "expected_revision": record.Revision, "expected_generation": record.ResumeGeneration,
	})}
	permissionEvents := 0
	results, approved, _, mask, _, err := svc.gateToolCalls(context.Background(), parent.ID, "resume-parent-run", 1, sessionruntime.ModeAuto, []tool.Call{call}, func(event StreamEvent) {
		if event.Type == StreamEventPermissionReq {
			permissionEvents++
		}
	}, nil)
	if err != nil {
		t.Fatalf("gate resume: %v", err)
	}
	if len(results) != 1 || len(approved) != 1 || len(mask) != 1 || !mask[0] || permissionEvents != 0 {
		t.Fatalf("guarded resume was not approved inline: results=%#v approved=%d mask=%v permission_events=%d", results, len(approved), mask, permissionEvents)
	}
	pending, err := permissions.ListPending(parent.ID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("guarded resume created redundant approval records: %#v", pending)
	}

	state, next := pebblestore.TaskProgramStateRunning, "resume_program_scheduler"
	resumed, _, err := svc.sessions.TransitionTaskProgram(parent.ID, record.ProgramID, pebblestore.TaskProgramTransition{
		ExpectedRevision: record.Revision, MutationID: "resume-test", State: &state, NextAction: &next,
		IncrementResumeGeneration: true, ResumeFromRevision: record.Revision, ResumeFromGeneration: record.ResumeGeneration,
	})
	if err != nil {
		t.Fatalf("persist resumed generation: %v", err)
	}
	results, approved, _, mask, _, err = svc.gateToolCalls(context.Background(), parent.ID, "resume-parent-run", 2, sessionruntime.ModeAuto, []tool.Call{call}, nil, nil)
	if err != nil || len(results) != 1 || len(approved) != 1 || len(mask) != 1 || !mask[0] {
		t.Fatalf("idempotent guarded resume was not approved inline after generation advanced to %d: results=%#v approved=%d mask=%v err=%v", resumed.ResumeGeneration, results, len(approved), mask, err)
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

func TestTaskProgramReconstructedDesignerLaunchesPreserveManagedAndWorkspaceModes(t *testing.T) {
	spec := &taskProgramSpec{ID: "design_program", Jobs: []taskProgramJob{
		{ID: "managed", StageID: "variants", RequestedSubagentType: "designer"},
		{ID: "workspace", StageID: "variants", RequestedSubagentType: "designer", OwnedScope: []string{"web/src/variant.tsx"}},
		{ID: "finder", StageID: "variants", RequestedSubagentType: "finder"},
	}}
	launches := taskProgramLaunchesFromSpec(spec)
	if len(launches) != 3 || launches[0].OutputMode != taskOutputModeManaged || launches[1].OutputMode != taskOutputModeWorkspace || launches[2].OutputMode != "" {
		t.Fatalf("reconstructed output modes = %#v", launches)
	}
}

func TestTaskProgramManagedDesignerOutcomeRequiresReadyArtifact(t *testing.T) {
	job := taskProgramJob{ID: "design", StageID: "variants", RequestedSubagentType: "designer"}
	ready := &taskArtifactReference{SessionID: "parent", CollectionID: "collection", VariantID: "variant", Status: pebblestore.SessionArtifactStatusReady}
	updates := taskProgramOutcomeTransitions(&taskProgramSpec{Jobs: []taskProgramJob{job}}, []taskLaunchOutcome{{ChildSessionID: "child", ArtifactReference: ready}}, []error{nil})
	if len(updates) != 1 || updates[0].State != pebblestore.TaskProgramJobCompleted || updates[0].IntegrationState != "artifact_ready" || updates[0].Blocker != nil {
		t.Fatalf("ready managed Designer transition = %#v", updates)
	}
	for _, tc := range []struct {
		name      string
		reference *taskArtifactReference
	}{
		{name: "missing"},
		{name: "failed", reference: &taskArtifactReference{Status: pebblestore.SessionArtifactStatusFailed, FailureCode: "render_failed"}},
		{name: "staging", reference: &taskArtifactReference{Status: pebblestore.SessionArtifactStatusStaging}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updates := taskProgramOutcomeTransitions(&taskProgramSpec{Jobs: []taskProgramJob{job}}, []taskLaunchOutcome{{ArtifactReference: tc.reference}}, []error{nil})
			if len(updates) != 1 || updates[0].State != pebblestore.TaskProgramJobFailed || updates[0].IntegrationState != "artifact_invalid" || updates[0].Blocker == nil || updates[0].Blocker.Code != "managed_artifact_invalid" {
				t.Fatalf("invalid managed Designer transition = %#v", updates)
			}
		})
	}
}

func TestTaskProgramOutcomesParseArtifactReference(t *testing.T) {
	payload := map[string]any{"launches": []any{map[string]any{
		"child_session_id": "child-design", "artifact_status": pebblestore.SessionArtifactStatusReady,
		"artifact_reference": map[string]any{"session_id": "parent", "collection_id": "collection", "variant_id": "variant", "status": pebblestore.SessionArtifactStatusReady},
	}}}
	outcomes := taskProgramOutcomesFromPayload(payload, 1)
	if len(outcomes) != 1 || outcomes[0].ArtifactReference == nil || outcomes[0].ArtifactReference.SessionID != "parent" || outcomes[0].ArtifactReference.CollectionID != "collection" || outcomes[0].ArtifactReference.VariantID != "variant" || outcomes[0].ArtifactReference.Status != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("parsed artifact outcome = %#v", outcomes)
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
	reference := &taskArtifactReference{SessionID: parent.ID, CollectionID: run.CollectionID, VariantID: run.VariantID, Status: pebblestore.SessionArtifactStatusReady}
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

func TestTaskProgramStatusExposesBoundedReadyArtifactReferences(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ParentSessionID: "parent", ProgramID: "managed_program", ReservationCallID: "call-managed", State: pebblestore.TaskProgramStateCompleted,
		Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{{ID: "design", StageID: "variants", AgentType: "designer"}}},
		Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "design", StageID: "variants", State: pebblestore.TaskProgramJobCompleted, IntegrationState: "artifact_ready", ChildSessionID: "child"}},
	}
	payload := taskProgramStatusPayload(record, false)
	references, ok := payload["artifact_references"].([]*taskArtifactReference)
	if !ok || len(references) != 1 || payload["artifact_count"] != 1 || references[0].SessionID != "parent" || references[0].Status != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("status artifact handoff = %#v", payload)
	}
	jobs := payload["jobs"].([]map[string]any)
	if len(jobs) != 1 || jobs[0]["artifact_status"] != pebblestore.SessionArtifactStatusReady {
		t.Fatalf("job artifact handoff = %#v", jobs)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"workspace_path", "digest_sha256", "filename", "media_type"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("artifact status leaked %q: %s", forbidden, raw)
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
