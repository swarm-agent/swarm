package videorender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const (
	DefaultMaxClips          = 100
	DefaultMaxDurationMs     = 3600000 // 1 hour
	DefaultMaxRenderBytes    = 2 << 30 // 2 GB
	DefaultRenderTimeout     = 10 * time.Minute
	DefaultCommandErrorLimit = 16 << 10
	DefaultRecoveryLimit     = 50
	MaxQueueGracePeriod      = 30 * time.Second
	MaxInspectionFrames      = 12
	MaxInspectionRangeMs     = 60_000
	MaxInspectionSpanMs      = 300_000
	MaxInspectionDurationMs  = 600_000
	MaxInspectionWidth       = 1920
	DefaultInspectionWidth   = 1280
	MaxInspectionPNGBytes    = 16 << 20
	MaxInspectionTotalBytes  = 64 << 20
	DefaultInspectionTimeout = 2 * time.Minute
)

type Config struct {
	MaxClips       int
	MaxDurationMs  int64
	MaxRenderBytes int64
	DefaultTimeout time.Duration
	WorkDir        string
	RecoveryLimit  int
}

type CommandRunner interface {
	LookPath(file string) (string, error)
	RunCommand(ctx context.Context, name string, args ...string) ([]byte, error)
}

type osCommandRunner struct{}

func (r *osCommandRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (r *osCommandRunner) RunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	stderr := &boundedBuffer{remaining: DefaultCommandErrorLimit}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
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

func (r *osCommandRunner) probeAudioStream(ctx context.Context, filePath string) (bool, error) {
	var stdout bytes.Buffer
	stderr := &boundedBuffer{remaining: DefaultCommandErrorLimit}
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "a",
		"-show_entries", "stream=index",
		"-of", "csv=p=0",
		filePath,
	)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return false, errors.New("ffprobe failed")
		}
		return false, fmt.Errorf("ffprobe failed: %s", detail)
	}
	return strings.TrimSpace(stdout.String()) != "", nil
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

type ArtifactAuthority interface {
	GetReference(principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error)
	ReadReference(ctx context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, maxBytes int64) ([]byte, pebblestore.SessionArtifactVariant, error)
	CreateFromFile(ctx context.Context, principal artifact.Principal, input artifact.CreateFileInput) (pebblestore.SessionArtifactVariant, error)
}

type SessionStore interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
	GetVideoProject(accountScopeID, sessionID, projectID string) (pebblestore.VideoProjectSnapshot, bool, error)
	GetVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID string) (pebblestore.VideoProjectRevisionSnapshot, bool, error)
	GetVideoRenderJob(accountScopeID, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error)
	UpdateVideoRenderJob(input pebblestore.UpdateVideoRenderJobInput) (pebblestore.VideoRenderJobSnapshot, error)
	GetVideoSourceRecord(accountScopeID, workspaceID, ref string) (pebblestore.VideoSourceRecord, bool, error)
	GetAudioSourceRecord(accountScopeID, workspaceID, ref string) (pebblestore.AudioSourceRecord, bool, error)
	GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error)
	ListVideoRenderJobs(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoRenderJobSnapshot, error)
	ListRecoverableVideoRenderJobs(limit int) ([]pebblestore.VideoRenderJobSnapshot, error)
}

type WorkspaceAuthority interface {
	ListSourceMediaDirectoriesForPrincipal(principal identity.Principal, workspacePath string) (workspaceruntime.Resolution, error)
}

type Service struct {
	cfg           Config
	store         SessionStore
	artifacts     ArtifactAuthority
	workspace     WorkspaceAuthority
	runner        CommandRunner
	mu            sync.Mutex
	cancels       map[string]context.CancelFunc
	pendingStarts map[string]context.CancelFunc
	workers       sync.WaitGroup
	workerMu      sync.Mutex
	workerN       int
	idle          chan struct{}
}

func NewService(cfg Config, store SessionStore, artifacts ArtifactAuthority, _ any, workspace WorkspaceAuthority, runner CommandRunner) *Service {
	if cfg.MaxClips <= 0 {
		cfg.MaxClips = DefaultMaxClips
	}
	if cfg.MaxDurationMs <= 0 {
		cfg.MaxDurationMs = DefaultMaxDurationMs
	}
	if cfg.MaxRenderBytes <= 0 {
		cfg.MaxRenderBytes = DefaultMaxRenderBytes
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = DefaultRenderTimeout
	}
	if cfg.RecoveryLimit <= 0 {
		cfg.RecoveryLimit = DefaultRecoveryLimit
	}
	if runner == nil {
		runner = &osCommandRunner{}
	}
	return &Service{
		cfg:           cfg,
		store:         store,
		artifacts:     artifacts,
		workspace:     workspace,
		runner:        runner,
		cancels:       make(map[string]context.CancelFunc),
		pendingStarts: make(map[string]context.CancelFunc),
	}
}

type FrameInspectionRange struct {
	StartMs int64
	EndMs   int64
	Count   int
}

type FrameInspectionRequest struct {
	SessionID         string
	ArtifactSessionID string
	ProjectID         string
	RevisionID        string
	WorkspacePath     string
	TimestampsMs      []int64
	Ranges            []FrameInspectionRange
	MaxWidth          int
	Timeout           time.Duration
	RequestID         string
}

type InspectedFrame struct {
	TimestampMs int64
	Artifact    pebblestore.SessionArtifactSelectionReference
}

type FrameInspectionResult struct {
	ProjectID        string
	RevisionID       string
	RevisionEventSeq uint64
	DurationMs       int64
	Width            int
	Height           int
	Frames           []InspectedFrame
}

type RenderJobRequest struct {
	SessionID     string
	ProjectID     string
	RevisionID    string
	JobID         string
	WorkspacePath string
	Timeout       time.Duration
	QueueGrace    time.Duration
}

func (s *Service) InspectFrames(ctx context.Context, principal identity.Principal, req FrameInspectionRequest) (FrameInspectionResult, error) {
	if s == nil || s.store == nil || s.artifacts == nil {
		return FrameInspectionResult{}, errors.New("video frame inspection is unavailable: render or artifact authority is not configured")
	}
	if !principal.Valid() {
		return FrameInspectionResult{}, errors.New("authenticated principal is required")
	}
	sessionID, projectID, revisionID := strings.TrimSpace(req.SessionID), strings.TrimSpace(req.ProjectID), strings.TrimSpace(req.RevisionID)
	if sessionID == "" || projectID == "" || revisionID == "" {
		return FrameInspectionResult{}, errors.New("inspect frames requires exact session_id, project_id, and revision_id")
	}
	session, ok, err := s.store.GetSession(sessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("video inspection session not found")
		}
		return FrameInspectionResult{}, err
	}
	if session.AccountScopeID != principal.AccountScopeID || (session.UserID != "" && session.UserID != principal.UserID) {
		return FrameInspectionResult{}, errors.New("video inspection session ownership does not match authenticated principal")
	}
	project, ok, err := s.store.GetVideoProject(principal.AccountScopeID, sessionID, projectID)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("video project %q not found", projectID)
		}
		return FrameInspectionResult{}, err
	}
	if project.SessionID != sessionID || project.AccountScopeID != principal.AccountScopeID || (project.UserID != "" && project.UserID != principal.UserID) {
		return FrameInspectionResult{}, errors.New("video project ownership does not match authenticated principal")
	}
	revision, ok, err := s.store.GetVideoProjectRevision(principal.AccountScopeID, sessionID, projectID, revisionID)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("video project revision %q not found", revisionID)
		}
		return FrameInspectionResult{}, err
	}
	if revision.SessionID != sessionID || revision.ProjectID != projectID || revision.AccountScopeID != principal.AccountScopeID || (revision.UserID != "" && revision.UserID != principal.UserID) {
		return FrameInspectionResult{}, errors.New("video revision ownership does not match authenticated principal")
	}
	timestamps, err := normalizeInspectionTimestamps(req, revision.Timeline.TotalDurationMs)
	if err != nil {
		return FrameInspectionResult{}, err
	}
	if _, err := s.runner.LookPath("ffmpeg"); err != nil {
		return FrameInspectionResult{}, fmt.Errorf("video frame inspection is unavailable because the checked ffmpeg runtime is missing: %w", err)
	}
	jobDir, err := os.MkdirTemp(s.cfg.WorkDir, "swarm-video-inspect-")
	if err != nil {
		return FrameInspectionResult{}, fmt.Errorf("create private frame inspection storage: %w", err)
	}
	defer os.RemoveAll(jobDir)
	timeout := req.Timeout
	if timeout <= 0 || timeout > DefaultInspectionTimeout {
		timeout = DefaultInspectionTimeout
	}
	inspectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	inputs, err := s.materializeTimelineInputs(inspectCtx, principal, session, req.WorkspacePath, jobDir, revision.Timeline)
	if err != nil {
		return FrameInspectionResult{}, fmt.Errorf("materialize exact revision for frame inspection: %w", err)
	}
	previewPath := filepath.Join(jobDir, "inspection.mp4")
	plan, err := BuildFFmpegCommandLine(revision.Timeline, inputs, previewPath)
	if err != nil {
		return FrameInspectionResult{}, fmt.Errorf("build exact revision inspection plan: %w", err)
	}
	if _, err := s.runner.RunCommand(inspectCtx, "ffmpeg", plan.FFmpegArgs...); err != nil {
		return FrameInspectionResult{}, fmt.Errorf("render exact revision for frame inspection: %w", err)
	}
	maxWidth := req.MaxWidth
	if maxWidth <= 0 {
		maxWidth = DefaultInspectionWidth
	}
	if maxWidth > MaxInspectionWidth {
		return FrameInspectionResult{}, fmt.Errorf("max_width %d exceeds inspection limit %d", maxWidth, MaxInspectionWidth)
	}
	dims := plan.Dimensions
	width := min(dims.Width, maxWidth)
	if width < 2 {
		width = 2
	}
	if width%2 != 0 {
		width--
	}
	height := int(math.Round(float64(dims.Height) * float64(width) / float64(dims.Width)))
	if height < 2 {
		height = 2
	}
	if height%2 != 0 {
		height--
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = "inspect"
	}
	collectionID := "vframes_" + projectID + "_" + revisionID
	result := FrameInspectionResult{ProjectID: projectID, RevisionID: revisionID, RevisionEventSeq: revision.EventSeq, DurationMs: plan.TotalDurationMs, Width: width, Height: height, Frames: make([]InspectedFrame, 0, len(timestamps))}
	artifactSessionID := strings.TrimSpace(req.ArtifactSessionID)
	if artifactSessionID == "" {
		artifactSessionID = sessionID
	}
	artifactPrincipal := artifact.Principal{SessionID: artifactSessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	var totalPNGBytes int64
	for index, timestamp := range timestamps {
		framePath := filepath.Join(jobDir, fmt.Sprintf("frame_%02d.png", index+1))
		seekTimestamp := inspectionSeekTimestamp(timestamp, plan.TotalDurationMs, plan.FPS)
		args := []string{"-v", "error", "-nostdin", "-ss", fmt.Sprintf("%.3f", float64(seekTimestamp)/1000), "-i", previewPath, "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:%d:flags=lanczos", width, height), "-f", "image2", "-c:v", "png", "-y", framePath}
		if _, err := s.runner.RunCommand(inspectCtx, "ffmpeg", args...); err != nil {
			return FrameInspectionResult{}, fmt.Errorf("extract frame at %dms: %w", timestamp, err)
		}
		info, err := os.Stat(framePath)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxInspectionPNGBytes {
			return FrameInspectionResult{}, fmt.Errorf("frame at %dms did not produce a bounded PNG", timestamp)
		}
		totalPNGBytes += info.Size()
		if totalPNGBytes > MaxInspectionTotalBytes {
			return FrameInspectionResult{}, errors.New("inspection PNG output exceeds total byte limit")
		}
		prefix := make([]byte, 8)
		file, openErr := os.Open(framePath)
		if openErr != nil {
			return FrameInspectionResult{}, openErr
		}
		_, readErr := io.ReadFull(file, prefix)
		file.Close()
		if readErr != nil || !bytes.Equal(prefix, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
			return FrameInspectionResult{}, fmt.Errorf("frame at %dms is not a valid PNG", timestamp)
		}
		digest, err := computeFileSHA256(framePath)
		if err != nil {
			return FrameInspectionResult{}, err
		}
		identityDigest := sha256.Sum256([]byte(projectID + "\x00" + revisionID + "\x00" + fmt.Sprint(timestamp)))
		variantID := fmt.Sprintf("vframe_%03d_%d_%s", index+1, timestamp, hex.EncodeToString(identityDigest[:6]))
		variant, err := s.artifacts.CreateFromFile(inspectCtx, artifactPrincipal, artifact.CreateFileInput{CreateInput: artifact.CreateInput{
			RequestID: fmt.Sprintf("%s:%s:%s:%d:%s", requestID, projectID, revisionID, timestamp, digest), CollectionID: collectionID,
			CollectionName: project.Title + " frame inspection", CollectionDescription: fmt.Sprintf("Bounded visual evidence for exact revision %s", revisionID), VariantID: variantID,
			Filename: fmt.Sprintf("frame_%03d_%dms.png", index+1, timestamp), MediaType: "image/png", Presentation: pebblestore.SessionArtifactPresentation{Kind: "image", Label: fmt.Sprintf("Frame at %dms", timestamp), Previewable: true, Width: width, Height: height},
			VideoProjectID: projectID, VideoRevisionID: revisionID, VideoRevisionEventSeq: revision.EventSeq,
		}, SourcePath: framePath})
		if err != nil {
			return FrameInspectionResult{}, fmt.Errorf("publish inspected frame at %dms: %w", timestamp, err)
		}
		if variant.Status != pebblestore.SessionArtifactStatusReady || variant.EventSeq == 0 {
			return FrameInspectionResult{}, fmt.Errorf("published frame at %dms is not ready", timestamp)
		}
		result.Frames = append(result.Frames, InspectedFrame{TimestampMs: timestamp, Artifact: pebblestore.SessionArtifactSelectionReference{SessionID: variant.SessionID, CollectionID: variant.CollectionID, VariantID: variant.ID, EventSeq: variant.EventSeq}})
	}
	return result, nil
}

func inspectionSeekTimestamp(timestamp, durationMs int64, fps float64) int64 {
	if timestamp <= 0 || durationMs <= 1 {
		return 0
	}
	if fps <= 0 {
		fps = 30
	}
	frameDurationMs := int64(math.Ceil(1000 / fps))
	lastFrameMs := durationMs - frameDurationMs
	if lastFrameMs < 0 {
		lastFrameMs = 0
	}
	return min(timestamp, lastFrameMs)
}

func normalizeInspectionTimestamps(req FrameInspectionRequest, durationMs int64) ([]int64, error) {
	if durationMs <= 0 || durationMs > MaxInspectionDurationMs {
		return nil, fmt.Errorf("exact revision duration must be between 1ms and %dms for bounded frame inspection", MaxInspectionDurationMs)
	}
	if len(req.TimestampsMs) == 0 && len(req.Ranges) == 0 {
		return nil, errors.New("inspect frames requires timestamps_ms and/or ranges")
	}
	if len(req.TimestampsMs)+len(req.Ranges) > MaxInspectionFrames {
		return nil, fmt.Errorf("inspection request exceeds maximum item count %d", MaxInspectionFrames)
	}
	values := append([]int64(nil), req.TimestampsMs...)
	for index, item := range req.Ranges {
		if item.Count <= 0 || item.Count > MaxInspectionFrames {
			return nil, fmt.Errorf("range %d count must be between 1 and %d", index, MaxInspectionFrames)
		}
		if item.StartMs < 0 || item.EndMs <= item.StartMs || item.EndMs > durationMs || item.EndMs-item.StartMs > MaxInspectionRangeMs {
			return nil, fmt.Errorf("range %d must be within the revision and no longer than %dms", index, MaxInspectionRangeMs)
		}
		if item.Count == 1 {
			values = append(values, item.StartMs)
			continue
		}
		span := item.EndMs - item.StartMs
		for n := 0; n < item.Count; n++ {
			values = append(values, item.StartMs+(span*int64(n))/int64(item.Count-1))
		}
	}
	if len(values) == 0 || len(values) > MaxInspectionFrames {
		return nil, fmt.Errorf("inspection resolves to between 1 and %d frames", MaxInspectionFrames)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	out := values[:0]
	for _, value := range values {
		if value < 0 || value >= durationMs {
			return nil, fmt.Errorf("timestamp %dms is outside exact revision duration %dms", value, durationMs)
		}
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	if out[len(out)-1]-out[0] > MaxInspectionSpanMs {
		return nil, fmt.Errorf("inspection timestamp span exceeds maximum %dms", MaxInspectionSpanMs)
	}
	return out, nil
}

func (s *Service) RenderJob(ctx context.Context, principal identity.Principal, req RenderJobRequest) (result pebblestore.VideoRenderJobSnapshot, retErr error) {
	if s == nil || s.store == nil {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("videorender service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("authenticated principal is required")
	}

	sessionID := strings.TrimSpace(req.SessionID)
	projectID := strings.TrimSpace(req.ProjectID)
	jobID := strings.TrimSpace(req.JobID)
	if sessionID == "" || projectID == "" || jobID == "" {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("session_id, project_id, and job_id are required")
	}

	session, ok, err := s.store.GetSession(sessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		return pebblestore.VideoRenderJobSnapshot{}, err
	}
	if session.AccountScopeID != principal.AccountScopeID || (session.UserID != "" && session.UserID != principal.UserID) {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("session ownership does not match authenticated principal")
	}

	job, ok, err := s.store.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("render job %q not found", jobID)
		}
		return pebblestore.VideoRenderJobSnapshot{}, err
	}
	if job.Status == pebblestore.VideoRenderJobStatusReady {
		return job, nil
	}
	if !s.admit(jobID) {
		return job, fmt.Errorf("render job %q is already admitted by this process", jobID)
	}
	defer s.release(jobID)
	if job.Status == pebblestore.VideoRenderJobStatusRendering {
		return job, fmt.Errorf("render job %q is already rendering", jobID)
	}
	if job.Status == pebblestore.VideoRenderJobStatusCancelled || job.Status == pebblestore.VideoRenderJobStatusFailed {
		return job, fmt.Errorf("render job %q is in terminal state %s", jobID, job.Status)
	}
	if job.ProjectID != projectID {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("render job %q does not belong to project %q", jobID, projectID)
	}

	project, ok, err := s.store.GetVideoProject(principal.AccountScopeID, sessionID, projectID)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("video project %q not found", projectID)
		}
		return pebblestore.VideoRenderJobSnapshot{}, err
	}

	revID := strings.TrimSpace(req.RevisionID)
	if revID == "" {
		revID = job.RevisionID
	}
	if revID == "" {
		revID = project.CurrentRevisionID
	}

	if job.RevisionID != "" && revID != job.RevisionID {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("render job %q is pinned to revision %q", jobID, job.RevisionID)
	}
	revision, ok, err := s.store.GetVideoProjectRevision(principal.AccountScopeID, sessionID, projectID, revID)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("video project revision %q not found", revID)
		}
		return pebblestore.VideoRenderJobSnapshot{}, err
	}

	timeline := revision.Timeline
	if len(timeline.Clips) == 0 {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("timeline contains no clips to render")
	}
	if len(timeline.Clips) > s.cfg.MaxClips {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("timeline clip count %d exceeds maximum limit %d", len(timeline.Clips), s.cfg.MaxClips)
	}

	// Update job status to rendering
	job, err = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		JobID:          jobID,
		Status:         pebblestore.VideoRenderJobStatusRendering,
		ExpectedStatus: pebblestore.VideoRenderJobStatusQueued,
		Progress:       0.05,
		NowUnixMs:      time.Now().UnixMilli(),
	})
	if err != nil {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("failed to transition render job to rendering: %w", err)
	}

	// Setup private scratch dir
	jobDir, err := os.MkdirTemp(s.cfg.WorkDir, "swarm-video-render-"+jobID+"-")
	if err != nil {
		s.failJob(principal, sessionID, jobID, "storage_error", "failed to create private job storage")
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("create private job storage: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(jobDir)
	}()

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = s.cfg.DefaultTimeout
	}
	renderCtx, cancel := context.WithTimeout(ctx, timeout)
	s.mu.Lock()
	s.cancels[jobID] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.cancels, jobID)
		s.mu.Unlock()
	}()

	// Materialize inputs
	materialized, err := s.materializeTimelineInputs(renderCtx, principal, session, req.WorkspacePath, jobDir, timeline)
	if err != nil {
		s.failJob(principal, sessionID, jobID, "input_materialization_error", err.Error())
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("materialize inputs: %w", err)
	}

	// Build FFmpeg arguments internally
	outputPath := filepath.Join(jobDir, "render_output.mp4")
	plan, err := BuildFFmpegCommandLine(timeline, materialized, outputPath)
	if err != nil {
		s.failJob(principal, sessionID, jobID, "timeline_plan_error", err.Error())
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("build timeline plan: %w", err)
	}

	// Update progress
	_, _ = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		JobID:          jobID,
		Status:         pebblestore.VideoRenderJobStatusRendering,
		Progress:       0.30,
		NowUnixMs:      time.Now().UnixMilli(),
	})

	// Run FFmpeg command
	_, err = s.runner.RunCommand(renderCtx, "ffmpeg", plan.FFmpegArgs...)
	if err != nil {
		if renderCtx.Err() != nil {
			current, currentOK, _ := s.store.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
			if currentOK && current.Status == pebblestore.VideoRenderJobStatusCancelled {
				return pebblestore.VideoRenderJobSnapshot{}, renderCtx.Err()
			}
			if ctx.Err() != nil {
				// Losing the daemon/request lifetime is an interruption, not a user
				// cancellation. Put the pinned job back on the durable queue so the
				// next recovery pass can safely rematerialize private scratch and retry.
				_, _ = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
					AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
					SessionID: sessionID, JobID: jobID,
					Status: pebblestore.VideoRenderJobStatusQueued, ExpectedStatus: pebblestore.VideoRenderJobStatusRendering,
					NowUnixMs: time.Now().UnixMilli(),
				})
			} else {
				s.cancelJob(principal, sessionID, jobID, "Render timed out")
			}
			return pebblestore.VideoRenderJobSnapshot{}, renderCtx.Err()
		}
		s.failJob(principal, sessionID, jobID, "ffmpeg_execution_error", err.Error())
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("ffmpeg execution failed: %w", err)
	}

	// Validate output file
	outInfo, err := os.Stat(outputPath)
	if err != nil || !outInfo.Mode().IsRegular() || outInfo.Size() <= 0 {
		s.failJob(principal, sessionID, jobID, "output_missing", "rendered video output file is missing or invalid")
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("rendered video output file is missing or invalid")
	}
	if outInfo.Size() > s.cfg.MaxRenderBytes {
		s.failJob(principal, sessionID, jobID, "quota_exceeded", "rendered video output exceeds size quota")
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("rendered video output %d bytes exceeds quota %d bytes", outInfo.Size(), s.cfg.MaxRenderBytes)
	}

	// Compute digest
	digest, err := computeFileSHA256(outputPath)
	if err != nil {
		s.failJob(principal, sessionID, jobID, "digest_error", "failed to compute output digest")
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("compute digest: %w", err)
	}

	// Update progress to 0.85
	_, _ = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		SessionID:      sessionID,
		JobID:          jobID,
		Status:         pebblestore.VideoRenderJobStatusRendering,
		Progress:       0.85,
		NowUnixMs:      time.Now().UnixMilli(),
	})

	// Import into canonical artifact authority
	if s.artifacts == nil {
		s.failJob(principal, sessionID, jobID, "artifact_authority_missing", "artifact authority is not configured")
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("artifact authority is not configured")
	}

	artifactPrincipal := artifact.Principal{
		SessionID:      sessionID,
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
	}

	collectionID := fmt.Sprintf("vproj_%s", projectID)
	variantID := fmt.Sprintf("vrender_%s", jobID)
	safeTitle := sanitizeFilename(project.Title)
	if safeTitle == "" {
		safeTitle = "video"
	}
	filename := fmt.Sprintf("%s_rev%d.mp4", safeTitle, revision.RevisionNumber)

	outputPreset := timeline.OutputPreset
	if outputPreset == "" {
		outputPreset = pebblestore.VideoPresetLandscape1080p
	}
	outputRequirements, err := artifact.ResolveOutputRequirements(&artifact.OutputRequirementsInput{
		Width:  plan.Dimensions.Width,
		Height: plan.Dimensions.Height,
	})
	if err != nil {
		s.failJob(principal, sessionID, jobID, "artifact_requirements_error", err.Error())
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("resolve rendered artifact output requirements: %w", err)
	}

	artifactVariant, err := s.artifacts.CreateFromFile(renderCtx, artifactPrincipal, artifact.CreateFileInput{
		CreateInput: artifact.CreateInput{
			RequestID:             fmt.Sprintf("render:%s:%s", jobID, digest),
			CollectionID:          collectionID,
			CollectionName:        project.Title,
			CollectionDescription: project.Description,
			VariantID:             variantID,
			Filename:              filename,
			MediaType:             "video/mp4",
			Presentation: pebblestore.SessionArtifactPresentation{
				Kind:        "video",
				Previewable: true,
				Width:       plan.Dimensions.Width,
				Height:      plan.Dimensions.Height,
			},
			OutputRequirements: outputRequirements,
		},
		SourcePath: outputPath,
	})
	if err != nil {
		s.failJob(principal, sessionID, jobID, "artifact_import_error", err.Error())
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("import rendered artifact: %w", err)
	}

	if artifactVariant.Status != pebblestore.SessionArtifactStatusReady {
		s.failJob(principal, sessionID, jobID, "artifact_not_ready", "imported artifact variant is not in ready status")
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("imported artifact variant %q is not in ready status (status: %s)", artifactVariant.ID, artifactVariant.Status)
	}

	artifactRef := &pebblestore.SessionArtifactSelectionReference{
		SessionID:    sessionID,
		CollectionID: collectionID,
		VariantID:    artifactVariant.ID,
		EventSeq:     artifactVariant.EventSeq,
	}

	// Complete render job
	readyJob, err := s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID:     principal.AccountScopeID,
		UserID:             principal.UserID,
		SessionID:          sessionID,
		JobID:              jobID,
		Status:             pebblestore.VideoRenderJobStatusReady,
		Progress:           1.0,
		OutputPreset:       outputPreset,
		OutputWidth:        plan.Dimensions.Width,
		OutputHeight:       plan.Dimensions.Height,
		OutputFPS:          plan.FPS,
		OutputDurationMs:   plan.TotalDurationMs,
		OutputSizeBytes:    outInfo.Size(),
		OutputDigestSHA256: digest,
		OutputArtifact:     artifactRef,
		NowUnixMs:          time.Now().UnixMilli(),
	})
	if err != nil {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("complete render job in session store: %w", err)
	}

	return readyJob, nil
}

func (s *Service) materializeTimelineInputs(ctx context.Context, principal identity.Principal, session pebblestore.SessionSnapshot, workspacePath string, jobDir string, timeline pebblestore.VideoProjectTimeline) ([]MaterializedInput, error) {
	var inputs []MaterializedInput

	var registeredRoots map[string]struct{}
	var workspaceID string
	if s.workspace != nil && workspacePath != "" {
		res, err := s.workspace.ListSourceMediaDirectoriesForPrincipal(principal, workspacePath)
		if err != nil {
			return nil, fmt.Errorf("resolve session workspace source authority: %w", err)
		}
		workspaceID = res.WorkspaceID
		registeredRoots = make(map[string]struct{}, len(res.SourceMediaDirectories))
		for _, root := range res.SourceMediaDirectories {
			registeredRoots[filepath.Clean(root)] = struct{}{}
		}
	}

	for i, clip := range timeline.Clips {
		if !clip.Visible && clip.SourceKind != pebblestore.VideoClipSourceKindSourceAudio {
			continue
		}

		input := MaterializedInput{
			Index:           len(inputs),
			ClipID:          clip.ID,
			Volume:          clip.Volume,
			Muted:           clip.Muted,
			StartMs:         clip.SourceStartMs,
			EndMs:           clip.SourceEndMs,
			DurationMs:      clip.DurationMs,
			Track:           clip.Track,
			Layer:           clip.Layer,
			TimelineStartMs: clip.TimelineStartMs,
			TimelineEndMs:   clip.TimelineEndMs,
			Captions:        clip.Captions,
			IsVideo:         true,
		}

		switch clip.SourceKind {
		case pebblestore.VideoClipSourceKindSourceVideo:
			ref := strings.TrimSpace(clip.SourceRef)
			if ref == "" {
				return nil, fmt.Errorf("clip %d is source_video but missing source_ref", i)
			}
			wsIDs := sessionVideoWorkspaceIDs(session)
			if workspaceID != "" {
				wsIDs = append([]string{workspaceID}, wsIDs...)
			}
			var record pebblestore.VideoSourceRecord
			var found bool
			for _, wsID := range wsIDs {
				r, ok, err := s.store.GetVideoSourceRecord(principal.AccountScopeID, wsID, ref)
				if err == nil && ok {
					record = r
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("clip %d referenced video source %q not found in workspace scope", i, ref)
			}
			if registeredRoots != nil && len(registeredRoots) > 0 {
				if _, ok := registeredRoots[filepath.Clean(record.RootPath)]; !ok {
					return nil, fmt.Errorf("clip %d referenced video source %q no longer belongs to registered source root", i, ref)
				}
			}
			if err := pebblestore.ValidateVideoSourceRecord(record); err != nil {
				return nil, fmt.Errorf("clip %d source video fingerprint mismatch or invalid: %w", i, err)
			}

			srcFile, err := pebblestore.OpenValidatedVideoSource(record)
			if err != nil {
				return nil, fmt.Errorf("open validated source clip %d: %w", i, err)
			}
			destPath := filepath.Join(jobDir, fmt.Sprintf("input_%d_%s", input.Index, filepath.Base(record.RelativePath)))
			if err := copyBoundedFile(destPath, srcFile, record.SizeBytes); err != nil {
				srcFile.Close()
				return nil, fmt.Errorf("materialize source clip %d: %w", i, err)
			}
			srcFile.Close()
			input.FilePath = destPath
			input.HasAudio, err = probeInputHasAudio(ctx, s.runner, destPath)
			if err != nil {
				return nil, fmt.Errorf("probe source clip %d audio streams: %w", i, err)
			}

		case pebblestore.VideoClipSourceKindSourceAudio:
			if clip.AudioSource == nil {
				return nil, fmt.Errorf("clip %d is source_audio but missing audio_source", i)
			}
			ref := strings.TrimSpace(clip.AudioSource.Ref)
			wsIDs := sessionVideoWorkspaceIDs(session)
			if workspaceID != "" {
				wsIDs = append([]string{workspaceID}, wsIDs...)
			}
			var record pebblestore.AudioSourceRecord
			var found bool
			for _, wsID := range wsIDs {
				r, ok, readErr := s.store.GetAudioSourceRecord(principal.AccountScopeID, wsID, ref)
				if readErr != nil {
					return nil, fmt.Errorf("resolve source audio clip %d: %w", i, readErr)
				}
				if ok {
					record = r
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("clip %d referenced audio source %q not found in workspace scope", i, ref)
			}
			if registeredRoots != nil {
				if _, ok := registeredRoots[filepath.Clean(record.RootPath)]; !ok {
					return nil, fmt.Errorf("clip %d referenced audio source %q no longer belongs to registered source root", i, ref)
				}
			}
			if record.DisplayName != clip.AudioSource.Name || record.MIMEType != clip.AudioSource.MIMEType ||
				record.SizeBytes != clip.AudioSource.SizeBytes || record.SourceFingerprint != clip.AudioSource.SourceFingerprint ||
				record.FingerprintVersion != clip.AudioSource.FingerprintVersion {
				return nil, fmt.Errorf("clip %d audio source %q exact metadata is stale or inconsistent", i, ref)
			}
			if err := pebblestore.ValidateAudioSourceRecord(record); err != nil {
				return nil, fmt.Errorf("clip %d source audio fingerprint mismatch or invalid: %w", i, err)
			}
			srcFile, err := pebblestore.OpenValidatedAudioSource(record)
			if err != nil {
				return nil, fmt.Errorf("open validated source audio clip %d: %w", i, err)
			}
			destPath := filepath.Join(jobDir, fmt.Sprintf("input_%d_%s", input.Index, filepath.Base(record.RelativePath)))
			if err := copyBoundedFile(destPath, srcFile, record.SizeBytes); err != nil {
				srcFile.Close()
				return nil, fmt.Errorf("materialize source audio clip %d: %w", i, err)
			}
			srcFile.Close()
			input.FilePath = destPath
			input.IsVideo = false
			input.IsAudio = true
			input.HasAudio = true

		case pebblestore.VideoClipSourceKindManagedArtifact:
			var targetRef *pebblestore.SessionArtifactSelectionReference
			if clip.ArtifactRef != nil {
				targetRef = clip.ArtifactRef
			}
			if targetRef == nil {
				return nil, fmt.Errorf("clip %d is managed_artifact but missing artifact_ref", i)
			}
			targetSessionID := targetRef.SessionID
			if targetSessionID == "" {
				targetSessionID = session.ID
			}

			variant, ok, err := s.store.GetSessionArtifactVariant(principal.AccountScopeID, targetSessionID, targetRef.CollectionID, targetRef.VariantID)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("artifact variant %q not found in collection %q", targetRef.VariantID, targetRef.CollectionID)
				}
				return nil, fmt.Errorf("resolve clip %d artifact: %w", i, err)
			}
			if variant.Status != pebblestore.SessionArtifactStatusReady {
				return nil, fmt.Errorf("clip %d referenced artifact variant %q is not in ready status (status: %s)", i, variant.ID, variant.Status)
			}
			if targetRef.EventSeq == 0 || targetRef.EventSeq != variant.EventSeq {
				return nil, fmt.Errorf("clip %d referenced artifact variant %q with a stale or missing event sequence", i, variant.ID)
			}

			destPath := filepath.Join(jobDir, fmt.Sprintf("input_%d_%s", input.Index, variant.Filename))
			body, _, err := s.artifacts.ReadReference(ctx, artifact.Principal{SessionID: session.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, *targetRef, s.cfg.MaxRenderBytes)
			if err != nil {
				return nil, fmt.Errorf("read artifact clip %d: %w", i, err)
			}
			if err := os.WriteFile(destPath, body, 0o600); err != nil {
				return nil, err
			}
			input.FilePath = destPath
			if strings.HasPrefix(variant.MediaType, "image/") {
				input.IsImage = true
				input.IsVideo = false
				input.HasAudio = false
			} else {
				input.HasAudio, err = probeInputHasAudio(ctx, s.runner, destPath)
				if err != nil {
					return nil, fmt.Errorf("probe artifact clip %d audio streams: %w", i, err)
				}
			}

		case pebblestore.VideoClipSourceKindColor:
			input.IsSynthetic = true
			input.HasAudio = false
			// Lavfi synthetic color input
			input.FilePath = ""

		default:
			return nil, fmt.Errorf("clip %d has unsupported source_kind %q", i, clip.SourceKind)
		}

		// Check DesignInput if present
		if clip.DesignInput != nil {
			designRef := clip.DesignInput
			designSessionID := designRef.SessionID
			if designSessionID == "" {
				designSessionID = session.ID
			}
			designVariant, ok, err := s.store.GetSessionArtifactVariant(principal.AccountScopeID, designSessionID, designRef.CollectionID, designRef.VariantID)
			if err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("design input variant %q not found in collection %q", designRef.VariantID, designRef.CollectionID)
				}
				return nil, fmt.Errorf("resolve design input clip %d: %w", i, err)
			}
			if designVariant.Status != pebblestore.SessionArtifactStatusReady {
				return nil, fmt.Errorf("clip %d design input variant %q is not ready", i, designVariant.ID)
			}
			if designRef.EventSeq == 0 || designRef.EventSeq != designVariant.EventSeq {
				return nil, fmt.Errorf("clip %d design input variant %q has a stale or missing event sequence", i, designVariant.ID)
			}
			designPath := filepath.Join(jobDir, fmt.Sprintf("design_%d_%s", input.Index, designVariant.Filename))
			designBody, _, err := s.artifacts.ReadReference(ctx, artifact.Principal{SessionID: session.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, pebblestore.SessionArtifactSelectionReference{SessionID: designSessionID, CollectionID: designRef.CollectionID, VariantID: designRef.VariantID, EventSeq: designRef.EventSeq}, s.cfg.MaxRenderBytes)
			if err != nil {
				return nil, fmt.Errorf("read design input clip %d: %w", i, err)
			}
			if err := os.WriteFile(designPath, designBody, 0o600); err != nil {
				return nil, err
			}
			input.DesignInputs = append(input.DesignInputs, MaterializedInput{
				FilePath:    designPath,
				OverlayMode: designRef.OverlayMode,
			})
		}

		inputs = append(inputs, input)
	}

	return inputs, nil
}

func probeInputHasAudio(ctx context.Context, runner CommandRunner, filePath string) (bool, error) {
	if runner == nil {
		return false, errors.New("video command runner is not configured")
	}
	if _, err := runner.LookPath("ffprobe"); err != nil {
		return false, fmt.Errorf("ffprobe is required to inspect input streams: %w", err)
	}
	if prober, ok := runner.(interface {
		probeAudioStream(context.Context, string) (bool, error)
	}); ok {
		return prober.probeAudioStream(ctx, filePath)
	}
	output, err := runner.RunCommand(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "a",
		"-show_entries", "stream=index",
		"-of", "csv=p=0",
		filePath,
	)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(output)) != "", nil
}

// StartRenderJob starts a tracked background render. Callers that own service
// shutdown can use WaitForIdle before closing durable dependencies.
func (s *Service) StartRenderJob(principal identity.Principal, req RenderJobRequest) {
	if s == nil {
		return
	}
	queueGrace := req.QueueGrace
	if queueGrace < 0 {
		queueGrace = 0
	}
	if queueGrace > MaxQueueGracePeriod {
		queueGrace = MaxQueueGracePeriod
	}
	startCtx, cancelStart := context.WithCancel(context.Background())
	s.mu.Lock()
	if _, pending := s.pendingStarts[req.JobID]; pending {
		s.mu.Unlock()
		cancelStart()
		return
	}
	if _, rendering := s.cancels[req.JobID]; rendering {
		s.mu.Unlock()
		cancelStart()
		return
	}
	s.pendingStarts[req.JobID] = cancelStart
	s.mu.Unlock()
	s.workerMu.Lock()
	if s.workerN == 0 {
		s.idle = make(chan struct{})
	}
	s.workerN++
	s.workers.Add(1)
	s.workerMu.Unlock()
	go func() {
		defer func() {
			cancelStart()
			s.mu.Lock()
			delete(s.pendingStarts, req.JobID)
			s.mu.Unlock()
			s.workers.Done()
			s.workerMu.Lock()
			s.workerN--
			if s.workerN == 0 {
				close(s.idle)
			}
			s.workerMu.Unlock()
		}()
		if queueGrace > 0 {
			timer := time.NewTimer(queueGrace)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-startCtx.Done():
				return
			}
		}
		_, _ = s.RenderJob(startCtx, principal, req)
	}()
}

// WaitForIdle waits for all background renders started through StartRenderJob.
func (s *Service) WaitForIdle(ctx context.Context) error {
	if s == nil {
		return errors.New("videorender service is not configured")
	}
	s.workerMu.Lock()
	if s.workerN == 0 {
		s.workerMu.Unlock()
		return nil
	}
	idle := s.idle
	s.workerMu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) CancelRenderJob(ctx context.Context, principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, error) {
	if s == nil || s.store == nil {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("videorender service is not configured")
	}
	if !principal.Valid() {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("authenticated principal is required")
	}
	job, ok, err := s.store.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("render job %q not found", jobID)
		}
		return pebblestore.VideoRenderJobSnapshot{}, err
	}
	if job.UserID != "" && job.UserID != principal.UserID {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("render job ownership does not match authenticated principal")
	}
	if job.Status == pebblestore.VideoRenderJobStatusCancelled {
		return job, nil
	}
	if job.Status == pebblestore.VideoRenderJobStatusReady || job.Status == pebblestore.VideoRenderJobStatusFailed {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("cannot cancel terminal render job in status %s", job.Status)
	}
	s.mu.Lock()
	cancelPending := s.pendingStarts[jobID]
	cancel := s.cancels[jobID]
	s.mu.Unlock()
	if cancelPending != nil {
		cancelPending()
	}
	if cancel != nil {
		cancel()
	}
	for attempts := 0; attempts < 3; attempts++ {
		job, ok, err := s.store.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("render job %q not found", jobID)
			}
			return pebblestore.VideoRenderJobSnapshot{}, err
		}
		if job.Status == pebblestore.VideoRenderJobStatusCancelled {
			return job, nil
		}
		if job.Status == pebblestore.VideoRenderJobStatusReady || job.Status == pebblestore.VideoRenderJobStatusFailed {
			return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("cannot cancel terminal render job in status %s", job.Status)
		}
		updated, err := s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
			AccountScopeID: principal.AccountScopeID,
			UserID:         principal.UserID,
			SessionID:      sessionID,
			JobID:          jobID,
			Status:         pebblestore.VideoRenderJobStatusCancelled,
			FailureCode:    "cancelled_by_user",
			FailureReason:  "Render job was cancelled",
			ExpectedStatus: job.Status,
			NowUnixMs:      time.Now().UnixMilli(),
		})
		if err == nil {
			return updated, nil
		}
		if !strings.Contains(err.Error(), "status conflict") {
			return pebblestore.VideoRenderJobSnapshot{}, err
		}
	}
	return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("render job %q changed status while cancellation was applied", jobID)
}

// RecoverJobs is the daemon-startup recovery authority. It discovers a bounded
// set of durable queued/orphaned jobs, preserves already-imported output, and
// otherwise re-admits each pinned revision for rendering.
func (s *Service) RecoverJobs(ctx context.Context) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("videorender service is not configured")
	}
	jobs, err := s.store.ListRecoverableVideoRenderJobs(s.cfg.RecoveryLimit)
	if err != nil {
		return 0, err
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		s.recoverJob(ctx, job)
	}
	return len(jobs), nil
}

func (s *Service) recoverJob(ctx context.Context, job pebblestore.VideoRenderJobSnapshot) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: job.AccountScopeID, UserID: job.UserID, SessionID: job.SessionID}
	if !principal.Valid() || strings.TrimSpace(job.SessionID) == "" || strings.TrimSpace(job.ProjectID) == "" || strings.TrimSpace(job.RevisionID) == "" {
		s.failRecoveryJob(job, job.Status, "recovery_metadata_invalid", "Render recovery requires durable account, session, project, and pinned revision metadata")
		return
	}
	if s.restoreReadyArtifact(job) {
		return
	}
	if job.Status == pebblestore.VideoRenderJobStatusRendering {
		if _, err := s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
			AccountScopeID: job.AccountScopeID, UserID: job.UserID, SessionID: job.SessionID, JobID: job.ID,
			Status: pebblestore.VideoRenderJobStatusQueued, ExpectedStatus: pebblestore.VideoRenderJobStatusRendering,
			NowUnixMs: time.Now().UnixMilli(),
		}); err != nil {
			return // another local caller or daemon instance won the durable CAS
		}
	}
	session, ok, err := s.store.GetSession(job.SessionID)
	if err != nil || !ok || session.AccountScopeID != job.AccountScopeID || (session.UserID != "" && session.UserID != job.UserID) {
		s.failRecoveryJob(job, pebblestore.VideoRenderJobStatusQueued, "recovery_session_unavailable", "Render could not resume because its owning session is unavailable or ownership changed")
		return
	}
	_, renderErr := s.RenderJob(ctx, principal, RenderJobRequest{
		SessionID: job.SessionID, ProjectID: job.ProjectID, RevisionID: job.RevisionID,
		JobID: job.ID, WorkspacePath: session.WorkspacePath,
	})
	if renderErr == nil || ctx.Err() != nil {
		return
	}
	current, ok, _ := s.store.GetVideoRenderJob(job.AccountScopeID, job.SessionID, job.ID)
	if ok && current.Status == pebblestore.VideoRenderJobStatusQueued {
		s.failRecoveryJob(job, pebblestore.VideoRenderJobStatusQueued, "recovery_unresumable", renderErr.Error())
	}
}

func (s *Service) restoreReadyArtifact(job pebblestore.VideoRenderJobSnapshot) bool {
	collectionID, variantID := "vproj_"+job.ProjectID, "vrender_"+job.ID
	variant, ok, err := s.store.GetSessionArtifactVariant(job.AccountScopeID, job.SessionID, collectionID, variantID)
	if err != nil || !ok || variant.SessionID != job.SessionID || variant.CollectionID != collectionID || variant.ID != variantID || variant.Status != pebblestore.SessionArtifactStatusReady || variant.EventSeq == 0 {
		return false
	}
	_, err = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID: job.AccountScopeID, UserID: job.UserID, SessionID: job.SessionID, JobID: job.ID,
		Status: pebblestore.VideoRenderJobStatusReady, ExpectedStatus: job.Status, Progress: 1,
		OutputSizeBytes: variant.Size,
		OutputArtifact:  &pebblestore.SessionArtifactSelectionReference{SessionID: job.SessionID, CollectionID: collectionID, VariantID: variant.ID, EventSeq: variant.EventSeq},
		NowUnixMs:       time.Now().UnixMilli(),
	})
	return err == nil
}

func (s *Service) failRecoveryJob(job pebblestore.VideoRenderJobSnapshot, expected, code, reason string) {
	_, _ = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID: job.AccountScopeID, UserID: job.UserID, SessionID: job.SessionID, JobID: job.ID,
		Status: pebblestore.VideoRenderJobStatusFailed, ExpectedStatus: expected,
		FailureCode: code, FailureReason: reason, NowUnixMs: time.Now().UnixMilli(),
	})
}

// ReconcileInterruptedJobs remains a scoped compatibility entrypoint. Startup
// recovery uses RecoverJobs so queued work is also discovered globally.
func (s *Service) ReconcileInterruptedJobs(ctx context.Context, accountScopeID, sessionID, projectID string) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("videorender service is not configured")
	}
	jobs, err := s.store.ListVideoRenderJobs(accountScopeID, sessionID, projectID, s.cfg.RecoveryLimit)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, job := range jobs {
		if job.Status != pebblestore.VideoRenderJobStatusQueued && job.Status != pebblestore.VideoRenderJobStatusRendering {
			continue
		}
		count++
		s.recoverJob(ctx, job)
	}
	return count, nil
}

func (s *Service) admit(jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cancels[jobID]; exists {
		return false
	}
	s.cancels[jobID] = nil
	return true
}

func (s *Service) release(jobID string) {
	s.mu.Lock()
	delete(s.cancels, jobID)
	s.mu.Unlock()
}

func (s *Service) GetRenderJobStatus(ctx context.Context, principal identity.Principal, sessionID, jobID string) (pebblestore.VideoRenderJobSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.VideoRenderJobSnapshot{}, false, errors.New("videorender service is not configured")
	}
	return s.store.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
}

func (s *Service) failJob(principal identity.Principal, sessionID, jobID, code, reason string) {
	if s != nil && s.store != nil {
		_, _ = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
			AccountScopeID: principal.AccountScopeID,
			UserID:         principal.UserID,
			SessionID:      sessionID,
			JobID:          jobID,
			Status:         pebblestore.VideoRenderJobStatusFailed,
			FailureCode:    code,
			FailureReason:  reason,
			NowUnixMs:      time.Now().UnixMilli(),
		})
	}
}

func (s *Service) cancelJob(principal identity.Principal, sessionID, jobID, reason string) {
	if s != nil && s.store != nil {
		_, _ = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
			AccountScopeID: principal.AccountScopeID,
			UserID:         principal.UserID,
			SessionID:      sessionID,
			JobID:          jobID,
			Status:         pebblestore.VideoRenderJobStatusCancelled,
			FailureCode:    "context_cancelled",
			FailureReason:  reason,
			NowUnixMs:      time.Now().UnixMilli(),
		})
	}
}

func copyBoundedFile(destPath string, src io.Reader, sizeBytes int64) error {
	dest, err := os.OpenFile(destPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer dest.Close()
	written, err := io.Copy(dest, io.LimitReader(src, sizeBytes+1))
	if err != nil {
		return err
	}
	if sizeBytes > 0 && written != sizeBytes {
		return fmt.Errorf("copied bytes %d did not match expected %d", written, sizeBytes)
	}
	return dest.Sync()
}

func computeFileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' || r == '.' {
			b.WriteRune('_')
		}
	}
	res := strings.Trim(b.String(), "_-")
	if len(res) > 40 {
		res = res[:40]
	}
	return res
}

func sessionVideoWorkspaceIDs(session pebblestore.SessionSnapshot) []string {
	ids := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	for _, key := range []string{"swarm_v3_source_workspace_id", "workspace_id"} {
		value, _ := session.Metadata[key].(string)
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	return ids
}
