package run

import (
	"errors"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: cohort errors must not destroy successful sibling handoffs, while
// missing outcomes fail closed. The payload adapter and transition builder are
// the narrowest layer deciding durable job state after the launch executor.
func TestTaskProgramCohortErrorPreservesSuccessfulSibling(t *testing.T) {
	payload := map[string]any{"launches": []map[string]any{{"phase": "completed", "child_session_id": "good", "base_commit": "base", "head_commit": "head"}, {"phase": "blocked", "error": "required input"}}}
	errs := taskProgramErrorsFromPayload(payload, errors.New("cohort failed"), 3)
	if errs[0] != nil || errs[1] == nil || errs[2] == nil {
		t.Fatalf("errors: %v", errs)
	}
	spec := &taskProgramSpec{Jobs: []taskProgramJob{{ID: "good", RequestedSubagentType: "coder"}, {ID: "bad", RequestedSubagentType: "coder"}, {ID: "missing", RequestedSubagentType: "coder"}}}
	updates := taskProgramOutcomeTransitions(spec, taskProgramOutcomesFromPayload(payload, 3), errs)
	if updates[0].State != pebblestore.TaskProgramJobHandoffReady || updates[0].ChildHead != "head" || updates[1].State != pebblestore.TaskProgramJobBlocked || updates[2].State != pebblestore.TaskProgramJobFailed {
		t.Fatalf("updates: %+v", updates)
	}
	if taskProgramErrorsFromPayload(nil, nil, 1)[0] == nil {
		t.Fatal("missing output silently succeeded")
	}
}

// Purpose: native artifacts must be returned from their persisted exact output,
// never reconstructed from job names or legacy IDs. Status must not manufacture
// ready artifacts for historical records lacking authenticated output evidence.
func TestTaskProgramNativeReferenceRoundTrip(t *testing.T) {
	ref := &taskArtifactReference{SessionID: "parent", ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 9, TurnID: "turn", CandidateID: "candidate", Status: "ready"}
	spec := &taskProgramSpec{Jobs: []taskProgramJob{{ID: "design", RequestedSubagentType: "designer", OutputMode: "managed"}}}
	updates := taskProgramOutcomeTransitions(spec, []taskLaunchOutcome{{ChildSessionID: "child", ArtifactReference: ref}}, []error{nil})
	if updates[0].ArtifactRef == nil || updates[0].ArtifactRef.CommitOID != ref.CommitOID {
		t.Fatalf("lost output: %+v", updates)
	}
	job := pebblestore.TaskProgramJobRecord{JobID: "design", State: "completed", IntegrationState: "artifact_ready", ArtifactRef: updates[0].ArtifactRef}
	def := pebblestore.TaskProgramJobSpec{ID: "design", AgentType: "designer", OutputMode: "managed"}
	got := taskProgramReadyArtifactReference(pebblestore.TaskProgramRecord{}, def, job)
	if got == nil || got.ArtifactID != ref.ArtifactID || got.CommitOID != ref.CommitOID || got.ProjectionSeq != ref.ProjectionSeq || got.CollectionID != "" || got.VariantID != "" {
		t.Fatalf("reference: %+v", got)
	}
	job.ArtifactRef = nil
	if taskProgramReadyArtifactReference(pebblestore.TaskProgramRecord{}, def, job) != nil {
		t.Fatal("fabricated ready reference")
	}
}

// Purpose: a Finder's own uncompleted report is not a prerequisite. Hydration
// must walk only dependencies, allowing a first-stage Finder to launch normally.
func TestTaskProgramFinderDoesNotHydrateItself(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	scheduler := taskProgramScheduler{service: svc, record: pebblestore.TaskProgramRecord{Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{{ID: "inspect", AgentType: "finder"}}}}}
	if handoff, err := scheduler.finderHandoffsForJob(0); err != nil || handoff != "" {
		t.Fatalf("self hydration: %q %v", handoff, err)
	}
}

// Purpose: approval stays bound to the declared source and narrow write scope;
// only the scheduler's typed lane can substitute an authenticated runtime path.
func TestTaskProgramApprovedLaneMappingDoesNotWidenScope(t *testing.T) {
	spec := taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: "/source", OwnedScope: []string{"source.go"}}
	row := taskLaunchManifestRow{RequestedSubagentType: "coder", TargetWorkspacePath: "/source", OwnedScope: []string{"source.go"}, ProfileSnapshot: &pebblestore.AgentProfile{}}
	manifest := taskLaunchManifest{Launches: []taskLaunchManifestRow{row}}
	hash, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	approved := mustJSON(t, map[string]any{"manifest_hash": hash, "manifest": manifest})
	spec.ProgramRepositoryLane = &pebblestore.TaskProgramRepositoryLane{SourcePath: "/source", WorkspacePath: "/managed", Branch: "agent/lane"}
	spec.TargetWorkspacePath = "/managed"
	if _, err := parseApprovedTaskLaunchManifest(approved, []taskLaunchSpec{spec}); err != nil {
		t.Fatal(err)
	}
	spec.ProgramRepositoryLane = nil
	if _, err := parseApprovedTaskLaunchManifest(approved, []taskLaunchSpec{spec}); err == nil {
		t.Fatal("unbound runtime substitution accepted")
	}
	spec.ProgramRepositoryLane = &pebblestore.TaskProgramRepositoryLane{SourcePath: "/source", WorkspacePath: "/managed", Branch: "agent/lane"}
	spec.OwnedScope = []string{"."}
	if _, err := parseApprovedTaskLaunchManifest(approved, []taskLaunchSpec{spec}); err == nil {
		t.Fatal("scope widening accepted")
	}
}

// Purpose: ambient chat selections must not override a declared program's own
// dependency graph or turn an initial Designer job into an unrelated revision.
func TestTaskProgramIgnoresAmbientArtifactSelection(t *testing.T) {
	for _, cohort := range []bool{false, true} {
		parsed := taskCallArguments{}
		launches := []taskLaunchSpec{{RequestedSubagentType: "designer", OutputMode: "managed"}}
		if cohort {
			launches[0].SourceArguments = map[string]any{"program_job_id": "design"}
		} else {
			parsed.Program = &taskProgramSpec{ID: "program"}
		}
		selection := &pebblestore.SessionArtifactSelectionReference{SessionID: "parent", ArtifactID: "ambient", CommitOID: "commit", ProjectionSeq: 1, RevisionRef: "revision-commit"}
		if err := bindTaskNativeArtifactSelection(&parsed, launches, selection); err != nil {
			t.Fatal(err)
		}
		if parsed.ArtifactV3Source != nil || launches[0].ArtifactV3Source != nil {
			t.Fatal("ambient selection entered program")
		}
	}
}
