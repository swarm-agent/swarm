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
	parent := pebblestore.SessionSnapshot{ID: "parent", UserID: "user-1", AccountScopeID: "account-1"}
	first := managedDesignerArtifactContext(parent, "task-call", managed.Launches[0], 1)
	second := managedDesignerArtifactContext(parent, "task-call", managed.Launches[1], 2)
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

	source := map[string]any{"session_id": "source-session", "collection_id": "source-collection", "variant_id": "source-variant", "event_seq": 41}
	workspaceSource, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "regular", "description": "workspace source alternatives", "prompt": "create repository variants", "source_artifact": source,
		"launches": []any{
			map[string]any{"subagent_type": "designer", "title": "Compact", "meta_prompt": "Create compact.", "deliverable": "Source compact", "output_mode": "workspace", "owned_scope": []any{"web/src/variants/source-compact"}, "dependency_evidence": "Target finalized."},
			map[string]any{"subagent_type": "designer", "title": "Spacious", "meta_prompt": "Create spacious.", "deliverable": "Source spacious", "output_mode": "workspace", "owned_scope": []any{"web/src/variants/source-spacious"}, "dependency_evidence": "Target finalized."},
		},
	}))
	if err != nil || !equalTaskImageSourceArtifact(workspaceSource.Launches[0].SourceArtifact, workspaceSource.SourceArtifact) || !equalTaskImageSourceArtifact(workspaceSource.Launches[1].SourceArtifact, workspaceSource.SourceArtifact) {
		t.Fatalf("workspace source-artifact routing = %#v err=%v", workspaceSource.Launches, err)
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
	_, err = parseTaskCallArguments(mustJSON(t, map[string]any{
		"mode": "swarm", "description": "workspace iteration", "prompt": "create alternatives", "agent_type": "designer", "count": 2,
		"output_mode": "workspace",
	}))
	if err == nil || !strings.Contains(err.Error(), "regular task launches") {
		t.Fatalf("workspace Designer Iteration Swarm error = %v", err)
	}
}

func TestTaskArtifactReferenceReturnsCompleteReadyLineage(t *testing.T) {
	variant := pebblestore.SessionArtifactVariant{
		ID: "variant-2", CollectionID: "collection-2", EventSeq: 42, Status: pebblestore.SessionArtifactStatusReady,
		Lineage: pebblestore.SessionArtifactLineage{
			SourceSessionID: "source-session", SourceCollectionID: "source-collection", SourceVariantID: "source-variant", SourceEventSeq: 41,
			IterationSectionID: "step-03-understand", IterationSectionLabel: "03A · UNDERSTAND", IterationSectionStartMs: 20220, IterationSectionEndMs: 27600,
		},
	}
	reference := taskArtifactReferenceFromVariant("parent-session", variant)
	if reference == nil || reference.SessionID != "parent-session" || reference.CollectionID != variant.CollectionID || reference.VariantID != variant.ID || reference.EventSeq != variant.EventSeq {
		t.Fatalf("ready artifact reference is incomplete: %#v", reference)
	}
	if reference.SourceArtifact == nil || reference.SourceArtifact.SessionID != "source-session" || reference.SourceArtifact.EventSeq != 41 {
		t.Fatalf("ready artifact reference lost exact source lineage: %#v", reference)
	}
	if reference.SectionTarget == nil || reference.SectionTarget.ID != "step-03-understand" || reference.SectionTarget.StartMs != 20220 || reference.SectionTarget.EndMs != 27600 {
		t.Fatalf("ready artifact reference lost exact section target: %#v", reference)
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

func TestCollectTaskReadyArtifactReferencesExcludesFailedAnimationInspectionSlot(t *testing.T) {
	ready := &taskArtifactReference{SessionID: "parent", CollectionID: "collection", VariantID: "ready", Status: pebblestore.SessionArtifactStatusReady}
	failedInspection := &taskArtifactReference{SessionID: "parent", CollectionID: "collection", VariantID: "failed-inspection", Status: pebblestore.SessionArtifactStatusFailed, FailureCode: "animation_inspection_failed"}
	outcomes := []taskLaunchOutcome{
		{ArtifactReference: ready},
		{ArtifactReference: failedInspection, Phase: "failed", Reason: "representative-frame inspection failed"},
	}
	references := collectTaskReadyArtifactReferences(outcomes, []error{nil, errContractRenderFailed{}})
	if len(references) != 1 || references[0] != ready {
		t.Fatalf("successful variant references = %#v, want only ready slot", references)
	}
}

// Requirement: a visually rejected managed animation is a failed immutable
// candidate, but a regular partial wave must expose one bounded replacement path
// instead of encouraging a terminal checkpoint failure or rerunning good slots.
// This helper layer is the narrowest proof because service_tools.go uses it to
// decide whether the task payload may advertise that recovery action.
func TestCountRecoverableManagedDesignerInspectionFailuresIsFailClosed(t *testing.T) {
	ready := &taskArtifactReference{Status: pebblestore.SessionArtifactStatusReady}
	failedInspection := &taskArtifactReference{Status: pebblestore.SessionArtifactStatusFailed, FailureCode: "animation_inspection_failed"}
	outcomes := []taskLaunchOutcome{
		{RequestedSubagent: "designer", ArtifactReference: ready},
		{RequestedSubagent: "designer", ArtifactReference: failedInspection, ReportExcerpt: "ANIMATION_INSPECTION frame=exit status=fail evidence=clipped label"},
	}
	runErrs := []error{nil, errContractRenderFailed{}}
	if got := countRecoverableManagedDesignerInspectionFailures(outcomes, runErrs); got != 1 {
		t.Fatalf("recoverable managed Designer inspection failures = %d, want 1", got)
	}

	for name, mutate := range map[string]func([]taskLaunchOutcome){
		"missing report": func(values []taskLaunchOutcome) { values[1].ReportExcerpt = "" },
		"publication failure": func(values []taskLaunchOutcome) {
			values[1].ArtifactReference.FailureCode = "animation_viewport_overflow"
		},
		"non-designer":            func(values []taskLaunchOutcome) { values[1].RequestedSubagent = "coder" },
		"successful non-designer": func(values []taskLaunchOutcome) { values[0].RequestedSubagent = "coder" },
	} {
		t.Run(name, func(t *testing.T) {
			copyOutcomes := append([]taskLaunchOutcome(nil), outcomes...)
			copyRef := *outcomes[1].ArtifactReference
			copyOutcomes[1].ArtifactReference = &copyRef
			mutate(copyOutcomes)
			if got := countRecoverableManagedDesignerInspectionFailures(copyOutcomes, runErrs); got != 0 {
				t.Fatalf("non-recoverable outcomes advertised %d replacement slot(s)", got)
			}
		})
	}

	allFailed := []taskLaunchOutcome{{RequestedSubagent: "designer", ArtifactReference: failedInspection, ReportExcerpt: "ANIMATION_INSPECTION frame=exit status=fail evidence=clipped label"}}
	if got := countRecoverableManagedDesignerInspectionFailures(allFailed, []error{errContractRenderFailed{}}); got != 0 {
		t.Fatalf("all-failed wave advertised %d replacement slot(s)", got)
	}
}

func TestBackendTaskProgramPartialFailurePreservesContextForNewProgram(t *testing.T) {
	spec := &taskProgramSpec{ID: "artifact_program", Jobs: []taskProgramJob{
		{ID: "ready", StageID: "variants", RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged},
		{ID: "failed", StageID: "variants", RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged},
	}}
	outcomes := []taskLaunchOutcome{
		{ChildSessionID: "child-ready", ArtifactReference: &taskArtifactReference{SessionID: "parent", CollectionID: "collection", VariantID: "ready", Status: pebblestore.SessionArtifactStatusReady}},
		{ChildSessionID: "child-failed", Phase: "failed", Reason: "render failed"},
	}
	updates := taskProgramOutcomeTransitions(spec, outcomes, []error{nil, errContractRenderFailed{}})
	if len(updates) != 2 || updates[0].State != pebblestore.TaskProgramJobCompleted || updates[0].IntegrationState != "artifact_ready" || updates[1].State != pebblestore.TaskProgramJobFailed || updates[1].Blocker == nil || updates[1].Blocker.NextAction != "author_new_program_for_remaining_work" {
		t.Fatalf("partial failure transitions = %#v", updates)
	}
}

type errContractRenderFailed struct{}

func (errContractRenderFailed) Error() string { return "render failed" }
