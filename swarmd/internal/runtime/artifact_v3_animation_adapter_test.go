package runtime

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/htmlcapture"
)

// Native conversion must use an authored synchronous seek when present; the
// CSS-only fallback must bind before the bootstrap's missing-runtime deadline.
// Real browser preflight is the narrowest test of parser/event ordering and
// visible pixels. Invalid authored runtimes must not be hidden by a fallback.
func TestArtifactV3AnimationAdapterPreservesAuthoredSeek(t *testing.T) {
	_, err := os.Stat(htmlcapture.SystemChromePath)
	if err != nil {
		t.Skip("Chrome required for adapter browser test")
	}
	renderer := htmlcapture.NewChromedpRenderer(htmlcapture.SystemChromePath, t.TempDir())
	for _, tc := range []struct {
		name, script, style string
		fail                bool
	}{
		{"authored", `<script>globalThis.__SWARM_ANIMATION_V1__={version:"swarm.animation/v1",ready:()=>true,seek:t=>{document.getElementById('box').style.transform='translateX('+t/4+'px)';document.documentElement.dataset.swarmAnimationTimeMs=String(t);return {time_ms:t}}}</script>`, "", false},
		{"css-only", "", `#box{animation:move 1s linear both}@keyframes move{to{transform:translateX(250px)}}`, false},
		{"invalid-authored", `<script>globalThis.__SWARM_ANIMATION_V1__={version:"invalid"}</script>`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := []byte(`<html><head><style>html,body{margin:0;overflow:hidden;background:#080b10}#box{position:absolute;left:50px;top:100px;width:200px;height:200px;background:#87ceeb}` + tc.style + `</style></head><body><div id="box"></div>` + tc.script + `</body></html>`)
			original := append([]byte(nil), source...)
			injected := injectArtifactV3AnimationAdapter(source, 1000, 30)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := renderer.PreflightAnimation(ctx, htmlcapture.AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": injected}, DurationMS: 1000, FPS: 30, AllowBooleanReady: true})
			if tc.fail {
				if err == nil {
					t.Fatal("invalid authored runtime replaced by fallback")
				}
				if captureErr, ok := err.(*htmlcapture.Error); !ok || captureErr.Code != "animation_not_ready" {
					t.Fatalf("wrong rejection: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result.InspectionFrames) < 2 || bytes.Equal(result.InspectionFrames[0].PNG, result.InspectionFrames[1].PNG) {
				t.Fatal("authored/CSS motion lost")
			}
			if !bytes.Equal(source, original) {
				t.Fatal("source mutated")
			}
		})
	}
}
