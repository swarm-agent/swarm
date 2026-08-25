package pebblestore

import "testing"

func TestValidateArtifactLineageAcceptsExactMultiTargetReviewSource(t *testing.T) {
	lineage := SessionArtifactLineage{
		ParentSessionID:         "parent-1",
		SourceSessionID:         "source-1",
		SourceCollectionID:      "collection-1",
		SourceVariantID:         "variant-1",
		SourceEventSeq:          41,
		SelectedReviewTargetIDs: "part-1,part-3",
	}
	if err := validateArtifactLineage(lineage); err != nil {
		t.Fatalf("valid multi-target review lineage rejected: %v", err)
	}
	lineage.SourceSessionID = ""
	if err := validateArtifactLineage(lineage); err == nil {
		t.Fatal("multi-target review lineage without a source session was accepted")
	}
}

func TestArtifactCollectionLineageCompatibleAllowsSiblingManagedVariants(t *testing.T) {
	existing := SessionArtifactLineage{
		ParentSessionID: "parent-1",
		TaskCallID:      "task-call-1",
		ProgramID:       "program-1",
		ChildSessionID:  "child-1",
		IterationID:     "iteration-1",
		IterationIndex:  1,
		RunID:           "run-1",
	}
	incoming := existing
	incoming.ChildSessionID = "child-2"
	incoming.IterationID = "iteration-2"
	incoming.IterationIndex = 2
	incoming.RunID = "run-2"
	incoming.PlanID = "plan-2"
	incoming.CheckpointID = "cp-2"
	incoming.AttemptID = "attempt-2"

	if !artifactCollectionLineageCompatible(existing, incoming) {
		t.Fatal("sibling managed variants from one task call should share a collection")
	}

	incoming.TaskCallID = "task-call-2"
	if artifactCollectionLineageCompatible(existing, incoming) {
		t.Fatal("a different task call must not replace collection lineage")
	}

	incoming.TaskCallID = existing.TaskCallID
	incoming.ProgramID = "program-2"
	if artifactCollectionLineageCompatible(existing, incoming) {
		t.Fatal("a different task program must not replace collection lineage")
	}
}
