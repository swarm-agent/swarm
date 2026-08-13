package run

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBackendDesignerRegularAndIterationRoutingContract(t *testing.T) {
	managed, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "regular", "description": "managed alternatives", "prompt": "create variants",
		"launches": []any{
			map[string]any{"subagent_type": "designer", "title": "Compact", "meta_prompt": "Create compact.", "deliverable": "Managed compact", "dependency_evidence": "Brief ready."},
			map[string]any{"subagent_type": "designer", "title": "Spacious", "meta_prompt": "Create spacious.", "deliverable": "Managed spacious", "dependency_evidence": "Brief ready."},
		},
	}))
	if err != nil {
		t.Fatalf("parse managed regular wave: %v", err)
	}
	if len(managed.Launches) != 2 || managed.Launches[0].OutputMode != taskOutputModeManaged || managed.Launches[1].OutputMode != taskOutputModeManaged || len(managed.Launches[0].OwnedScope) != 0 {
		t.Fatalf("managed regular routing = %#v", managed.Launches)
	}
	first := managedDesignerArtifactContext(pebblestore.SessionSnapshot{ID: "parent"}, "task-call", managed.Launches[0], 1)
	second := managedDesignerArtifactContext(pebblestore.SessionSnapshot{ID: "parent"}, "task-call", managed.Launches[1], 2)
	if first.CollectionID == "" || first.CollectionID != second.CollectionID || first.VariantID == second.VariantID || first.VariantID == "" || second.VariantID == "" {
		t.Fatalf("managed regular destinations = %#v / %#v", first, second)
	}

	workspace, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "regular", "description": "workspace alternatives", "prompt": "create repository variants",
		"launches": []any{
			map[string]any{"subagent_type": "designer", "title": "Compact", "meta_prompt": "Create compact.", "deliverable": "Source compact", "output_mode": "workspace", "owned_scope": []any{"web/src/variants/compact.tsx"}, "dependency_evidence": "Target finalized."},
			map[string]any{"subagent_type": "designer", "title": "Spacious", "meta_prompt": "Create spacious.", "deliverable": "Source spacious", "output_mode": "workspace", "owned_scope": []any{"web/src/variants/spacious.tsx"}, "dependency_evidence": "Target finalized."},
		},
	}))
	if err != nil || workspace.Launches[0].OutputMode != taskOutputModeWorkspace || workspace.Launches[1].OutputMode != taskOutputModeWorkspace {
		t.Fatalf("workspace regular routing = %#v err=%v", workspace.Launches, err)
	}

	iteration, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "swarm", "description": "managed iteration", "prompt": "create alternatives", "agent_type": "designer", "count": 3,
	}))
	if err != nil {
		t.Fatalf("parse managed Iteration Swarm: %v", err)
	}
	for i, launch := range iteration.Launches {
		if launch.OutputMode != taskOutputModeManaged || len(launch.OwnedScope) != 0 {
			t.Fatalf("managed swarm launch %d = %#v", i, launch)
		}
	}
	workspaceIteration, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "swarm", "description": "workspace iteration", "prompt": "create alternatives", "agent_type": "designer", "count": 2,
		"output_mode": "workspace", "owned_scope_template": "web/src/variants/iteration-{index}.tsx",
	}))
	if err != nil || len(workspaceIteration.Launches) != 2 || workspaceIteration.Launches[0].OwnedScope[0] != "web/src/variants/iteration-1.tsx" || workspaceIteration.Launches[1].OwnedScope[0] != "web/src/variants/iteration-2.tsx" {
		t.Fatalf("workspace swarm routing = %#v err=%v", workspaceIteration.Launches, err)
	}
}

func TestBackendDesignerWorkspaceModeRejectsImplicitOrOverlappingRepositoryTargets(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "implicit managed with repo target", args: map[string]any{"prompt": "design", "agent": "designer", "role": "create", "owned_scope": []any{"web/src/variant.tsx"}}, want: "managed Designer must omit owned_scope"},
		{name: "workspace without target", args: map[string]any{"prompt": "design", "agent": "designer", "role": "create", "output_mode": "workspace"}, want: "requires a concrete workspace-relative owned_scope"},
		{name: "workspace traversal", args: map[string]any{"prompt": "design", "agent": "designer", "role": "create", "output_mode": "workspace", "owned_scope": []any{"web/src/../escape.tsx"}}, want: "concrete clean workspace-relative path"},
		{name: "overlap", args: map[string]any{"prompt": "design", "launches": []any{
			map[string]any{"agent": "designer", "role": "first", "output_mode": "workspace", "owned_scope": []any{"web/src/variants"}},
			map[string]any{"agent": "designer", "role": "second", "output_mode": "workspace", "owned_scope": []any{"web/src/variants/child.tsx"}},
		}}, want: "distinct output target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTaskCallArguments(mustJSON(t, tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBackendTaskProgramPartialFailurePreservesCompletedSiblingForGuardedResume(t *testing.T) {
	spec := &taskProgramSpec{ID: "artifact_program", Jobs: []taskProgramJob{
		{ID: "ready", StageID: "variants", RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged},
		{ID: "failed", StageID: "variants", RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged},
	}}
	outcomes := []taskLaunchOutcome{
		{ChildSessionID: "child-ready", ArtifactReference: &taskArtifactReference{SessionID: "parent", CollectionID: "collection", VariantID: "ready", Status: pebblestore.SessionArtifactStatusReady}},
		{ChildSessionID: "child-failed", Phase: "failed", Reason: "render failed"},
	}
	updates := taskProgramOutcomeTransitions(spec, outcomes, []error{nil, errContractRenderFailed{}})
	if len(updates) != 2 || updates[0].State != pebblestore.TaskProgramJobCompleted || updates[0].IntegrationState != "artifact_ready" || updates[1].State != pebblestore.TaskProgramJobFailed || updates[1].Blocker == nil {
		t.Fatalf("partial failure transitions = %#v", updates)
	}

	prior := pebblestore.TaskProgramRecord{Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{
		{ID: "ready", StageID: "variants", AgentType: "designer", OutputMode: taskOutputModeManaged},
		{ID: "failed", StageID: "variants", AgentType: "designer", OutputMode: taskOutputModeManaged},
	}}, Jobs: []pebblestore.TaskProgramJobRecord{
		{JobID: "ready", StageID: "variants", State: pebblestore.TaskProgramJobCompleted, ChildSessionID: "child-ready", IntegrationState: "artifact_ready"},
		{JobID: "failed", StageID: "variants", State: pebblestore.TaskProgramJobFailed, ChildSessionID: "child-failed", AttemptNumber: 1, Blocker: &pebblestore.TaskProgramBlocker{Message: "render failed"}},
	}}
	prepared := prior
	prepared.Jobs = append([]pebblestore.TaskProgramJobRecord(nil), prior.Jobs...)
	prepared.Jobs[1].State = pebblestore.TaskProgramJobDeclared
	launches := taskProgramResumeLaunches(prepared, prior, spec)
	if launches[0].ResumeChildSessionID != "" || launches[1].ResumeChildSessionID != "child-failed" || launches[1].ResumeReason != "render failed" {
		t.Fatalf("guarded partial resume launches = %#v", launches)
	}
}

type errContractRenderFailed struct{}

func (errContractRenderFailed) Error() string { return "render failed" }
