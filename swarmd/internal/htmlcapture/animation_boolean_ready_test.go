package htmlcapture

import (
	"context"
	"testing"
)

// Native V3 permits boolean readiness because timing is server-owned. Legacy
// animation imports retain exact metadata matching, and false never means ready.
func TestAnimationBooleanReadyIsNativeOptIn(t *testing.T) {
	renderer := requireAnimationRuntime(t)
	for _, tc := range []struct {
		name, ack   string
		allow, pass bool
	}{
		{"legacy-boolean", "true", false, false}, {"native-boolean", "true", true, true}, {"false", "false", true, false}, {"wrong-metadata", "({duration_ms:999,fps:30})", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`<html><head><style>html,body{margin:0;overflow:hidden;background:#080b10}</style></head><body><script>globalThis.__SWARM_ANIMATION_V1__={version:'swarm.animation/v1',ready:()=>` + tc.ack + `,seek:t=>{document.documentElement.dataset.swarmAnimationTimeMs=String(t);return {time_ms:t}}}</script></body></html>`)
			_, err := renderer.PreflightAnimation(context.Background(), AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": body}, DurationMS: 1000, FPS: 30, AllowBooleanReady: tc.allow})
			if tc.pass {
				if err != nil {
					t.Fatal(err)
				}
			} else if e, ok := err.(*Error); !ok || e.Code != "animation_manifest_mismatch" {
				t.Fatalf("wrong rejection: %v", err)
			}
		})
	}
}
