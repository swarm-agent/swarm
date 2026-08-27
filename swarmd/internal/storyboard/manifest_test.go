package storyboard

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/videocomposition"
)

func TestParseHTMLPreservesOrderedCanonicalSections(t *testing.T) {
	html := []byte(`<script id="swarm-storyboard-manifest" type="application/json">{"version":"swarm.storyboard/v1","sections":[{"id":"opening","capture_state_id":"opening-still","title":"Opening","duration_ms":2500,"narration":"Meet Swarm.","on_screen_text":"Local-first","creative_direction":"Slow push toward the workstation.","filming_requirements":["Locked 35mm camera","No screen reflections"],"production_state":"pending"},{"id":"proof","capture_state_id":"proof-still","title":"Proof","duration_ms":3000,"creative_direction":"Over-shoulder product proof.","filming_requirements":["Readable screen"],"production_state":"ready"}]}</script>`)
	manifest, err := ParseHTML(html, []string{"opening-still", "proof-still"})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Sections) != 2 || manifest.Sections[0].ID != "opening" || manifest.Sections[1].CaptureStateID != "proof-still" || manifest.Sections[0].ProductionState != ProductionStatePending {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestParseHTMLPreservesOptionalCompositionContract(t *testing.T) {
	html := []byte(`<script id="swarm-storyboard-manifest" type="application/json">{"version":"swarm.storyboard/v1","compositions":{"schema_version":1,"layouts":[{"id":"phone-grid","slots":[{"id":"phone-a","requirement":"Portrait product capture","geometry":{"x":0.1,"y":0.1,"width":0.3,"height":0.8},"z_index":1,"fit":"cover","alignment_x":0.5,"alignment_y":0.5,"mask":{"kind":"rounded_rect","radius":0.05},"aspect_lock":0.5625}]}]},"sections":[{"id":"opening","capture_state_id":"opening-still","title":"Opening","duration_ms":2500,"creative_direction":"Product hero.","filming_requirements":["Capture portrait video"],"production_state":"pending","composition":{"layout_id":"phone-grid"}}]}</script>`)
	manifest, err := ParseHTML(html, []string{"opening-still"})
	if err != nil { t.Fatal(err) }
	if manifest.Compositions == nil || manifest.Sections[0].Composition == nil || manifest.Sections[0].Composition.LayoutID != "phone-grid" { t.Fatalf("manifest=%#v", manifest) }
	resolved, err := videocomposition.Resolve(manifest.Compositions, manifest.Sections[0].Composition, 1920, 1080, manifest.Sections[0].DurationMs)
	if err != nil || len(resolved) != 1 || resolved[0].Pixels.Width%2 != 0 { t.Fatalf("resolved=%#v err=%v", resolved, err) }
}

func TestParseHTMLRejectsInvalidComposition(t *testing.T) {
	html := []byte(`<script id="swarm-storyboard-manifest" type="application/json">{"version":"swarm.storyboard/v1","compositions":{"schema_version":1,"layouts":[{"id":"bad","extends_layout_id":"bad","slots":[]}]},"sections":[{"id":"opening","capture_state_id":"opening-still","title":"Opening","duration_ms":2500,"creative_direction":"Product hero.","filming_requirements":["Capture"],"production_state":"pending","composition":{"layout_id":"bad"}}]}</script>`)
	_, err := ParseHTML(html, []string{"opening-still"})
	if err == nil || !strings.Contains(err.Error(), "storyboard_composition_invalid") { t.Fatalf("err=%v", err) }
}

func TestParseHTMLRejectsMalformedDuplicateAndMissingState(t *testing.T) {
	valid := `{"version":"swarm.storyboard/v1","sections":[{"id":"opening","capture_state_id":"opening-still","title":"Opening","duration_ms":2500,"creative_direction":"Product hero.","filming_requirements":["Locked camera"],"production_state":"pending"}]}`
	cases := []struct {
		name string
		html string
		code string
	}{
		{"malformed", `<script id="swarm-storyboard-manifest" type="application/json">{</script>`, "storyboard_manifest_invalid"},
		{"duplicate section", `<script id="swarm-storyboard-manifest" type="application/json">{"version":"swarm.storyboard/v1","sections":[{"id":"opening","capture_state_id":"opening-still","title":"Opening","duration_ms":1,"creative_direction":"A","filming_requirements":["A"],"production_state":"pending"},{"id":"opening","capture_state_id":"other","title":"Other","duration_ms":1,"creative_direction":"A","filming_requirements":["A"],"production_state":"pending"}]}</script>`, "storyboard_section_duplicate"},
		{"missing state", `<script id="swarm-storyboard-manifest" type="application/json">` + valid + `</script>`, "storyboard_capture_state_missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureStates := []string{"different"}
			if tc.name == "duplicate section" {
				captureStates = []string{"opening-still", "other"}
			}
			_, err := ParseHTML([]byte(tc.html), captureStates)
			if err == nil || !strings.Contains(err.Error(), tc.code) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
