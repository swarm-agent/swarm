package runtime

import (
	"bytes"
	"fmt"
	"testing"

	"swarm/packages/swarmd/internal/artifactv3video"
	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: native conversion passes 85s/120s/15min at exactly 30 FPS through
// artifactV3AnimationRenderer.request without rewriting the selected Git bytes
// or injecting a replacement seek runtime. The request boundary is the narrowest
// proof of adapter fidelity; full-duration browser encoding is a separate proof.
func TestArtifactV3LongDurationRequest(t *testing.T) {
	renderer := artifactV3AnimationRenderer{renderer: htmlcapture.NewChromedpRenderer(htmlcapture.SystemChromePath, t.TempDir())}
	for _, duration := range []int64{85000, 120000, 900000} {
		body := []byte(fmt.Sprintf(`<script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":%d,"fps":30}</script><script>globalThis.__SWARM_ANIMATION_V1__={version:'swarm.animation/v1',ready:()=>true,seek:ms=>({time_ms:ms})}</script>`, duration))
		original := append([]byte(nil), body...)
		input := artifactv3video.RenderRequest{Project: artifactv3video.Project{Files: map[string][]byte{pebblestore.ArtifactV3ManifestFilename: []byte(`{"entrypoint":"index.html","animation_profile":{}}`), "index.html": body}}, DurationMs: duration, FPS: 30, AnimationAdapter: htmlcapture.AnimationVersion}
		req, err := renderer.request(input)
		if err != nil || int64(req.DurationMS) != duration || req.FPS != 30 || req.OutputFPS != 30 || !bytes.Equal(req.Files["index.html"], original) || !bytes.Equal(body, original) {
			t.Fatalf("native request drift: %v", err)
		}
		input.DurationMs--
		if _, err := renderer.request(input); err == nil {
			t.Fatal("timing conflict accepted")
		}
	}
}
