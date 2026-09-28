package videogen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Requirement: ordered multi-scene assembly must produce a decodable cut with
// measured total duration, not pretend to be a provider-retained last scene.
// Boundary: AssembleClips using the real local ffmpeg/ffprobe pipeline. Tiny
// generated color clips are encoder fixtures, never claimed as AI outputs.
func TestAssembleClips(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatal("ffmpeg required for assembly test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var clips []ManagedVideoResult
	for _, color := range []string{"red", "blue"} {
		path := filepath.Join(t.TempDir(), "fixture.mp4")
		cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "color=c="+color+":s=64x64:r=24", "-t", "1", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		clips = append(clips, ManagedVideoResult{Bytes: data, MediaType: "video/mp4", InteractionID: "must-not-retain", ProviderResource: "must-not-retain"})
	}
	result, err := AssembleClips(ctx, clips)
	if err != nil {
		t.Fatal(err)
	}
	if result.DurationMs < 1950 || result.DurationMs > 2200 || result.Width != 64 || result.Height != 64 {
		t.Fatalf("incorrect measured assembly: duration=%d %dx%d", result.DurationMs, result.Width, result.Height)
	}
	if result.InteractionID != "" || result.ProviderResource != "" || result.Provenance != nil {
		t.Fatal("assembly fabricated native provider lineage")
	}
	for i, want := range []string{"red", "blue"} {
		path := filepath.Join(t.TempDir(), "out.mp4")
		if err := os.WriteFile(path, result.Bytes, 0600); err != nil {
			t.Fatal(err)
		}
		at := "0.5"
		if i == 1 {
			at = "1.5"
		}
		cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-threads", "1", "-ss", at, "-i", path, "-frames:v", "1", "-vf", "scale=1:1", "-threads", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
		pixel, err := cmd.Output()
		if err != nil || len(pixel) != 3 {
			t.Fatalf("decode: %v bytes=%d", err, len(pixel))
		}
		if want == "red" && pixel[0] < 200 || want == "blue" && pixel[2] < 200 {
			t.Fatalf("scene order at %s: %v", at, pixel)
		}
	}
	if _, err := AssembleClips(ctx, clips[:1]); err == nil {
		t.Fatal("accepted one-clip assembly")
	}
	clips[1].Bytes = []byte("malformed")
	if result, err := AssembleClips(ctx, clips); err == nil || len(result.Bytes) != 0 {
		t.Fatal("published invalid partial assembly")
	}
}
