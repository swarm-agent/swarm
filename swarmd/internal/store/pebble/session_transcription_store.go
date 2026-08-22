package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	TranscriptionAttachmentSchemaVersion = 2
	TranscriptionAttachmentLegacyVersion = 1
	TranscriptionJobSchemaVersion        = 2
	NormalizedTranscriptSchemaVersion    = "normalized_transcript.v3"
	NormalizedTranscriptMultimodalLegacy = "normalized_transcript.v2"
	NormalizedTranscriptLegacyVersion    = "normalized_transcript.v1"

	TranscriptionMediaVideo      = "video"
	TranscriptionMediaAudio      = "audio"
	ContentEmptyVideoDescription = "No meaningful visual or auditory content was detected."

	TranscriptionJobQueued     = "queued"
	TranscriptionJobUploading  = "uploading"
	TranscriptionJobProcessing = "processing"
	TranscriptionJobReady      = "ready"
	TranscriptionJobPartial    = "partial"
	TranscriptionJobFailed     = "failed"
	TranscriptionJobCancelled  = "cancelled"
	TranscriptionJobStale      = "stale"

	TranscriptValidationPending   = "pending"
	TranscriptValidationValidated = "validated"
	TranscriptValidationRejected  = "rejected"

	V3SessionMutationBindTranscriptionAttachment = "transcription.attachment.bind"
	V3SessionMutationCreateTranscriptionJob      = "transcription.job.create"
	V3SessionMutationUpdateTranscriptionJob      = "transcription.job.update"

	defaultTranscriptRetention = 90 * 24 * time.Hour
	maxTranscriptRetention     = 365 * 24 * time.Hour
	maxTranscriptTextBytes     = 2 << 20
	maxTranscriptSegments      = 10_000
	maxTranscriptWords         = 50_000
	maxTranscriptWordBytes     = 1 << 10
	maxTranscriptSegmentBytes  = 16 << 10
)

// TranscriptionAttachmentRecord is the durable, message-scoped authority for a
// registered video or audio source. It stores only opaque source identity and
// fingerprint data; the registered source record remains private path authority.
// Legacy schema-v1 records omit MediaKind and are interpreted as video.
type TranscriptionAttachmentRecord struct {
	SchemaVersion      int    `json:"schema_version"`
	MediaKind          string `json:"media_kind,omitempty"`
	Ref                string `json:"ref"`
	AccountScopeID     string `json:"account_scope_id"`
	UserID             string `json:"user_id,omitempty"`
	WorkspaceID        string `json:"workspace_id"`
	SessionID          string `json:"session_id"`
	MessageID          string `json:"message_id"`
	SourceRecordRef    string `json:"source_record_ref"`
	SourceThreadID     string `json:"source_thread_id"`
	SourceClipID       string `json:"source_clip_id"`
	SourceFingerprint  string `json:"source_fingerprint"`
	FingerprintVersion string `json:"fingerprint_version"`
	MIMEType           string `json:"mime_type"`
	SizeBytes          int64  `json:"size_bytes"`
	CreatedAt          int64  `json:"created_at"`
}

// TranscriptionJob owns one provider-neutral attempt for one exact attachment.
// Provider file identifiers and local paths deliberately have no fields here.
type TranscriptionJob struct {
	SchemaVersion          int    `json:"schema_version"`
	Ref                    string `json:"ref"`
	TranscriptRef          string `json:"transcript_ref"`
	AccountScopeID         string `json:"account_scope_id"`
	UserID                 string `json:"user_id,omitempty"`
	WorkspaceID            string `json:"workspace_id"`
	SessionID              string `json:"session_id"`
	MessageID              string `json:"message_id"`
	AttachmentRef          string `json:"attachment_ref"`
	SourceFingerprint      string `json:"source_fingerprint"`
	ProviderID             string `json:"provider_id"`
	Model                  string `json:"model"`
	ModelSnapshot          string `json:"model_snapshot"`
	MediaSettingsHash      string `json:"media_settings_hash"`
	FocusNotes             string `json:"focus_notes,omitempty"`
	TranscriptSchema       string `json:"transcript_schema"`
	IdempotencyFingerprint string `json:"idempotency_fingerprint"`
	Status                 string `json:"status"`
	FailureCode            string `json:"failure_code,omitempty"`
	FailureReason          string `json:"failure_reason,omitempty"`
	ProviderCacheExpiresAt int64  `json:"provider_cache_expires_at,omitempty"`
	RetentionExpiresAt     int64  `json:"retention_expires_at"`
	CreatedAt              int64  `json:"created_at"`
	UpdatedAt              int64  `json:"updated_at"`
	StartedAt              int64  `json:"started_at,omitempty"`
	CompletedAt            int64  `json:"completed_at,omitempty"`
}

type NormalizedTranscriptSegment struct {
	StartMs      int64  `json:"start_ms"`
	EndMs        int64  `json:"end_ms"`
	Speech       string `json:"speech,omitempty"`
	Audio        string `json:"audio,omitempty"`
	Visual       string `json:"visual,omitempty"`
	OnScreenText string `json:"on_screen_text,omitempty"`
	Text         string `json:"text"`
}

type NormalizedTranscriptWord struct {
	Text       string   `json:"text"`
	StartMs    int64    `json:"start_ms"`
	EndMs      int64    `json:"end_ms"`
	Confidence *float64 `json:"confidence,omitempty"`
	Provenance string   `json:"provenance,omitempty"`
}

type NormalizedTranscriptMetadata struct {
	Language          string `json:"language,omitempty"`
	DurationMs        int64  `json:"duration_ms,omitempty"`
	Summary           string `json:"summary,omitempty"`
	ContentEmpty      bool   `json:"content_empty,omitempty"`
	ProviderID        string `json:"provider_id"`
	Model             string `json:"model"`
	ModelSnapshot     string `json:"model_snapshot"`
	MediaSettingsHash string `json:"media_settings_hash"`
	GeneratedAt       int64  `json:"generated_at"`
}

type TranscriptValidation struct {
	State       string `json:"state"`
	ValidatedAt int64  `json:"validated_at,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// NormalizedTranscript is provider-neutral and explicitly model-generated. Its
// typed metadata is intentionally bounded; arbitrary provider payloads, file
// URIs, and host paths are not part of this durable schema.
type NormalizedTranscript struct {
	SchemaVersion     string                        `json:"schema_version"`
	Ref               string                        `json:"ref"`
	JobRef            string                        `json:"job_ref"`
	AccountScopeID    string                        `json:"account_scope_id"`
	WorkspaceID       string                        `json:"workspace_id"`
	SessionID         string                        `json:"session_id"`
	MessageID         string                        `json:"message_id"`
	AttachmentRef     string                        `json:"attachment_ref"`
	SourceFingerprint string                        `json:"source_fingerprint"`
	ModelGenerated    bool                          `json:"model_generated"`
	Text              string                        `json:"text"`
	Segments          []NormalizedTranscriptSegment `json:"segments"`
	Words             []NormalizedTranscriptWord    `json:"words,omitempty"`
	Metadata          NormalizedTranscriptMetadata  `json:"metadata"`
	Validation        TranscriptValidation          `json:"validation"`
	ContentDigest     string                        `json:"content_digest"`
	CreatedAt         int64                         `json:"created_at"`
}

type V3TranscriptionMutation struct {
	Attachment     *TranscriptionAttachmentRecord `json:"attachment,omitempty"`
	Job            *TranscriptionJob              `json:"job,omitempty"`
	ExpectedStatus string                         `json:"expected_status,omitempty"`
}

type V3TranscriptionProjection struct {
	AttachmentRef string `json:"attachment_ref,omitempty"`
	JobRef        string `json:"job_ref,omitempty"`
	TranscriptRef string `json:"transcript_ref,omitempty"`
	Status        string `json:"status,omitempty"`
}

type BindVideoTranscriptionAttachmentInput struct {
	AccountScopeID  string
	UserID          string
	SessionID       string
	MessageID       string
	VideoThreadID   string
	VideoClipID     string
	ClientRequestID string
	NowUnixMs       int64
}

type BindAudioTranscriptionAttachmentInput struct {
	AccountScopeID  string
	UserID          string
	SessionID       string
	MessageID       string
	AudioSourceRef  string
	ClientRequestID string
	NowUnixMs       int64
}

type CreateTranscriptionJobInput struct {
	AccountScopeID         string
	UserID                 string
	SessionID              string
	AttachmentRef          string
	ProviderID             string
	Model                  string
	ModelSnapshot          string
	MediaSettingsHash      string
	FocusNotes             string
	TranscriptSchema       string
	ProviderCacheExpiresAt int64
	Retention              time.Duration
	NowUnixMs              int64
}

type TransitionTranscriptionJobInput struct {
	AccountScopeID  string
	UserID          string
	SessionID       string
	JobRef          string
	ExpectedStatus  string
	Status          string
	FailureCode     string
	FailureReason   string
	ClientRequestID string
	NowUnixMs       int64
}

type CommitNormalizedTranscriptInput struct {
	AccountScopeID string
	UserID         string
	SessionID      string
	JobRef         string
	Segments       []NormalizedTranscriptSegment
	Words          []NormalizedTranscriptWord
	Language       string
	DurationMs     int64
	Summary        string
	ContentEmpty   bool
	GeneratedAt    int64
	NowUnixMs      int64
}

type preparedV3TranscriptionMutation struct {
	Attachment *TranscriptionAttachmentRecord
	Job        *TranscriptionJob
	Projection V3TranscriptionProjection
}

func KeyTranscriptionAttachment(accountScopeID, sessionID, ref string) string {
	return fmt.Sprintf("v3/transcription/attachment/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(ref))
}

func TranscriptionAttachmentPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/transcription/attachment/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeyTranscriptionJob(accountScopeID, sessionID, ref string) string {
	return fmt.Sprintf("v3/transcription/job/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(ref))
}

func TranscriptionJobPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/transcription/job/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeyNormalizedTranscript(accountScopeID, sessionID, ref string) string {
	return fmt.Sprintf("v3/transcription/transcript/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(ref))
}

func NormalizedTranscriptPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/transcription/transcript/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func (s *SessionStore) BindVideoTranscriptionAttachment(input BindVideoTranscriptionAttachmentInput) (TranscriptionAttachmentRecord, bool, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.MessageID = strings.TrimSpace(input.MessageID)
	input.VideoThreadID = strings.TrimSpace(input.VideoThreadID)
	input.VideoClipID = strings.TrimSpace(input.VideoClipID)
	session, ok, err := s.GetSession(input.SessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		return TranscriptionAttachmentRecord{}, false, err
	}
	if err := validateTranscriptionSessionOwnership(session, input.AccountScopeID, input.UserID); err != nil {
		return TranscriptionAttachmentRecord{}, false, err
	}
	record, err := s.buildVideoTranscriptionAttachment(session, input.MessageID, input.VideoThreadID, input.VideoClipID, input.NowUnixMs)
	if err != nil {
		return TranscriptionAttachmentRecord{}, false, err
	}
	if stored, exists, readErr := s.GetTranscriptionAttachment(input.AccountScopeID, input.SessionID, record.Ref); readErr != nil {
		return TranscriptionAttachmentRecord{}, false, readErr
	} else if exists {
		if !sameTranscriptionAttachmentSource(stored, record) {
			return TranscriptionAttachmentRecord{}, false, errors.New("transcription attachment authority is inconsistent")
		}
		return stored, true, nil
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = "bind:" + record.Ref
	}
	mutation := V3SessionMutationInput{
		SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID,
		ClientRequestID: input.ClientRequestID, Kind: V3SessionMutationBindTranscriptionAttachment,
		Transcription: &V3TranscriptionMutation{Attachment: &record}, NowUnixMs: input.NowUnixMs,
	}
	mutation.PayloadHash = transcriptionMutationHash(mutation.Kind, mutation.Transcription)
	result, err := s.ApplyV3SessionMutation(mutation)
	if err != nil {
		return TranscriptionAttachmentRecord{}, false, err
	}
	stored, ok, err := s.GetTranscriptionAttachment(input.AccountScopeID, input.SessionID, record.Ref)
	return stored, result.Replayed, firstTranscriptionReadError(ok, err, "transcription attachment")
}

func (s *SessionStore) BindAudioTranscriptionAttachment(input BindAudioTranscriptionAttachmentInput) (TranscriptionAttachmentRecord, bool, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.MessageID = strings.TrimSpace(input.MessageID)
	input.AudioSourceRef = strings.TrimSpace(input.AudioSourceRef)
	session, ok, err := s.GetSession(input.SessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("session not found")
		}
		return TranscriptionAttachmentRecord{}, false, err
	}
	if err := validateTranscriptionSessionOwnership(session, input.AccountScopeID, input.UserID); err != nil {
		return TranscriptionAttachmentRecord{}, false, err
	}
	record, err := s.buildAudioTranscriptionAttachment(session, input.MessageID, input.AudioSourceRef, input.NowUnixMs)
	if err != nil {
		return TranscriptionAttachmentRecord{}, false, err
	}
	if stored, exists, readErr := s.GetTranscriptionAttachment(input.AccountScopeID, input.SessionID, record.Ref); readErr != nil {
		return TranscriptionAttachmentRecord{}, false, readErr
	} else if exists {
		if !sameTranscriptionAttachmentSource(stored, record) {
			return TranscriptionAttachmentRecord{}, false, errors.New("transcription attachment authority is inconsistent")
		}
		return stored, true, nil
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = "bind:" + record.Ref
	}
	mutation := V3SessionMutationInput{
		SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID,
		ClientRequestID: input.ClientRequestID, Kind: V3SessionMutationBindTranscriptionAttachment,
		Transcription: &V3TranscriptionMutation{Attachment: &record}, NowUnixMs: input.NowUnixMs,
	}
	mutation.PayloadHash = transcriptionMutationHash(mutation.Kind, mutation.Transcription)
	result, err := s.ApplyV3SessionMutation(mutation)
	if err != nil {
		return TranscriptionAttachmentRecord{}, false, err
	}
	stored, ok, err := s.GetTranscriptionAttachment(input.AccountScopeID, input.SessionID, record.Ref)
	return stored, result.Replayed, firstTranscriptionReadError(ok, err, "transcription attachment")
}

func (s *SessionStore) CreateTranscriptionJob(input CreateTranscriptionJobInput) (TranscriptionJob, bool, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.AttachmentRef = strings.TrimSpace(input.AttachmentRef)
	attachment, ok, err := s.GetTranscriptionAttachment(input.AccountScopeID, input.SessionID, input.AttachmentRef)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("transcription attachment not found in authenticated session scope")
		}
		return TranscriptionJob{}, false, err
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	input.FocusNotes = strings.TrimSpace(input.FocusNotes)
	if len(input.FocusNotes) > 500 {
		return TranscriptionJob{}, false, errors.New("transcription focus notes exceed the bounded limit")
	}
	input.TranscriptSchema = strings.TrimSpace(input.TranscriptSchema)
	if input.TranscriptSchema == "" {
		input.TranscriptSchema = NormalizedTranscriptSchemaVersion
	}
	if input.TranscriptSchema != NormalizedTranscriptSchemaVersion {
		return TranscriptionJob{}, false, errors.New("new transcription jobs require the current normalized transcript schema")
	}
	retention := input.Retention
	if retention <= 0 {
		retention = defaultTranscriptRetention
	}
	if retention > maxTranscriptRetention {
		retention = maxTranscriptRetention
	}
	identity := transcriptionDigest(strings.Join([]string{
		attachment.SourceFingerprint, strings.TrimSpace(input.ModelSnapshot), strings.TrimSpace(input.MediaSettingsHash), input.FocusNotes, input.TranscriptSchema,
	}, "\x00"))
	jobRef := "trjob_" + identity
	job := TranscriptionJob{
		SchemaVersion: TranscriptionJobSchemaVersion, Ref: jobRef, TranscriptRef: "transcript_" + transcriptionDigest(jobRef),
		AccountScopeID: attachment.AccountScopeID, UserID: attachment.UserID, WorkspaceID: attachment.WorkspaceID,
		SessionID: attachment.SessionID, MessageID: attachment.MessageID, AttachmentRef: attachment.Ref,
		SourceFingerprint: attachment.SourceFingerprint, ProviderID: strings.ToLower(strings.TrimSpace(input.ProviderID)),
		Model: strings.TrimSpace(input.Model), ModelSnapshot: strings.TrimSpace(input.ModelSnapshot), MediaSettingsHash: strings.TrimSpace(input.MediaSettingsHash),
		FocusNotes: input.FocusNotes, TranscriptSchema: input.TranscriptSchema, IdempotencyFingerprint: identity, Status: TranscriptionJobQueued,
		ProviderCacheExpiresAt: input.ProviderCacheExpiresAt, RetentionExpiresAt: now + retention.Milliseconds(), CreatedAt: now, UpdatedAt: now,
	}
	mutation := V3SessionMutationInput{
		SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID,
		ClientRequestID: "transcription-job:" + identity, Kind: V3SessionMutationCreateTranscriptionJob,
		Transcription: &V3TranscriptionMutation{Job: &job}, NowUnixMs: now,
	}
	mutation.PayloadHash = transcriptionMutationHash(mutation.Kind, mutation.Transcription)
	result, err := s.ApplyV3SessionMutation(mutation)
	if err != nil {
		return TranscriptionJob{}, false, err
	}
	stored, ok, err := s.GetTranscriptionJob(input.AccountScopeID, input.SessionID, jobRef)
	return stored, result.Replayed, firstTranscriptionReadError(ok, err, "transcription job")
}

func (s *SessionStore) TransitionTranscriptionJob(input TransitionTranscriptionJobInput) (TranscriptionJob, bool, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.JobRef = strings.TrimSpace(input.JobRef)
	job, ok, err := s.GetTranscriptionJob(input.AccountScopeID, input.SessionID, input.JobRef)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("transcription job not found in authenticated session scope")
		}
		return TranscriptionJob{}, false, err
	}
	if job.UserID != input.UserID {
		return TranscriptionJob{}, false, errors.New("transcription job user ownership mismatch")
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	next := job
	next.Status = strings.ToLower(strings.TrimSpace(input.Status))
	next.FailureCode = boundedTranscriptionField(input.FailureCode, 128)
	next.FailureReason = boundedTranscriptionField(input.FailureReason, 1024)
	next.UpdatedAt = now
	if next.StartedAt == 0 && (next.Status == TranscriptionJobUploading || next.Status == TranscriptionJobProcessing || next.Status == TranscriptionJobPartial) {
		next.StartedAt = now
	}
	if isTerminalTranscriptionStatus(next.Status) {
		next.CompletedAt = now
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = "transcription-transition:" + next.Ref + ":" + next.Status
	}
	mutation := V3SessionMutationInput{
		SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID,
		ClientRequestID: input.ClientRequestID, Kind: V3SessionMutationUpdateTranscriptionJob,
		Transcription: &V3TranscriptionMutation{Job: &next, ExpectedStatus: strings.ToLower(strings.TrimSpace(input.ExpectedStatus))}, NowUnixMs: now,
	}
	mutation.PayloadHash = transcriptionMutationHash(mutation.Kind, mutation.Transcription)
	result, err := s.ApplyV3SessionMutation(mutation)
	if err != nil {
		return TranscriptionJob{}, false, err
	}
	stored, ok, err := s.GetTranscriptionJob(input.AccountScopeID, input.SessionID, input.JobRef)
	return stored, result.Replayed, firstTranscriptionReadError(ok, err, "transcription job")
}

// CommitNormalizedTranscript uses a deliberate two-phase protocol: first commit
// immutable normalized content synchronously, then read and validate it through
// the canonical store, and only then publish the ready lifecycle mutation. A
// crash can leave a recoverable processing job, never a false-ready job.
func (s *SessionStore) CommitNormalizedTranscript(input CommitNormalizedTranscriptInput) (NormalizedTranscript, TranscriptionJob, bool, error) {
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.JobRef = strings.TrimSpace(input.JobRef)
	job, ok, err := s.GetTranscriptionJob(input.AccountScopeID, input.SessionID, input.JobRef)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("transcription job not found in authenticated session scope")
		}
		return NormalizedTranscript{}, TranscriptionJob{}, false, err
	}
	if job.UserID != input.UserID {
		return NormalizedTranscript{}, TranscriptionJob{}, false, errors.New("transcription job user ownership mismatch")
	}
	if job.TranscriptSchema != NormalizedTranscriptSchemaVersion {
		return NormalizedTranscript{}, TranscriptionJob{}, false, errors.New("new transcript commits require the current normalized transcript schema")
	}
	if job.Status != TranscriptionJobProcessing && job.Status != TranscriptionJobPartial {
		return NormalizedTranscript{}, TranscriptionJob{}, false, fmt.Errorf("transcription content cannot be committed while job is %s", job.Status)
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	transcript := NormalizedTranscript{
		SchemaVersion: job.TranscriptSchema, Ref: job.TranscriptRef, JobRef: job.Ref, AccountScopeID: job.AccountScopeID,
		WorkspaceID: job.WorkspaceID, SessionID: job.SessionID, MessageID: job.MessageID, AttachmentRef: job.AttachmentRef,
		SourceFingerprint: job.SourceFingerprint, ModelGenerated: true, Segments: append([]NormalizedTranscriptSegment(nil), input.Segments...),
		Words:      append([]NormalizedTranscriptWord(nil), input.Words...),
		Metadata:   NormalizedTranscriptMetadata{Language: input.Language, DurationMs: input.DurationMs, Summary: input.Summary, ContentEmpty: input.ContentEmpty, ProviderID: job.ProviderID, Model: job.Model, ModelSnapshot: job.ModelSnapshot, MediaSettingsHash: job.MediaSettingsHash, GeneratedAt: input.GeneratedAt},
		Validation: TranscriptValidation{State: TranscriptValidationValidated, ValidatedAt: now}, CreatedAt: now,
	}
	transcript, err = normalizeAndValidateTranscript(transcript)
	if err != nil {
		return NormalizedTranscript{}, TranscriptionJob{}, false, err
	}
	replayed, err := s.putNormalizedTranscriptIfAbsent(transcript)
	if err != nil {
		return NormalizedTranscript{}, TranscriptionJob{}, false, err
	}
	readBack, ok, err := s.GetNormalizedTranscript(input.AccountScopeID, input.SessionID, transcript.Ref)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("durable transcript read-back failed")
		}
		return NormalizedTranscript{}, TranscriptionJob{}, false, err
	}
	if readBack.ContentDigest != transcript.ContentDigest || readBack.Validation.State != TranscriptValidationValidated {
		return NormalizedTranscript{}, TranscriptionJob{}, false, errors.New("durable transcript read-back validation failed")
	}
	ready, transitionReplayed, err := s.TransitionTranscriptionJob(TransitionTranscriptionJobInput{
		AccountScopeID: input.AccountScopeID, UserID: input.UserID, SessionID: input.SessionID, JobRef: input.JobRef,
		ExpectedStatus: job.Status, Status: TranscriptionJobReady,
		ClientRequestID: "transcription-ready:" + job.Ref + ":" + transcript.ContentDigest, NowUnixMs: now,
	})
	if err != nil {
		return readBack, TranscriptionJob{}, replayed, err
	}
	return readBack, ready, replayed || transitionReplayed, nil
}

func (s *SessionStore) GetTranscriptionAttachment(accountScopeID, sessionID, ref string) (TranscriptionAttachmentRecord, bool, error) {
	var record TranscriptionAttachmentRecord
	accountScopeID, sessionID, ref = strings.TrimSpace(accountScopeID), strings.TrimSpace(sessionID), strings.TrimSpace(ref)
	if !validOpaqueTranscriptionRef(ref, "vatt_") {
		return record, false, errors.New("invalid transcription attachment reference")
	}
	ok, err := s.store.GetJSON(KeyTranscriptionAttachment(accountScopeID, sessionID, ref), &record)
	if err != nil || !ok {
		return TranscriptionAttachmentRecord{}, ok, err
	}
	if record.AccountScopeID != accountScopeID || record.SessionID != sessionID || record.Ref != ref {
		return TranscriptionAttachmentRecord{}, false, errors.New("transcription attachment ownership metadata is inconsistent")
	}
	if record.SchemaVersion == TranscriptionAttachmentLegacyVersion && strings.TrimSpace(record.MediaKind) == "" {
		record.MediaKind = TranscriptionMediaVideo
	}
	if record.SchemaVersion != TranscriptionAttachmentSchemaVersion && record.SchemaVersion != TranscriptionAttachmentLegacyVersion {
		return TranscriptionAttachmentRecord{}, false, errors.New("transcription attachment schema is unsupported")
	}
	if record.MediaKind != TranscriptionMediaVideo && record.MediaKind != TranscriptionMediaAudio {
		return TranscriptionAttachmentRecord{}, false, errors.New("transcription attachment media kind is invalid")
	}
	return record, true, nil
}

func (s *SessionStore) GetTranscriptionJob(accountScopeID, sessionID, ref string) (TranscriptionJob, bool, error) {
	var job TranscriptionJob
	accountScopeID, sessionID, ref = strings.TrimSpace(accountScopeID), strings.TrimSpace(sessionID), strings.TrimSpace(ref)
	if !validOpaqueTranscriptionRef(ref, "trjob_") {
		return job, false, errors.New("invalid transcription job reference")
	}
	ok, err := s.store.GetJSON(KeyTranscriptionJob(accountScopeID, sessionID, ref), &job)
	if err != nil || !ok {
		return TranscriptionJob{}, ok, err
	}
	if job.AccountScopeID != accountScopeID || job.SessionID != sessionID || job.Ref != ref {
		return TranscriptionJob{}, false, errors.New("transcription job ownership metadata is inconsistent")
	}
	return job, true, nil
}

func (s *SessionStore) FindNormalizedTranscriptByRef(accountScopeID, userID, workspaceID, ref string) (NormalizedTranscript, bool, error) {
	accountScopeID, userID, workspaceID, ref = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), strings.TrimSpace(workspaceID), strings.TrimSpace(ref)
	if accountScopeID == "" || userID == "" || workspaceID == "" || !validOpaqueTranscriptionRef(ref, "transcript_") {
		return NormalizedTranscript{}, false, errors.New("valid account, user, workspace, and transcript references are required")
	}
	var found NormalizedTranscript
	var scanned int
	err := s.store.IteratePrefix(fmt.Sprintf("v3/transcription/transcript/%s/", keyPart(accountScopeID)), 10_001, func(_ string, value []byte) error {
		scanned++
		if scanned > 10_000 {
			return errors.New("transcript workspace lookup exceeded the bounded record limit")
		}
		var candidate NormalizedTranscript
		if err := json.Unmarshal(value, &candidate); err != nil {
			return err
		}
		if candidate.Ref != ref || candidate.AccountScopeID != accountScopeID || candidate.WorkspaceID != workspaceID {
			return nil
		}
		job, ok, err := s.GetTranscriptionJob(accountScopeID, candidate.SessionID, candidate.JobRef)
		if err != nil {
			return err
		}
		if !ok || job.UserID != userID || job.WorkspaceID != workspaceID || job.Status != TranscriptionJobReady {
			return nil
		}
		if found.Ref != "" && found.ContentDigest != candidate.ContentDigest {
			return errors.New("transcript reference collision across workspace records")
		}
		found = candidate
		return nil
	})
	if err != nil || found.Ref == "" {
		return NormalizedTranscript{}, false, err
	}
	originalDigest := found.ContentDigest
	normalized, err := normalizeAndValidateTranscript(found)
	if err != nil || normalized.ContentDigest != originalDigest {
		if err == nil {
			err = errors.New("transcript content digest mismatch")
		}
		return NormalizedTranscript{}, false, err
	}
	return normalized, true, nil
}

func (s *SessionStore) FindNormalizedTranscriptBySourceFingerprint(accountScopeID, userID, workspaceID, sourceFingerprint string) (NormalizedTranscript, bool, error) {
	accountScopeID, userID, workspaceID, sourceFingerprint = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), strings.TrimSpace(workspaceID), strings.ToLower(strings.TrimSpace(sourceFingerprint))
	if accountScopeID == "" || userID == "" || workspaceID == "" || !validFingerprint(sourceFingerprint) {
		return NormalizedTranscript{}, false, errors.New("valid account, user, workspace, and source fingerprint are required")
	}
	var found NormalizedTranscript
	var scanned int
	err := s.store.IteratePrefix(fmt.Sprintf("v3/transcription/transcript/%s/", keyPart(accountScopeID)), 10_001, func(_ string, value []byte) error {
		scanned++
		if scanned > 10_000 {
			return errors.New("source transcript lookup exceeded the bounded record limit")
		}
		var candidate NormalizedTranscript
		if err := json.Unmarshal(value, &candidate); err != nil {
			return err
		}
		if candidate.AccountScopeID != accountScopeID || candidate.WorkspaceID != workspaceID || candidate.SourceFingerprint != sourceFingerprint {
			return nil
		}
		job, ok, err := s.GetTranscriptionJob(accountScopeID, candidate.SessionID, candidate.JobRef)
		if err != nil {
			return err
		}
		if !ok || job.UserID != userID || job.WorkspaceID != workspaceID || job.Status != TranscriptionJobReady {
			return nil
		}
		if found.Ref == "" || candidate.CreatedAt > found.CreatedAt {
			found = candidate
		}
		return nil
	})
	if err != nil || found.Ref == "" {
		return NormalizedTranscript{}, false, err
	}
	originalDigest := found.ContentDigest
	normalized, err := normalizeAndValidateTranscript(found)
	if err != nil || normalized.ContentDigest != originalDigest {
		if err == nil {
			err = errors.New("transcript content digest mismatch")
		}
		return NormalizedTranscript{}, false, err
	}
	return normalized, true, nil
}

func (s *SessionStore) GetNormalizedTranscript(accountScopeID, sessionID, ref string) (NormalizedTranscript, bool, error) {
	var transcript NormalizedTranscript
	accountScopeID, sessionID, ref = strings.TrimSpace(accountScopeID), strings.TrimSpace(sessionID), strings.TrimSpace(ref)
	if !validOpaqueTranscriptionRef(ref, "transcript_") {
		return transcript, false, errors.New("invalid transcript reference")
	}
	ok, err := s.store.GetJSON(KeyNormalizedTranscript(accountScopeID, sessionID, ref), &transcript)
	if err != nil || !ok {
		return NormalizedTranscript{}, ok, err
	}
	if transcript.AccountScopeID != accountScopeID || transcript.SessionID != sessionID || transcript.Ref != ref {
		return NormalizedTranscript{}, false, errors.New("transcript ownership metadata is inconsistent")
	}
	originalDigest := transcript.ContentDigest
	normalized, err := normalizeAndValidateTranscript(transcript)
	if err != nil || normalized.ContentDigest != originalDigest {
		if err == nil {
			err = errors.New("transcript content digest mismatch")
		}
		return NormalizedTranscript{}, false, err
	}
	return normalized, true, nil
}

func (s *SessionStore) buildVideoTranscriptionAttachment(session SessionSnapshot, messageID, threadID, clipID string, now int64) (TranscriptionAttachmentRecord, error) {
	messageID, threadID, clipID = strings.TrimSpace(messageID), strings.TrimSpace(threadID), strings.TrimSpace(clipID)
	if messageID == "" || threadID == "" || clipID == "" {
		return TranscriptionAttachmentRecord{}, errors.New("message, video thread, and video clip ids are required")
	}
	message, ok, err := s.findV3MessageByID(session, messageID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("triggering message not found in authenticated session scope")
		}
		return TranscriptionAttachmentRecord{}, err
	}
	attached := false
	for _, reference := range message.VideoAttachments {
		if strings.TrimSpace(reference.Ref) == clipID {
			attached = true
			break
		}
	}
	if !attached {
		return TranscriptionAttachmentRecord{}, errors.New("video source is not attached to the triggering message")
	}
	workspaceID := transcriptionWorkspaceID(session)
	if strings.HasPrefix(clipID, "videosrc_") {
		record, ok, err := s.GetVideoSourceRecord(session.AccountScopeID, workspaceID, clipID)
		if err != nil || !ok {
			if err == nil {
				err = errors.New("registered video source not found in authenticated session scope")
			}
			return TranscriptionAttachmentRecord{}, err
		}
		file, err := openValidatedVideoSource(record)
		if err != nil {
			return TranscriptionAttachmentRecord{}, err
		}
		file.Close()
		sourceRecordRef := record.Ref
		ref := "vatt_" + transcriptionDigest(strings.Join([]string{session.AccountScopeID, workspaceID, session.ID, messageID, sourceRecordRef, record.SourceFingerprint}, "\x00"))
		if now == 0 {
			now = time.Now().UnixMilli()
		}
		return TranscriptionAttachmentRecord{
			SchemaVersion: TranscriptionAttachmentSchemaVersion, MediaKind: TranscriptionMediaVideo, Ref: ref, AccountScopeID: session.AccountScopeID, UserID: session.UserID,
			WorkspaceID: workspaceID, SessionID: session.ID, MessageID: messageID, SourceRecordRef: sourceRecordRef,
			SourceThreadID: "registered-source", SourceClipID: record.Ref, SourceFingerprint: record.SourceFingerprint, FingerprintVersion: "sha256-root-relative-size-mtime.v1",
			MIMEType: record.MIMEType, SizeBytes: record.SizeBytes, CreatedAt: now,
		}, nil
	}
	var thread VideoThreadSnapshot
	ok, err = s.store.GetJSON(KeyVideoThreadForAccount(session.AccountScopeID, threadID), &thread)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("registered video source not found in authenticated account scope")
		}
		return TranscriptionAttachmentRecord{}, err
	}
	if strings.TrimSpace(thread.AccountScopeID) != session.AccountScopeID || strings.TrimSpace(thread.WorkspacePath) != strings.TrimSpace(session.WorkspacePath) {
		return TranscriptionAttachmentRecord{}, errors.New("registered video source is outside the session workspace scope")
	}
	var clip VideoClipSnapshot
	found := false
	for _, candidate := range thread.VideoClips {
		if strings.TrimSpace(candidate.ID) == clipID {
			clip, found = candidate, true
			break
		}
	}
	if !found {
		return TranscriptionAttachmentRecord{}, errors.New("registered video clip not found")
	}
	fingerprint := transcriptionDigest(strings.Join([]string{filepath.Clean(clip.Path), fmt.Sprint(clip.SizeBytes), fmt.Sprint(clip.ModifiedAt)}, "\x00"))
	sourceRecordRef := "videosrc_" + transcriptionDigest(strings.Join([]string{session.AccountScopeID, workspaceID, threadID, clipID}, "\x00"))
	ref := "vatt_" + transcriptionDigest(strings.Join([]string{session.AccountScopeID, workspaceID, session.ID, messageID, sourceRecordRef, fingerprint}, "\x00"))
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	return TranscriptionAttachmentRecord{
		SchemaVersion: TranscriptionAttachmentSchemaVersion, MediaKind: TranscriptionMediaVideo, Ref: ref, AccountScopeID: session.AccountScopeID, UserID: session.UserID,
		WorkspaceID: workspaceID, SessionID: session.ID, MessageID: messageID, SourceRecordRef: sourceRecordRef,
		SourceThreadID: threadID, SourceClipID: clipID, SourceFingerprint: fingerprint, FingerprintVersion: "sha256-path-size-mtime.v1",
		MIMEType: videoMIMEFromExtension(clip.Extension), SizeBytes: clip.SizeBytes, CreatedAt: now,
	}, nil
}

func (s *SessionStore) buildAudioTranscriptionAttachment(session SessionSnapshot, messageID, sourceRef string, now int64) (TranscriptionAttachmentRecord, error) {
	messageID, sourceRef = strings.TrimSpace(messageID), strings.TrimSpace(sourceRef)
	if messageID != "" || !strings.HasPrefix(sourceRef, "audiosrc_") {
		return TranscriptionAttachmentRecord{}, errors.New("registered audio transcription requires a valid source reference without a forged message")
	}
	var record AudioSourceRecord
	var workspaceID string
	var ok bool
	var err error
	for _, candidateWorkspaceID := range SessionVideoWorkspaceIDs(session) {
		record, ok, err = s.GetAudioSourceRecord(session.AccountScopeID, candidateWorkspaceID, sourceRef)
		if err != nil || ok {
			workspaceID = candidateWorkspaceID
			break
		}
	}
	if err != nil || !ok {
		if err == nil {
			err = errors.New("registered audio source not found in authenticated session scope")
		}
		return TranscriptionAttachmentRecord{}, err
	}
	file, err := OpenValidatedAudioSource(record)
	if err != nil {
		return TranscriptionAttachmentRecord{}, err
	}
	if err := file.Close(); err != nil {
		return TranscriptionAttachmentRecord{}, err
	}
	ref := "vatt_" + transcriptionDigest(strings.Join([]string{session.AccountScopeID, workspaceID, session.ID, record.Ref, record.SourceFingerprint}, "\x00"))
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	return TranscriptionAttachmentRecord{
		SchemaVersion: TranscriptionAttachmentSchemaVersion, MediaKind: TranscriptionMediaAudio, Ref: ref,
		AccountScopeID: session.AccountScopeID, UserID: session.UserID, WorkspaceID: workspaceID,
		SessionID: session.ID, MessageID: messageID, SourceRecordRef: record.Ref,
		SourceThreadID: "registered-audio-source", SourceClipID: record.Ref,
		SourceFingerprint: record.SourceFingerprint, FingerprintVersion: record.FingerprintVersion,
		MIMEType: record.MIMEType, SizeBytes: record.SizeBytes, CreatedAt: now,
	}, nil
}

func (s *SessionStore) GetV3MessageByID(sessionID, messageID string) (MessageSnapshot, bool, error) {
	session, ok, err := s.GetSession(strings.TrimSpace(sessionID))
	if err != nil || !ok {
		return MessageSnapshot{}, false, err
	}
	return s.findV3MessageByID(session, strings.TrimSpace(messageID))
}

// OpenTranscriptionAttachmentSource resolves the private source path only after
// revalidating the durable attachment against the authenticated session scope.
func (s *SessionStore) OpenTranscriptionAttachmentSource(accountScopeID, sessionID, attachmentRef string) (*os.File, error) {
	attachment, ok, err := s.GetTranscriptionAttachment(strings.TrimSpace(accountScopeID), strings.TrimSpace(sessionID), strings.TrimSpace(attachmentRef))
	if err != nil || !ok {
		if err == nil {
			err = errors.New("transcription attachment not found in authenticated session scope")
		}
		return nil, err
	}
	session, ok, err := s.GetSession(attachment.SessionID)
	if err != nil || !ok || session.AccountScopeID != attachment.AccountScopeID {
		if err == nil {
			err = errors.New("transcription attachment session authority is unavailable")
		}
		return nil, err
	}
	if attachment.MediaKind == TranscriptionMediaAudio {
		record, found, readErr := s.GetAudioSourceRecord(attachment.AccountScopeID, attachment.WorkspaceID, attachment.SourceRecordRef)
		if readErr != nil {
			return nil, readErr
		}
		if !found {
			return nil, errors.New("transcription audio source is unavailable in the session workspace scope")
		}
		if record.SourceFingerprint != attachment.SourceFingerprint || record.FingerprintVersion != attachment.FingerprintVersion || record.MIMEType != attachment.MIMEType || record.SizeBytes != attachment.SizeBytes {
			return nil, errors.New("transcription source fingerprint is stale")
		}
		return OpenValidatedAudioSource(record)
	}
	for _, workspaceID := range SessionVideoWorkspaceIDs(session) {
		record, found, readErr := s.GetVideoSourceRecord(attachment.AccountScopeID, workspaceID, attachment.SourceRecordRef)
		if readErr != nil {
			return nil, readErr
		}
		if !found {
			continue
		}
		if record.SourceFingerprint != attachment.SourceFingerprint || record.MIMEType != attachment.MIMEType || record.SizeBytes != attachment.SizeBytes {
			return nil, errors.New("transcription source fingerprint is stale")
		}
		return openValidatedVideoSource(record)
	}
	return nil, errors.New("transcription source is unavailable in the session workspace scope")
}

func (s *SessionStore) findV3MessageByID(session SessionSnapshot, messageID string) (MessageSnapshot, bool, error) {
	var found MessageSnapshot
	err := s.store.IteratePrefix(V3SessionMessagePrefix(session.ID), 100_000, func(_ string, value []byte) error {
		var message MessageSnapshot
		if err := json.Unmarshal(value, &message); err != nil {
			return err
		}
		if strings.TrimSpace(message.ID) != messageID {
			return nil
		}
		if message.SessionID != session.ID || message.AccountScopeID != session.AccountScopeID || (session.UserID != "" && message.UserID != session.UserID) {
			return errors.New("triggering message ownership metadata is inconsistent")
		}
		found = message
		return nil
	})
	return found, found.ID != "", err
}

func (s *SessionStore) prepareV3TranscriptionMutation(input V3SessionMutationInput, now int64) (preparedV3TranscriptionMutation, error) {
	if input.Transcription == nil {
		return preparedV3TranscriptionMutation{}, nil
	}
	session, ok, err := s.GetSession(input.SessionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("transcription mutation session not found")
		}
		return preparedV3TranscriptionMutation{}, err
	}
	if err := validateTranscriptionSessionOwnership(session, input.AccountScopeID, input.UserID); err != nil {
		return preparedV3TranscriptionMutation{}, err
	}
	switch input.Kind {
	case V3SessionMutationBindTranscriptionAttachment:
		if input.Transcription.Attachment == nil || input.Transcription.Job != nil {
			return preparedV3TranscriptionMutation{}, errors.New("attachment bind requires exactly one attachment")
		}
		record := *input.Transcription.Attachment
		var expected TranscriptionAttachmentRecord
		var err error
		switch record.MediaKind {
		case TranscriptionMediaVideo:
			expected, err = s.buildVideoTranscriptionAttachment(session, record.MessageID, record.SourceThreadID, record.SourceClipID, record.CreatedAt)
		case TranscriptionMediaAudio:
			expected, err = s.buildAudioTranscriptionAttachment(session, record.MessageID, record.SourceRecordRef, record.CreatedAt)
		default:
			err = errors.New("transcription attachment media kind is invalid")
		}
		if err != nil {
			return preparedV3TranscriptionMutation{}, err
		}
		if !equalTranscriptionAttachmentAuthority(record, expected) {
			return preparedV3TranscriptionMutation{}, errors.New("transcription attachment does not match trusted media source authority")
		}
		return preparedV3TranscriptionMutation{Attachment: &record, Projection: V3TranscriptionProjection{AttachmentRef: record.Ref}}, nil
	case V3SessionMutationCreateTranscriptionJob:
		if input.Transcription.Job == nil || input.Transcription.Attachment != nil {
			return preparedV3TranscriptionMutation{}, errors.New("transcription job create requires exactly one job")
		}
		job := *input.Transcription.Job
		attachment, ok, err := s.GetTranscriptionAttachment(input.AccountScopeID, input.SessionID, job.AttachmentRef)
		if err != nil || !ok {
			if err == nil {
				err = errors.New("transcription attachment not found")
			}
			return preparedV3TranscriptionMutation{}, err
		}
		if err := validateJobMatchesAttachment(job, attachment); err != nil {
			return preparedV3TranscriptionMutation{}, err
		}
		if job.SchemaVersion != TranscriptionJobSchemaVersion || job.TranscriptSchema != NormalizedTranscriptSchemaVersion || job.Status != TranscriptionJobQueued || job.ProviderID == "" || job.Model == "" || job.ModelSnapshot == "" || job.MediaSettingsHash == "" {
			return preparedV3TranscriptionMutation{}, errors.New("new transcription job requires the current schema, queued status, and complete model identity")
		}
		if existing, ok, err := s.GetTranscriptionJob(input.AccountScopeID, input.SessionID, job.Ref); err != nil {
			return preparedV3TranscriptionMutation{}, err
		} else if ok && existing.IdempotencyFingerprint != job.IdempotencyFingerprint {
			return preparedV3TranscriptionMutation{}, errors.New("transcription job identity collision")
		}
		return preparedV3TranscriptionMutation{Job: &job, Projection: V3TranscriptionProjection{AttachmentRef: job.AttachmentRef, JobRef: job.Ref, TranscriptRef: job.TranscriptRef, Status: job.Status}}, nil
	case V3SessionMutationUpdateTranscriptionJob:
		if input.Transcription.Job == nil || input.Transcription.Attachment != nil {
			return preparedV3TranscriptionMutation{}, errors.New("transcription job update requires exactly one job")
		}
		next := *input.Transcription.Job
		current, ok, err := s.GetTranscriptionJob(input.AccountScopeID, input.SessionID, next.Ref)
		if err != nil || !ok {
			if err == nil {
				err = errors.New("transcription job not found")
			}
			return preparedV3TranscriptionMutation{}, err
		}
		expectedStatus := strings.TrimSpace(input.Transcription.ExpectedStatus)
		if expectedStatus != "" && current.Status != expectedStatus {
			return preparedV3TranscriptionMutation{}, fmt.Errorf("transcription job status conflict: expected %s, actual %s", expectedStatus, current.Status)
		}
		if !equalTranscriptionJobAuthority(current, next) {
			return preparedV3TranscriptionMutation{}, errors.New("transcription job immutable authority cannot be changed")
		}
		if !validTranscriptionTransition(current.Status, next.Status) {
			return preparedV3TranscriptionMutation{}, fmt.Errorf("invalid transcription job transition %s -> %s", current.Status, next.Status)
		}
		if next.Status == TranscriptionJobReady {
			transcript, ok, err := s.GetNormalizedTranscript(input.AccountScopeID, input.SessionID, next.TranscriptRef)
			if err != nil || !ok || transcript.JobRef != next.Ref || transcript.Validation.State != TranscriptValidationValidated {
				if err == nil {
					err = errors.New("ready requires a durably readable validated transcript")
				}
				return preparedV3TranscriptionMutation{}, err
			}
		}
		if next.Status != TranscriptionJobFailed {
			next.FailureCode, next.FailureReason = "", ""
		}
		return preparedV3TranscriptionMutation{Job: &next, Projection: V3TranscriptionProjection{AttachmentRef: next.AttachmentRef, JobRef: next.Ref, TranscriptRef: next.TranscriptRef, Status: next.Status}}, nil
	default:
		return preparedV3TranscriptionMutation{}, errors.New("transcription payload requires a transcription mutation kind")
	}
}

func setV3TranscriptionMutationInBatch(batch *pebble.Batch, prepared preparedV3TranscriptionMutation) error {
	if prepared.Attachment != nil {
		payload, err := json.Marshal(prepared.Attachment)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyTranscriptionAttachment(prepared.Attachment.AccountScopeID, prepared.Attachment.SessionID, prepared.Attachment.Ref)), payload, nil); err != nil {
			return err
		}
	}
	if prepared.Job != nil {
		payload, err := json.Marshal(prepared.Job)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyTranscriptionJob(prepared.Job.AccountScopeID, prepared.Job.SessionID, prepared.Job.Ref)), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) putNormalizedTranscriptIfAbsent(transcript NormalizedTranscript) (bool, error) {
	unlock := s.store.sessionMutations.lockSessions(transcript.SessionID)
	defer unlock()
	if existing, ok, err := s.GetNormalizedTranscript(transcript.AccountScopeID, transcript.SessionID, transcript.Ref); err != nil {
		return false, err
	} else if ok {
		if existing.ContentDigest != transcript.ContentDigest {
			return false, errors.New("immutable transcript reference collision")
		}
		return true, nil
	}
	payload, err := json.Marshal(transcript)
	if err != nil {
		return false, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyNormalizedTranscript(transcript.AccountScopeID, transcript.SessionID, transcript.Ref)), payload, nil); err != nil {
		return false, err
	}
	return false, batch.Commit(pebble.Sync)
}

func normalizeAndValidateTranscript(transcript NormalizedTranscript) (NormalizedTranscript, error) {
	transcript.SchemaVersion = strings.TrimSpace(transcript.SchemaVersion)
	transcript.Text = strings.TrimSpace(transcript.Text)
	transcript.Metadata.Language = strings.TrimSpace(transcript.Metadata.Language)
	if len(transcript.Metadata.Language) > 64 {
		return NormalizedTranscript{}, errors.New("normalized transcript language exceeds the bounded limit")
	}
	transcript.Metadata.Summary = strings.TrimSpace(transcript.Metadata.Summary)
	if len(transcript.Metadata.Summary) > maxTranscriptSegmentBytes {
		return NormalizedTranscript{}, errors.New("normalized transcript summary exceeds the bounded limit")
	}
	transcript.Validation.State = strings.ToLower(strings.TrimSpace(transcript.Validation.State))
	transcript.Validation.Reason = boundedTranscriptionField(transcript.Validation.Reason, 512)
	if (transcript.SchemaVersion != NormalizedTranscriptSchemaVersion && transcript.SchemaVersion != NormalizedTranscriptMultimodalLegacy && transcript.SchemaVersion != NormalizedTranscriptLegacyVersion) || !transcript.ModelGenerated {
		return NormalizedTranscript{}, errors.New("normalized transcript schema and model-generated marker are required")
	}
	if (transcript.SchemaVersion == NormalizedTranscriptLegacyVersion || transcript.SchemaVersion == NormalizedTranscriptMultimodalLegacy) && len(transcript.Words) > 0 {
		return NormalizedTranscript{}, errors.New("legacy normalized transcript schemas cannot contain word timing")
	}
	if transcript.SchemaVersion == NormalizedTranscriptLegacyVersion && (len(transcript.Text) == 0 || len(transcript.Text) > maxTranscriptTextBytes) {
		return NormalizedTranscript{}, errors.New("normalized transcript text is empty or exceeds the bounded limit")
	}
	if len(transcript.Segments) == 0 || len(transcript.Segments) > maxTranscriptSegments {
		return NormalizedTranscript{}, errors.New("normalized transcript segments are empty or exceed the bounded limit")
	}
	previousEnd := int64(0)
	for index := range transcript.Segments {
		segment := &transcript.Segments[index]
		segment.Speech = strings.TrimSpace(segment.Speech)
		segment.Audio = strings.TrimSpace(segment.Audio)
		segment.Visual = strings.TrimSpace(segment.Visual)
		segment.OnScreenText = strings.TrimSpace(segment.OnScreenText)
		if len(segment.Speech) > maxTranscriptSegmentBytes || len(segment.Audio) > maxTranscriptSegmentBytes || len(segment.Visual) > maxTranscriptSegmentBytes || len(segment.OnScreenText) > maxTranscriptSegmentBytes {
			return NormalizedTranscript{}, fmt.Errorf("normalized transcript segment %d modality exceeds the bounded limit", index)
		}
		segment.Text = strings.TrimSpace(segment.Text)
		if transcript.SchemaVersion == NormalizedTranscriptSchemaVersion || transcript.SchemaVersion == NormalizedTranscriptMultimodalLegacy {
			if segment.Speech == "" && segment.Audio == "" && segment.Visual == "" && segment.OnScreenText == "" {
				return NormalizedTranscript{}, fmt.Errorf("normalized transcript segment %d has no multimodal content", index)
			}
			segment.Text = ReadableVideoSegmentText(*segment)
		}
		if segment.StartMs < 0 || segment.EndMs <= segment.StartMs || segment.StartMs < previousEnd || len(segment.Text) == 0 || len(segment.Text) > maxTranscriptSegmentBytes {
			return NormalizedTranscript{}, fmt.Errorf("normalized transcript segment %d is invalid", index)
		}
		previousEnd = segment.EndMs
	}
	if len(transcript.Words) > maxTranscriptWords {
		return NormalizedTranscript{}, errors.New("normalized transcript words exceed the bounded limit")
	}
	previousWordStart := int64(-1)
	for index := range transcript.Words {
		word := &transcript.Words[index]
		word.Text = strings.TrimSpace(word.Text)
		word.Provenance = strings.ToLower(strings.TrimSpace(word.Provenance))
		if word.Text == "" || len(word.Text) > maxTranscriptWordBytes || word.StartMs < 0 || word.EndMs <= word.StartMs || word.StartMs < previousWordStart {
			return NormalizedTranscript{}, fmt.Errorf("normalized transcript word %d is invalid", index)
		}
		if word.Confidence != nil && (math.IsNaN(*word.Confidence) || math.IsInf(*word.Confidence, 0) || *word.Confidence < 0 || *word.Confidence > 1) {
			return NormalizedTranscript{}, fmt.Errorf("normalized transcript word %d confidence is invalid", index)
		}
		if word.Provenance == "" || len(word.Provenance) > 128 {
			return NormalizedTranscript{}, fmt.Errorf("normalized transcript word %d provenance is invalid", index)
		}
		previousWordStart = word.StartMs
	}
	if transcript.Metadata.DurationMs > 0 && len(transcript.Words) > 0 && transcript.Words[len(transcript.Words)-1].EndMs > transcript.Metadata.DurationMs+1000 {
		return NormalizedTranscript{}, errors.New("normalized transcript words exceed declared duration")
	}
	if transcript.SchemaVersion == NormalizedTranscriptSchemaVersion || transcript.SchemaVersion == NormalizedTranscriptMultimodalLegacy {
		for _, segment := range transcript.Segments {
			if len(ReadableVideoSegmentText(segment)) > maxTranscriptSegmentBytes {
				return NormalizedTranscript{}, errors.New("normalized transcript derived segment text exceeds the bounded limit")
			}
		}
		transcript.Text = BuildReadableVideoTranscript(transcript.Metadata.Summary, transcript.Segments)
		if len(transcript.Text) == 0 || len(transcript.Text) > maxTranscriptTextBytes {
			return NormalizedTranscript{}, errors.New("normalized transcript text is empty or exceeds the bounded limit")
		}
	}
	if (transcript.SchemaVersion == NormalizedTranscriptSchemaVersion || transcript.SchemaVersion == NormalizedTranscriptMultimodalLegacy) && transcript.Metadata.DurationMs <= 0 {
		return NormalizedTranscript{}, errors.New("normalized multimodal transcript requires a positive duration")
	}
	if transcript.Metadata.DurationMs > 0 && previousEnd > transcript.Metadata.DurationMs+1000 {
		return NormalizedTranscript{}, errors.New("normalized transcript segments exceed declared duration")
	}
	if (transcript.SchemaVersion == NormalizedTranscriptSchemaVersion || transcript.SchemaVersion == NormalizedTranscriptMultimodalLegacy) && transcript.Metadata.ContentEmpty {
		if len(transcript.Segments) != 1 {
			return NormalizedTranscript{}, errors.New("content-empty transcript must contain exactly one timeline segment")
		}
		segment := transcript.Segments[0]
		if transcript.Metadata.Summary != "" || transcript.Metadata.Language != "" || segment.Speech != "" || segment.Audio != "" || segment.OnScreenText != "" || segment.Visual != ContentEmptyVideoDescription || segment.StartMs != 0 || segment.EndMs < transcript.Metadata.DurationMs-1000 || segment.EndMs > transcript.Metadata.DurationMs+1000 {
			return NormalizedTranscript{}, errors.New("content-empty transcript must contain the canonical duration-spanning visual placeholder without fabricated summary, language, audio, or speech")
		}
	}
	if transcript.Validation.State != TranscriptValidationValidated || transcript.Validation.ValidatedAt == 0 {
		return NormalizedTranscript{}, errors.New("normalized transcript must be validated before durable commit")
	}
	content := struct {
		SchemaVersion string                        `json:"schema_version"`
		Text          string                        `json:"text"`
		Segments      []NormalizedTranscriptSegment `json:"segments"`
		Words         []NormalizedTranscriptWord    `json:"words,omitempty"`
		Metadata      NormalizedTranscriptMetadata  `json:"metadata"`
	}{transcript.SchemaVersion, transcript.Text, transcript.Segments, transcript.Words, transcript.Metadata}
	payload, err := json.Marshal(content)
	if err != nil {
		return NormalizedTranscript{}, err
	}
	transcript.ContentDigest = transcriptionDigest(string(payload))
	return transcript, nil
}

func ReadableVideoSegmentText(segment NormalizedTranscriptSegment) string {
	parts := make([]string, 0, 4)
	if segment.Speech != "" {
		parts = append(parts, "Speech: "+segment.Speech)
	}
	if segment.Audio != "" {
		parts = append(parts, "Audio: "+segment.Audio)
	}
	if segment.Visual != "" {
		parts = append(parts, "Visual: "+segment.Visual)
	}
	if segment.OnScreenText != "" {
		parts = append(parts, "On-screen text: "+segment.OnScreenText)
	}
	return strings.Join(parts, " | ")
}

func BuildReadableVideoTranscript(summary string, segments []NormalizedTranscriptSegment) string {
	lines := make([]string, 0, len(segments)+3)
	if summary = strings.TrimSpace(summary); summary != "" {
		lines = append(lines, "Summary: "+summary, "")
	}
	lines = append(lines, "Timeline:")
	for _, segment := range segments {
		totalStart, totalEnd := segment.StartMs/1000, segment.EndMs/1000
		lines = append(lines, fmt.Sprintf("- [%02d:%02d - %02d:%02d] %s", totalStart/60, totalStart%60, totalEnd/60, totalEnd%60, ReadableVideoSegmentText(segment)))
	}
	return strings.Join(lines, "\n")
}

func normalizeV3TranscriptionMutation(input *V3SessionMutationInput) {
	if input == nil || input.Transcription == nil {
		return
	}
	input.Transcription.ExpectedStatus = strings.ToLower(strings.TrimSpace(input.Transcription.ExpectedStatus))
	if input.Transcription.Attachment != nil {
		record := input.Transcription.Attachment
		record.MediaKind = strings.ToLower(strings.TrimSpace(record.MediaKind))
		if record.SchemaVersion == TranscriptionAttachmentLegacyVersion && record.MediaKind == "" {
			record.MediaKind = TranscriptionMediaVideo
		}
		record.Ref = strings.TrimSpace(record.Ref)
		record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
		record.UserID = strings.TrimSpace(record.UserID)
		record.WorkspaceID = strings.TrimSpace(record.WorkspaceID)
		record.SessionID = strings.TrimSpace(record.SessionID)
		record.MessageID = strings.TrimSpace(record.MessageID)
		record.SourceRecordRef = strings.TrimSpace(record.SourceRecordRef)
		record.SourceThreadID = strings.TrimSpace(record.SourceThreadID)
		record.SourceClipID = strings.TrimSpace(record.SourceClipID)
		record.SourceFingerprint = strings.ToLower(strings.TrimSpace(record.SourceFingerprint))
		record.MIMEType = strings.ToLower(strings.TrimSpace(record.MIMEType))
	}
	if input.Transcription.Job != nil {
		job := input.Transcription.Job
		job.Ref = strings.TrimSpace(job.Ref)
		job.TranscriptRef = strings.TrimSpace(job.TranscriptRef)
		job.AccountScopeID = strings.TrimSpace(job.AccountScopeID)
		job.UserID = strings.TrimSpace(job.UserID)
		job.WorkspaceID = strings.TrimSpace(job.WorkspaceID)
		job.SessionID = strings.TrimSpace(job.SessionID)
		job.MessageID = strings.TrimSpace(job.MessageID)
		job.AttachmentRef = strings.TrimSpace(job.AttachmentRef)
		job.SourceFingerprint = strings.ToLower(strings.TrimSpace(job.SourceFingerprint))
		job.ProviderID = strings.ToLower(strings.TrimSpace(job.ProviderID))
		job.Model = strings.TrimSpace(job.Model)
		job.ModelSnapshot = strings.TrimSpace(job.ModelSnapshot)
		job.MediaSettingsHash = strings.TrimSpace(job.MediaSettingsHash)
		job.FocusNotes = strings.TrimSpace(job.FocusNotes)
		job.TranscriptSchema = strings.TrimSpace(job.TranscriptSchema)
		job.IdempotencyFingerprint = strings.ToLower(strings.TrimSpace(job.IdempotencyFingerprint))
		job.Status = strings.ToLower(strings.TrimSpace(job.Status))
	}
}

func validateV3TranscriptionMutationInput(input V3SessionMutationInput) error {
	isKind := input.Kind == V3SessionMutationBindTranscriptionAttachment || input.Kind == V3SessionMutationCreateTranscriptionJob || input.Kind == V3SessionMutationUpdateTranscriptionJob
	if input.Transcription == nil {
		if isKind {
			return errors.New("transcription mutation payload is required")
		}
		return nil
	}
	if !isKind {
		return errors.New("transcription payload requires a transcription mutation kind")
	}
	if record := input.Transcription.Attachment; record != nil {
		if err := validateV3MutationEmbeddedOwnership(input, "transcription attachment", record.SessionID, record.UserID, record.AccountScopeID); err != nil {
			return err
		}
		validMediaIdentity := (record.MediaKind == TranscriptionMediaVideo && record.MessageID != "") || (record.MediaKind == TranscriptionMediaAudio && record.MessageID == "" && strings.HasPrefix(record.SourceRecordRef, "audiosrc_"))
		if !validOpaqueTranscriptionRef(record.Ref, "vatt_") || !validFingerprint(record.SourceFingerprint) || record.WorkspaceID == "" || !validMediaIdentity {
			return errors.New("transcription attachment has invalid durable identity")
		}
	}
	if job := input.Transcription.Job; job != nil {
		if err := validateV3MutationEmbeddedOwnership(input, "transcription job", job.SessionID, job.UserID, job.AccountScopeID); err != nil {
			return err
		}
		if !validOpaqueTranscriptionRef(job.Ref, "trjob_") || !validOpaqueTranscriptionRef(job.TranscriptRef, "transcript_") || !validOpaqueTranscriptionRef(job.AttachmentRef, "vatt_") || !validFingerprint(job.SourceFingerprint) || job.WorkspaceID == "" || len(job.FocusNotes) > 500 {
			return errors.New("transcription job has invalid durable identity or focus notes")
		}
	}
	return nil
}

func validateTranscriptionSessionOwnership(session SessionSnapshot, accountScopeID, userID string) error {
	if session.AccountScopeID != strings.TrimSpace(accountScopeID) {
		return errors.New("session is outside the authenticated account scope")
	}
	if session.UserID != "" && session.UserID != strings.TrimSpace(userID) {
		return errors.New("session is outside the authenticated user scope")
	}
	return nil
}

func validateJobMatchesAttachment(job TranscriptionJob, attachment TranscriptionAttachmentRecord) error {
	if job.AccountScopeID != attachment.AccountScopeID || job.UserID != attachment.UserID || job.WorkspaceID != attachment.WorkspaceID || job.SessionID != attachment.SessionID || job.MessageID != attachment.MessageID || job.AttachmentRef != attachment.Ref || job.SourceFingerprint != attachment.SourceFingerprint {
		return errors.New("transcription job does not match attachment ownership authority")
	}
	return nil
}

func equalTranscriptionAttachmentAuthority(left, right TranscriptionAttachmentRecord) bool {
	return left.SchemaVersion == right.SchemaVersion && left.MediaKind == right.MediaKind && left.Ref == right.Ref && left.AccountScopeID == right.AccountScopeID && left.UserID == right.UserID && left.WorkspaceID == right.WorkspaceID && left.SessionID == right.SessionID && left.MessageID == right.MessageID && left.SourceRecordRef == right.SourceRecordRef && left.SourceThreadID == right.SourceThreadID && left.SourceClipID == right.SourceClipID && left.SourceFingerprint == right.SourceFingerprint && left.FingerprintVersion == right.FingerprintVersion && left.MIMEType == right.MIMEType && left.SizeBytes == right.SizeBytes
}

func equalTranscriptionJobAuthority(left, right TranscriptionJob) bool {
	left.Status, right.Status = "", ""
	left.FailureCode, right.FailureCode = "", ""
	left.FailureReason, right.FailureReason = "", ""
	left.UpdatedAt, right.UpdatedAt = 0, 0
	left.StartedAt, right.StartedAt = 0, 0
	left.CompletedAt, right.CompletedAt = 0, 0
	return left == right
}

func validTranscriptionTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case TranscriptionJobQueued:
		return to == TranscriptionJobUploading || to == TranscriptionJobProcessing || to == TranscriptionJobFailed || to == TranscriptionJobCancelled || to == TranscriptionJobStale
	case TranscriptionJobUploading:
		return to == TranscriptionJobProcessing || to == TranscriptionJobFailed || to == TranscriptionJobCancelled || to == TranscriptionJobStale
	case TranscriptionJobProcessing:
		return to == TranscriptionJobPartial || to == TranscriptionJobReady || to == TranscriptionJobFailed || to == TranscriptionJobCancelled || to == TranscriptionJobStale
	case TranscriptionJobPartial:
		return to == TranscriptionJobProcessing || to == TranscriptionJobReady || to == TranscriptionJobFailed || to == TranscriptionJobCancelled || to == TranscriptionJobStale
	case TranscriptionJobReady:
		return to == TranscriptionJobStale
	default:
		return false
	}
}

func isTerminalTranscriptionStatus(status string) bool {
	return status == TranscriptionJobReady || status == TranscriptionJobFailed || status == TranscriptionJobCancelled || status == TranscriptionJobStale
}

func transcriptionWorkspaceID(session SessionSnapshot) string {
	for _, key := range []string{"workspace_id", "swarm_v3_source_workspace_id"} {
		if value, ok := session.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "workspace_" + transcriptionDigest(filepath.Clean(strings.TrimSpace(session.WorkspacePath)))
}

func sameTranscriptionAttachmentSource(left, right TranscriptionAttachmentRecord) bool {
	return left.Ref == right.Ref &&
		left.MediaKind == right.MediaKind &&
		left.AccountScopeID == right.AccountScopeID &&
		left.UserID == right.UserID &&
		left.WorkspaceID == right.WorkspaceID &&
		left.SessionID == right.SessionID &&
		left.MessageID == right.MessageID &&
		left.SourceRecordRef == right.SourceRecordRef &&
		left.SourceThreadID == right.SourceThreadID &&
		left.SourceClipID == right.SourceClipID &&
		left.SourceFingerprint == right.SourceFingerprint &&
		left.MIMEType == right.MIMEType &&
		left.SizeBytes == right.SizeBytes
}

func transcriptionMutationHash(kind string, mutation *V3TranscriptionMutation) string {
	payload, _ := json.Marshal(struct {
		Kind string                   `json:"kind"`
		Body *V3TranscriptionMutation `json:"body"`
	}{kind, mutation})
	return transcriptionDigest(string(payload))
}

func transcriptionDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validOpaqueTranscriptionRef(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validFingerprint(strings.TrimPrefix(value, prefix))
}

func boundedTranscriptionField(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func videoMIMEFromExtension(extension string) string {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), ".")) {
	case "mov":
		return "video/quicktime"
	case "webm":
		return "video/webm"
	case "mkv":
		return "video/x-matroska"
	default:
		return "video/mp4"
	}
}

func firstTranscriptionReadError(ok bool, err error, label string) error {
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s was not durably readable after commit", label)
	}
	return nil
}
