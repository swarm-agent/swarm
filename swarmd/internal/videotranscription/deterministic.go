package videotranscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	DeterministicFrameBatchSize   = 20
	DeterministicFrameConcurrency = 2
	deterministicRetryRounds      = 2
	deterministicMaxFrames        = 7_200
	deterministicMaxFrameBytes    = 2 << 20
	deterministicMaxFramesBytes   = 512 << 20
	deterministicMaxAudioBytes    = 512 << 20
	deterministicCommandErrorSize = 8 << 10
)

// DeterministicAdapter interprets Swarm-owned timestamped observations. It does
// not select frames, timestamps, batches, concurrency, retry scope, or merge
// behavior.
type DeterministicAdapter interface {
	AnalyzeFrameBatch(context.Context, FrameBatchRequest) ([]FrameObservation, error)
	AnalyzeAudio(context.Context, AudioAnalysisRequest) (GeneratedTranscript, error)
}

type PreparedFrame struct {
	ID          string
	TimestampMs int64
	EndMs       int64
	MIMEType    string
	PrivatePath string // private ephemeral path; never persist, return, or log
	SizeBytes   int64
}

type PreparedMedia struct {
	DurationMs       int64
	Frames           []PreparedFrame
	HasAudio         bool
	AudioMIMEType    string
	AudioPrivatePath string // private ephemeral path; never persist, return, or log
	AudioSizeBytes   int64
	privateDir       string
}

func (m *PreparedMedia) Close() error {
	if m == nil || strings.TrimSpace(m.privateDir) == "" {
		return nil
	}
	err := os.RemoveAll(m.privateDir)
	m.privateDir = ""
	return err
}

type FrameBatchRequest struct {
	AccountScopeID string
	Model          string
	Frames         []PreparedFrame
	FocusNotes     string
}

type FrameObservation struct {
	FrameID      string
	Visual       string
	OnScreenText string
}

type AudioAnalysisRequest struct {
	AccountScopeID string
	Model          string
	MIMEType       string
	PrivatePath    string // private ephemeral path; never persist, return, or log
	SizeBytes      int64
	DurationMs     int64
	FocusNotes     string
}

type mediaProbe struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
	} `json:"streams"`
}

// PrepareDeterministicMedia copies the trusted source into TMPDIR-backed private
// job storage, probes it, and deterministically extracts one bounded JPEG per
// second plus a normalized mono audio track when audio exists.
func PrepareDeterministicMedia(ctx context.Context, source io.ReadSeeker, sizeBytes int64) (prepared *PreparedMedia, err error) {
	if source == nil || sizeBytes <= 0 || sizeBytes > pebblestore.SessionVideoAttachmentMaxBytes {
		return nil, errors.New("deterministic video preparation requires a bounded trusted source")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, errors.New("deterministic video analysis requires ffmpeg in PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return nil, errors.New("deterministic video analysis requires ffprobe in PATH")
	}
	dir, err := os.MkdirTemp("", "swarm-video-analysis-")
	if err != nil {
		return nil, errors.New("private video analysis storage could not be created")
	}
	prepared = &PreparedMedia{privateDir: dir}
	defer func() {
		if err != nil {
			_ = prepared.Close()
		}
	}()

	inputPath := filepath.Join(dir, "source.media")
	input, err := os.OpenFile(inputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, errors.New("private video analysis source could not be created")
	}
	if _, seekErr := source.Seek(0, io.SeekStart); seekErr != nil {
		input.Close()
		return nil, errors.New("video source could not be rewound for deterministic analysis")
	}
	written, copyErr := io.Copy(input, io.LimitReader(source, sizeBytes+1))
	closeErr := input.Close()
	if copyErr != nil || closeErr != nil || written != sizeBytes {
		return nil, errors.New("video source could not be copied into bounded private analysis storage")
	}

	probePayload, err := runBoundedCommand(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "json", inputPath)
	if err != nil {
		return nil, fmt.Errorf("deterministic video probe failed: %w", err)
	}
	var probe mediaProbe
	if json.Unmarshal(probePayload, &probe) != nil {
		return nil, errors.New("deterministic video probe returned malformed metadata")
	}
	durationSeconds, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err != nil || durationSeconds <= 0 || math.IsInf(durationSeconds, 0) || math.IsNaN(durationSeconds) {
		return nil, errors.New("deterministic video probe returned an invalid duration")
	}
	prepared.DurationMs = int64(math.Ceil(durationSeconds * 1000))
	maxExpectedFrames := int(math.Ceil(durationSeconds - 1e-6))
	if maxExpectedFrames <= 0 || maxExpectedFrames > deterministicMaxFrames {
		return nil, fmt.Errorf("deterministic video analysis requires between 1 and %d sampled frames", deterministicMaxFrames)
	}
	for _, stream := range probe.Streams {
		if strings.EqualFold(strings.TrimSpace(stream.CodecType), "audio") {
			prepared.HasAudio = true
			break
		}
	}

	framesPattern := filepath.Join(dir, "frame-%08d.jpg")
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		_, commandErr := runBoundedCommand(groupCtx, "ffmpeg", "-v", "error", "-nostdin", "-i", inputPath, "-map", "0:v:0", "-vf", "fps=fps=1:start_time=0:round=down,scale=1280:-2:force_original_aspect_ratio=decrease", "-q:v", "4", framesPattern)
		if commandErr != nil {
			return fmt.Errorf("deterministic frame extraction failed: %w", commandErr)
		}
		return nil
	})
	if prepared.HasAudio {
		prepared.AudioMIMEType = "audio/flac"
		prepared.AudioPrivatePath = filepath.Join(dir, "audio.flac")
		group.Go(func() error {
			_, commandErr := runBoundedCommand(groupCtx, "ffmpeg", "-v", "error", "-nostdin", "-i", inputPath, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "flac", prepared.AudioPrivatePath)
			if commandErr != nil {
				return fmt.Errorf("deterministic audio extraction failed: %w", commandErr)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	framePaths, err := filepath.Glob(filepath.Join(dir, "frame-*.jpg"))
	if err != nil {
		return nil, errors.New("deterministic frame manifest could not be read")
	}
	sort.Strings(framePaths)
	if len(framePaths) == 0 || len(framePaths) > maxExpectedFrames || len(framePaths) < maxExpectedFrames-1 {
		return nil, fmt.Errorf("deterministic frame extraction coverage mismatch: got %d frames, expected at most %d", len(framePaths), maxExpectedFrames)
	}
	var totalFrameBytes int64
	prepared.Frames = make([]PreparedFrame, len(framePaths))
	for index, framePath := range framePaths {
		info, statErr := os.Stat(framePath)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > deterministicMaxFrameBytes {
			return nil, fmt.Errorf("deterministic frame %d violates the bounded media contract", index)
		}
		totalFrameBytes += info.Size()
		if totalFrameBytes > deterministicMaxFramesBytes {
			return nil, errors.New("deterministic sampled frames exceed the bounded private storage limit")
		}
		startMs := int64(index) * 1000
		endMs := startMs + 1000
		if endMs > prepared.DurationMs {
			endMs = prepared.DurationMs
		}
		prepared.Frames[index] = PreparedFrame{
			ID: fmt.Sprintf("frame_%012d", startMs), TimestampMs: startMs, EndMs: endMs,
			MIMEType: "image/jpeg", PrivatePath: framePath, SizeBytes: info.Size(),
		}
	}
	prepared.Frames[len(prepared.Frames)-1].EndMs = prepared.DurationMs
	if prepared.HasAudio {
		info, statErr := os.Stat(prepared.AudioPrivatePath)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > deterministicMaxAudioBytes {
			return nil, errors.New("deterministic audio track violates the bounded media contract")
		}
		prepared.AudioSizeBytes = info.Size()
	}
	return prepared, nil
}

func runBoundedCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	stderr := &boundedBuffer{remaining: deterministicCommandErrorSize}
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = &stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return nil, fmt.Errorf("%s failed", name)
		}
		return nil, fmt.Errorf("%s failed: %s", name, detail)
	}
	return stdout.Bytes(), nil
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
}

func (b *boundedBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(payload)
	if len(payload) > b.remaining {
		payload = payload[:b.remaining]
	}
	if len(payload) > 0 {
		_, _ = b.buffer.Write(payload)
		b.remaining -= len(payload)
	}
	return original, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func AnalyzeDeterministicFrames(ctx context.Context, adapter DeterministicAdapter, request FrameBatchRequest) ([]FrameObservation, error) {
	if adapter == nil || len(request.Frames) == 0 {
		return nil, errors.New("deterministic frame analysis requires an adapter and sampled frames")
	}
	pending := append([]PreparedFrame(nil), request.Frames...)
	observed := make(map[string]FrameObservation, len(pending))
	for round := 0; round <= deterministicRetryRounds && len(pending) > 0; round++ {
		batches := consecutiveRetryBatches(request.Frames, pending, DeterministicFrameBatchSize)
		type batchResult struct {
			frames       []PreparedFrame
			observations []FrameObservation
			err          error
		}
		results := make(chan batchResult, len(batches))
		semaphore := make(chan struct{}, DeterministicFrameConcurrency)
		group, groupCtx := errgroup.WithContext(ctx)
		for _, batch := range batches {
			batch := batch
			group.Go(func() error {
				select {
				case semaphore <- struct{}{}:
				case <-groupCtx.Done():
					return groupCtx.Err()
				}
				defer func() { <-semaphore }()
				batchRequest := request
				batchRequest.Frames = batch
				observations, err := adapter.AnalyzeFrameBatch(groupCtx, batchRequest)
				results <- batchResult{frames: batch, observations: observations, err: err}
				return err
			})
		}
		waitErr := group.Wait()
		close(results)
		if waitErr != nil {
			return nil, waitErr
		}
		roundObserved := make(map[string]struct{}, len(pending))
		for result := range results {
			allowed := make(map[string]struct{}, len(result.frames))
			for _, frame := range result.frames {
				allowed[frame.ID] = struct{}{}
			}
			for _, observation := range result.observations {
				observation.FrameID = strings.TrimSpace(observation.FrameID)
				observation.Visual = strings.TrimSpace(observation.Visual)
				observation.OnScreenText = strings.TrimSpace(observation.OnScreenText)
				if _, ok := allowed[observation.FrameID]; !ok {
					return nil, errors.New("frame analysis returned an unknown frame ID")
				}
				if _, duplicate := roundObserved[observation.FrameID]; duplicate {
					return nil, errors.New("frame analysis returned a duplicate frame ID")
				}
				roundObserved[observation.FrameID] = struct{}{}
				observed[observation.FrameID] = observation
			}
		}
		pending = pending[:0]
		for _, frame := range request.Frames {
			if _, ok := observed[frame.ID]; !ok {
				pending = append(pending, frame)
			}
		}
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("frame analysis omitted %d sampled frames after bounded selective retries", len(pending))
	}
	ordered := make([]FrameObservation, len(request.Frames))
	for index, frame := range request.Frames {
		ordered[index] = observed[frame.ID]
	}
	return ordered, nil
}

func consecutiveRetryBatches(manifest, pending []PreparedFrame, size int) [][]PreparedFrame {
	if len(pending) == len(manifest) {
		return consecutiveFrameBatches(pending, size)
	}
	pendingIDs := make(map[string]struct{}, len(pending))
	for _, frame := range pending {
		pendingIDs[frame.ID] = struct{}{}
	}
	var batches [][]PreparedFrame
	var run []PreparedFrame
	flush := func() {
		if len(run) == 0 {
			return
		}
		batches = append(batches, consecutiveFrameBatches(run, size)...)
		run = nil
	}
	for _, frame := range manifest {
		if _, retry := pendingIDs[frame.ID]; !retry {
			flush()
			continue
		}
		run = append(run, frame)
	}
	flush()
	return batches
}

func consecutiveFrameBatches(frames []PreparedFrame, size int) [][]PreparedFrame {
	if size <= 0 {
		size = DeterministicFrameBatchSize
	}
	batches := make([][]PreparedFrame, 0, (len(frames)+size-1)/size)
	for start := 0; start < len(frames); start += size {
		end := start + size
		if end > len(frames) {
			end = len(frames)
		}
		batches = append(batches, append([]PreparedFrame(nil), frames[start:end]...))
	}
	return batches
}

func MergeDeterministicTracks(media *PreparedMedia, visual []FrameObservation, audio GeneratedTranscript) (GeneratedTranscript, error) {
	if media == nil || media.DurationMs <= 0 || len(media.Frames) == 0 || len(visual) != len(media.Frames) {
		return GeneratedTranscript{}, errors.New("deterministic video tracks do not match the prepared media manifest")
	}
	segments := make([]pebblestore.NormalizedTranscriptSegment, len(media.Frames))
	for index, frame := range media.Frames {
		if visual[index].FrameID != frame.ID {
			return GeneratedTranscript{}, errors.New("deterministic visual observations are out of manifest order")
		}
		segments[index] = pebblestore.NormalizedTranscriptSegment{
			StartMs: frame.TimestampMs, EndMs: frame.EndMs,
			Visual: strings.TrimSpace(visual[index].Visual), OnScreenText: strings.TrimSpace(visual[index].OnScreenText),
		}
	}
	for _, audioSegment := range audio.Segments {
		first := int(audioSegment.StartMs / 1000)
		last := int((audioSegment.EndMs - 1) / 1000)
		if first < 0 {
			first = 0
		}
		if last >= len(segments) {
			last = len(segments) - 1
		}
		for index := first; index <= last; index++ {
			segments[index].Speech = joinTrackText(segments[index].Speech, audioSegment.Speech)
			segments[index].Audio = joinTrackText(segments[index].Audio, audioSegment.Audio)
		}
	}
	contentEmpty := true
	for _, segment := range segments {
		if segment.Speech != "" || segment.Audio != "" || segment.Visual != "" || segment.OnScreenText != "" {
			contentEmpty = false
			break
		}
	}
	if contentEmpty {
		segments = []pebblestore.NormalizedTranscriptSegment{{StartMs: 0, EndMs: media.DurationMs, Visual: pebblestore.ContentEmptyVideoDescription}}
	}
	return NormalizeGeneratedTranscript(GeneratedTranscript{
		Segments: segments, Language: audio.Language, DurationMs: media.DurationMs,
		Summary: audio.Summary, ContentEmpty: contentEmpty,
	})
}

func joinTrackText(existing, next string) string {
	existing, next = strings.TrimSpace(existing), strings.TrimSpace(next)
	if existing == "" {
		return next
	}
	if next == "" || existing == next {
		return existing
	}
	return existing + "\n" + next
}
