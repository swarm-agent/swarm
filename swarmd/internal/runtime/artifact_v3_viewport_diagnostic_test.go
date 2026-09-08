package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifactv3video"
	"swarm/packages/swarmd/internal/htmlcapture"
)

type viewportFailureRenderer struct{}

func (viewportFailureRenderer) PreflightAnimation(context.Context, htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	timeMS := 0
	return htmlcapture.AnimationResult{Diagnostics: []htmlcapture.AnimationDiagnostic{{Stage: "viewport", Outcome: "bounds_overflow", TimestampMS: &timeMS, Selector: "* > *:nth-child(2)", Bounds: &htmlcapture.AnimationBounds{Left: -10, Top: 0, Right: 10, Bottom: 20}}}}, htmlcapture.NewError("animation_viewport_overflow", "animation document renders visible content outside the fixed viewport")
}
func (r viewportFailureRenderer) RenderAnimation(ctx context.Context, req htmlcapture.AnimationRequest) (htmlcapture.AnimationResult, error) {
	return r.PreflightAnimation(ctx, req)
}

// Requirement: native V3's error-only adapter must retain bounded selector/time/
// bounds evidence and errors.As identity, without returning any failed media or
// changing approved source. A fake renderer isolates this diagnostic-loss seam.
func TestArtifactV3ViewportDiagnosticPropagation(t *testing.T) {
	entry := []byte(`<!doctype html><html><body>unchanged</body></html>`)
	req := artifactv3video.RenderRequest{Project: artifactv3video.Project{Files: map[string][]byte{"swarm-artifact.json": []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[]}`), "index.html": entry}}, DurationMs: 400, FPS: 10, AnimationAdapter: htmlcapture.AnimationVersion}
	renderer := artifactV3AnimationRenderer{renderer: viewportFailureRenderer{}}
	preflightErr := renderer.Preflight(context.Background(), req)
	result, renderErr := renderer.Render(context.Background(), req)
	for _, err := range []error{preflightErr, renderErr} {
		var captureErr *htmlcapture.Error
		if !errors.As(err, &captureErr) || captureErr.Code != "animation_viewport_overflow" {
			t.Fatalf("lost typed error: %v", err)
		}
		for _, want := range []string{`selector="* > *:nth-child(2)"`, `time_ms=0`, `bounds=[-10.00,0.00,10.00,20.00]`} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("missing %q: %v", want, err)
			}
		}
	}
	if len(result.FallbackPNG) != 0 || len(result.SilentMP4) != 0 || string(req.Project.Files["index.html"]) != string(entry) {
		t.Fatal("failure returned media or mutated source")
	}
}
