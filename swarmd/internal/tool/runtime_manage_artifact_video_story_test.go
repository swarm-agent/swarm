package tool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/audiogen"
	"swarm/packages/swarmd/internal/videogen"
)

func TestManageArtifactGenerateVideoStoryValidation(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)

	ctx, scope := artifactToolContext()

	// Missing scenes
	call1 := Call{
		CallID:    "story-val-1",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_video_story"}`,
	}
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call1)
	if err == nil || !strings.Contains(err.Error(), "requires 'scenes' or 'parts' array") {
		t.Fatalf("expected missing scenes error, got: %v", err)
	}

	// Fewer than 2 scenes
	call2 := Call{
		CallID:    "story-val-2",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_video_story","scenes":[{"prompt":"Scene 1"}]}`,
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call2)
	if err == nil || !strings.Contains(err.Error(), "requires at least 2 scenes") {
		t.Fatalf("expected at least 2 scenes error, got: %v", err)
	}

	// Empty prompt in scene
	call3 := Call{
		CallID:    "story-val-3",
		Name:      "manage_artifact",
		Arguments: `{"action":"generate_video_story","scenes":[{"prompt":"Scene 1"},{"prompt":""}]}`,
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call3)
	if err == nil || !strings.Contains(err.Error(), "requires non-empty 'prompt'") {
		t.Fatalf("expected empty prompt error, got: %v", err)
	}
}

func TestManageArtifactGenerateVideoStoryEndToEndOneCall(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed in environment")
	}

	tmpDir := t.TempDir()

	// Create a test seed image (e.g. Swarm logo)
	logoPath := filepath.Join(tmpDir, "swarm-logo.png")
	cmdLogo := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=cyan:s=200x200:d=1",
		"-frames:v", "1", logoPath)
	if out, err := cmdLogo.CombinedOutput(); err != nil {
		t.Fatalf("create test logo failed: %v, out: %s", err, string(out))
	}

	// Create synthetic 1-second video bytes
	clip1Path := filepath.Join(tmpDir, "clip1.mp4")
	cmd1 := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x240:r=24:d=1.0",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=1.0",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", clip1Path)
	if out, err := cmd1.CombinedOutput(); err != nil {
		t.Fatalf("create test clip 1 failed: %v, out: %s", err, string(out))
	}
	clip1Bytes, err := os.ReadFile(clip1Path)
	if err != nil {
		t.Fatalf("read clip 1: %v", err)
	}

	audioPath := filepath.Join(tmpDir, "soundtrack.mp3")
	cmdAud := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=550:sample_rate=44100:duration=2.0",
		"-c:a", "libmp3lame", audioPath)
	if out, err := cmdAud.CombinedOutput(); err != nil {
		t.Fatalf("create test audio failed: %v, out: %s", err, string(out))
	}
	audioBytes, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatalf("read audio: %v", err)
	}

	runtime := NewRuntime(1)
	authority := newVideoChainFakeArtifactAuthority()

	// Video generation mock that returns synthetic clips
	videoService := &fakeVideoGenerationService{
		result: videogen.ManagedVideoResult{
			Bytes:         clip1Bytes,
			MediaType:     "video/mp4",
			Model:         "veo-3.1-generate-preview",
			Provider:      "google",
			InteractionID: "interaction-test",
		},
	}

	// Audio generation mock
	audioService := &fakeAudioGenerationService{
		result: audiogen.ManagedAudioResult{
			Bytes:      audioBytes,
			MediaType:  "audio/mp3",
			Model:      "lyria-3.5",
			Provider:   "google",
			DurationMs: 16000,
		},
	}

	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedVideoGenerationService(videoService)
	runtime.SetManagedAudioGenerationService(audioService)

	ctx, scope := artifactToolContext()
	scope.PrimaryPath = tmpDir
	storyCall := Call{
		CallID: "story-test-e2e",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{
			"action": "generate_video_story",
			"title": "Swarm Launch Thriller",
			"image_path": "%s",
			"aspect_ratio": "16:9",
			"soundtrack": {
				"prompt": "High-energy 174 BPM Drum & Bass thriller",
				"audio_mode": "mix_ducked",
				"foley_volume": 0.35
			},
			"scenes": [
				{"prompt": "Part 1: Swarm logo awakens with glowing cyan energy on the monolith"},
				{"prompt": "Part 2: The logo fractures into a swarm of micro-drones racing down the trench"}
			]
		}`, strings.ReplaceAll(logoPath, `\`, `/`)),
	}

	res, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, storyCall)
	if err != nil {
		t.Fatalf("generate_video_story failed: %v", err)
	}

	if !strings.Contains(res, `"status":"ok"`) {
		t.Fatalf("expected status ok, got: %s", res)
	}
	if !strings.Contains(res, `"parts_count":2`) && !strings.Contains(res, `"scenes_count":2`) {
		t.Fatalf("expected scenes_count 2 in result: %s", res)
	}
	if !strings.Contains(res, `"soundtrack_reference"`) {
		t.Fatalf("expected soundtrack_reference in result: %s", res)
	}
	if !strings.Contains(res, `"keyframe_reference"`) {
		t.Fatalf("expected keyframe_reference in result: %s", res)
	}

	// Verify video service was called twice (once for Part 1, once for Part 2)
	if videoService.calls != 2 {
		t.Fatalf("expected 2 video generations, got %d", videoService.calls)
	}

	// Verify audio service was called once for total story duration
	if audioService.calls != 1 {
		t.Fatalf("expected 1 audio generation, got %d", audioService.calls)
	}
}

func TestManageArtifactGenerateVideoAliasRoutesToStory(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed in environment")
	}

	tmpDir := t.TempDir()
	clipPath := filepath.Join(tmpDir, "clip.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=cyan:s=320x240:r=24:d=1.0",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=1.0",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", clipPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create test clip failed: %v, out: %s", err, string(out))
	}
	clipBytes, err := os.ReadFile(clipPath)
	if err != nil {
		t.Fatalf("read clip: %v", err)
	}

	runtime := NewRuntime(1)
	authority := newVideoChainFakeArtifactAuthority()

	videoService := &fakeVideoGenerationService{
		result: videogen.ManagedVideoResult{
			Bytes:     clipBytes,
			MediaType: "video/mp4",
			Model:     "veo-3.1-generate-preview",
			Provider:  "google",
		},
	}

	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedVideoGenerationService(videoService)

	ctx, scope := artifactToolContext()

	// Calling action: "generate_video" with "scenes" array routes to generateVideoStory
	aliasCall := Call{
		CallID: "alias-test",
		Name:   "manage_artifact",
		Arguments: `{
			"action": "generate_video",
			"title": "Two-Part Story",
			"scenes": [
				{"prompt": "Scene A"},
				{"prompt": "Scene B"}
			]
		}`,
	}

	res, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, aliasCall)
	if err != nil {
		t.Fatalf("generate_video with scenes alias failed: %v", err)
	}
	if !strings.Contains(res, `"status":"ok"`) {
		t.Fatalf("expected status ok in alias call: %s", res)
	}
	if videoService.calls != 2 {
		t.Fatalf("expected 2 video generations, got %d", videoService.calls)
	}
}
