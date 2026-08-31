package videorender

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestFFmpegRenderMixedTimebasesAcrossCutThenCrossfade(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("requires local ffmpeg runtime")
	}

	dir := t.TempDir()
	inputPaths := []string{
		filepath.Join(dir, "microsecond.mp4"),
		filepath.Join(dir, "thirty-fps-a.mp4"),
		filepath.Join(dir, "thirty-fps-b.mp4"),
	}
	fixtureCommands := [][]string{
		{"-v", "error", "-f", "lavfi", "-i", "color=c=red:s=320x240:r=30:d=1.2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-video_track_timescale", "1000000", inputPaths[0]},
		{"-v", "error", "-f", "lavfi", "-i", "color=c=green:s=320x240:r=30:d=1.2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-video_track_timescale", "30", inputPaths[1]},
		{"-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x240:r=30:d=1.2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-video_track_timescale", "30", inputPaths[2]},
	}
	for _, command := range fixtureCommands {
		if output, commandErr := exec.Command(ffmpeg, command...).CombinedOutput(); commandErr != nil {
			t.Fatalf("fixture generation failed: %v: %s", commandErr, output)
		}
	}

	outputPath := filepath.Join(dir, "mixed-timebases.mp4")
	plan, err := BuildFFmpegCommandLine(pebblestore.VideoProjectTimeline{
		Width:  320,
		Height: 240,
		FPS:    30,
		Transitions: []pebblestore.VideoTimelineTransition{
			{ID: "cut", Kind: pebblestore.VideoTransitionKindCut, FromClipID: "one", ToClipID: "two"},
			{ID: "dissolve", Kind: pebblestore.VideoTransitionKindCrossfade, FromClipID: "two", ToClipID: "three", DurationMs: 200},
		},
	}, []MaterializedInput{
		{Index: 0, ClipID: "one", FilePath: inputPaths[0], IsVideo: true, DurationMs: 1200},
		{Index: 1, ClipID: "two", FilePath: inputPaths[1], IsVideo: true, DurationMs: 1200},
		{Index: 2, ClipID: "three", FilePath: inputPaths[2], IsVideo: true, DurationMs: 1200},
	}, outputPath)
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	if output, commandErr := exec.Command(ffmpeg, plan.FFmpegArgs...).CombinedOutput(); commandErr != nil {
		t.Fatalf("ffmpeg render failed for mixed timebases: %v: %s\nfilter=%s", commandErr, output, plan.FilterComplex)
	}
}

func TestFFmpegRenderPreservesDeclaredDurationAcrossCrossfade(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("requires local ffmpeg runtime")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("requires local ffprobe runtime")
	}

	dir := t.TempDir()
	inputPaths := []string{filepath.Join(dir, "one.png"), filepath.Join(dir, "two.png")}
	for index, color := range []string{"red", "blue"} {
		if output, commandErr := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c="+color+":s=320x240", "-frames:v", "1", inputPaths[index]).CombinedOutput(); commandErr != nil {
			t.Fatalf("fixture generation failed: %v: %s", commandErr, output)
		}
	}

	outputPath := filepath.Join(dir, "duration.mp4")
	plan, err := BuildFFmpegCommandLine(pebblestore.VideoProjectTimeline{
		Width: 320, Height: 240, FPS: 30, TotalDurationMs: 8000,
		Transitions: []pebblestore.VideoTimelineTransition{{
			ID: "dissolve", Kind: pebblestore.VideoTransitionKindCrossfade,
			FromClipID: "one", ToClipID: "two", DurationMs: 300,
		}},
	}, []MaterializedInput{
		{Index: 0, ClipID: "one", FilePath: inputPaths[0], IsImage: true, DurationMs: 4000, TimelineEndMs: 4000},
		{Index: 1, ClipID: "two", FilePath: inputPaths[1], IsImage: true, DurationMs: 4000, TimelineStartMs: 4000, TimelineEndMs: 8000},
	}, outputPath)
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	if output, commandErr := exec.Command(ffmpeg, plan.FFmpegArgs...).CombinedOutput(); commandErr != nil {
		t.Fatalf("ffmpeg render failed: %v: %s\nfilter=%s", commandErr, output, plan.FilterComplex)
	}

	probe, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", outputPath).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe failed: %v: %s", err, probe)
	}
	if got := strings.TrimSpace(string(probe)); got != "8.000000" {
		t.Fatalf("rendered duration = %s, want 8.000000", got)
	}
	framePath := filepath.Join(dir, "near-end.png")
	seekTimestamp := inspectionSeekTimestamp(7999, plan.TotalDurationMs, plan.FPS)
	if output, commandErr := exec.Command(ffmpeg, "-v", "error", "-ss", fmt.Sprintf("%.3f", float64(seekTimestamp)/1000), "-i", outputPath, "-frames:v", "1", "-f", "image2", "-c:v", "png", framePath).CombinedOutput(); commandErr != nil {
		t.Fatalf("near-end frame extraction failed: %v: %s", commandErr, output)
	}
	if info, statErr := os.Stat(framePath); statErr != nil || info.Size() == 0 {
		t.Fatalf("near-end frame was not produced: info=%v err=%v", info, statErr)
	}
}

func TestFFmpegRenderStillWithDeterministicSoundtrack(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("requires local ffmpeg runtime")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("requires local ffprobe runtime")
	}

	dir := t.TempDir()
	stillPath := filepath.Join(dir, "still.ppm")
	audioPath := filepath.Join(dir, "soundtrack.wav")
	outputPath := filepath.Join(dir, "output.mp4")
	for _, command := range [][]string{
		{"-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x240", "-frames:v", "1", stillPath},
		{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=2", "-c:a", "pcm_s16le", audioPath},
	} {
		if output, commandErr := exec.Command(ffmpeg, command...).CombinedOutput(); commandErr != nil {
			t.Fatalf("fixture generation failed: %v: %s", commandErr, output)
		}
	}

	timeline := pebblestore.VideoProjectTimeline{
		Width: 320, Height: 240, FPS: 10, TotalDurationMs: 3000,
		AudioPolicy: &pebblestore.VideoAudioPolicy{MasterVolume: 0.8},
	}
	plan, err := BuildFFmpegCommandLine(timeline, []MaterializedInput{
		{Index: 0, ClipID: "still", FilePath: stillPath, IsImage: true, DurationMs: 3000, TimelineEndMs: 3000},
		{Index: 1, ClipID: "soundtrack", FilePath: audioPath, IsAudio: true, HasAudio: true, StartMs: 250, EndMs: 1750, DurationMs: 1500, Track: 1, TimelineStartMs: 750, TimelineEndMs: 2250, Volume: 0.6},
	}, outputPath)
	if err != nil {
		t.Fatalf("BuildFFmpegCommandLine() error = %v", err)
	}
	if output, commandErr := exec.Command(ffmpeg, plan.FFmpegArgs...).CombinedOutput(); commandErr != nil {
		t.Fatalf("ffmpeg render failed: %v: %s\nfilter=%s", commandErr, output, plan.FilterComplex)
	}

	probe, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,codec_type", "-show_entries", "format=duration", "-of", "default=nw=1", outputPath).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe failed: %v: %s", err, probe)
	}
	got := string(probe)
	for _, want := range []string{"codec_name=h264", "codec_type=video", "codec_name=aac", "codec_type=audio", "duration=3.000000"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ffprobe output missing %q: %s", want, got)
		}
	}
}
