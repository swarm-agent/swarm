package videotranscription

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBuildVideoEvidenceDeduplicatesRepeatedModalitiesAndRanges(t *testing.T) {
	transcript := pebblestore.NormalizedTranscript{
		Ref: "transcript_range", ContentDigest: "digest_range", Metadata: pebblestore.NormalizedTranscriptMetadata{DurationMs: 30_000},
		Segments: []pebblestore.NormalizedTranscriptSegment{
			{StartMs: 0, EndMs: 10_000, Speech: "same speech", Visual: "first view", OnScreenText: "same label"},
			{StartMs: 10_000, EndMs: 20_000, Speech: "same speech", OnScreenText: "same label"},
		},
	}
	evidence, raw := BuildVideoEvidence(transcript, 9_000, 21_000)
	if raw != 5 {
		t.Fatalf("raw evidence = %d, want 5", raw)
	}
	if len(evidence) != 3 {
		t.Fatalf("deduplicated evidence = %#v", evidence)
	}
	var speech *VideoEvidence
	for index := range evidence {
		if evidence[index].StartMs < 9_000 || evidence[index].EndMs > 21_000 {
			t.Fatalf("range leaked evidence: %#v", evidence[index])
		}
		if evidence[index].Modality == "speech" {
			speech = &evidence[index]
		}
	}
	if speech == nil || speech.StartMs != 9_000 || speech.EndMs != 20_000 || speech.Provenance.FirstSegment != 0 || speech.Provenance.LastSegment != 1 {
		t.Fatalf("speech evidence = %#v", speech)
	}
	if speech.Provenance.TranscriptRef != transcript.Ref || speech.Provenance.TranscriptContentDigest != transcript.ContentDigest {
		t.Fatalf("speech provenance = %#v", speech.Provenance)
	}
}

func TestBuildVideoSectionIndexProducesNineLaunchBenchmarkSections(t *testing.T) {
	transcript := launchBenchmarkTranscript()
	index, manifest, err := BuildVideoSectionIndex(transcript)
	if err != nil {
		t.Fatal(err)
	}
	if index.SchemaVersion != VideoSectionIndexSchemaVersion || index.Source.TranscriptContentDigest != transcript.ContentDigest {
		t.Fatalf("index authority = %#v", index.Source)
	}
	if len(index.Sections) != 9 {
		t.Fatalf("sections = %d, want 9: %#v", len(index.Sections), index.Sections)
	}
	for sectionIndex, section := range index.Sections {
		if section.ID == "" || section.Order != sectionIndex+1 || section.StartMs >= section.EndMs || len(section.EvidenceRanges) != 1 || len(section.FrameAnchors) != 3 {
			t.Fatalf("section %d = %#v", sectionIndex, section)
		}
		if sectionIndex > 0 && section.StartMs != index.Sections[sectionIndex-1].EndMs {
			t.Fatalf("section gap at %d", sectionIndex)
		}
	}
	if index.Sections[0].StartMs != 0 || index.Sections[len(index.Sections)-1].EndMs != 184_000 {
		t.Fatalf("section coverage = %d..%d", index.Sections[0].StartMs, index.Sections[len(index.Sections)-1].EndMs)
	}
	if len(manifest.Cuts) != 8 || manifest.FailureMode != "require_boundary_verification" {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestVideoSpliceManifestFailsClosedForSpeechBoundary(t *testing.T) {
	// The section index accepts provider transcripts only after chronological
	// validation, so exercise splice safety with explicit crossing evidence.
	source := VideoIndexSource{DurationMs: 40_000}
	sections := []VideoSection{{ID: "sec_0001", EndMs: 20_000, Confidence: VideoSectionConfidence{EndBoundary: 0.95}}, {ID: "sec_0002", StartMs: 20_000, EndMs: 40_000}}
	evidence := []VideoEvidence{{StartMs: 19_000, EndMs: 21_000, Modality: "speech", Text: "continuous sentence"}}
	manifest := buildVideoSpliceManifest(source, sections, evidence, []boundaryCandidate{{at: 20_000, confidence: 0.95, reasons: []string{"visual_change"}}})
	if manifest.Automatic || len(manifest.Cuts) != 1 || manifest.Cuts[0].Status != "verification_required" || manifest.Cuts[0].Confidence > 0.54 {
		t.Fatalf("manifest did not fail closed: %#v", manifest)
	}
	if manifest.Cuts[0].ExactCutMethod != "reencode_exact" || len(manifest.Cuts[0].FrameAnchors) != 3 {
		t.Fatalf("cut contract = %#v", manifest.Cuts[0])
	}
}

func launchBenchmarkTranscript() pebblestore.NormalizedTranscript {
	segments := make([]pebblestore.NormalizedTranscriptSegment, 0, 19)
	for index := 0; index < 9; index++ {
		start := int64(index) * 20_000
		end := start + 20_000
		if index == 8 {
			end = 184_000
		}
		segments = append(segments,
			pebblestore.NormalizedTranscriptSegment{StartMs: start, EndMs: start + 10_000, Speech: "Founder explains stage " + benchmarkWord(index), Visual: "Workspace shows stage " + benchmarkWord(index), OnScreenText: "Stage " + benchmarkWord(index)},
			pebblestore.NormalizedTranscriptSegment{StartMs: start + 10_000, EndMs: end, Speech: "Founder explains stage " + benchmarkWord(index), Visual: "Workspace shows stage " + benchmarkWord(index), OnScreenText: "Stage " + benchmarkWord(index)},
		)
	}
	return pebblestore.NormalizedTranscript{
		SchemaVersion: pebblestore.NormalizedTranscriptSchemaVersion,
		Ref:           "transcript_benchmark", SourceFingerprint: "fingerprint_benchmark", ContentDigest: "digest_benchmark",
		Segments: segments, Metadata: pebblestore.NormalizedTranscriptMetadata{DurationMs: 184_000},
		Validation: pebblestore.TranscriptValidation{State: pebblestore.TranscriptValidationValidated, ValidatedAt: 1},
	}
}

func benchmarkWord(index int) string {
	return []string{"introduction", "setup", "agents", "planning", "coding", "review", "media", "automation", "launch"}[index]
}
