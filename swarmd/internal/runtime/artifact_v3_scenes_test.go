package runtime

import (
	"reflect"
	"strings"
	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: native chapters must use one playhead with exact scene/time
// acknowledgement, shared real output selectors and immutable source bytes.
// This adapter-layer test proves capture requests, not executed browser pixels.
func TestArtifactV3SceneCaptureContract(t *testing.T) {
	profile, _ := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: "motion_ui"})
	m := pebblestore.ArtifactV3Manifest{Entrypoint: "index.html", AnimationProfile: profile}
	for _, s := range []pebblestore.ArtifactV3TemporalScene{{SceneID: "opening", StartMS: 0, EndMS: 4000}, {SceneID: "resolve", StartMS: 4000, EndMS: 8000}} {
		s := s
		m.Parts = append(m.Parts, pebblestore.ArtifactV3Part{ID: s.SceneID, Temporal: &s, Locator: pebblestore.ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#canvas"}})
	}
	files := map[string][]byte{"index.html": []byte("<canvas id='canvas'></canvas>" + nativePreviewAnimationManifest)}
	before := cloneArtifactProject(files)
	r, err := artifactV3PreviewCaptureRequest(m, files)
	if err != nil {
		t.Fatal(err)
	}
	if !r.TemporalStates || !reflect.DeepEqual(r.StateIDs, []string{"opening", "resolve"}) || len(r.RequiredSelectors) != 0 {
		t.Fatalf("wrong routing: %+v", r)
	}
	for _, id := range r.StateIDs {
		if !reflect.DeepEqual(r.StateRequiredSelectors[id], []string{"#canvas"}) {
			t.Fatal("shared canvas lost")
		}
	}
	for _, s := range []string{`"opening":2000`, `"resolve":6000`, `ack.scene_id!==scenes[id]`, `ack.time_ms!==times[id]`} {
		if !strings.Contains(string(r.Files["index.html"]), s) {
			t.Fatal("missing deterministic contract", s)
		}
	}
	m.Parts[1].Temporal.EndMS = 9000
	if _, err := artifactV3PreviewCaptureRequest(m, files); err == nil {
		t.Fatal("accepted duration mismatch")
	}
	if !reflect.DeepEqual(before, files) {
		t.Fatal("source mutated")
	}
}
