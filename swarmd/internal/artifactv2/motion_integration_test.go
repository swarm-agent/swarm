package artifactv2

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/htmlcapture"
)

// Requirement: the V2 server-owned template must actually satisfy the audited
// Chrome runtime contract and produce representative rendered evidence.
// Threat: static/template tests could pass while binder, playback, seek, stage,
// or pixel stability fails in the real browser primitive.
func TestMotionCompilerTrustedChromePreflight(t *testing.T) {
	if os.Getenv("SWARM_ARTIFACT_V2_CHROME_TEST") != "1" {
		t.Skip("set SWARM_ARTIFACT_V2_CHROME_TEST=1 for trusted Chrome integration proof")
	}
	product, err := (MotionCompiler{}).Compile(context.Background(), motionCompileTestInput(t))
	if err != nil {
		t.Fatal(err)
	}
	renderer := TrustedMotionRenderer{Renderer: htmlcapture.NewChromedpRenderer(htmlcapture.SystemChromePath, filepath.Join(t.TempDir(), "capture"))}
	result, err := renderer.Preflight(context.Background(), product.Bytes, product.DurationMS, product.FPS)
	if err != nil {
		t.Fatalf("trusted Chrome preflight: %v diagnostics=%+v", err, result.Diagnostics)
	}
	if len(result.PreviewPNG) == 0 || len(result.Frames) < 3 {
		t.Fatalf("trusted Chrome returned incomplete rendered evidence: preview=%d frames=%d", len(result.PreviewPNG), len(result.Frames))
	}
}
