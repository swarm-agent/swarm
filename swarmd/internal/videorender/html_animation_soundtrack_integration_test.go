package videorender

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestDeterministicHTMLAnimationRendersWithSoundtrack(t *testing.T) {
	if _, err := os.Stat(htmlcapture.SystemChromePath); err != nil {
		t.Skipf("system-managed Chrome unavailable: %v", err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("requires local ffmpeg runtime")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("requires local ffprobe runtime")
	}

	dir := t.TempDir()
	html := []byte(`<!doctype html><html><head><meta charset="utf-8">
<script>globalThis.__SWARM_ANIMATION_V1__={version:"swarm.animation/v1",ready(){return {duration_ms:600,fps:10}},seek(timeMs){document.documentElement.dataset.swarmAnimationTimeMs=String(timeMs);const p=Math.min(1,timeMs/500);document.getElementById("disc").style.transform="translateX("+(p*1440)+"px) rotate("+(p*720)+"deg)";return {time_ms:timeMs}}};</script>
<style>html,body{width:100%;height:100%;margin:0;overflow:hidden;background:#08111f}#disc{width:320px;height:320px;border-radius:50%;margin:380px 80px;background:conic-gradient(#7dd3fc,#c084fc,#7dd3fc)}</style></head><body><div id="disc"></div></body></html>`)
	renderer := htmlcapture.NewChromedpRenderer(htmlcapture.SystemChromePath, filepath.Join(dir, "capture-cache"))
	animation, err := renderer.RenderAnimation(context.Background(), htmlcapture.AnimationRequest{Entry: "index.html", Files: map[string][]byte{"index.html": html}, DurationMS: 600, FPS: 10})
	if err != nil {
		t.Fatalf("RenderAnimation: %v; cause: %v", err, errors.Unwrap(err))
	}
	videoPath := filepath.Join(dir, "animation.mp4")
	if err := os.WriteFile(videoPath, animation.MP4, 0o600); err != nil {
		t.Fatal(err)
	}
	audioPath := filepath.Join(dir, "soundtrack.wav")
	if output, commandErr := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=0.6", "-c:a", "pcm_s16le", audioPath).CombinedOutput(); commandErr != nil {
		t.Fatalf("soundtrack fixture failed: %v: %s", commandErr, output)
	}
	outputPath := filepath.Join(dir, "dogfood-animation-soundtrack.mp4")
	plan, err := BuildFFmpegCommandLine(pebblestore.VideoProjectTimeline{Width: 1920, Height: 1080, FPS: 10, TotalDurationMs: 600}, []MaterializedInput{
		{Index: 0, ClipID: "animation", FilePath: videoPath, IsVideo: true, DurationMs: 600, TimelineEndMs: 600},
		{Index: 1, ClipID: "soundtrack", FilePath: audioPath, IsAudio: true, HasAudio: true, DurationMs: 600, EndMs: 600, TimelineEndMs: 600, Volume: 0.5},
	}, outputPath)
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine: %v", err)
	}
	if output, commandErr := exec.Command(ffmpeg, plan.FFmpegArgs...).CombinedOutput(); commandErr != nil {
		t.Fatalf("animation + soundtrack render failed: %v: %s\nfilter=%s", commandErr, output, plan.FilterComplex)
	}
	probeOutput, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", outputPath).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe output: %v: %s", err, probeOutput)
	}
	streams := string(probeOutput)
	if !strings.Contains(streams, "video") || !strings.Contains(streams, "audio") {
		t.Fatalf("dogfood output streams = %q, want video and audio", streams)
	}
}
