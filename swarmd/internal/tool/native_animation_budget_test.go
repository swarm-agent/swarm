package tool

import (
	"fmt"
	"strings"
	"testing"
)

// Requirement: ArtifactHTMLAnimationDurationMS and the tool manifest parser use
// renderer frame admission, not a two-minute ceiling. This pure boundary test
// prevents ready native sources from later failing solely because of duration.
func TestNativeAnimationFrameAdmission(t *testing.T) {
	for _, tc := range []struct {
		duration, fps int
		valid         bool
	}{
		{900000, 30, true}, {1200000, 30, true}, {900000, 60, false}, {1200001, 30, false},
	} {
		body := []byte(fmt.Sprintf(`<script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":%d,"fps":%d}</script>`, tc.duration, tc.fps))
		duration, err := ArtifactHTMLAnimationDurationMS(body)
		if tc.valid {
			if err != nil || duration != int64(tc.duration) {
				t.Fatalf("admission: %d %v", duration, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "36000") {
			t.Fatalf("missing actionable frame rejection: %v", err)
		}
	}
}
