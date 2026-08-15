package videotranscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

const (
	TranscriptPromptVersion = "video_transcript_prompt.v1"
	maxBatchSize            = pebblestore.SessionVideoAttachmentMaxCount
	workerTimeout           = 12 * time.Minute
)

// Adapter is the provider-neutral boundary for one exact video. Implementations
// own temporary provider uploads and must remove them before returning.
type Adapter interface {
	Transcribe(context.Context, TranscribeRequest) (GeneratedTranscript, error)
}

type TranscribeRequest struct {
	AccountScopeID string
	Model          string
	MIMEType       string
	SizeBytes      int64
	Source         io.ReadSeeker
	Prompt         string
}

type GeneratedTranscript struct {
	Text       string
	Segments   []pebblestore.NormalizedTranscriptSegment
	Language   string
	DurationMs int64
	Partial    bool
}

type ModelCatalog interface {
	ListCatalog(providerID string, limit int) ([]pebblestore.ModelCatalogRecord, error)
}

type Settings interface {
	GetForAccount(accountScopeID string) (uisettings.UISettings, error)
}

type Service struct {
	sessions *pebblestore.SessionStore
	catalog  ModelCatalog
	settings Settings
	google   Adapter

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func NewService(sessions *pebblestore.SessionStore, catalog ModelCatalog, settings Settings, google Adapter) *Service {
	return &Service{sessions: sessions, catalog: catalog, settings: settings, google: google, running: make(map[string]context.CancelFunc)}
}

type StartResult struct {
	Jobs []pebblestore.TranscriptionJob
}

func (s *Service) Start(ctx context.Context, principal identity.Principal, sessionID, messageID string) (StartResult, error) {
	if s == nil || s.sessions == nil || s.catalog == nil || s.settings == nil || s.google == nil {
		return StartResult{}, errors.New("video transcription service is not configured")
	}
	sessionID, messageID = strings.TrimSpace(sessionID), strings.TrimSpace(messageID)
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		return StartResult{}, err
	}
	if err := validatePrincipal(principal, session); err != nil {
		return StartResult{}, err
	}
	message, ok, err := s.sessions.GetV3MessageByID(sessionID, messageID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("triggering message not found")
		}
		return StartResult{}, err
	}
	if message.Role != "user" || len(message.VideoAttachments) == 0 {
		return StartResult{}, errors.New("triggering user message has no video attachments")
	}
	if len(message.VideoAttachments) > maxBatchSize {
		return StartResult{}, fmt.Errorf("video transcription batch exceeds %d attachments", maxBatchSize)
	}
	model, snapshot, settingsHash, err := s.resolveModel(principal.AccountScopeID)
	if err != nil {
		return StartResult{}, err
	}
	jobs := make([]pebblestore.TranscriptionJob, 0, len(message.VideoAttachments))
	for index, source := range message.VideoAttachments {
		attachment, _, err := s.sessions.BindVideoTranscriptionAttachment(pebblestore.BindVideoTranscriptionAttachmentInput{
			AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID,
			MessageID: messageID, VideoThreadID: "registered-source", VideoClipID: source.Ref,
			ClientRequestID: fmt.Sprintf("video-transcription-attachment:%s:%d", messageID, index),
		})
		if err != nil {
			return StartResult{}, fmt.Errorf("bind video attachment %d: %w", index, err)
		}
		job, _, err := s.sessions.CreateTranscriptionJob(pebblestore.CreateTranscriptionJobInput{
			AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID,
			AttachmentRef: attachment.Ref, ProviderID: "google", Model: model, ModelSnapshot: snapshot,
			MediaSettingsHash: settingsHash, TranscriptSchema: pebblestore.NormalizedTranscriptSchemaVersion,
		})
		if err != nil {
			return StartResult{}, fmt.Errorf("create video transcription job %d: %w", index, err)
		}
		jobs = append(jobs, job)
		s.resume(principal, job)
	}
	return StartResult{Jobs: jobs}, nil
}

func (s *Service) Status(principal identity.Principal, sessionID string, refs []string) ([]pebblestore.TranscriptionJob, error) {
	if len(refs) == 0 || len(refs) > maxBatchSize {
		return nil, fmt.Errorf("status requires between 1 and %d exact job references", maxBatchSize)
	}
	session, ok, err := s.sessions.GetSession(strings.TrimSpace(sessionID))
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		return nil, err
	}
	if err := validatePrincipal(principal, session); err != nil {
		return nil, err
	}
	jobs := make([]pebblestore.TranscriptionJob, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		job, ok, err := s.sessions.GetTranscriptionJob(principal.AccountScopeID, session.ID, ref)
		if err != nil || !ok {
			if err == nil {
				err = errors.New("transcription job not found in authenticated session scope")
			}
			return nil, err
		}
		if job.UserID != principal.UserID {
			return nil, errors.New("transcription job user ownership mismatch")
		}
		jobs = append(jobs, job)
		if !terminal(job.Status) {
			s.resume(principal, job)
		}
	}
	return jobs, nil
}

func (s *Service) Read(principal identity.Principal, sessionID, transcriptRef string) (pebblestore.NormalizedTranscript, error) {
	session, ok, err := s.sessions.GetSession(strings.TrimSpace(sessionID))
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		return pebblestore.NormalizedTranscript{}, err
	}
	if err := validatePrincipal(principal, session); err != nil {
		return pebblestore.NormalizedTranscript{}, err
	}
	transcript, ok, err := s.sessions.GetNormalizedTranscript(principal.AccountScopeID, session.ID, strings.TrimSpace(transcriptRef))
	if err != nil || !ok {
		if err == nil {
			err = errors.New("transcript not found in authenticated session scope")
		}
		return pebblestore.NormalizedTranscript{}, err
	}
	job, ok, err := s.sessions.GetTranscriptionJob(principal.AccountScopeID, session.ID, transcript.JobRef)
	if err != nil || !ok || job.UserID != principal.UserID || job.Status != pebblestore.TranscriptionJobReady {
		if err == nil {
			err = errors.New("transcript is not backed by a ready authenticated job")
		}
		return pebblestore.NormalizedTranscript{}, err
	}
	return transcript, nil
}

func (s *Service) Cancel(principal identity.Principal, sessionID, jobRef string) (pebblestore.TranscriptionJob, error) {
	jobs, err := s.Status(principal, sessionID, []string{jobRef})
	if err != nil {
		return pebblestore.TranscriptionJob{}, err
	}
	job := jobs[0]
	if terminal(job.Status) {
		return job, nil
	}
	s.mu.Lock()
	cancel := s.running[job.Ref]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	cancelled, _, err := s.sessions.TransitionTranscriptionJob(pebblestore.TransitionTranscriptionJobInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: sessionID,
		JobRef: job.Ref, ExpectedStatus: job.Status, Status: pebblestore.TranscriptionJobCancelled,
		ClientRequestID: "video-transcription-cancel:" + job.Ref,
	})
	return cancelled, err
}

func (s *Service) resume(principal identity.Principal, job pebblestore.TranscriptionJob) {
	s.mu.Lock()
	if _, exists := s.running[job.Ref]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(identity.ContextWithPrincipal(context.Background(), principal), workerTimeout)
	s.running[job.Ref] = cancel
	s.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.running, job.Ref)
			s.mu.Unlock()
		}()
		s.process(ctx, principal, job)
	}()
}

func (s *Service) process(ctx context.Context, principal identity.Principal, initial pebblestore.TranscriptionJob) {
	job, ok, err := s.sessions.GetTranscriptionJob(principal.AccountScopeID, initial.SessionID, initial.Ref)
	if err != nil || !ok || terminal(job.Status) {
		return
	}
	if job.Status == pebblestore.TranscriptionJobQueued {
		job, _, err = s.sessions.TransitionTranscriptionJob(pebblestore.TransitionTranscriptionJobInput{
			AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: job.SessionID,
			JobRef: job.Ref, ExpectedStatus: job.Status, Status: pebblestore.TranscriptionJobUploading,
			ClientRequestID: fmt.Sprintf("video-transcription-upload:%s:%d", job.Ref, time.Now().UnixMilli()),
		})
		if err != nil {
			return
		}
	} else if job.Status != pebblestore.TranscriptionJobUploading && job.Status != pebblestore.TranscriptionJobProcessing && job.Status != pebblestore.TranscriptionJobPartial {
		return
	}
	attachment, ok, err := s.sessions.GetTranscriptionAttachment(principal.AccountScopeID, job.SessionID, job.AttachmentRef)
	if err != nil || !ok {
		s.fail(principal, job, "source_unavailable", "trusted video attachment is unavailable")
		return
	}
	file, err := s.sessions.OpenTranscriptionAttachmentSource(principal.AccountScopeID, job.SessionID, attachment.Ref)
	if err != nil {
		s.fail(principal, job, "stale_source", "source video changed or is unavailable")
		return
	}
	defer file.Close()
	generated, err := s.google.Transcribe(ctx, TranscribeRequest{
		AccountScopeID: principal.AccountScopeID, Model: job.Model, MIMEType: attachment.MIMEType,
		SizeBytes: attachment.SizeBytes, Source: file, Prompt: StructuredTranscriptPrompt(),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.fail(principal, job, "provider", boundedError(err))
		return
	}
	job, ok, err = s.sessions.GetTranscriptionJob(principal.AccountScopeID, job.SessionID, job.Ref)
	if err != nil || !ok || terminal(job.Status) {
		return
	}
	job, _, err = s.sessions.TransitionTranscriptionJob(pebblestore.TransitionTranscriptionJobInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: job.SessionID,
		JobRef: job.Ref, ExpectedStatus: job.Status, Status: pebblestore.TranscriptionJobProcessing,
		ClientRequestID: fmt.Sprintf("video-transcription-process:%s:%d", job.Ref, time.Now().UnixMilli()),
	})
	if err != nil {
		return
	}
	if generated.Partial {
		_, _, _ = s.sessions.TransitionTranscriptionJob(pebblestore.TransitionTranscriptionJobInput{
			AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: job.SessionID,
			JobRef: job.Ref, ExpectedStatus: job.Status, Status: pebblestore.TranscriptionJobPartial,
			ClientRequestID: fmt.Sprintf("video-transcription-partial:%s:%d", job.Ref, time.Now().UnixMilli()),
		})
		return
	}
	_, _, _, err = s.sessions.CommitNormalizedTranscript(pebblestore.CommitNormalizedTranscriptInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: job.SessionID, JobRef: job.Ref,
		Text: generated.Text, Segments: generated.Segments, Language: generated.Language,
		DurationMs: generated.DurationMs, GeneratedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		s.fail(principal, job, "persistence", "normalized transcript could not be durably committed and read back")
	}
}

func (s *Service) fail(principal identity.Principal, job pebblestore.TranscriptionJob, code, reason string) {
	current, ok, err := s.sessions.GetTranscriptionJob(principal.AccountScopeID, job.SessionID, job.Ref)
	if err != nil || !ok || terminal(current.Status) {
		return
	}
	_, _, _ = s.sessions.TransitionTranscriptionJob(pebblestore.TransitionTranscriptionJobInput{
		AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: job.SessionID,
		JobRef: job.Ref, ExpectedStatus: current.Status, Status: pebblestore.TranscriptionJobFailed,
		FailureCode: code, FailureReason: reason,
		ClientRequestID: fmt.Sprintf("video-transcription-failed:%s:%d", job.Ref, time.Now().UnixMilli()),
	})
}

func (s *Service) resolveModel(accountScopeID string) (string, string, string, error) {
	settings, err := s.settings.GetForAccount(accountScopeID)
	if err != nil {
		return "", "", "", err
	}
	model := strings.TrimSpace(settings.Media.TranscriptionModel)
	if model == "" {
		return "", "", "", errors.New("a Google video transcription model must be selected in Media settings")
	}
	records, err := s.catalog.ListCatalog("google", 2000)
	if err != nil {
		return "", "", "", err
	}
	for _, record := range records {
		if record.Model != model || !containsFold(record.CatalogModalities.Inputs, "video") || !containsFold(record.CatalogModalities.Outputs, "text") {
			continue
		}
		snapshot := strings.TrimSpace(record.SourceSnapshotID + ":" + record.SourceSnapshotVersion)
		if snapshot == ":" {
			return "", "", "", errors.New("configured transcription model lacks snapshot identity")
		}
		settingsPayload, _ := json.Marshal(map[string]string{"prompt": TranscriptPromptVersion, "model": model})
		hash := sha256.Sum256(settingsPayload)
		return model, snapshot, hex.EncodeToString(hash[:]), nil
	}
	return "", "", "", errors.New("configured transcription model is absent from the current Google video catalog")
}

func StructuredTranscriptPrompt() string {
	return `video_transcript_prompt.v1
Return only one JSON object with this exact shape:
{"text":"full model-generated transcript","language":"BCP-47 or empty","duration_ms":1234,"segments":[{"start_ms":0,"end_ms":1234,"text":"spoken text"}]}
Use integer milliseconds. Segments must be non-empty, chronological, non-overlapping, and cover only spoken content. Do not use markdown fences. This is model-generated transcription, not guaranteed verbatim ASR.`
}

func validatePrincipal(principal identity.Principal, session pebblestore.SessionSnapshot) error {
	if !principal.Valid() || session.AccountScopeID != principal.AccountScopeID || (session.UserID != "" && session.UserID != principal.UserID) {
		return errors.New("video transcription session ownership does not match the authenticated principal")
	}
	return nil
}

func terminal(status string) bool {
	return status == pebblestore.TranscriptionJobReady || status == pebblestore.TranscriptionJobFailed || status == pebblestore.TranscriptionJobCancelled || status == pebblestore.TranscriptionJobStale
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}

func boundedError(err error) string {
	if err == nil {
		return "provider operation failed"
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
