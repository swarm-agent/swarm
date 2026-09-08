package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
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

func TestParseTaskProgramPreservesWorkspacePathAtStartAndJobLevels(t *testing.T) {
	args := taskProgramFixture(nil)
	args["workspace_path"] = "/shared/default"
	jobs := args["program"].(map[string]any)["jobs"].([]any)
	jobs[2].(map[string]any)["workspace_path"] = "/shared/default"

	parsed, err := parseTaskCallArguments(mustJSON(t, args))
	if err != nil {
		t.Fatalf("parse targeted Task Program: %v", err)
	}
	if parsed.ProgramWorkspacePath != "/shared/default" {
		t.Fatalf("program workspace path = %q", parsed.ProgramWorkspacePath)
	}
	for i := range parsed.Program.Jobs {
		if parsed.Program.Jobs[i].TargetWorkspacePath != "/shared/default" || parsed.Launches[i].TargetWorkspacePath != "/shared/default" {
			t.Fatalf("job %d workspace target not preserved: job=%#v launch=%#v", i, parsed.Program.Jobs[i], parsed.Launches[i])
		}
	}
	definition, _, err := taskProgramDefinitionFromSpec(parsed.Program)
	if err != nil {
		t.Fatalf("build durable targeted definition: %v", err)
	}
	for i, job := range definition.Jobs {
		if job.WorkspacePath != "/shared/default" {
			t.Fatalf("durable job %d workspace path = %q", i, job.WorkspacePath)
		}
	}
}

func TestParseTaskProgramRejectsDesignerAndSplitCoderWorkspaceTargets(t *testing.T) {
	designer := taskProgramFixture(nil)
	designerProgram := designer["program"].(map[string]any)
	designerProgram["stages"] = []any{map[string]any{"id": "build", "dependency_evidence": "Ready."}}
	designerProgram["jobs"] = []any{map[string]any{"id": "design", "stage_id": "build", "agent_type": "designer", "workspace_path": "/shared/design", "title": "Design", "meta_prompt": "Create design.", "deliverable": "Design", "acceptance_criteria": []any{"Done"}, "dependency_evidence": "Ready."}}
	if _, err := parseTaskCallArguments(mustJSON(t, designer)); err == nil || !strings.Contains(err.Error(), "workspace_path is supported only for Coder or Finder") {
		t.Fatalf("Designer workspace target error = %v", err)
	}

	split := taskProgramFixture(nil)
	splitJobs := split["program"].(map[string]any)["jobs"].([]any)
	splitJobs[0].(map[string]any)["workspace_path"] = "/shared/one"
	splitJobs[1].(map[string]any)["workspace_path"] = "/shared/two"
	splitJobs[2].(map[string]any)["workspace_path"] = "/shared/one"
	if _, err := parseTaskCallArguments(mustJSON(t, split)); err == nil || !strings.Contains(err.Error(), "must target one workspace") {
		t.Fatalf("split Coder workspace target error = %v", err)
	}
}

func TestParseTaskProgramDesignerOutputModesAreExplicitAndDurable(t *testing.T) {
	args := taskProgramFixture(nil)
	program := args["program"].(map[string]any)
	program["stages"] = []any{map[string]any{"id": "build", "dependency_evidence": "Both Designer assignments are ready."}}
	program["jobs"] = []any{
		map[string]any{"id": "managed", "stage_id": "build", "agent_type": "designer", "title": "Managed Variant", "meta_prompt": "Create one managed variant.", "deliverable": "Ready managed variant", "acceptance_criteria": []any{"Variant is ready"}, "dependency_evidence": "Brief is complete."},
		map[string]any{"id": "workspace", "stage_id": "build", "agent_type": "designer", "title": "Workspace Variant", "meta_prompt": "Create one workspace variant.", "deliverable": "Reusable source artifact", "output_mode": "workspace", "owned_scope": []any{"web/src/variants/program.tsx"}, "acceptance_criteria": []any{"Source artifact exists"}, "dependency_evidence": "Target is distinct."},
	}
	parsed, err := parseTaskCallArguments(mustJSON(t, args))
	if err != nil {
		t.Fatalf("parse Designer program: %v", err)
	}
	if parsed.Program.Jobs[0].OutputMode != taskOutputModeManaged || len(parsed.Program.Jobs[0].OwnedScope) != 0 || parsed.Program.Jobs[1].OutputMode != taskOutputModeWorkspace || len(parsed.Program.Jobs[1].OwnedScope) != 1 {
		t.Fatalf("Designer program modes = %#v", parsed.Program.Jobs)
	}
	record, _, err := taskProgramDefinitionFromSpec(parsed.Program)
	if err != nil {
		t.Fatalf("persist Designer program definition: %v", err)
	}
	if record.Jobs[0].OutputMode != taskOutputModeManaged || record.Jobs[1].OutputMode != taskOutputModeWorkspace {
		t.Fatalf("durable Designer output modes = %#v", record.Jobs)
	}
	for _, mutate := range []func(map[string]any){
		func(job map[string]any) { job["owned_scope"] = []any{"web/src/managed.tsx"} },
		func(job map[string]any) { job["output_mode"] = "workspace" },
		func(job map[string]any) {
			job["output_mode"] = "managed"
			job["owned_scope"] = []any{"web/src/managed.tsx"}
		},
	} {
		invalid := taskProgramFixture(nil)
		invalidProgram := invalid["program"].(map[string]any)
		invalidProgram["stages"] = []any{map[string]any{"id": "build", "dependency_evidence": "Ready."}}
		job := map[string]any{"id": "invalid", "stage_id": "build", "agent_type": "designer", "title": "Invalid Variant", "meta_prompt": "Create one variant.", "deliverable": "Variant", "acceptance_criteria": []any{"Done"}, "dependency_evidence": "Ready."}
		mutate(job)
		invalidProgram["jobs"] = []any{job}
		if _, err := parseTaskCallArguments(mustJSON(t, invalid)); err == nil {
			t.Fatalf("invalid Designer program accepted: %#v", job)
		}
	}
	overlap := taskProgramFixture(nil)
	overlapProgram := overlap["program"].(map[string]any)
	overlapProgram["stages"] = []any{map[string]any{"id": "build", "dependency_evidence": "Ready."}}
	overlapProgram["jobs"] = []any{
		map[string]any{"id": "first", "stage_id": "build", "agent_type": "designer", "title": "First Variant", "meta_prompt": "Create first.", "deliverable": "First", "output_mode": "workspace", "owned_scope": []any{"web/src/variants"}, "acceptance_criteria": []any{"Done"}, "dependency_evidence": "Ready."},
		map[string]any{"id": "second", "stage_id": "build", "agent_type": "designer", "title": "Second Variant", "meta_prompt": "Create second.", "deliverable": "Second", "output_mode": "workspace", "owned_scope": []any{"web/src/variants/second.tsx"}, "acceptance_criteria": []any{"Done"}, "dependency_evidence": "Ready."},
	}
	if _, err := parseTaskCallArguments(mustJSON(t, overlap)); err == nil || !strings.Contains(err.Error(), "workspace Designer owned scopes overlap") {
		t.Fatalf("overlapping workspace Designer program error = %v", err)
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
		ParentSessionID: "parent", ProgramID: "release", Revision: 4,
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
		Revision: 6, State: pebblestore.TaskProgramStateRunning, ActiveStageID: "build",
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
		Revision: 3, State: pebblestore.TaskProgramStateRunning, ActiveStageID: "build",
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
		Revision: 2, State: pebblestore.TaskProgramStateRunning, ActiveStageID: "build",
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

func TestTaskProgramSchedulerRequiresSessionOwnedRepositoryLane(t *testing.T) {
	definition := pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{{ID: "coder", AgentType: "coder", WorkspacePath: "/repos/product"}}}
	for _, tc := range []struct {
		name   string
		parent pebblestore.SessionSnapshot
		want   string
		err    string
	}{
		{
			name: "routes source repository into parent lane",
			parent: pebblestore.SessionSnapshot{
				WorkspacePath: "/lanes/session-product", WorktreeEnabled: true, WorktreeRootPath: "/lanes/session-product",
				WorktreeBaseBranch: "dev", WorktreeBranch: "agent/session-product",
				Metadata: map[string]any{"swarm_v3_source_workspace_path": "/repos/product", "swarm_v3_runtime_workspace_path": "/lanes/session-product"},
			},
			want: "/lanes/session-product",
		},
		{
			name:   "rejects captured checkout",
			parent: pebblestore.SessionSnapshot{WorkspacePath: "/repos/product", Metadata: map[string]any{"swarm_v3_source_workspace_path": "/repos/product"}},
			err:    "session-owned parent lane",
		},
		{
			name: "rejects source checkout disguised as worktree",
			parent: pebblestore.SessionSnapshot{
				WorkspacePath: "/repos/product", WorktreeEnabled: true, WorktreeRootPath: "/repos/product",
				WorktreeBaseBranch: "dev", WorktreeBranch: "agent/session-product",
				Metadata: map[string]any{"swarm_v3_source_workspace_path": "/repos/product", "swarm_v3_runtime_workspace_path": "/repos/product"},
			},
			err: "distinct managed worktree",
		},
		{
			name: "rejects base branch as parent lane branch",
			parent: pebblestore.SessionSnapshot{
				WorkspacePath: "/lanes/session-product", WorktreeEnabled: true, WorktreeRootPath: "/lanes/session-product",
				WorktreeBaseBranch: "dev", WorktreeBranch: "dev",
				Metadata: map[string]any{"swarm_v3_source_workspace_path": "/repos/product", "swarm_v3_runtime_workspace_path": "/lanes/session-product"},
			},
			err: "distinct worktree branch",
		},
		{
			name: "rejects cross workspace without lane",
			parent: pebblestore.SessionSnapshot{
				WorkspacePath: "/lanes/session-product", WorktreeEnabled: true, WorktreeRootPath: "/lanes/session-product",
				WorktreeBaseBranch: "dev", WorktreeBranch: "agent/session-product",
				Metadata: map[string]any{"swarm_v3_source_workspace_path": "/repos/other", "swarm_v3_runtime_workspace_path": "/lanes/session-product"},
			},
			err: "repository lane authorities unavailable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := taskProgramScheduler{parentSession: tc.parent, record: pebblestore.TaskProgramRecord{Definition: definition}}
			got, err := scheduler.programWorkspacePath()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("program lane error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("program lane = %q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestTaskProgramSchedulerRecordsIntegratedWorktreeCleanupOutcome(t *testing.T) {
	tests := []struct {
		name            string
		cleanupErr      error
		wantIntegration string
	}{
		{name: "removed", wantIntegration: "integrated_worktree_removed"},
		{name: "cleanup failure remains nonblocking", cleanupErr: errors.New("worktree busy"), wantIntegration: "integrated_worktree_cleanup_failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
			defer cleanup()
			parent, ok, err := svc.sessions.GetSession(parentID)
			if err != nil || !ok {
				t.Fatalf("load parent: ok=%v err=%v", ok, err)
			}
			parent.WorkspacePath = "/shared/repo"
			parent.WorktreeEnabled = true
			parent.WorktreeRootPath = "/shared/repo"
			parent.WorktreeBaseBranch = "dev"
			parent.WorktreeBranch = "agent/parent-lane"
			parent.Metadata = map[string]any{"swarm_v3_source_workspace_path": "/captured/repo", "swarm_v3_runtime_workspace_path": "/shared/repo"}
			stub := &taskProgramCleanupWorktreeStub{taskLaunchWorktreeStub: taskLaunchWorktreeStub{cleanupErr: tc.cleanupErr, taskBase: worktreeruntime.TaskBase{BaseCommit: strings.Repeat("a", 40)}}}
			svc.SetWorktreeService(stub)
			record, _, err := svc.sessions.CreateTaskProgram(pebblestore.TaskProgramRecord{
				ParentSessionID: parentID, ProgramID: "cleanup-program", DefinitionHash: "cleanup-definition",
				Definition: pebblestore.TaskProgramDefinition{
					Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}},
					Jobs: []pebblestore.TaskProgramJobSpec{
						{ID: "api", StageID: "build", AgentType: "coder", WorkspacePath: "/captured/repo", DependencyEvidence: "ready"},
						{ID: "recoverable", StageID: "build", AgentType: "coder", DependencyEvidence: "ready"},
						{ID: "failed", StageID: "build", AgentType: "coder", DependencyEvidence: "ready"},
					},
				},
				ActiveStageID: "build", State: pebblestore.TaskProgramStateRunning, NextAction: "advance_stage", ParentHead: "parent-head",
				Jobs: []pebblestore.TaskProgramJobRecord{
					{
						JobID: "api", StageID: "build", State: pebblestore.TaskProgramJobIntegrated,
						ChildSessionID: "child-api", CurrentSessionID: "child-api", WorkspacePath: "/worktrees/child-api",
						WorktreeBranch: "agent/api", ImmutableStageBase: "base", ChildHead: "head", IntegrationState: "integrated",
					},
					{JobID: "recoverable", StageID: "build", State: pebblestore.TaskProgramJobHandoffReady, WorkspacePath: "/worktrees/recoverable"},
					{JobID: "failed", StageID: "build", State: pebblestore.TaskProgramJobFailed, WorkspacePath: "/worktrees/failed"},
				},
			})
			if err != nil {
				t.Fatalf("create task program: %v", err)
			}
			scheduler := taskProgramScheduler{service: svc, parentSession: parent, record: record}
			if err := scheduler.cleanupIntegratedStageWorktrees("build"); err != nil {
				t.Fatalf("cleanup integrated stage: %v", err)
			}
			if len(stub.cleanupCalls) != 1 || stub.cleanupCalls[0] != "/worktrees/child-api" || len(stub.cleanupParentPaths) != 1 || stub.cleanupParentPaths[0] != "/shared/repo" {
				t.Fatalf("cleanup calls = %v parent paths = %v", stub.cleanupCalls, stub.cleanupParentPaths)
			}
			if scheduler.record.Jobs[0].State != pebblestore.TaskProgramJobIntegrated || scheduler.record.Jobs[0].IntegrationState != tc.wantIntegration {
				t.Fatalf("cleanup job state = %#v", scheduler.record.Jobs[0])
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

func TestTaskProgramStatusIncludesCoherentParentHead(t *testing.T) {
	record := pebblestore.TaskProgramRecord{ParentSessionID: "parent", ProgramID: "release", ParentHead: strings.Repeat("a", 40), Revision: 2, State: pebblestore.TaskProgramStateCompleted}
	payload := taskProgramStatusPayload(record, false)
	if payload["parent_head"] != record.ParentHead {
		t.Fatalf("parent head = %#v", payload["parent_head"])
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

func TestMasterHarnessRequiresParentToResolveTaskProgramConflictBeforeReplacement(t *testing.T) {
	prompt := masterHarnessPromptWithScope(tool.WorkspaceScope{})
	for _, required := range []string{
		"take ownership of repairing the blocker before continuing",
		"For integration_conflict, you must resolve the reported conflict first",
		"verify the parent worktree is clean and consistent",
		"do not start replacement work while the conflict remains unresolved",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("Task Program recovery prompt missing %q", required)
		}
	}
}

func TestTaskProgramBlockerRetainsCompletedAndUnfinishedContextForNewProgram(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ProgramID: "release", Revision: 4, ActiveStageID: "build",
		Jobs: []pebblestore.TaskProgramJobRecord{
			{JobID: "done", StageID: "build", State: pebblestore.TaskProgramJobCompleted, AttemptNumber: 1, ChildSessionID: "child-done", IntegrationState: "not_required"},
			{JobID: "failed", StageID: "build", State: pebblestore.TaskProgramJobFailed, AttemptNumber: 1, ChildSessionID: "child-failed", IntegrationState: "blocked"},
		},
	}
	scheduler := taskProgramScheduler{record: record}
	blocker := scheduler.structuredBlocker("child_execution_failed", errors.New("failed child"), "author_new_program_for_remaining_work", "failed")
	if blocker.NextAction != "author_new_program_for_remaining_work" || blocker.RepairAction != "author_new_program_for_remaining_work" || len(blocker.PreservedChildren) != 2 {
		t.Fatalf("new-program reconstruction context = %#v", blocker)
	}
	if blocker.PreservedChildren[0].State != pebblestore.TaskProgramJobCompleted || blocker.PreservedChildren[1].State != pebblestore.TaskProgramJobFailed {
		t.Fatalf("completed/unfinished states were not preserved: %#v", blocker.PreservedChildren)
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

func TestTaskProgramStatusExposesBlockedChildLineageAndDirtyRecoveryDetails(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ParentSessionID: "parent", ProgramID: "blocked-program", Revision: 3, State: pebblestore.TaskProgramStateBlocked,
		ActiveStageID: "build", NextAction: "resolve_named_blocker_then_author_new_program_for_unfinished_work",
		Jobs: []pebblestore.TaskProgramJobRecord{{
			JobID: "implement", StageID: "build", State: pebblestore.TaskProgramJobBlocked, AttemptNumber: 1,
			ChildSessionID: "child-1", CurrentSessionID: "child-1", CurrentRunID: "run-1", WorkspacePath: "/workspace/child",
			WorktreeBranch: "agent/child", ParentBranch: "dev", ImmutableStageBase: "base", ChildHead: "head", IntegrationState: "blocked",
			Blocker: &pebblestore.TaskProgramBlocker{Code: "required_input", Message: "schema token required", Evidence: []string{"API returned 401"}, CompletedScope: []string{"parser implemented"}, ResolutionRequirement: "provide a scoped schema token", Dirty: true, ChangedFiles: []string{"parser.go"}, NextAction: "author_new_program_for_remaining_work"},
		}},
		Blocker: &pebblestore.TaskProgramBlocker{Code: "required_input", Message: "schema token required", ResolutionRequirement: "provide a scoped schema token", Dirty: true, ChangedFiles: []string{"parser.go"}, PreservedChildren: []pebblestore.TaskProgramPreservedChild{{JobID: "implement", State: pebblestore.TaskProgramJobBlocked, ChildSessionID: "child-1", RunID: "run-1", WorkspacePath: "/workspace/child", WorktreeBranch: "agent/child", Dirty: true, ChangedFiles: []string{"parser.go"}}}},
	}
	payload := taskProgramStatusPayload(record, false)
	jobs := payload["jobs"].([]map[string]any)
	if len(jobs) != 1 || jobs[0]["current_session_id"] != "child-1" || jobs[0]["current_run_id"] != "run-1" || jobs[0]["workspace_path"] != "/workspace/child" {
		t.Fatalf("blocked child lineage status = %#v", jobs)
	}
	jobBlocker, ok := jobs[0]["blocker"].(*pebblestore.TaskProgramBlocker)
	if !ok || !jobBlocker.Dirty || len(jobBlocker.ChangedFiles) != 1 || jobBlocker.ChangedFiles[0] != "parser.go" || jobBlocker.ResolutionRequirement == "" {
		t.Fatalf("blocked child recovery status = %#v", jobs[0]["blocker"])
	}
	programBlocker, ok := payload["blocker"].(*pebblestore.TaskProgramBlocker)
	if !ok || len(programBlocker.PreservedChildren) != 1 || !programBlocker.PreservedChildren[0].Dirty || programBlocker.PreservedChildren[0].RunID != "run-1" || len(programBlocker.PreservedChildren[0].ChangedFiles) != 1 {
		t.Fatalf("parent-visible preserved child = %#v", payload["blocker"])
	}
}

func TestTaskProgramBlockedChildPreservesStructuredRecoveryEvidence(t *testing.T) {
	job := taskProgramJob{ID: "implement", StageID: "build", RequestedSubagentType: "coder"}
	outcome := taskLaunchOutcome{
		ChildSessionID: "child-1", ChildRunID: "run-1", WorkspacePath: "/workspace/child", WorktreeBranch: "agent/child",
		ParentBranch: "dev", BaseCommit: "base", HeadCommit: "head", Phase: "blocked", BlockerCode: "required_input",
		Reason: "schema token required", BlockerEvidence: []string{"API returned 401"}, CompletedScope: []string{"parser implemented"},
		ResolutionRequired: "provide a scoped schema token", WorktreeClean: false, ChangedFiles: []string{"parser.go"},
	}
	updates := taskProgramOutcomeTransitions(&taskProgramSpec{Jobs: []taskProgramJob{job}}, []taskLaunchOutcome{outcome}, []error{taskChildBlockedError{code: "required_input", message: outcome.Reason}})
	if len(updates) != 1 || updates[0].State != pebblestore.TaskProgramJobBlocked || updates[0].CurrentRunID != "run-1" || updates[0].Blocker == nil {
		t.Fatalf("blocked child transition = %#v", updates)
	}
	blocker := updates[0].Blocker
	if blocker.Code != "required_input" || blocker.ResolutionRequirement != "provide a scoped schema token" || !blocker.Dirty || len(blocker.ChangedFiles) != 1 || len(blocker.Evidence) != 1 || len(blocker.CompletedScope) != 1 {
		t.Fatalf("structured blocker evidence = %#v", blocker)
	}
}

func TestTaskProgramBlockedOutcomePayloadRoundTripsRecoveryFields(t *testing.T) {
	payload := map[string]any{"launches": []any{map[string]any{
		"child_session_id": "child-1", "child_run_id": "run-1", "workspace_path": "/workspace/child", "phase": "blocked",
		"blocker_code": "required_input", "blocker_evidence": []any{"API returned 401"}, "completed_scope": []any{"parser implemented"},
		"resolution_requirement": "provide a scoped schema token", "worktree_clean": false, "changed_files": []any{"parser.go"},
	}}}
	outcomes := taskProgramOutcomesFromPayload(payload, 1)
	if len(outcomes) != 1 || outcomes[0].ChildRunID != "run-1" || outcomes[0].BlockerCode != "required_input" || outcomes[0].WorktreeClean || len(outcomes[0].BlockerEvidence) != 1 || len(outcomes[0].CompletedScope) != 1 || len(outcomes[0].ChangedFiles) != 1 {
		t.Fatalf("blocked outcome payload = %#v", outcomes)
	}
}

func TestCollectTaskReadyArtifactReferencesExcludesFailedLaunchArtifacts(t *testing.T) {
	outcomes := []taskLaunchOutcome{
		{ArtifactReference: &taskArtifactReference{VariantID: "ready", Status: pebblestore.SessionArtifactStatusReady}},
		{ArtifactReference: &taskArtifactReference{VariantID: "blocked-but-published", Status: pebblestore.SessionArtifactStatusReady}},
		{ArtifactReference: &taskArtifactReference{VariantID: "failed", Status: pebblestore.SessionArtifactStatusFailed}},
	}
	refs := collectTaskReadyArtifactReferences(outcomes, []error{nil, taskChildBlockedError{code: "source_fidelity_not_preserved", message: "inexact"}, nil})
	if len(refs) != 1 || refs[0].VariantID != "ready" {
		t.Fatalf("ready artifact references = %#v", refs)
	}
}

func TestTaskChildBlockedReportRequiresExplicitMarker(t *testing.T) {
	var outcome taskLaunchOutcome
	if err := parseTaskChildBlockedReport("ordinary failure", &outcome); err != nil {
		t.Fatalf("ordinary failure classified blocked: %v", err)
	}
	report := "BLOCKED:\n- blocker code: external_dependency\n- blocker message: registry unavailable\n- authoritative evidence: registry returned 503\n- completed scope: local package built\n- resolution requirement: restore registry"
	if err := parseTaskChildBlockedReport(report, &outcome); err == nil || outcome.BlockerCode != "external_dependency" || outcome.ResolutionRequired != "restore registry" || len(outcome.BlockerEvidence) != 1 || len(outcome.CompletedScope) != 1 {
		t.Fatalf("explicit blocked report = outcome %#v err %v", outcome, err)
	}
}

func TestTaskChangedFilesFromGitStatusNormalizesDirtyRecoveryPaths(t *testing.T) {
	status := " M parser.go\n?? schema.json\nR  old.go -> new.go\n M parser.go"
	files := taskChangedFilesFromGitStatus(status)
	if strings.Join(files, ",") != "parser.go,schema.json,new.go" {
		t.Fatalf("changed files = %v", files)
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

func TestTaskProgramCoderPromptRequiresFinderVerification(t *testing.T) {
	prompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{Description: "implement", Prompt: "change the scoped file", RequestedSubagent: "coder"})
	for _, required := range []string{"Treat Finder handoffs", "Agents can make mistakes", "independently verify every relevant claim against the current workspace before editing files", "at most two materially distinct safe recovery paths", "BLOCKED:", "Never auto-commit dirty blocked work"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("Coder delegation prompt missing %q: %s", required, prompt)
		}
	}
}

func TestTaskProgramFinderOutcomeRequiresAndPersistsDurableHandoff(t *testing.T) {
	job := taskProgramJob{ID: "inspect", StageID: "research", RequestedSubagentType: "finder"}
	ref := &taskReportRef{SessionID: "child-finder", MessageID: "msg-7", GlobalSeq: 7, Role: "assistant", Source: "child_session_transcript"}
	updates := taskProgramOutcomeTransitions(&taskProgramSpec{Jobs: []taskProgramJob{job}}, []taskLaunchOutcome{{ChildSessionID: "child-finder", ReportRef: ref}}, []error{nil})
	if len(updates) != 1 || updates[0].State != pebblestore.TaskProgramJobCompleted || updates[0].HandoffRef == nil || updates[0].HandoffRef.SessionID != "child-finder" || updates[0].HandoffRef.MessageID != "msg-7" || updates[0].HandoffRef.GlobalSeq != 7 {
		t.Fatalf("Finder durable handoff transition = %#v", updates)
	}
	missing := taskProgramOutcomeTransitions(&taskProgramSpec{Jobs: []taskProgramJob{job}}, []taskLaunchOutcome{{ChildSessionID: "child-finder"}}, []error{nil})
	if len(missing) != 1 || missing[0].State != pebblestore.TaskProgramJobFailed || missing[0].Blocker == nil || missing[0].Blocker.Code != "finder_handoff_missing" {
		t.Fatalf("missing Finder handoff transition = %#v", missing)
	}
}

func TestTaskProgramOutcomesParseReportReference(t *testing.T) {
	payload := map[string]any{"launches": []any{map[string]any{
		"child_session_id": "child-finder",
		"report_ref":       map[string]any{"session_id": "child-finder", "message_id": "msg-9", "global_seq": float64(9), "role": "assistant", "source": "child_session_transcript"},
	}}}
	outcomes := taskProgramOutcomesFromPayload(payload, 1)
	if len(outcomes) != 1 || outcomes[0].ReportRef == nil || outcomes[0].ReportRef.SessionID != "child-finder" || outcomes[0].ReportRef.MessageID != "msg-9" || outcomes[0].ReportRef.GlobalSeq != 9 {
		t.Fatalf("parsed report outcome = %#v", outcomes)
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

func TestTaskProgramStatusExposesBoundedReadyArtifactReferences(t *testing.T) {
	record := pebblestore.TaskProgramRecord{
		ParentSessionID: "parent", ProgramID: "managed_program", ReservationCallID: "call-managed", State: pebblestore.TaskProgramStateCompleted,
		Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{{ID: "design", StageID: "variants", AgentType: "designer"}}},
		Jobs:       []pebblestore.TaskProgramJobRecord{{JobID: "design", StageID: "variants", State: pebblestore.TaskProgramJobCompleted, IntegrationState: "artifact_ready", ChildSessionID: "child", ArtifactRef: &pebblestore.TaskProgramArtifactRef{SessionID: "parent", ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 7, TurnID: "turn", CandidateID: "candidate"}}},
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

func TestParseTaskProgramStartWithoutDefinitionSelectsApprovedCheckpointProgram(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"action":"start","workspace_path":"/shared/repo"}`)
	if err != nil || !parsed.PlannedProgram || parsed.Program != nil || parsed.Action != taskProgramActionStart || parsed.ProgramWorkspacePath != "/shared/repo" {
		t.Fatalf("planned start = %#v err=%v", parsed, err)
	}
}

func TestResolveApprovedCheckpointTaskProgramUsesCanonicalDefinition(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()
	sessionID := createPlanManageTestSession(t, sessionSvc)
	program := pebblestore.TaskProgramDefinition{
		ID:     "approved_program",
		Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "Ready from the approved checkpoint."}},
		Jobs:   []pebblestore.TaskProgramJobSpec{{ID: "api", StageID: "build", AgentType: "coder", Title: "API Work", MetaPrompt: "Implement the approved API scope.", Deliverable: "Committed API change", OwnedScope: []string{"swarmd/internal/api/**"}, AcceptanceCriteria: []string{"API is complete"}, DependencyEvidence: "No unfinished dependency."}},
	}
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-program", "Approved program", "# display", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID: "plan-program", Title: "Approved program", Status: "approved", Info: pebblestore.SessionPlanInfo{Goal: "Run approved implementation"},
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints:     []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Build", Objective: "Build the feature", Status: sessionruntime.PlanCheckpointStatusInProgress, Order: 1, AcceptanceCriteria: []string{"Feature is built"}, TaskProgram: &program}}, ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseTaskCallArguments(`{"action":"start","workspace_path":"/shared/repo"}`)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := runSvc.resolveApprovedCheckpointTaskProgram(sessionID, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Program == nil || resolved.Program.ID != program.ID || len(resolved.Launches) != 1 || resolved.Launches[0].AssignmentLabel != "API Work" || resolved.Launches[0].TargetWorkspacePath != "/shared/repo" || resolved.Program.Jobs[0].TargetWorkspacePath != "/shared/repo" {
		t.Fatalf("resolved program = %#v", resolved)
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
