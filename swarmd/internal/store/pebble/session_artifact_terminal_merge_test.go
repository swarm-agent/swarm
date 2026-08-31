package pebblestore

import "testing"

func TestMergeTerminalArtifactVariantPersistsProducedLocatorParts(t *testing.T) {
	current := SessionArtifactVariant{
		ID:                    "variant-1",
		CollectionID:          "collection-1",
		ProjectionReservation: true,
		Lineage:               SessionArtifactLineage{TaskCallID: "task-1"},
	}
	produced := []SessionArtifactPart{
		{ID: "part-1", Label: "Opening", Kind: "temporal", EndMs: 4000},
		{ID: "part-2", Label: "Transformation", Kind: "temporal", StartMs: 4000, EndMs: 8000},
		{ID: "part-3", Label: "Resolution", Kind: "temporal", StartMs: 8000, EndMs: 12000},
	}
	incoming := SessionArtifactVariant{
		ID:           current.ID,
		CollectionID: current.CollectionID,
		Filename:     "candidate.html",
		MediaType:    "text/html",
		Parts:        produced,
	}

	merged := mergeTerminalArtifactVariant(current, incoming)
	if len(merged.Parts) != len(produced) {
		t.Fatalf("merged parts = %#v", merged.Parts)
	}
	for index := range produced {
		if merged.Parts[index] != produced[index] {
			t.Fatalf("merged part %d = %#v, want %#v", index, merged.Parts[index], produced[index])
		}
	}
	produced[0].Label = "mutated"
	if merged.Parts[0].Label == produced[0].Label {
		t.Fatal("merged locator parts alias the incoming slice")
	}
}

func TestMergeTerminalArtifactVariantPreservesProgressUntilTerminalStateClearsIt(t *testing.T) {
	progress := &SessionArtifactProgress{Stage: "frame_capture", Completed: 5, Total: 10, Percent: 47.5, HeartbeatAt: 1000}
	merged := mergeTerminalArtifactVariant(SessionArtifactVariant{Progress: progress}, SessionArtifactVariant{})
	if merged.Progress != progress {
		t.Fatalf("progress = %#v, want preserved pointer", merged.Progress)
	}
	incoming := &SessionArtifactProgress{Stage: "segment_encode", Completed: 1, Total: 2, Percent: 94, HeartbeatAt: 2000}
	merged = mergeTerminalArtifactVariant(merged, SessionArtifactVariant{Progress: incoming})
	if merged.Progress == incoming || merged.Progress.Stage != incoming.Stage || merged.Progress.Percent != incoming.Percent {
		t.Fatalf("merged progress = %#v", merged.Progress)
	}
}

func TestMergeTerminalArtifactVariantPreservesLocatorPartsWhenIncomingOmitsThem(t *testing.T) {
	current := SessionArtifactVariant{Parts: []SessionArtifactPart{{ID: "hero", Label: "Hero", Kind: "semantic"}}}
	merged := mergeTerminalArtifactVariant(current, SessionArtifactVariant{Filename: "candidate.html", MediaType: "text/html"})
	if len(merged.Parts) != 1 || merged.Parts[0] != current.Parts[0] {
		t.Fatalf("merged parts = %#v, want %#v", merged.Parts, current.Parts)
	}
}
