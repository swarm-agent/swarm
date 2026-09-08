package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/artifactv3video"
	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
		{"authored", `<script>globalThis.__SWARM_ANIMATION_V1__={version:"swarm.animation/v1",ready:()=>true,seek:t=>{document.getElementById('box').style.transform='translateX('+t/4+'px)';return {time_ms:t}}}</script>`, "", false},
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

// Requirement: request must preserve declared source bytes and exact timing,
// withholding the CSS fallback while retaining boolean-ready compatibility.
// Timing-bearing acknowledgements must still match the declaration.
// This adapter-level test isolates request construction before browser/publication.
func TestArtifactV3AnimationRequestDeclaredTiming(t *testing.T) {
	renderer := artifactV3AnimationRenderer{renderer: htmlcapture.NewChromedpRenderer(htmlcapture.SystemChromePath, t.TempDir())}
	body := []byte(`<script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":8000,"fps":60}</script>`)
	input := artifactv3video.RenderRequest{Project: artifactv3video.Project{Files: map[string][]byte{pebblestore.ArtifactV3ManifestFilename: []byte(`{"entrypoint":"index.html"}`), "index.html": body}}, DurationMs: 8000, FPS: 60, AnimationAdapter: htmlcapture.AnimationVersion}
	request, err := renderer.request(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(request.Files["index.html"], body) || request.DurationMS != 8000 || request.FPS != 60 || request.OutputFPS != 60 || !request.AllowBooleanReady {
		t.Fatalf("declared contract drift: %#v", request)
	}
	input.FPS = 30
	if _, err := renderer.request(input); err == nil {
		t.Fatal("conflicting timing accepted")
	}
	input.FPS = 60
	input.Project.Files["index.html"] = []byte(`<script id="swarm-animation-manifest" type="application/json">{}</script>`)
	if _, err := renderer.request(input); err == nil {
		t.Fatal("invalid declaration received fallback")
	}
	input.Project.Files["index.html"] = []byte(`<html><head></head><body></body></html>`)
	request, err = renderer.request(input)
	if err != nil || !request.AllowBooleanReady || !bytes.Contains(request.Files["index.html"], []byte("data-swarm-artifact-v3-animation")) {
		t.Fatalf("legacy fallback lost: %v", err)
	}
}

// Requirement: the native renderer must accept historical boolean readiness with
// authenticated manifest timing, without masking contradictory metadata, bad seek
// acknowledgements, or viewport overflow. Exercise request -> real browser
// preflight: a request-only fake cannot prove the readiness/capture boundary.
func TestArtifactV3DeclaredBooleanReadyPreflight(t *testing.T) {
	if _, err := os.Stat(htmlcapture.SystemChromePath); err != nil {
		t.Skip("Chrome required for declared readiness browser test")
	}
	renderer := artifactV3AnimationRenderer{renderer: htmlcapture.NewChromedpRenderer(htmlcapture.SystemChromePath, t.TempDir())}
	for _, tc := range []struct{ name, ack, seek, style, code string }{
		{"boolean", "true", "t", "", ""},
		{"matching", "({duration_ms:8000,fps:60})", "t", "", ""},
		{"wrong-duration", "({duration_ms:4000,fps:60})", "t", "", "animation_manifest_mismatch"},
		{"wrong-fps", "({duration_ms:8000,fps:30})", "t", "", "animation_manifest_mismatch"},
		{"false", "false", "t", "", "animation_manifest_mismatch"},
		{"incomplete", "({duration_ms:8000})", "t", "", "animation_manifest_mismatch"},
		{"bad-seek", "true", "-1", "", "animation_seek_ack_mismatch"},
		{"overflow", "true", "t", "#box{left:1900px!important}", "animation_viewport_overflow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`<!doctype html><html><head><style>html,body{margin:0;overflow:hidden;background:#080b10}#box{position:absolute;left:50px;top:100px;width:200px;height:200px;background:#87ceeb}` + tc.style + `</style><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":8000,"fps":60}</script></head><body><main id="box"></main><script>let raf=0;const render=t=>{document.getElementById('box').style.transform='translateY('+t/100+'px)'};const tick=t=>{render(t%8000);raf=requestAnimationFrame(tick)};raf=requestAnimationFrame(tick);globalThis.__SWARM_ANIMATION_V1__={version:'swarm.animation/v1',ready:()=>` + tc.ack + `,seek:t=>{cancelAnimationFrame(raf);render(t);return {time_ms:` + tc.seek + `}}}</script></body></html>`)
			original := append([]byte(nil), body...)
			input := artifactv3video.RenderRequest{Project: artifactv3video.Project{Files: map[string][]byte{pebblestore.ArtifactV3ManifestFilename: []byte(`{"entrypoint":"index.html"}`), "index.html": body}}, DurationMs: 8000, FPS: 60, AnimationAdapter: htmlcapture.AnimationVersion}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := renderer.Preflight(ctx, input)
			if !bytes.Equal(body, original) {
				t.Fatal("source mutated")
			}
			if tc.code != "" {
				var captureErr *htmlcapture.Error
				if !errors.As(err, &captureErr) || captureErr.Code != tc.code {
					t.Fatalf("want %s, got %v", tc.code, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
