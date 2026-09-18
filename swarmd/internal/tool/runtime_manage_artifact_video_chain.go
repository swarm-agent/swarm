package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	defaultVideoChainTimeout = 5 * time.Minute
	maxChainedVideoParts     = 16
	minChainedVideoParts     = 2
	defaultFoleyDuckingVol   = 0.35
)

// runFFmpeg runs an ffmpeg command with context cancellation and bounded output capture.
func runFFmpeg(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg runtime is required but not installed or not in PATH: %w", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg failed: %s", errMsg)
	}
	return stdout.Bytes(), nil
}

// runFFprobe runs an ffprobe command with context cancellation.
func runFFprobe(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return nil, fmt.Errorf("ffprobe runtime is required but not installed or not in PATH: %w", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, fmt.Errorf("ffprobe failed: %s", errMsg)
	}
	return stdout.Bytes(), nil
}

type videoProbeMetadata struct {
	DurationSeconds float64 `json:"duration_seconds"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	Bitrate         int64   `json:"bitrate"`
	SizeBytes       int64   `json:"size_bytes"`
	FormatName      string  `json:"format_name"`
	VideoCodec      string  `json:"video_codec"`
	AudioCodec      string  `json:"audio_codec"`
}

func probeVideoFile(ctx context.Context, filePath string) (videoProbeMetadata, error) {
	raw, err := runFFprobe(ctx,
		"-v", "error",
		"-show_entries", "format=duration,size,bit_rate,format_name:stream=index,codec_type,codec_name,width,height",
		"-of", "json",
		filePath,
	)
	if err != nil {
		return videoProbeMetadata{}, err
	}

	var parsed struct {
		Format struct {
			Duration   string `json:"duration"`
			Size       string `json:"size"`
			BitRate    string `json:"bit_rate"`
			FormatName string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			Index     int    `json:"index"`
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return videoProbeMetadata{}, fmt.Errorf("parse ffprobe json: %w", err)
	}

	meta := videoProbeMetadata{
		FormatName: parsed.Format.FormatName,
	}
	if dur, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil {
		meta.DurationSeconds = dur
	}
	if sz, err := strconv.ParseInt(parsed.Format.Size, 10, 64); err == nil {
		meta.SizeBytes = sz
	}
	if br, err := strconv.ParseInt(parsed.Format.BitRate, 10, 64); err == nil {
		meta.Bitrate = br
	}
	for _, stream := range parsed.Streams {
		if stream.CodecType == "video" && meta.VideoCodec == "" {
			meta.VideoCodec = stream.CodecName
			meta.Width = stream.Width
			meta.Height = stream.Height
		} else if stream.CodecType == "audio" && meta.AudioCodec == "" {
			meta.AudioCodec = stream.CodecName
		}
	}
	return meta, nil
}

// extractLastKeyframeBytes extracts the exact final frame of videoBytes as a PNG.
func extractLastKeyframeBytes(ctx context.Context, videoBytes []byte) ([]byte, error) {
	return extractVideoFrameBytes(ctx, videoBytes, "last", 0)
}

// extractVideoFrameBytes extracts a specified frame (last, first, or at timestamp_ms) from videoBytes.
func extractVideoFrameBytes(ctx context.Context, videoBytes []byte, frame string, timestampMs int64) ([]byte, error) {
	if len(videoBytes) == 0 {
		return nil, errors.New("video bytes are empty")
	}
	tmpDir, err := os.MkdirTemp("", "swarm-video-extract-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir for keyframe extraction: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	videoPath := filepath.Join(tmpDir, "input.mp4")
	if err := os.WriteFile(videoPath, videoBytes, 0600); err != nil {
		return nil, fmt.Errorf("write temp video for extraction: %w", err)
	}
	pngPath := filepath.Join(tmpDir, "frame.png")

	frameChoice := strings.ToLower(strings.TrimSpace(frame))
	if frameChoice == "" {
		frameChoice = "last"
	}

	var ffmpegArgs []string
	switch {
	case timestampMs > 0:
		sec := float64(timestampMs) / 1000.0
		ffmpegArgs = []string{
			"-y", "-v", "error", "-nostdin",
			"-ss", fmt.Sprintf("%.3f", sec),
			"-i", videoPath,
			"-fps_mode", "passthrough",
			"-frames:v", "1",
			"-update", "1",
			pngPath,
		}
	case frameChoice == "first":
		ffmpegArgs = []string{
			"-y", "-v", "error", "-nostdin",
			"-i", videoPath,
			"-fps_mode", "passthrough",
			"-frames:v", "1",
			"-update", "1",
			pngPath,
		}
	case frameChoice == "last":
		fallthrough
	default:
		// Extract exact last frame using -sseof -0.1
		ffmpegArgs = []string{
			"-y", "-v", "error", "-nostdin",
			"-sseof", "-0.1",
			"-i", videoPath,
			"-fps_mode", "passthrough",
			"-frames:v", "1",
			"-update", "1",
			pngPath,
		}
	}

	if _, err := runFFmpeg(ctx, ffmpegArgs...); err != nil {
		return nil, fmt.Errorf("extract frame via ffmpeg: %w", err)
	}

	pngBytes, err := os.ReadFile(pngPath)
	if err != nil {
		return nil, fmt.Errorf("read extracted frame PNG: %w", err)
	}
	if len(pngBytes) < 8 || !bytes.Equal(pngBytes[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return nil, errors.New("extracted keyframe is not a valid PNG")
	}
	return pngBytes, nil
}

// resolveVideoSourceBytes resolves video bytes and source reference from an argument map or string.
func (r *Runtime) resolveVideoSourceBytes(
	ctx context.Context,
	scope WorkspaceScope,
	principal artifact.Principal,
	raw any,
) ([]byte, *pebblestore.SessionArtifactSelectionReference, string, error) {
	if raw == nil {
		return nil, nil, "", errors.New("missing video source")
	}

	switch v := raw.(type) {
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return nil, nil, "", errors.New("empty video source string")
		}
		// Check if it's a workspace path
		rooted, err := openRootedWorkspacePath(scope, str)
		if err == nil {
			defer rooted.Close()
			info, statErr := rooted.stat()
			if statErr == nil && info.Mode().IsRegular() {
				if info.Size() > 512<<20 {
					return nil, nil, "", fmt.Errorf("video file %q exceeds maximum limit of 512 MiB", str)
				}
				f, openErr := rooted.open()
				if openErr != nil {
					return nil, nil, "", fmt.Errorf("open video file %q: %w", str, openErr)
				}
				defer f.Close()
				data, readErr := io.ReadAll(f)
				if readErr != nil {
					return nil, nil, "", fmt.Errorf("read video file %q: %w", str, readErr)
				}
				return data, nil, filepath.Base(str), nil
			}
		}
		return nil, nil, "", fmt.Errorf("video file %q not found or not a regular file", str)

	case map[string]any:
		if pathVal := strings.TrimSpace(asString(v["video_path"])); pathVal != "" {
			return r.resolveVideoSourceBytes(ctx, scope, principal, pathVal)
		}
		if pathVal := strings.TrimSpace(asString(v["path"])); pathVal != "" {
			return r.resolveVideoSourceBytes(ctx, scope, principal, pathVal)
		}

		sessionID := strings.TrimSpace(firstNonEmptyString(asString(v["session_id"]), asString(v["source_session_id"])))
		if sessionID == "" {
			sessionID = principal.SessionID
		}
		collectionID := strings.TrimSpace(firstNonEmptyString(asString(v["collection_id"]), asString(v["source_collection_id"])))
		variantID := strings.TrimSpace(firstNonEmptyString(asString(v["variant_id"]), asString(v["source_variant_id"])))
		eventSeq := asUint64(v["event_seq"])
		if eventSeq == 0 {
			eventSeq = asUint64(v["source_event_seq"])
		}

		if collectionID == "" || variantID == "" || eventSeq == 0 {
			return nil, nil, "", errors.New("video artifact reference requires collection_id, variant_id, and non-zero event_seq")
		}

		if r.artifactAuthority == nil {
			return nil, nil, "", errors.New("artifact authority is not configured")
		}

		ref := pebblestore.SessionArtifactSelectionReference{
			SessionID:    sessionID,
			CollectionID: collectionID,
			VariantID:    variantID,
			EventSeq:     eventSeq,
		}
		body, variant, err := r.artifactAuthority.ReadReference(ctx, principal, ref, 512<<20)
		if err != nil {
			return nil, nil, "", fmt.Errorf("read video artifact reference: %w", err)
		}
		if len(body) == 0 {
			return nil, nil, "", errors.New("video artifact body is empty")
		}
		return body, &ref, variant.Filename, nil

	default:
		return nil, nil, "", fmt.Errorf("unsupported video source type %T", raw)
	}
}

// resolveAudioSourceBytes resolves audio bytes and source reference from an argument map or string.
func (r *Runtime) resolveAudioSourceBytes(
	ctx context.Context,
	scope WorkspaceScope,
	principal artifact.Principal,
	raw any,
) ([]byte, *pebblestore.SessionArtifactSelectionReference, string, error) {
	if raw == nil {
		return nil, nil, "", nil
	}

	switch v := raw.(type) {
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return nil, nil, "", nil
		}
		rooted, err := openRootedWorkspacePath(scope, str)
		if err == nil {
			defer rooted.Close()
			info, statErr := rooted.stat()
			if statErr == nil && info.Mode().IsRegular() {
				if info.Size() > 128<<20 {
					return nil, nil, "", fmt.Errorf("audio file %q exceeds maximum limit of 128 MiB", str)
				}
				f, openErr := rooted.open()
				if openErr != nil {
					return nil, nil, "", fmt.Errorf("open audio file %q: %w", str, openErr)
				}
				defer f.Close()
				data, readErr := io.ReadAll(f)
				if readErr != nil {
					return nil, nil, "", fmt.Errorf("read audio file %q: %w", str, readErr)
				}
				return data, nil, filepath.Base(str), nil
			}
		}
		return nil, nil, "", fmt.Errorf("audio file %q not found", str)

	case map[string]any:
		if pathVal := strings.TrimSpace(asString(v["audio_path"])); pathVal != "" {
			return r.resolveAudioSourceBytes(ctx, scope, principal, pathVal)
		}
		if pathVal := strings.TrimSpace(asString(v["path"])); pathVal != "" {
			return r.resolveAudioSourceBytes(ctx, scope, principal, pathVal)
		}

		sessionID := strings.TrimSpace(firstNonEmptyString(asString(v["session_id"]), asString(v["source_session_id"])))
		if sessionID == "" {
			sessionID = principal.SessionID
		}
		collectionID := strings.TrimSpace(firstNonEmptyString(asString(v["collection_id"]), asString(v["source_collection_id"])))
		variantID := strings.TrimSpace(firstNonEmptyString(asString(v["variant_id"]), asString(v["source_variant_id"])))
		eventSeq := asUint64(v["event_seq"])
		if eventSeq == 0 {
			eventSeq = asUint64(v["source_event_seq"])
		}

		if collectionID == "" || variantID == "" || eventSeq == 0 {
			return nil, nil, "", errors.New("audio artifact reference requires collection_id, variant_id, and non-zero event_seq")
		}

		if r.artifactAuthority == nil {
			return nil, nil, "", errors.New("artifact authority is not configured")
		}

		ref := pebblestore.SessionArtifactSelectionReference{
			SessionID:    sessionID,
			CollectionID: collectionID,
			VariantID:    variantID,
			EventSeq:     eventSeq,
		}
		body, variant, err := r.artifactAuthority.ReadReference(ctx, principal, ref, 128<<20)
		if err != nil {
			return nil, nil, "", fmt.Errorf("read audio artifact reference: %w", err)
		}
		if len(body) == 0 {
			return nil, nil, "", errors.New("audio artifact body is empty")
		}
		return body, &ref, variant.Filename, nil

	default:
		return nil, nil, "", fmt.Errorf("unsupported audio source type %T", raw)
	}
}

// extractVideoFrame extracts a frame from a video artifact and publishes it as a managed image/png artifact.
func (r *Runtime) extractVideoFrame(
	ctx context.Context,
	scope WorkspaceScope,
	principal artifact.Principal,
	callID string,
	requestID string,
	args map[string]any,
) (pebblestore.SessionArtifactVariant, error) {
	if r.artifactAuthority == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact authority is not configured")
	}

	// Resolve video source
	var videoRaw any
	if asString(args["collection_id"]) != "" || asString(args["source_collection_id"]) != "" {
		videoRaw = args
	} else if v, ok := args["video"]; ok && v != nil {
		videoRaw = v
	} else if v, ok := args["video_path"]; ok && v != nil {
		videoRaw = v
	} else if v, ok := args["source"]; ok && v != nil {
		videoRaw = v
	}
	if videoRaw == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("extract_video_frame requires video artifact reference or video_path")
	}

	videoBytes, _, originalFilename, err := r.resolveVideoSourceBytes(ctx, scope, principal, videoRaw)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("resolve video for extraction: %w", err)
	}

	frame := asString(args["frame"])
	timestampMs := int64(asUint64(args["timestamp_ms"]))

	pngBytes, err := extractVideoFrameBytes(ctx, videoBytes, frame, timestampMs)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("extract frame: %w", err)
	}

	title := strings.TrimSpace(asString(args["title"]))
	if title == "" {
		if frame == "" || frame == "last" {
			title = "Video Last Keyframe"
		} else if frame == "first" {
			title = "Video First Frame"
		} else {
			title = fmt.Sprintf("Video Keyframe (%s)", frame)
		}
	}

	filename := strings.TrimSpace(asString(args["filename"]))
	if filename == "" {
		base := strings.TrimSuffix(originalFilename, filepath.Ext(originalFilename))
		if base == "" {
			base = "video"
		}
		filename = fmt.Sprintf("%s-keyframe.png", base)
	}

	collectionID := managedArtifactOpaqueID("collection", principal.SessionID, callID)
	variantID := managedArtifactOpaqueID("variant", principal.SessionID, callID)

	presentation := pebblestore.SessionArtifactPresentation{
		Kind:        "image",
		Previewable: true,
		Label:       title,
		Description: fmt.Sprintf("Extracted keyframe (%s) from video", frame),
	}

	create := artifact.CreateInput{
		RequestID:             requestID,
		CollectionID:          collectionID,
		CollectionName:        title,
		CollectionDescription: fmt.Sprintf("Extracted keyframe from %s", originalFilename),
		VariantID:             variantID,
		Filename:              filename,
		MediaType:             "image/png",
		Presentation:          presentation,
		Body:                  pngBytes,
		Role:                  pebblestore.SessionArtifactRoleKeyframe,
		AutoAccept:            true,
	}

	published, err := r.artifactAuthority.Create(ctx, principal, create)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("publish keyframe artifact: %w", err)
	}
	return published, nil
}

func asFloat64(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err == nil {
			return parsed
		}
		return fallback
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return parsed
		}
		return fallback
	default:
		return fallback
	}
}

// chainVideo concatenates multiple video artifacts and optionally overrides or mixes continuous audio soundtrack.
func (r *Runtime) chainVideo(
	ctx context.Context,
	scope WorkspaceScope,
	principal artifact.Principal,
	callID string,
	requestID string,
	args map[string]any,
) (pebblestore.SessionArtifactVariant, map[string]any, error) {
	if r.artifactAuthority == nil {
		return pebblestore.SessionArtifactVariant{}, nil, errors.New("artifact authority is not configured")
	}

	rawVideos, ok := args["videos"].([]any)
	if !ok || len(rawVideos) < minChainedVideoParts {
		return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("chain_video requires at least %d items in 'videos'", minChainedVideoParts)
	}
	if len(rawVideos) > maxChainedVideoParts {
		return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("chain_video exceeds maximum of %d videos", maxChainedVideoParts)
	}

	tempDir, err := os.MkdirTemp("", "swarm-video-chain-")
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("create temp dir for video chaining: %w", err)
	}
	defer os.RemoveAll(tempDir)

	var videoPaths []string
	var sourceRefs []pebblestore.SessionArtifactSelectionReference
	for i, raw := range rawVideos {
		data, ref, _, err := r.resolveVideoSourceBytes(ctx, scope, principal, raw)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("resolve video part %d: %w", i+1, err)
		}
		partPath := filepath.Join(tempDir, fmt.Sprintf("part_%02d.mp4", i+1))
		if err := os.WriteFile(partPath, data, 0600); err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("write video part %d: %w", i+1, err)
		}
		videoPaths = append(videoPaths, partPath)
		if ref != nil {
			sourceRefs = append(sourceRefs, *ref)
		}
	}

	// Resolve optional audio soundtrack
	var audioPath string
	var audioRef *pebblestore.SessionArtifactSelectionReference
	if rawAudio, hasAudio := args["audio"]; hasAudio && rawAudio != nil {
		data, ref, fname, err := r.resolveAudioSourceBytes(ctx, scope, principal, rawAudio)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("resolve audio soundtrack: %w", err)
		}
		if len(data) > 0 {
			ext := filepath.Ext(fname)
			if ext == "" {
				ext = ".mp3"
			}
			audioPath = filepath.Join(tempDir, "soundtrack"+ext)
			if err := os.WriteFile(audioPath, data, 0600); err != nil {
				return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("write audio soundtrack: %w", err)
			}
			audioRef = ref
		}
	}

	audioMode := strings.ToLower(strings.TrimSpace(asString(args["audio_mode"])))
	if audioMode == "" {
		if audioPath != "" {
			audioMode = "override"
		} else {
			audioMode = "native"
		}
	}

	foleyVol := asFloat64(args["foley_volume"], defaultFoleyDuckingVol)
	if foleyVol <= 0 {
		foleyVol = defaultFoleyDuckingVol
	}
	if foleyVol > 2.0 {
		foleyVol = 2.0
	}

	transition := strings.ToLower(strings.TrimSpace(asString(args["transition"])))
	if transition == "" {
		transition = "cut"
	}

	masterPath := filepath.Join(tempDir, "master.mp4")

	// Create concat demuxer file list
	concatListPath := filepath.Join(tempDir, "concat_list.txt")
	var listContent strings.Builder
	for _, p := range videoPaths {
		listContent.WriteString(fmt.Sprintf("file '%s'\n", p))
	}
	if err := os.WriteFile(concatListPath, []byte(listContent.String()), 0600); err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("write concat list: %w", err)
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, defaultVideoChainTimeout)
	defer cancel()

	switch {
	case audioPath != "" && audioMode == "override":
		// Step 1: Concat video streams losslessly without audio
		tempVideo := filepath.Join(tempDir, "temp_video.mp4")
		if _, err := runFFmpeg(ctxTimeout,
			"-y", "-v", "error", "-nostdin",
			"-f", "concat", "-safe", "0",
			"-i", concatListPath,
			"-c", "copy", "-an",
			tempVideo,
		); err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("lossless video concat: %w", err)
		}

		// Step 2: Mux with continuous soundtrack
		if _, err := runFFmpeg(ctxTimeout,
			"-y", "-v", "error", "-nostdin",
			"-i", tempVideo,
			"-i", audioPath,
			"-map", "0:v:0",
			"-map", "1:a:0",
			"-c:v", "copy",
			"-c:a", "aac", "-b:a", "256k",
			"-shortest",
			masterPath,
		); err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("mux continuous soundtrack: %w", err)
		}

	case audioPath != "" && audioMode == "mix_ducked":
		// Step 1: Concat videos preserving audio tracks
		tempCombined := filepath.Join(tempDir, "temp_combined.mp4")
		if _, err := runFFmpeg(ctxTimeout,
			"-y", "-v", "error", "-nostdin",
			"-f", "concat", "-safe", "0",
			"-i", concatListPath,
			"-c", "copy",
			tempCombined,
		); err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("concat videos for mixing: %w", err)
		}

		// Step 2: Mix ducked native audio with full soundtrack
		filterGraph := fmt.Sprintf(
			"[0:a]volume=%.2f[foley]; [1:a]volume=1.0[bgm]; [foley][bgm]amix=inputs=2:duration=first:dropout_transition=2[aout]",
			foleyVol,
		)
		if _, err := runFFmpeg(ctxTimeout,
			"-y", "-v", "error", "-nostdin",
			"-i", tempCombined,
			"-i", audioPath,
			"-filter_complex", filterGraph,
			"-map", "0:v:0",
			"-map", "[aout]",
			"-c:v", "copy",
			"-c:a", "aac", "-b:a", "256k",
			"-shortest",
			masterPath,
		); err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("mix ducked Foley with soundtrack: %w", err)
		}

	default:
		// Native audio concat
		if _, err := runFFmpeg(ctxTimeout,
			"-y", "-v", "error", "-nostdin",
			"-f", "concat", "-safe", "0",
			"-i", concatListPath,
			"-c", "copy",
			masterPath,
		); err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("concat native audio/video: %w", err)
		}
	}

	masterBytes, err := os.ReadFile(masterPath)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("read chained master video: %w", err)
	}

	// Probe output metadata
	probe, probeErr := probeVideoFile(ctx, masterPath)

	title := strings.TrimSpace(asString(args["title"]))
	if title == "" {
		title = fmt.Sprintf("Chained Video (%d Parts)", len(videoPaths))
	}
	filename := strings.TrimSpace(asString(args["filename"]))
	if filename == "" {
		filename = "chained-master.mp4"
	}

	collectionID := managedArtifactOpaqueID("collection", principal.SessionID, callID)
	variantID := managedArtifactOpaqueID("variant", principal.SessionID, callID)

	presentation := pebblestore.SessionArtifactPresentation{
		Kind:        "video",
		Previewable: true,
		Label:       title,
		Description: fmt.Sprintf("Chained multi-part video (%d parts, audio mode: %s)", len(videoPaths), audioMode),
	}

	create := artifact.CreateInput{
		RequestID:             requestID,
		CollectionID:          collectionID,
		CollectionName:        title,
		CollectionDescription: presentation.Description,
		VariantID:             variantID,
		Filename:              filename,
		MediaType:             "video/mp4",
		Presentation:          presentation,
		Body:                  masterBytes,
		Role:                  pebblestore.SessionArtifactRoleChainedVideo,
		AutoAccept:            true,
	}

	published, err := r.artifactAuthority.Create(ctx, principal, create)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("publish chained video artifact: %w", err)
	}

	details := map[string]any{
		"parts_count":      len(videoPaths),
		"audio_mode":       audioMode,
		"transition":       transition,
		"video_references": sourceRefs,
	}
	if probeErr == nil {
		details["duration_seconds"] = probe.DurationSeconds
		details["width"] = probe.Width
		details["height"] = probe.Height
		details["bitrate"] = probe.Bitrate
		details["size_bytes"] = len(masterBytes)
	}
	if audioRef != nil {
		details["soundtrack_reference"] = managedArtifactReferenceWithSession(audioRef.SessionID, audioRef.CollectionID, audioRef.VariantID, audioRef.EventSeq)
	}

	return published, details, nil
}
