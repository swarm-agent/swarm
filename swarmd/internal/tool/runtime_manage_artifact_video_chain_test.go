package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

type videoChainFakeArtifactAuthority struct {
	fakeArtifactAuthority
	items map[string]struct {
		body    []byte
		variant pebblestore.SessionArtifactVariant
	}
	allCreated []artifact.CreateInput
}

func newVideoChainFakeArtifactAuthority() *videoChainFakeArtifactAuthority {
	return &videoChainFakeArtifactAuthority{
		items: make(map[string]struct {
			body    []byte
			variant pebblestore.SessionArtifactVariant
		}),
	}
}

func (f *videoChainFakeArtifactAuthority) addItem(ref pebblestore.SessionArtifactSelectionReference, body []byte, variant pebblestore.SessionArtifactVariant) {
	key := fmt.Sprintf("%s:%s:%s:%d", ref.SessionID, ref.CollectionID, ref.VariantID, ref.EventSeq)
	f.items[key] = struct {
		body    []byte
		variant pebblestore.SessionArtifactVariant
	}{
		body:    body,
		variant: variant,
	}
}

func (f *videoChainFakeArtifactAuthority) ReadReference(_ context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, _ int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	key := fmt.Sprintf("%s:%s:%s:%d", ref.SessionID, ref.CollectionID, ref.VariantID, ref.EventSeq)
	if item, ok := f.items[key]; ok {
		return append([]byte(nil), item.body...), item.variant, nil
	}
	return f.fakeArtifactAuthority.ReadReference(context.Background(), principal, ref, 0)
}

func (f *videoChainFakeArtifactAuthority) Create(ctx context.Context, principal artifact.Principal, input artifact.CreateInput) (pebblestore.SessionArtifactVariant, error) {
	f.allCreated = append(f.allCreated, input)
	variant, err := f.fakeArtifactAuthority.Create(ctx, principal, input)
	if err != nil {
		return variant, err
	}
	key := fmt.Sprintf("%s:%s:%s:%d", variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	f.items[key] = struct {
		body    []byte
		variant pebblestore.SessionArtifactVariant
	}{
		body:    input.Body,
		variant: variant,
	}
	return variant, nil
}

func TestManageArtifactExtractVideoFrameRequiresVideoSource(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID:    "extract-call-1",
		Name:      "manage_artifact",
		Arguments: `{"action":"extract_video_frame"}`,
	}
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err == nil || !strings.Contains(err.Error(), "extract_video_frame requires video artifact reference or video_path") {
		t.Fatalf("expected missing video source error, got: %v", err)
	}
}

func TestManageArtifactChainVideoRequiresAtLeastTwoVideos(t *testing.T) {
	runtime := NewRuntime(1)
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactAuthority(authority)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID:    "chain-call-1",
		Name:      "manage_artifact",
		Arguments: `{"action":"chain_video","videos":[]}`,
	}
	_, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err == nil || !strings.Contains(err.Error(), "chain_video requires at least 2 items") {
		t.Fatalf("expected at least 2 items error, got: %v", err)
	}

	callSingle := Call{
		CallID:    "chain-call-2",
		Name:      "manage_artifact",
		Arguments: `{"action":"chain_video","videos":[{"path":"part1.mp4"}]}`,
	}
	_, err = runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, callSingle)
	if err == nil || !strings.Contains(err.Error(), "chain_video requires at least 2 items") {
		t.Fatalf("expected at least 2 items error, got: %v", err)
	}
}

func TestManageArtifactVideoChainingRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed in environment")
	}

	tmpDir := t.TempDir()

	// Generate two synthetic test video files using ffmpeg (1 sec each, 320x240, 24fps, with audio)
	video1Path := filepath.Join(tmpDir, "clip1.mp4")
	video2Path := filepath.Join(tmpDir, "clip2.mp4")
	audioPath := filepath.Join(tmpDir, "soundtrack.mp3")

	cmd1 := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=320x240:r=24:d=1.0",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=1.0",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", video1Path)
	if out, err := cmd1.CombinedOutput(); err != nil {
		t.Fatalf("create test clip 1 failed: %v, out: %s", err, string(out))
	}

	cmd2 := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x240:r=24:d=1.0",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=44100:duration=1.0",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", video2Path)
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("create test clip 2 failed: %v, out: %s", err, string(out))
	}

	cmd3 := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=660:sample_rate=44100:duration=2.0",
		"-c:a", "libmp3lame", audioPath)
	if out, err := cmd3.CombinedOutput(); err != nil {
		t.Fatalf("create test audio failed: %v, out: %s", err, string(out))
	}

	video1Bytes, err := os.ReadFile(video1Path)
	if err != nil {
		t.Fatalf("read clip 1: %v", err)
	}
	video2Bytes, err := os.ReadFile(video2Path)
	if err != nil {
		t.Fatalf("read clip 2: %v", err)
	}
	audioBytes, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatalf("read audio: %v", err)
	}

	runtime := NewRuntime(1)
	authority := newVideoChainFakeArtifactAuthority()
	refVid1 := pebblestore.SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "vid1", VariantID: "var1", EventSeq: 1}
	refVid2 := pebblestore.SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "vid2", VariantID: "var2", EventSeq: 2}
	refAud := pebblestore.SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "aud", VariantID: "var3", EventSeq: 3}

	authority.addItem(refVid1, video1Bytes, pebblestore.SessionArtifactVariant{ID: "var1", CollectionID: "vid1", SessionID: "sess", EventSeq: 1, Filename: "clip1.mp4", MediaType: "video/mp4"})
	authority.addItem(refVid2, video2Bytes, pebblestore.SessionArtifactVariant{ID: "var2", CollectionID: "vid2", SessionID: "sess", EventSeq: 2, Filename: "clip2.mp4", MediaType: "video/mp4"})
	authority.addItem(refAud, audioBytes, pebblestore.SessionArtifactVariant{ID: "var3", CollectionID: "aud", SessionID: "sess", EventSeq: 3, Filename: "soundtrack.mp3", MediaType: "audio/mp3"})

	runtime.SetArtifactAuthority(authority)

	ctx, scope := artifactToolContext()

	// 1. Test extract_video_frame
	extractCall := Call{
		CallID: "extract-test-1",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{
			"action": "extract_video_frame",
			"session_id": "sess",
			"collection_id": "vid1",
			"variant_id": "var1",
			"event_seq": 1,
			"frame": "last"
		}`),
	}
	extractRes, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, extractCall)
	if err != nil {
		t.Fatalf("extract_video_frame failed: %v", err)
	}
	if !strings.Contains(extractRes, `"status":"ok"`) {
		t.Fatalf("expected status ok in extract result: %s", extractRes)
	}
	if len(authority.allCreated) == 0 {
		t.Fatalf("expected created keyframe artifact")
	}
	keyframeCreated := authority.allCreated[len(authority.allCreated)-1]
	if keyframeCreated.MediaType != "image/png" {
		t.Fatalf("expected image/png keyframe, got: %s", keyframeCreated.MediaType)
	}
	if len(keyframeCreated.Body) < 8 || !strings.HasPrefix(string(keyframeCreated.Body[:4]), "\x89PNG") {
		t.Fatalf("expected valid PNG magic bytes in extracted keyframe")
	}

	// 2. Test chain_video with audio override
	chainCall := Call{
		CallID: "chain-test-1",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{
			"action": "chain_video",
			"videos": [
				{"session_id": "sess", "collection_id": "vid1", "variant_id": "var1", "event_seq": 1},
				{"session_id": "sess", "collection_id": "vid2", "variant_id": "var2", "event_seq": 2}
			],
			"audio": {"session_id": "sess", "collection_id": "aud", "variant_id": "var3", "event_seq": 3},
			"audio_mode": "override",
			"title": "Chained 2-Part Master"
		}`),
	}
	chainRes, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, chainCall)
	if err != nil {
		t.Fatalf("chain_video override failed: %v", err)
	}
	if !strings.Contains(chainRes, `"status":"ok"`) {
		t.Fatalf("expected status ok in chain result: %s", chainRes)
	}
	masterCreated := authority.allCreated[len(authority.allCreated)-1]
	if masterCreated.MediaType != "video/mp4" {
		t.Fatalf("expected video/mp4 master, got: %s", masterCreated.MediaType)
	}
	if len(masterCreated.Body) == 0 {
		t.Fatalf("expected non-empty master video body")
	}

	// 3. Test chain_video with mix_ducked
	chainDuckedCall := Call{
		CallID: "chain-test-2",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{
			"action": "chain_video",
			"videos": [
				{"session_id": "sess", "collection_id": "vid1", "variant_id": "var1", "event_seq": 1},
				{"session_id": "sess", "collection_id": "vid2", "variant_id": "var2", "event_seq": 2}
			],
			"audio": {"session_id": "sess", "collection_id": "aud", "variant_id": "var3", "event_seq": 3},
			"audio_mode": "mix_ducked",
			"foley_volume": 0.4
		}`),
	}
	chainDuckedRes, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, chainDuckedCall)
	if err != nil {
		t.Fatalf("chain_video mix_ducked failed: %v", err)
	}
	if !strings.Contains(chainDuckedRes, `"status":"ok"`) {
		t.Fatalf("expected status ok in chain ducked result: %s", chainDuckedRes)
	}
}

func TestManageArtifactGenerateVideoWithChainFrom(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed in environment")
	}

	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "part1.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=cyan:s=320x240:r=24:d=1.0",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-an", videoPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create test clip failed: %v, out: %s", err, string(out))
	}
	videoBytes, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatalf("read clip: %v", err)
	}

	runtime := NewRuntime(1)
	authority := newVideoChainFakeArtifactAuthority()
	refVid1 := pebblestore.SessionArtifactSelectionReference{SessionID: "sess", CollectionID: "vid1", VariantID: "var1", EventSeq: 1}
	authority.addItem(refVid1, videoBytes, pebblestore.SessionArtifactVariant{ID: "var1", CollectionID: "vid1", SessionID: "sess", EventSeq: 1, Filename: "part1.mp4", MediaType: "video/mp4"})

	videoService := &fakeVideoGenerationService{
		result: videogen.ManagedVideoResult{
			Bytes:     []byte("fake-part2-video"),
			MediaType: "video/mp4",
			Model:     "veo-3.1-generate-preview",
			Provider:  "google",
		},
	}
	runtime.SetArtifactAuthority(authority)
	runtime.SetManagedVideoGenerationService(videoService)

	ctx, scope := artifactToolContext()
	call := Call{
		CallID: "chain-gen-call-1",
		Name:   "manage_artifact",
		Arguments: fmt.Sprintf(`{
			"action": "generate_video",
			"prompt": "Continue the epic adventure into deep space",
			"chain_from": {
				"session_id": "sess",
				"collection_id": "vid1",
				"variant_id": "var1",
				"event_seq": 1
			}
		}`),
	}
	res, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, call)
	if err != nil {
		t.Fatalf("generate_video with chain_from failed: %v", err)
	}
	if !strings.Contains(res, `"status":"ok"`) {
		t.Fatalf("expected status ok, got: %s", res)
	}

	if videoService.calls != 1 {
		t.Fatalf("expected 1 call to video service, got %d", videoService.calls)
	}
	if videoService.lastReq.Image == nil || len(videoService.lastReq.Image.Bytes) == 0 {
		t.Fatalf("expected Image input populated from chain_from keyframe, got nil or empty")
	}
	if videoService.lastReq.Image.MediaType != "image/png" {
		t.Fatalf("expected image/png media type, got %s", videoService.lastReq.Image.MediaType)
	}
	if videoService.lastReq.Source != nil {
		t.Fatalf("expected Source to be nil for chained image-to-video, got non-nil")
	}
}
