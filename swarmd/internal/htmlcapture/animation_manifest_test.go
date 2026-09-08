package htmlcapture

import "testing"

// Requirement: AnimationTiming must distinguish absent declarations from invalid
// ones, including HTML syntax variants. Tokenizer-level tests prevent fallback
// on malformed/duplicate declarations without requiring a browser or source edits.
func TestAnimationTimingDeclaration(t *testing.T) {
	const value = `{"version":"swarm.animation/v1","duration_ms":8000,"fps":60}`
	for _, tc := range []struct {
		name, body    string
		present, fail bool
	}{
		{"absent", `<html></html>`, false, false},
		{"comment", `<!-- <script id="swarm-animation-manifest">bad</script> -->`, false, false},
		{"unquoted", `<SCRIPT id=swarm-animation-manifest type=application/json>` + value + `</SCRIPT>`, true, false},
		{"bad-type", `<script id="swarm-animation-manifest">` + value + `</script>`, true, true},
		{"bad-version", `<script id="swarm-animation-manifest" type="application/json">{"version":"bad","duration_ms":8000,"fps":60}</script>`, true, true},
		{"trailing", `<script id="swarm-animation-manifest" type="application/json">` + value + `{}</script>`, true, true},
		{"fractional", `<script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":8000,"fps":59.5}</script>`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, f, p, err := AnimationTiming([]byte(tc.body))
			if p != tc.present || (err != nil) != tc.fail {
				t.Fatalf("got %d %d %v %v", d, f, p, err)
			}
			if p && !tc.fail && (d != 8000 || f != 60) {
				t.Fatal("timing lost")
			}
		})
	}
}
