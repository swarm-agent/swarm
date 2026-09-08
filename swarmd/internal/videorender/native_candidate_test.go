package videorender

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: pending-revision inspection must retain the exact native V3 MP4
// selected by canonical conversion. Threat: treating V3 as legacy HTML rejects
// valid output, while a blind bypass could accept mixed/stale candidate identity.
// applySelectedHTMLAnimationSources is the narrowest shared decoder boundary;
// all rejection cases must leave the input timeline unchanged.
func TestApplySelectedNativeV3AnimationPreservesExactDerivative(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong-candidate", "mixed", "wrong-clip", "timing"} {
		t.Run(scenario, func(t *testing.T) {
			mp4 := testRendererArtifactV3Reference()
			source := mp4
			source.DerivativeID, source.MediaType, source.DigestSHA256 = "", "text/html", source.ManifestDigestSHA256
			still := mp4
			still.MediaType, still.DerivativeID, still.DigestSHA256 = "image/png", "av3der_"+strings.Repeat("e", 64), strings.Repeat("e", 64)
			candidateSource := source
			set := &pebblestore.VideoAnimationCandidateSet{Status: pebblestore.VideoAnimationCandidateStatusReady, SelectedCandidateID: "chosen", Candidates: []pebblestore.VideoAnimationCandidate{{ID: "chosen", V3Source: &candidateSource}}, V3SelectedSource: &source, V3Derivative: &mp4}
			part := pebblestore.VideoPlanPart{ID: "motion", Title: "Motion", DurationMs: 2000, SourceEndMs: 2000, VisualMediaType: "video/mp4", ProductionState: pebblestore.VideoProductionStateReady, FilmingRequirements: []string{"Preserve exact source"}, ArtifactV3Source: &source, ArtifactV3Still: &still, ArtifactV3Visual: &mp4, AnimationCandidates: set}
			clipRef := mp4
			clip := pebblestore.VideoTimelineClip{ID: "motion", SourceKind: pebblestore.VideoClipSourceKindManagedArtifact, ArtifactV3Ref: &clipRef, MediaType: "video/mp4", SourceEndMs: 2000, DurationMs: 2000, TimelineEndMs: 2000, Visible: true}
			switch scenario {
			case "wrong-candidate":
				candidateSource.RevisionID = "revision-" + strings.Repeat("f", 40)
			case "mixed":
				set.SelectedSource = &pebblestore.SessionArtifactSelectionReference{SessionID: "legacy"}
			case "wrong-clip":
				clipRef.DigestSHA256 = strings.Repeat("f", 64)
			case "timing":
				clip.SourceEndMs = 1000
			}
			timeline := pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{clip}, Metadata: map[string]any{"accepted_video_plan": pebblestore.VideoPlanProposal{Kind: pebblestore.VideoPlanKindInitial, Parts: []pebblestore.VideoPlanPart{part}}}}
			before, _ := json.Marshal(timeline)
			err := applySelectedHTMLAnimationSources(&timeline)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("unexpected result: %v", err)
			}
			after, _ := json.Marshal(timeline)
			if string(before) != string(after) {
				t.Fatal("candidate resolution mutated timeline")
			}
		})
	}
}
