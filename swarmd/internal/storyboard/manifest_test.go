package storyboard

import (
	"strings"
	"testing"
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
