package videorender

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/htmlcapture"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videocomposition"
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

	htmlAnimationMaxSourceBytes = 32 << 20
	htmlAnimationMaxEntryBytes  = 8 << 20
	htmlAnimationMaxEntries     = 128
	htmlAnimationMaxManifest    = 64 << 10
)

var (
	htmlAnimationScriptPattern = regexp.MustCompile(`(?is)<script\s+([^>]*)>(.*?)</script\s*>`)
	htmlAnimationManifestID    = regexp.MustCompile(`(?i)(?:^|\s)id\s*=\s*["']swarm-animation-manifest["'](?:\s|$)`)
	htmlAnimationManifestType  = regexp.MustCompile(`(?i)(?:^|\s)type\s*=\s*["']application/json["'](?:\s|$)`)
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
	GetVideoEditProposal(accountScopeID, sessionID, projectID, proposalID string) (pebblestore.VideoEditProposalSnapshot, bool, error)
	ListVideoEditProposals(accountScopeID, sessionID, projectID string, limit int) ([]pebblestore.VideoEditProposalSnapshot, error)
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
	animation     htmlcapture.AnimationRenderer
	mu            sync.Mutex
	cancels       map[string]context.CancelFunc
	pendingStarts map[string]context.CancelFunc
	workers       sync.WaitGroup
	workerMu      sync.Mutex
	workerN       int
	idle          chan struct{}
}

func NewService(cfg Config, store SessionStore, artifacts ArtifactAuthority, animation htmlcapture.AnimationRenderer, workspace WorkspaceAuthority, runner CommandRunner) *Service {
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
		animation:     animation,
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
	timeline, err := cloneVideoTimeline(revision.Timeline)
	if err != nil {
		return FrameInspectionResult{}, fmt.Errorf("copy exact revision for frame inspection: %w", err)
	}
	if proposalID := pebblestore.VideoPlanRenderAuthorityProposalID(timeline); proposalID != "" {
		proposal, found, proposalErr := s.store.GetVideoEditProposal(principal.AccountScopeID, sessionID, projectID, proposalID)
		if proposalErr != nil {
			return FrameInspectionResult{}, fmt.Errorf("resolve video plan inspection authority: %w", proposalErr)
		}
		if !found {
			return FrameInspectionResult{}, fmt.Errorf("video plan inspection authority proposal %q not found", proposalID)
		}
		if proposal.UserID != "" && proposal.UserID != principal.UserID {
			return FrameInspectionResult{}, errors.New("video plan inspection authority ownership does not match authenticated principal")
		}
		plan, resolveErr := pebblestore.ResolveVideoPlanRenderAuthority(revision, &proposal)
		if resolveErr != nil {
			return FrameInspectionResult{}, resolveErr
		}
		if plan != nil {
			if timeline.Metadata == nil {
				timeline.Metadata = make(map[string]any)
			}
			timeline.Metadata["accepted_video_plan"] = *plan
		}
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err != nil {
		return FrameInspectionResult{}, err
	}
	timestamps, err := normalizeInspectionTimestamps(req, timeline.TotalDurationMs)
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
	inputs, err := s.materializeTimelineInputs(inspectCtx, principal, session, req.WorkspacePath, jobDir, timeline)
	if err != nil {
		return FrameInspectionResult{}, fmt.Errorf("materialize exact revision for frame inspection: %w", err)
	}
	previewPath := filepath.Join(jobDir, "inspection.mp4")
	plan, err := BuildFFmpegCommandLine(timeline, inputs, previewPath)
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

	timeline, err := cloneVideoTimeline(revision.Timeline)
	if err != nil {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("copy exact revision for render: %w", err)
	}
	proposals, err := s.store.ListVideoEditProposals(principal.AccountScopeID, sessionID, projectID, 100)
	if err != nil {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("inspect pending video proposals before render: %w", err)
	}
	if pending := pebblestore.PendingVideoEditProposalForRevision(revision, proposals); pending != nil {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("final render blocked: revision %q is the pending working cut for proposal %q; confirm or reject the pending changes before rendering", revision.ID, pending.ID)
	}
	if proposalID := pebblestore.VideoPlanRenderAuthorityProposalID(timeline); proposalID != "" {
		proposal, found, proposalErr := s.store.GetVideoEditProposal(principal.AccountScopeID, sessionID, projectID, proposalID)
		if proposalErr != nil {
			return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("resolve video plan render authority: %w", proposalErr)
		}
		if !found {
			return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("video plan render authority proposal %q not found", proposalID)
		}
		if proposal.UserID != "" && proposal.UserID != principal.UserID {
			return pebblestore.VideoRenderJobSnapshot{}, errors.New("video plan render authority ownership does not match authenticated principal")
		}
		if plan, resolveErr := pebblestore.ResolveVideoPlanRenderAuthority(revision, &proposal); resolveErr != nil {
			return pebblestore.VideoRenderJobSnapshot{}, resolveErr
		} else if plan != nil {
			if timeline.Metadata == nil {
				timeline.Metadata = make(map[string]any)
			}
			timeline.Metadata["accepted_video_plan"] = *plan
		}
	}
	if len(timeline.Clips) == 0 {
		return pebblestore.VideoRenderJobSnapshot{}, errors.New("timeline contains no clips to render")
	}
	if len(timeline.Clips) > s.cfg.MaxClips {
		return pebblestore.VideoRenderJobSnapshot{}, fmt.Errorf("timeline clip count %d exceeds maximum limit %d", len(timeline.Clips), s.cfg.MaxClips)
	}
	if err := applySelectedHTMLAnimationSources(&timeline); err != nil {
		return pebblestore.VideoRenderJobSnapshot{}, err
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
		ProgressStage:  "Preparing trusted render workspace",
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

	// Materialize inputs. HTML conversion reports bounded deterministic capture
	// progress directly into the durable render job.
	materialized, err := s.materializeTimelineInputs(renderCtx, principal, session, req.WorkspacePath, jobDir, timeline, func(progress htmlcapture.AnimationProgress) {
		fraction := 0.0
		if progress.Total > 0 {
			fraction = math.Max(0, math.Min(1, float64(progress.Completed)/float64(progress.Total)))
		}
		base, span := 0.08, 0.16
		stage := "Converting selected HTML animation"
		switch progress.Stage {
		case "queue_wait":
			base, span, stage = 0.06, 0.01, "Waiting for trusted HTML renderer"
		case "readiness_preflight":
			base, span, stage = 0.07, 0.02, "Checking HTML animation readiness"
		case "deterministic_preflight":
			base, span, stage = 0.09, 0.02, "Auditing deterministic animation frames"
		case "frame_capture":
			base, span, stage = 0.11, 0.10, "Capturing HTML animation frames"
		case "segment_encode":
			base, span, stage = 0.21, 0.03, "Encoding HTML animation segments"
		case "segment_concatenation":
			base, span, stage = 0.24, 0.01, "Finalizing HTML animation derivative"
		}
		s.updateProgress(principal, sessionID, jobID, base+span*fraction, stage)
	})
	if err != nil {
		s.failJob(principal, sessionID, jobID, renderFailureCode(err, "input_materialization_error"), renderFailureReason(err))
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
		ProgressStage:  "Composing project timeline",
		NowUnixMs:      time.Now().UnixMilli(),
	})

	// Run FFmpeg command. A bounded heartbeat makes long project composition
	// visibly live without claiming byte-level ffmpeg progress.
	heartbeatDone := make(chan struct{})
	go s.progressHeartbeat(renderCtx, principal, sessionID, jobID, 0.30, 0.82, "Composing final project", heartbeatDone)
	_, err = s.runner.RunCommand(renderCtx, "ffmpeg", plan.FFmpegArgs...)
	close(heartbeatDone)
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
				s.failJob(principal, sessionID, jobID, "render_timeout", "Render exceeded the configured deadline")
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
		ProgressStage:  "Publishing managed video artifact",
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
			OutputRequirements:    outputRequirements,
			VideoProjectID:        projectID,
			VideoRevisionID:       revision.ID,
			VideoRevisionEventSeq: revision.EventSeq,
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
		ProgressStage:      "Render ready",
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

func (s *Service) materializeTimelineInputs(ctx context.Context, principal identity.Principal, session pebblestore.SessionSnapshot, workspacePath string, jobDir string, timeline pebblestore.VideoProjectTimeline, progress ...func(htmlcapture.AnimationProgress)) ([]MaterializedInput, error) {
	var inputs []MaterializedInput
	// One exact HTML source and duration has one conversion authority per render.
	// Repeated clips may reuse the immutable materialized MP4, while different
	// exact refs remain isolated and can never borrow another part's derivative.
	htmlAnimationPaths := make(map[string]string)

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

	compositionPlacements, err := resolveTimelineCompositionPlacements(timeline)
	if err != nil {
		return nil, err
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

			mediaType := strings.ToLower(strings.TrimSpace(variant.MediaType))
			if mediaType == "text/html" || mediaType == "application/zip" {
				if strings.TrimSpace(targetRef.SessionID) == "" || strings.TrimSpace(targetRef.CollectionID) == "" || strings.TrimSpace(targetRef.VariantID) == "" || targetRef.EventSeq == 0 {
					return nil, fmt.Errorf("clip %d selected HTML animation requires all four exact reference fields", i)
				}
				if strings.TrimSpace(variant.SessionID) == "" || variant.SessionID != targetSessionID || variant.CollectionID != targetRef.CollectionID || variant.ID != targetRef.VariantID || variant.EventSeq != targetRef.EventSeq || variant.AccountScopeID != "" && variant.AccountScopeID != principal.AccountScopeID {
					return nil, fmt.Errorf("clip %d selected HTML animation authority does not match the exact authenticated reference", i)
				}
				if err := validateHTMLAnimationAuthority(variant); err != nil {
					return nil, fmt.Errorf("clip %d selected HTML animation authority: %w", i, err)
				}
				artifactPrincipal := artifact.Principal{SessionID: targetSessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
				cacheKey := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", targetSessionID, targetRef.CollectionID, targetRef.VariantID, targetRef.EventSeq, clip.DurationMs)
				animationPath := htmlAnimationPaths[cacheKey]
				if animationPath == "" {
					var progressFn func(htmlcapture.AnimationProgress)
					if len(progress) > 0 {
						progressFn = progress[0]
					}
					var renderErr error
					animationPath, renderErr = s.renderHTMLAnimationClip(ctx, artifactPrincipal, *targetRef, variant, clip.DurationMs, jobDir, input.Index, progressFn)
					if renderErr != nil {
						return nil, fmt.Errorf("render HTML animation clip %d: %w", i, renderErr)
					}
					htmlAnimationPaths[cacheKey] = animationPath
				}
				input.FilePath = animationPath
				input.StartMs = 0
				input.EndMs = clip.DurationMs
				input.IsVideo = true
				input.IsImage = false
				input.HasAudio = false
				break
			}

			artifactPrincipal := artifact.Principal{SessionID: targetSessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
			destPath := filepath.Join(jobDir, fmt.Sprintf("input_%d_%s", input.Index, variant.Filename))
			body, _, err := s.artifacts.ReadReference(ctx, artifactPrincipal, *targetRef, s.cfg.MaxRenderBytes)
			if err != nil {
				return nil, fmt.Errorf("read artifact clip %d: %w", i, err)
			}
			if err := os.WriteFile(destPath, body, 0o600); err != nil {
				return nil, err
			}
			input.FilePath = destPath
			if strings.HasPrefix(mediaType, "image/") {
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
			input.IsImage = true
			input.IsVideo = false
			input.HasAudio = false
			input.FilePath = filepath.Join(jobDir, fmt.Sprintf("input_%d_color.ppm", input.Index))
			dims := ResolveDimensions(timeline)
			if err := writePPMColorFrame(input.FilePath, dims.Width, dims.Height, clip.Name); err != nil {
				return nil, fmt.Errorf("materialize color clip %d: %w", i, err)
			}

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
		if clip.Track != 0 {
			continue
		}
		for _, placement := range compositionPlacements[clip.ID] {
			if len(inputs) >= s.cfg.MaxClips {
				return nil, fmt.Errorf("materialized clip and composition input count exceeds maximum limit %d", s.cfg.MaxClips)
			}
			slotInput, materializeErr := s.materializeCompositionInput(ctx, principal, session, workspaceID, registeredRoots, jobDir, clip, placement, len(inputs))
			if materializeErr != nil {
				return nil, fmt.Errorf("materialize composition for clip %q slot %q: %w", clip.ID, placement.SlotID, materializeErr)
			}
			inputs = append(inputs, slotInput)
		}
	}

	return inputs, nil
}

func writePPMColorFrame(filePath string, width, height int, color string) error {
	if width < 2 || height < 2 || width > 3840 || height > 3840 {
		return errors.New("color frame dimensions are outside render bounds")
	}
	r, g, b := byte(0), byte(0), byte(0)
	switch strings.ToLower(strings.TrimSpace(color)) {
	case "white":
		r, g, b = 255, 255, 255
	case "red":
		r = 255
	case "green":
		g = 128
	case "blue":
		b = 255
	}
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(file, "P6\n%d %d\n255\n", width, height); err != nil {
		_ = file.Close()
		return err
	}
	pixelRow := make([]byte, width*3)
	for x := 0; x < width; x++ {
		pixelRow[x*3], pixelRow[x*3+1], pixelRow[x*3+2] = r, g, b
	}
	for y := 0; y < height; y++ {
		if _, err := file.Write(pixelRow); err != nil {
			_ = file.Close()
			return err
		}
	}
	return file.Close()
}

type resolvedCompositionPlacement struct {
	CompositionPlacement
	Source videocomposition.SourceBinding
}

func resolveTimelineCompositionPlacements(timeline pebblestore.VideoProjectTimeline) (map[string][]resolvedCompositionPlacement, error) {
	placements := make(map[string][]resolvedCompositionPlacement)
	catalog := timeline.CompositionCatalog
	links := make(map[string]*videocomposition.Link)
	if timeline.Metadata != nil && timeline.Metadata["accepted_video_plan"] != nil {
		encoded, err := json.Marshal(timeline.Metadata["accepted_video_plan"])
		if err != nil {
			return nil, fmt.Errorf("encode accepted composition plan: %w", err)
		}
		var plan pebblestore.VideoPlanProposal
		if err := json.Unmarshal(encoded, &plan); err != nil {
			return nil, fmt.Errorf("decode accepted composition plan: %w", err)
		}
		if catalog == nil {
			catalog = plan.CompositionCatalog
		}
		for index := range plan.Parts {
			links[plan.Parts[index].ID] = plan.Parts[index].Composition
		}
	}
	if catalog == nil {
		return placements, nil
	}
	dims := ResolveDimensions(timeline)
	for _, clip := range timeline.Clips {
		link := links[clip.ID]
		if link == nil {
			continue
		}
		resolved, err := videocomposition.Resolve(catalog, link, dims.Width, dims.Height, clip.DurationMs)
		if err != nil {
			return nil, fmt.Errorf("resolve clip %q composition: %w", clip.ID, err)
		}
		includedAudioSlot := ""
		for _, slot := range resolved {
			if slot.Source == nil {
				continue
			}
			if slot.Source.AudioPolicy == videocomposition.AudioInclude {
				if includedAudioSlot != "" {
					return nil, fmt.Errorf("clip %q composition permits one included audio source; slots %q and %q both request audio", clip.ID, includedAudioSlot, slot.ID)
				}
				includedAudioSlot = slot.ID
			}
			placements[clip.ID] = append(placements[clip.ID], resolvedCompositionPlacement{
				CompositionPlacement: CompositionPlacement{
					SlotID: slot.ID, X: slot.Pixels.X, Y: slot.Pixels.Y, Width: slot.Pixels.Width, Height: slot.Pixels.Height,
					Fit: slot.Fit, AlignmentX: slot.AlignmentX, AlignmentY: slot.AlignmentY,
					CropTop: slot.Crop.Top, CropRight: slot.Crop.Right, CropBottom: slot.Crop.Bottom, CropLeft: slot.Crop.Left,
					MaskKind: slot.Mask.Kind, MaskRadius: slot.Mask.Radius, ZIndex: slot.ZIndex,
					SourceSpanMs:   slot.Source.SourceEndMs - slot.Source.SourceStartMs,
					TimelineSpanMs: slot.Source.TimelineEndMs - slot.Source.TimelineStartMs,
				},
				Source: *slot.Source,
			})
		}
	}
	return placements, nil
}

func (s *Service) materializeCompositionInput(ctx context.Context, principal identity.Principal, session pebblestore.SessionSnapshot, workspaceID string, registeredRoots map[string]struct{}, jobDir string, parent pebblestore.VideoTimelineClip, placement resolvedCompositionPlacement, index int) (MaterializedInput, error) {
	workspaceIDs := sessionVideoWorkspaceIDs(session)
	if workspaceID != "" {
		workspaceIDs = append([]string{workspaceID}, workspaceIDs...)
	}
	var record pebblestore.VideoSourceRecord
	found := false
	for _, candidate := range workspaceIDs {
		resolved, ok, err := s.store.GetVideoSourceRecord(principal.AccountScopeID, candidate, placement.Source.SourceRef)
		if err != nil {
			return MaterializedInput{}, err
		}
		if ok {
			record, found = resolved, true
			break
		}
	}
	if !found {
		return MaterializedInput{}, fmt.Errorf("registered video source %q not found in workspace scope", placement.Source.SourceRef)
	}
	if !strings.EqualFold(record.MIMEType, placement.Source.MediaType) {
		return MaterializedInput{}, fmt.Errorf("registered video source media type %q does not match exact binding %q", record.MIMEType, placement.Source.MediaType)
	}
	if registeredRoots != nil && len(registeredRoots) > 0 {
		if _, ok := registeredRoots[filepath.Clean(record.RootPath)]; !ok {
			return MaterializedInput{}, errors.New("registered video source no longer belongs to a source root")
		}
	}
	if err := pebblestore.ValidateVideoSourceRecord(record); err != nil {
		return MaterializedInput{}, fmt.Errorf("source fingerprint mismatch or invalid: %w", err)
	}
	source, err := pebblestore.OpenValidatedVideoSource(record)
	if err != nil {
		return MaterializedInput{}, err
	}
	defer source.Close()
	destination := filepath.Join(jobDir, fmt.Sprintf("composition_%d_%s", index, filepath.Base(record.RelativePath)))
	if err := copyBoundedFile(destination, source, record.SizeBytes); err != nil {
		return MaterializedInput{}, err
	}
	hasAudio, err := probeInputHasAudio(ctx, s.runner, destination)
	if err != nil {
		return MaterializedInput{}, err
	}
	start := parent.TimelineStartMs + placement.Source.TimelineStartMs
	end := parent.TimelineStartMs + placement.Source.TimelineEndMs
	includeAudio := placement.Source.AudioPolicy == videocomposition.AudioInclude
	if includeAudio && !hasAudio {
		return MaterializedInput{}, errors.New("composition source requests included audio but has no audio stream")
	}
	return MaterializedInput{
		Index: index, ClipID: parent.ID + ":" + placement.SlotID, FilePath: destination, IsVideo: true,
		HasAudio: hasAudio, Muted: !includeAudio, Volume: placement.Source.Gain,
		StartMs: placement.Source.SourceStartMs, EndMs: placement.Source.SourceEndMs,
		DurationMs: placement.Source.TimelineEndMs - placement.Source.TimelineStartMs,
		Track:      max(parent.Track+1, 1), Layer: placement.ZIndex, TimelineStartMs: start, TimelineEndMs: end,
		Composition: &placement.CompositionPlacement,
	}, nil
}

type htmlAnimationManifest struct {
	Version    string `json:"version"`
	DurationMS int    `json:"duration_ms"`
	FPS        int    `json:"fps"`
}

func cloneVideoTimeline(source pebblestore.VideoProjectTimeline) (pebblestore.VideoProjectTimeline, error) {
	encoded, err := json.Marshal(source)
	if err != nil {
		return pebblestore.VideoProjectTimeline{}, err
	}
	var cloned pebblestore.VideoProjectTimeline
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return pebblestore.VideoProjectTimeline{}, err
	}
	return cloned, nil
}

func applySelectedHTMLAnimationSources(timeline *pebblestore.VideoProjectTimeline) error {
	if timeline == nil || timeline.Metadata == nil {
		return nil
	}
	raw := timeline.Metadata["accepted_video_plan"]
	if raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encode exact video plan for render: %w", err)
	}
	var plan pebblestore.VideoPlanProposal
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return fmt.Errorf("decode exact video plan for render: %w", err)
	}
	clips := make(map[string]*pebblestore.VideoTimelineClip, len(timeline.Clips))
	for index := range timeline.Clips {
		clips[timeline.Clips[index].ID] = &timeline.Clips[index]
	}
	for _, part := range plan.Parts {
		candidates := part.AnimationCandidates
		if candidates == nil {
			continue
		}
		clip := clips[part.ID]
		if clip == nil {
			// A later accepted partial revision may remove a clip while retaining
			// the earlier full-plan metadata for immutable history. The current
			// timeline is the render authority, so stale plan-only parts do not
			// participate in this render.
			continue
		}
		if part.DurationMs <= 0 || part.DurationMs != clip.DurationMs {
			return fmt.Errorf("HTML animation part %q duration %dms does not match timeline clip duration %dms", part.ID, part.DurationMs, clip.DurationMs)
		}
		switch candidates.Status {
		case pebblestore.VideoAnimationCandidateStatusAwaitingSelection, pebblestore.VideoAnimationCandidateStatusAwaitingExport, pebblestore.VideoAnimationCandidateStatusReady, pebblestore.VideoAnimationCandidateStatusFailed:
		default:
			return fmt.Errorf("HTML animation part %q has unsupported render status %q", part.ID, candidates.Status)
		}
		if candidates.Status == pebblestore.VideoAnimationCandidateStatusAwaitingSelection && len(candidates.Candidates) > 1 {
			return fmt.Errorf("HTML animation part %q is still awaiting explicit candidate selection", part.ID)
		}
		if len(candidates.Candidates) == 0 {
			return fmt.Errorf("HTML animation part %q has no exact candidate source set", part.ID)
		}
		selectedSource := candidates.SelectedSource
		if candidates.SelectedCandidateID == "" {
			if len(candidates.Candidates) == 1 {
				selectedSource = candidates.Candidates[0].Source
			} else {
				return fmt.Errorf("HTML animation part %q has no durably locked candidate", part.ID)
			}
		} else {
			var lockedSource *pebblestore.SessionArtifactSelectionReference
			for _, candidate := range candidates.Candidates {
				if candidate.ID == candidates.SelectedCandidateID {
					lockedSource = candidate.Source
					break
				}
			}
			if lockedSource == nil {
				return fmt.Errorf("HTML animation part %q selected candidate %q is missing from its exact candidate set", part.ID, candidates.SelectedCandidateID)
			}
			if !sameExactArtifactReference(selectedSource, lockedSource) {
				return fmt.Errorf("HTML animation part %q selected source does not match its durably locked candidate", part.ID)
			}
		}
		if candidates.Status == pebblestore.VideoAnimationCandidateStatusFailed {
			reason := strings.TrimSpace(candidates.FailureReason)
			if reason == "" {
				reason = "the selected HTML animation export failed"
			}
			if len(reason) > 512 {
				reason = reason[:512]
			}
			return fmt.Errorf("HTML animation part %q is not renderable: %s", part.ID, reason)
		}
		if candidates.Derivative != nil && candidates.Status == pebblestore.VideoAnimationCandidateStatusReady {
			if strings.TrimSpace(candidates.Derivative.SessionID) == "" || strings.TrimSpace(candidates.Derivative.CollectionID) == "" || strings.TrimSpace(candidates.Derivative.VariantID) == "" || candidates.Derivative.EventSeq == 0 {
				return fmt.Errorf("HTML animation part %q promoted derivative requires all four exact reference fields", part.ID)
			}
			clip.ArtifactRef = candidates.Derivative
			clip.MediaType = "video/mp4"
			clip.SourceStartMs = 0
			clip.SourceEndMs = clip.DurationMs
			continue
		}
		if selectedSource == nil {
			return fmt.Errorf("HTML animation part %q has a locked candidate but no exact selected source", part.ID)
		}
		if strings.TrimSpace(selectedSource.SessionID) == "" || strings.TrimSpace(selectedSource.CollectionID) == "" || strings.TrimSpace(selectedSource.VariantID) == "" || selectedSource.EventSeq == 0 {
			return fmt.Errorf("HTML animation part %q selected source requires all four exact reference fields", part.ID)
		}
		clip.ArtifactRef = selectedSource
		clip.MediaType = "text/html"
		clip.SourceStartMs = 0
		clip.SourceEndMs = clip.DurationMs
	}
	return nil
}

func (s *Service) renderHTMLAnimationClip(ctx context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, variant pebblestore.SessionArtifactVariant, expectedDurationMs int64, jobDir string, inputIndex int, progress ...func(htmlcapture.AnimationProgress)) (string, error) {
	if err := validateHTMLAnimationAuthority(variant); err != nil {
		return "", err
	}
	if !sameExactArtifactReference(&ref, &pebblestore.SessionArtifactSelectionReference{SessionID: variant.SessionID, CollectionID: variant.CollectionID, VariantID: variant.ID, EventSeq: variant.EventSeq}) {
		return "", errors.New("selected HTML animation does not match the exact authenticated variant")
	}
	if s.animation == nil {
		return "", errors.New("selected HTML animation requires the trusted HTML-to-MP4 renderer, but it is unavailable")
	}
	files, entry, err := s.readHTMLAnimationSource(ctx, principal, ref, variant)
	if err != nil {
		return "", err
	}
	manifest, err := parseHTMLAnimationManifest(files[entry])
	if err != nil {
		return "", err
	}
	if expectedDurationMs <= 0 || int64(manifest.DurationMS) != expectedDurationMs {
		return "", fmt.Errorf("animation manifest duration %dms does not match clip duration %dms", manifest.DurationMS, expectedDurationMs)
	}
	if duration := temporalAnimationDuration(variant.Parts); duration <= 0 || duration != expectedDurationMs {
		return "", fmt.Errorf("reviewed animation temporal authority duration %dms does not match clip duration %dms", duration, expectedDurationMs)
	}
	var progressFn func(htmlcapture.AnimationProgress)
	if len(progress) > 0 {
		progressFn = progress[0]
	}
	result, err := s.animation.RenderAnimation(ctx, htmlcapture.AnimationRequest{Entry: entry, Files: files, DurationMS: manifest.DurationMS, FPS: manifest.FPS, Progress: progressFn})
	if err != nil {
		var captureErr *htmlcapture.Error
		if errors.As(err, &captureErr) {
			return "", fmt.Errorf("%s: %s", captureErr.Code, captureErr.SafeMessage)
		}
		return "", fmt.Errorf("trusted HTML animation capture failed: %w", err)
	}
	if result.DurationMS != manifest.DurationMS || result.FPS != manifest.FPS || result.FrameCount != (manifest.DurationMS*manifest.FPS+999)/1000 {
		return "", errors.New("trusted HTML animation renderer returned inconsistent timeline metadata")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if len(result.MP4) < 12 || len(result.MP4) > htmlcapture.MaxMP4Bytes || string(result.MP4[4:8]) != "ftyp" || int64(len(result.MP4)) > s.cfg.MaxRenderBytes {
		return "", errors.New("trusted HTML animation renderer returned an invalid bounded MP4")
	}
	destPath := filepath.Join(jobDir, fmt.Sprintf("input_%d_html-animation.mp4", inputIndex))
	if err := os.WriteFile(destPath, result.MP4, 0o600); err != nil {
		return "", fmt.Errorf("write rendered HTML animation clip: %w", err)
	}
	return destPath, nil
}

func sameExactArtifactReference(left, right *pebblestore.SessionArtifactSelectionReference) bool {
	return left != nil && right != nil && strings.TrimSpace(left.SessionID) != "" && left.SessionID == right.SessionID && strings.TrimSpace(left.CollectionID) != "" && left.CollectionID == right.CollectionID && strings.TrimSpace(left.VariantID) != "" && left.VariantID == right.VariantID && left.EventSeq != 0 && left.EventSeq == right.EventSeq
}

func validateHTMLAnimationAuthority(variant pebblestore.SessionArtifactVariant) error {
	if strings.ToLower(strings.TrimSpace(variant.MediaType)) != "text/html" && strings.ToLower(strings.TrimSpace(variant.MediaType)) != "application/zip" {
		return fmt.Errorf("reviewed animation source media type %q is not HTML authority", variant.MediaType)
	}
	if variant.Status != pebblestore.SessionArtifactStatusReady || variant.EventSeq == 0 {
		return errors.New("reviewed animation source is not an exact ready revision")
	}
	profile := variant.AnimationProfile
	if profile == nil {
		return errors.New("a reviewed animation profile is required")
	}
	switch profile.ProfileID {
	case "motion_ui", "spatial_3d", "vector_playback":
	default:
		return fmt.Errorf("reviewed animation profile %q is not renderable HTML authority", profile.ProfileID)
	}
	canonicalProfile, err := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: profile.ProfileID})
	if err != nil || canonicalProfile == nil || *canonicalProfile != *profile || profile.Budgets.NetworkAllowed {
		return errors.New("reviewed animation profile does not match the canonical non-network runtime and budget snapshot")
	}
	canonicalOutput, err := artifact.ResolveOutputRequirements(&artifact.OutputRequirementsInput{Preset: "landscape_video"})
	if err != nil || canonicalOutput == nil || variant.OutputRequirements == nil || *canonicalOutput != *variant.OutputRequirements {
		return fmt.Errorf("reviewed animation output requirements must authenticate canonical %dx%d output", htmlcapture.Width, htmlcapture.Height)
	}
	if duration := temporalAnimationDuration(variant.Parts); duration <= 0 || duration > htmlcapture.MaxAnimationDurationMS {
		return errors.New("reviewed animation temporal authority is missing or outside fixed bounds")
	}
	if variant.Presentation.Width != 0 && variant.Presentation.Width != canonicalOutput.Width || variant.Presentation.Height != 0 && variant.Presentation.Height != canonicalOutput.Height {
		return fmt.Errorf("reviewed animation presentation must match canonical %dx%d output", canonicalOutput.Width, canonicalOutput.Height)
	}
	return nil
}

func temporalAnimationDuration(parts []pebblestore.SessionArtifactPart) int64 {
	var duration int64
	for _, part := range parts {
		if part.Kind != "temporal" {
			continue
		}
		if strings.TrimSpace(part.ID) == "" || part.StartMs < 0 || part.EndMs <= part.StartMs {
			return -1
		}
		if part.EndMs > duration {
			duration = part.EndMs
		}
	}
	return duration
}

func (s *Service) readHTMLAnimationSource(ctx context.Context, principal artifact.Principal, ref pebblestore.SessionArtifactSelectionReference, variant pebblestore.SessionArtifactVariant) (map[string][]byte, string, error) {
	if s == nil || s.artifacts == nil {
		return nil, "", errors.New("artifact authority is unavailable for exact HTML animation source")
	}
	mediaType := strings.ToLower(strings.TrimSpace(variant.MediaType))
	body, resolved, err := s.artifacts.ReadReference(ctx, principal, ref, htmlAnimationMaxSourceBytes)
	if err != nil {
		return nil, "", fmt.Errorf("read exact HTML animation source: %w", err)
	}
	if resolved.SessionID != ref.SessionID || resolved.CollectionID != ref.CollectionID || resolved.ID != ref.VariantID || resolved.EventSeq != ref.EventSeq || resolved.Status != pebblestore.SessionArtifactStatusReady {
		return nil, "", errors.New("artifact authority did not return the exact authenticated ready HTML revision")
	}
	if !strings.EqualFold(strings.TrimSpace(resolved.MediaType), strings.TrimSpace(variant.MediaType)) || resolved.Size != variant.Size || resolved.DigestSHA256 != variant.DigestSHA256 || resolved.AnimationProfile == nil || variant.AnimationProfile == nil || *resolved.AnimationProfile != *variant.AnimationProfile || resolved.OutputRequirements == nil || variant.OutputRequirements == nil || *resolved.OutputRequirements != *variant.OutputRequirements {
		return nil, "", errors.New("artifact authority returned HTML bytes with inconsistent immutable metadata")
	}
	if resolved.Size > 0 && resolved.Size != int64(len(body)) {
		return nil, "", errors.New("artifact authority returned HTML bytes with an invalid immutable size")
	}
	if strings.TrimSpace(resolved.DigestSHA256) != "" {
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != resolved.DigestSHA256 {
			return nil, "", errors.New("artifact authority returned HTML bytes with an invalid immutable digest")
		}
	}
	switch mediaType {
	case "text/html":
		if len(body) == 0 || len(body) > htmlAnimationMaxSourceBytes || !utf8.Valid(body) {
			return nil, "", errors.New("selected HTML animation is not valid bounded UTF-8")
		}
		return map[string][]byte{"index.html": append([]byte(nil), body...)}, "index.html", nil
	case "application/zip":
		return readHTMLAnimationPackage(body)
	default:
		return nil, "", fmt.Errorf("selected animation artifact has unsupported media type %q", variant.MediaType)
	}
}

func readHTMLAnimationPackage(body []byte) (map[string][]byte, string, error) {
	if len(body) == 0 || len(body) > htmlAnimationMaxSourceBytes {
		return nil, "", errors.New("HTML animation package exceeds fixed bounds")
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil || len(archive.File) == 0 || len(archive.File) > htmlAnimationMaxEntries {
		return nil, "", errors.New("HTML animation package is malformed or exceeds fixed entry bounds")
	}
	files := make(map[string][]byte, len(archive.File))
	total := 0
	for _, file := range archive.File {
		name := file.Name
		if file.FileInfo().IsDir() || name == "" || len(name) > 1024 || path.Clean(name) != name || strings.Contains(name, "\\") || !file.Mode().IsRegular() {
			return nil, "", errors.New("HTML animation package contains an unsafe entry")
		}
		if _, exists := files[name]; exists || file.UncompressedSize64 > htmlAnimationMaxEntryBytes {
			return nil, "", errors.New("HTML animation package contains duplicate or oversized entries")
		}
		reader, openErr := file.Open()
		if openErr != nil {
			return nil, "", errors.New("HTML animation package entry could not be opened")
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, htmlAnimationMaxEntryBytes+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(data) > htmlAnimationMaxEntryBytes || uint64(len(data)) != file.UncompressedSize64 {
			return nil, "", errors.New("HTML animation package entry failed bounded validation")
		}
		total += len(data)
		if total > htmlAnimationMaxSourceBytes {
			return nil, "", errors.New("HTML animation package exceeds fixed expanded bounds")
		}
		files[name] = data
	}
	if _, ok := files["index.html"]; !ok {
		return nil, "", errors.New("HTML animation package is missing canonical index.html")
	}
	return files, "index.html", nil
}

func parseHTMLAnimationManifest(html []byte) (htmlAnimationManifest, error) {
	var manifestBodies [][]byte
	for _, script := range htmlAnimationScriptPattern.FindAllSubmatch(html, -1) {
		if htmlAnimationManifestID.Match(script[1]) {
			if !htmlAnimationManifestType.Match(script[1]) {
				return htmlAnimationManifest{}, errors.New("HTML animation manifest has an invalid media type")
			}
			manifestBodies = append(manifestBodies, script[2])
		}
	}
	if len(manifestBodies) == 0 {
		return htmlAnimationManifest{}, errors.New("canonical swarm.animation/v1 manifest is missing")
	}
	if len(manifestBodies) != 1 || len(manifestBodies[0]) > htmlAnimationMaxManifest {
		return htmlAnimationManifest{}, errors.New("HTML animation manifest is duplicated or exceeds fixed bounds")
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestBodies[0]))
	decoder.DisallowUnknownFields()
	var manifest htmlAnimationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return htmlAnimationManifest{}, errors.New("HTML animation manifest is malformed")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return htmlAnimationManifest{}, errors.New("HTML animation manifest is malformed")
	}
	frames := (manifest.DurationMS*manifest.FPS + 999) / 1000
	if manifest.Version != htmlcapture.AnimationVersion || manifest.DurationMS < 100 || manifest.DurationMS > htmlcapture.MaxAnimationDurationMS || manifest.FPS < 1 || manifest.FPS > htmlcapture.MaxAnimationFPS || frames > htmlcapture.MaxAnimationFrames {
		return htmlAnimationManifest{}, errors.New("HTML animation manifest version, duration, FPS, or frame count is outside fixed bounds")
	}
	return manifest, nil
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
		if _, err := s.RenderJob(startCtx, principal, req); err != nil && startCtx.Err() == nil {
			// RenderJob can reject invalid pinned input before it transitions the
			// durable job out of queued. Never leave that preflight failure looking
			// like live 0% work indefinitely.
			current, ok, _ := s.store.GetVideoRenderJob(principal.AccountScopeID, req.SessionID, req.JobID)
			if ok && current.Status == pebblestore.VideoRenderJobStatusQueued {
				s.failJob(principal, req.SessionID, req.JobID, renderFailureCode(err, "render_preflight_error"), renderFailureReason(err))
			}
		}
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
			ProgressStage:  "Render cancelled",
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
		Status: pebblestore.VideoRenderJobStatusReady, ExpectedStatus: job.Status, Progress: 1, ProgressStage: "Render ready",
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

func renderFailureCode(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	message := strings.ToLower(err.Error())
	for _, code := range []string{
		"animation_renderer_unavailable", "animation_encoder_unavailable", "animation_runtime_missing",
		"animation_not_ready", "animation_seek_failed", "animation_frame_unstable", "animation_network_blocked",
		"animation_timeout", "animation_encode_failed", "animation_concat_failed", "animation_mp4_invalid",
	} {
		if strings.Contains(message, code) {
			return code
		}
	}
	switch {
	case strings.Contains(message, "requires the trusted html-to-mp4 renderer"):
		return "animation_renderer_unavailable"
	case strings.Contains(message, "animation manifest"), strings.Contains(message, "swarm.animation/v1 manifest"):
		return "animation_manifest_invalid"
	case strings.Contains(message, "pinned to revision"):
		return "stale_pinned_revision"
	case strings.Contains(message, "video project revision") && strings.Contains(message, "not found"):
		return "invalid_pinned_revision"
	default:
		return fallback
	}
}

func renderFailureReason(err error) string {
	if err == nil {
		return "Render failed"
	}
	var captureErr *htmlcapture.Error
	if errors.As(err, &captureErr) {
		return captureErr.SafeMessage
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func (s *Service) updateProgress(principal identity.Principal, sessionID, jobID string, progress float64, stage string) {
	if s == nil || s.store == nil {
		return
	}
	current, ok, err := s.store.GetVideoRenderJob(principal.AccountScopeID, sessionID, jobID)
	if err != nil || !ok || current.Status != pebblestore.VideoRenderJobStatusRendering || progress <= current.Progress {
		return
	}
	_, _ = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID, JobID: jobID,
		Status: pebblestore.VideoRenderJobStatusRendering, ExpectedStatus: pebblestore.VideoRenderJobStatusRendering,
		Progress: progress, ProgressStage: stage, NowUnixMs: time.Now().UnixMilli(),
	})
}

func (s *Service) progressHeartbeat(ctx context.Context, principal identity.Principal, sessionID, jobID string, start, limit float64, stage string, done <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	progress := start
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			progress = math.Min(limit, progress+0.02)
			s.updateProgress(principal, sessionID, jobID, progress, stage)
		}
	}
}

func (s *Service) failJob(principal identity.Principal, sessionID, jobID, code, reason string) {
	if s != nil && s.store != nil {
		_, _ = s.store.UpdateVideoRenderJob(pebblestore.UpdateVideoRenderJobInput{
			AccountScopeID: principal.AccountScopeID,
			UserID:         principal.UserID,
			SessionID:      sessionID,
			JobID:          jobID,
			Status:         pebblestore.VideoRenderJobStatusFailed,
			ProgressStage:  "Render failed",
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
			ProgressStage:  "Render cancelled",
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
