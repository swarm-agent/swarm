package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	ArtifactV2SchemaVersion = 1

	ArtifactV2StateAllocated     = "allocated"
	ArtifactV2StateAuthoring     = "authoring"
	ArtifactV2StateBuilding      = "building"
	ArtifactV2StateValidating    = "validating"
	ArtifactV2StateInvalid       = "invalid"
	ArtifactV2StateReady         = "ready"
	ArtifactV2StateIterating     = "iterating"
	ArtifactV2StatePublishedView = "published_view"
	ArtifactV2StateCancelled     = "cancelled"

	ArtifactV2BuildQueued    = "queued"
	ArtifactV2BuildRunning   = "running"
	ArtifactV2BuildSucceeded = "succeeded"
	ArtifactV2BuildFailed    = "failed"
	ArtifactV2BuildCancelled = "cancelled"

	ArtifactV2ValidationQueued      = "queued"
	ArtifactV2ValidationRunning     = "running"
	ArtifactV2ValidationValid       = "valid"
	ArtifactV2ValidationInvalid     = "invalid"
	ArtifactV2ValidationFailedToRun = "failed_to_run"
	ArtifactV2ValidationCancelled   = "cancelled"

	ArtifactV2IterationOpen              = "open"
	ArtifactV2IterationGenerating        = "generating"
	ArtifactV2IterationAwaitingSelection = "awaiting_selection"
	ArtifactV2IterationSelected          = "selected"
	ArtifactV2IterationClosed            = "closed_without_selection"
	ArtifactV2IterationCancelled         = "cancelled"
)

// ArtifactV2BlobReceipt names immutable private bytes. Repository and blob
// identities are server-owned storage receipts, never caller destinations.
type ArtifactV2BlobReceipt struct {
	RepositoryID string `json:"repository_id"`
	CommitOID    string `json:"commit_oid"`
	BlobOID      string `json:"blob_oid"`
	DigestSHA256 string `json:"digest_sha256"`
	Size         int64  `json:"size"`
	MediaType    string `json:"media_type"`
}

type ArtifactV2WorkingArtifact struct {
	SchemaVersion      int                               `json:"schema_version"`
	ID                 string                            `json:"id"`
	AccountScopeID     string                            `json:"account_scope_id"`
	UserID             string                            `json:"user_id"`
	SessionID          string                            `json:"session_id"`
	Kind               string                            `json:"kind"`
	State              string                            `json:"state"`
	PolicyRevision     string                            `json:"policy_revision"`
	CapabilityClass    string                            `json:"capability_class"`
	IntentReference    string                            `json:"intent_reference"`
	CreationRequestID  string                            `json:"creation_request_id"`
	Revision           uint64                            `json:"revision"`
	EventSeq           uint64                            `json:"event_seq"`
	CompositionHead    *ArtifactV2CompositionHead        `json:"composition_head,omitempty"`
	PublishedHead      *ArtifactV2PublishedHeadReference `json:"published_head,omitempty"`
	LatestBuildID      string                            `json:"latest_build_id,omitempty"`
	LatestValidationID string                            `json:"latest_validation_id,omitempty"`
	ActiveIterationID  string                            `json:"active_iteration_id,omitempty"`
	LatestDiagnostic   *ArtifactV2Diagnostic             `json:"latest_diagnostic,omitempty"`
	CreatedAt          int64                             `json:"created_at"`
	UpdatedAt          int64                             `json:"updated_at"`
}

type ArtifactV2Part struct {
	SchemaVersion  int    `json:"schema_version"`
	ID             string `json:"id"`
	ArtifactID     string `json:"artifact_id"`
	AccountScopeID string `json:"account_scope_id"`
	UserID         string `json:"user_id"`
	SessionID      string `json:"session_id"`
	Key            string `json:"key"`
	Label          string `json:"label"`
	Role           string `json:"role"`
	MediaClass     string `json:"media_class"`
	LocatorKind    string `json:"locator_kind,omitempty"`
	LocatorValue   string `json:"locator_value,omitempty"`
	Order          int    `json:"order"`
	Revision       uint64 `json:"revision"`
	EventSeq       uint64 `json:"event_seq"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

type ArtifactV2PartRevision struct {
	SchemaVersion    int                   `json:"schema_version"`
	ID               string                `json:"id"`
	ArtifactID       string                `json:"artifact_id"`
	PartID           string                `json:"part_id"`
	AccountScopeID   string                `json:"account_scope_id"`
	UserID           string                `json:"user_id"`
	SessionID        string                `json:"session_id"`
	ParentRevisionID string                `json:"parent_revision_id,omitempty"`
	ProducerRunID    string                `json:"producer_run_id,omitempty"`
	CapabilityGrant  string                `json:"capability_grant_id,omitempty"`
	Blob             ArtifactV2BlobReceipt `json:"blob"`
	Revision         uint64                `json:"revision"`
	EventSeq         uint64                `json:"event_seq"`
	CreatedAt        int64                 `json:"created_at"`
}

type ArtifactV2CompositionPart struct {
	PartID         string `json:"part_id"`
	PartRevisionID string `json:"part_revision_id"`
	DigestSHA256   string `json:"digest_sha256"`
	Locked         bool   `json:"locked,omitempty"`
}

type ArtifactV2Composition struct {
	SchemaVersion       int                         `json:"schema_version"`
	ID                  string                      `json:"id"`
	ArtifactID          string                      `json:"artifact_id"`
	AccountScopeID      string                      `json:"account_scope_id"`
	UserID              string                      `json:"user_id"`
	SessionID           string                      `json:"session_id"`
	ParentCompositionID string                      `json:"parent_composition_id,omitempty"`
	PolicyRevision      string                      `json:"policy_revision"`
	ConstructionVersion string                      `json:"construction_version"`
	Parts               []ArtifactV2CompositionPart `json:"parts"`
	DigestSHA256        string                      `json:"digest_sha256"`
	Revision            uint64                      `json:"revision"`
	EventSeq            uint64                      `json:"event_seq"`
	CreatedAt           int64                       `json:"created_at"`
}

type ArtifactV2CompositionHead struct {
	CompositionID string `json:"composition_id"`
	HeadRevision  uint64 `json:"head_revision"`
	DigestSHA256  string `json:"digest_sha256"`
	EventSeq      uint64 `json:"event_seq"`
}

type ArtifactV2Diagnostic struct {
	Code               string   `json:"code"`
	Phase              string   `json:"phase"`
	Severity           string   `json:"severity"`
	PartID             string   `json:"part_id,omitempty"`
	AuthoredLocator    string   `json:"authored_locator,omitempty"`
	FrameSlotOrTime    string   `json:"frame_slot_or_time,omitempty"`
	Bounds             string   `json:"bounds,omitempty"`
	PreservationProofs []string `json:"preservation_proofs,omitempty"`
	RetryClass         string   `json:"retry_class"`
	SafeMessage        string   `json:"safe_message"`
}

type ArtifactV2BuildResult struct {
	SchemaVersion              int                    `json:"schema_version"`
	ID                         string                 `json:"id"`
	ArtifactID                 string                 `json:"artifact_id"`
	AccountScopeID             string                 `json:"account_scope_id"`
	UserID                     string                 `json:"user_id"`
	SessionID                  string                 `json:"session_id"`
	CompositionID              string                 `json:"composition_id"`
	CompositionDigest          string                 `json:"composition_digest"`
	PolicyRevision             string                 `json:"policy_revision"`
	CompilerVersion            string                 `json:"compiler_version"`
	TemplateVersion            string                 `json:"template_version,omitempty"`
	SourceDigests              []string               `json:"source_digests,omitempty"`
	RepresentativeTimestampsMS []int                  `json:"representative_timestamps_ms,omitempty"`
	OutputDigestSHA256         string                 `json:"output_digest_sha256,omitempty"`
	DurationMS                 int                    `json:"duration_ms,omitempty"`
	FPS                        int                    `json:"fps,omitempty"`
	Status                     string                 `json:"status"`
	Output                     *ArtifactV2BlobReceipt `json:"output,omitempty"`
	Diagnostics                []ArtifactV2Diagnostic `json:"diagnostics,omitempty"`
	RetryOfBuildID             string                 `json:"retry_of_build_id,omitempty"`
	Revision                   uint64                 `json:"revision"`
	EventSeq                   uint64                 `json:"event_seq"`
	CreatedAt                  int64                  `json:"created_at"`
	CompletedAt                int64                  `json:"completed_at,omitempty"`
}

type ArtifactV2ValidationResult struct {
	SchemaVersion              int                    `json:"schema_version"`
	ID                         string                 `json:"id"`
	ArtifactID                 string                 `json:"artifact_id"`
	AccountScopeID             string                 `json:"account_scope_id"`
	UserID                     string                 `json:"user_id"`
	SessionID                  string                 `json:"session_id"`
	BuildID                    string                 `json:"build_id"`
	CompositionID              string                 `json:"composition_id"`
	CompositionDigest          string                 `json:"composition_digest"`
	PolicyRevision             string                 `json:"policy_revision"`
	ValidatorVersion           string                 `json:"validator_version"`
	CompilerVersion            string                 `json:"compiler_version"`
	TemplateVersion            string                 `json:"template_version,omitempty"`
	SourceDigests              []string               `json:"source_digests,omitempty"`
	RepresentativeTimestampsMS []int                  `json:"representative_timestamps_ms,omitempty"`
	DurationMS                 int                    `json:"duration_ms,omitempty"`
	FPS                        int                    `json:"fps,omitempty"`
	RendererSnapshot           string                 `json:"renderer_snapshot,omitempty"`
	Status                     string                 `json:"status"`
	Diagnostics                []ArtifactV2Diagnostic `json:"diagnostics,omitempty"`
	EvidenceDigests            []string               `json:"evidence_digests,omitempty"`
	Revision                   uint64                 `json:"revision"`
	EventSeq                   uint64                 `json:"event_seq"`
	CreatedAt                  int64                  `json:"created_at"`
	CompletedAt                int64                  `json:"completed_at,omitempty"`
}

type ArtifactV2Derivative struct {
	SchemaVersion        int                    `json:"schema_version"`
	ID                   string                 `json:"id"`
	ArtifactID           string                 `json:"artifact_id"`
	AccountScopeID       string                 `json:"account_scope_id"`
	UserID               string                 `json:"user_id"`
	SessionID            string                 `json:"session_id"`
	CompositionID        string                 `json:"composition_id"`
	CompositionDigest    string                 `json:"composition_digest"`
	BuildID              string                 `json:"build_id"`
	ValidationID         string                 `json:"validation_id"`
	PolicyRevision       string                 `json:"policy_revision"`
	Kind                 string                 `json:"kind"`
	Status               string                 `json:"status"`
	SourcePartID         string                 `json:"source_part_id,omitempty"`
	SourcePartRevisionID string                 `json:"source_part_revision_id,omitempty"`
	CaptureStateID       string                 `json:"capture_state_id,omitempty"`
	Output               *ArtifactV2BlobReceipt `json:"output,omitempty"`
	Diagnostics          []ArtifactV2Diagnostic `json:"diagnostics,omitempty"`
	Revision             uint64                 `json:"revision"`
	EventSeq             uint64                 `json:"event_seq"`
	CreatedAt            int64                  `json:"created_at"`
}

type ArtifactV2IterationCandidate struct {
	SlotID        string `json:"slot_id"`
	CompositionID string `json:"composition_id,omitempty"`
	Status        string `json:"status"`
	FailureCode   string `json:"failure_code,omitempty"`
	EventSeq      uint64 `json:"event_seq"`
}

type ArtifactV2IterationRound struct {
	SchemaVersion         int                            `json:"schema_version"`
	ID                    string                         `json:"id"`
	ArtifactID            string                         `json:"artifact_id"`
	AccountScopeID        string                         `json:"account_scope_id"`
	UserID                string                         `json:"user_id"`
	SessionID             string                         `json:"session_id"`
	BaseCompositionID     string                         `json:"base_composition_id"`
	BaseCompositionDigest string                         `json:"base_composition_digest"`
	TargetPartIDs         []string                       `json:"target_part_ids"`
	RequestedCandidates   int                            `json:"requested_candidates"`
	Status                string                         `json:"status"`
	Candidates            []ArtifactV2IterationCandidate `json:"candidates,omitempty"`
	SelectedSlotID        string                         `json:"selected_slot_id,omitempty"`
	Revision              uint64                         `json:"revision"`
	EventSeq              uint64                         `json:"event_seq"`
	CreatedAt             int64                          `json:"created_at"`
	UpdatedAt             int64                          `json:"updated_at"`
}

type ArtifactV2PublishedHeadReference struct {
	PublishedHeadID string `json:"published_head_id"`
	CompositionID   string `json:"composition_id"`
	DigestSHA256    string `json:"digest_sha256"`
	EventSeq        uint64 `json:"event_seq"`
}

type ArtifactV2PublishedHead struct {
	SchemaVersion     int    `json:"schema_version"`
	ID                string `json:"id"`
	ArtifactID        string `json:"artifact_id"`
	AccountScopeID    string `json:"account_scope_id"`
	UserID            string `json:"user_id"`
	SessionID         string `json:"session_id"`
	CompositionID     string `json:"composition_id"`
	CompositionDigest string `json:"composition_digest"`
	BuildID           string `json:"build_id"`
	ValidationID      string `json:"validation_id"`
	PolicyRevision    string `json:"policy_revision"`
	PreviousHeadID    string `json:"previous_head_id,omitempty"`
	AuthorizingActor  string `json:"authorizing_actor"`
	Revision          uint64 `json:"revision"`
	EventSeq          uint64 `json:"event_seq"`
	CreatedAt         int64  `json:"created_at"`
}

// ArtifactV2Projection is the durable V3 event/sidebar projection. It contains
// bounded state only; exact records remain in the Artifact V2 store.
type ArtifactV2Projection struct {
	SchemaVersion      int                               `json:"schema_version"`
	ArtifactID         string                            `json:"artifact_id"`
	SessionID          string                            `json:"session_id"`
	Kind               string                            `json:"kind"`
	State              string                            `json:"state"`
	Revision           uint64                            `json:"revision"`
	EventSeq           uint64                            `json:"event_seq"`
	PartCount          int                               `json:"part_count"`
	CompositionHead    *ArtifactV2CompositionHead        `json:"composition_head,omitempty"`
	LatestBuildID      string                            `json:"latest_build_id,omitempty"`
	LatestValidationID string                            `json:"latest_validation_id,omitempty"`
	ActiveIterationID  string                            `json:"active_iteration_id,omitempty"`
	PublishedHead      *ArtifactV2PublishedHeadReference `json:"published_head,omitempty"`
	LatestDiagnostic   *ArtifactV2Diagnostic             `json:"latest_diagnostic,omitempty"`
	UpdatedAt          int64                             `json:"updated_at"`
}

// ArtifactV2Mutation is a closed V2-owned persistence envelope committed with
// its V3 event, session projection, idempotency row, and realtime outbox.
type ArtifactV2Mutation struct {
	Working                         *ArtifactV2WorkingArtifact  `json:"working,omitempty"`
	Part                            *ArtifactV2Part             `json:"part,omitempty"`
	PartRevision                    *ArtifactV2PartRevision     `json:"part_revision,omitempty"`
	PartRevisions                   []ArtifactV2PartRevision    `json:"part_revisions,omitempty"`
	Composition                     *ArtifactV2Composition      `json:"composition,omitempty"`
	Build                           *ArtifactV2BuildResult      `json:"build,omitempty"`
	Validation                      *ArtifactV2ValidationResult `json:"validation,omitempty"`
	Derivative                      *ArtifactV2Derivative       `json:"derivative,omitempty"`
	Iteration                       *ArtifactV2IterationRound   `json:"iteration,omitempty"`
	PublishedHead                   *ArtifactV2PublishedHead    `json:"published_head,omitempty"`
	ExpectedWorkingRevision         *uint64                     `json:"expected_working_revision,omitempty"`
	ExpectedCompositionHeadRevision *uint64                     `json:"expected_composition_head_revision,omitempty"`
	ExpectedIterationRevision       *uint64                     `json:"expected_iteration_revision,omitempty"`
	AdvanceCompositionHead          bool                        `json:"advance_composition_head,omitempty"`
	// AllowLockedPartChanges is a trusted in-process user-command capability.
	// It is never decoded from API or provider JSON and is excluded from events.
	AllowLockedPartChanges bool `json:"-"`
}

func KeyArtifactV2Working(accountScopeID, artifactID string) string {
	return fmt.Sprintf("artifact_v2/working/%s/%s", keyPart(accountScopeID), keyPart(artifactID))
}
func ArtifactV2WorkingPrefix(accountScopeID string) string {
	return fmt.Sprintf("artifact_v2/working/%s/", keyPart(accountScopeID))
}
func KeyArtifactV2BySession(accountScopeID, sessionID, artifactID string) string {
	return fmt.Sprintf("artifact_v2/by_session/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(artifactID))
}
func ArtifactV2BySessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("artifact_v2/by_session/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}
func KeyArtifactV2ByAccountUpdated(accountScopeID string, updatedAt int64, artifactID string) string {
	return fmt.Sprintf("artifact_v2/by_account_updated/%s/%020d/%s", keyPart(accountScopeID), reverseMillis(updatedAt), keyPart(artifactID))
}
func ArtifactV2ByAccountUpdatedPrefix(accountScopeID string) string {
	return fmt.Sprintf("artifact_v2/by_account_updated/%s/", keyPart(accountScopeID))
}
func KeyArtifactV2Part(accountScopeID, artifactID, partID string) string {
	return fmt.Sprintf("artifact_v2/part/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(partID))
}
func ArtifactV2PartPrefix(accountScopeID, artifactID string) string {
	return fmt.Sprintf("artifact_v2/part/%s/%s/", keyPart(accountScopeID), keyPart(artifactID))
}
func KeyArtifactV2PartRevision(accountScopeID, artifactID, partID, revisionID string) string {
	return fmt.Sprintf("artifact_v2/part_revision/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(partID), keyPart(revisionID))
}
func ArtifactV2PartRevisionPrefix(accountScopeID, artifactID, partID string) string {
	return fmt.Sprintf("artifact_v2/part_revision/%s/%s/%s/", keyPart(accountScopeID), keyPart(artifactID), keyPart(partID))
}
func KeyArtifactV2Composition(accountScopeID, artifactID, compositionID string) string {
	return fmt.Sprintf("artifact_v2/composition/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(compositionID))
}
func ArtifactV2CompositionPrefix(accountScopeID, artifactID string) string {
	return fmt.Sprintf("artifact_v2/composition/%s/%s/", keyPart(accountScopeID), keyPart(artifactID))
}
func KeyArtifactV2Build(accountScopeID, artifactID, buildID string) string {
	return fmt.Sprintf("artifact_v2/build/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(buildID))
}
func ArtifactV2BuildPrefix(accountScopeID, artifactID string) string {
	return fmt.Sprintf("artifact_v2/build/%s/%s/", keyPart(accountScopeID), keyPart(artifactID))
}
func KeyArtifactV2Validation(accountScopeID, artifactID, validationID string) string {
	return fmt.Sprintf("artifact_v2/validation/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(validationID))
}
func ArtifactV2ValidationPrefix(accountScopeID, artifactID string) string {
	return fmt.Sprintf("artifact_v2/validation/%s/%s/", keyPart(accountScopeID), keyPart(artifactID))
}
func KeyArtifactV2Derivative(accountScopeID, artifactID, derivativeID string) string {
	return fmt.Sprintf("artifact_v2/derivative/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(derivativeID))
}
func ArtifactV2DerivativePrefix(accountScopeID, artifactID string) string {
	return fmt.Sprintf("artifact_v2/derivative/%s/%s/", keyPart(accountScopeID), keyPart(artifactID))
}
func KeyArtifactV2Iteration(accountScopeID, artifactID, iterationID string) string {
	return fmt.Sprintf("artifact_v2/iteration/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(iterationID))
}
func ArtifactV2IterationPrefix(accountScopeID, artifactID string) string {
	return fmt.Sprintf("artifact_v2/iteration/%s/%s/", keyPart(accountScopeID), keyPart(artifactID))
}
func KeyArtifactV2PublishedHead(accountScopeID, artifactID, publishedHeadID string) string {
	return fmt.Sprintf("artifact_v2/published_head/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(publishedHeadID))
}
func ArtifactV2PublishedHeadPrefix(accountScopeID, artifactID string) string {
	return fmt.Sprintf("artifact_v2/published_head/%s/%s/", keyPart(accountScopeID), keyPart(artifactID))
}

func ArtifactV2CompositionDigest(policyRevision, constructionVersion string, parts []ArtifactV2CompositionPart) string {
	ordered := append([]ArtifactV2CompositionPart(nil), parts...)
	payload, _ := json.Marshal(struct {
		PolicyRevision      string                      `json:"policy_revision"`
		ConstructionVersion string                      `json:"construction_version"`
		Parts               []ArtifactV2CompositionPart `json:"parts"`
	}{strings.TrimSpace(policyRevision), strings.TrimSpace(constructionVersion), ordered})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (s *SessionStore) GetArtifactV2Working(accountScopeID, artifactID string) (ArtifactV2WorkingArtifact, bool, error) {
	var out ArtifactV2WorkingArtifact
	ok, err := s.store.GetJSON(KeyArtifactV2Working(accountScopeID, artifactID), &out)
	return out, ok, err
}

func (s *SessionStore) ListArtifactV2WorkingForSession(accountScopeID, sessionID string, limit int) ([]ArtifactV2WorkingArtifact, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := make([]ArtifactV2WorkingArtifact, 0, limit)
	err := s.store.IteratePrefix(ArtifactV2BySessionPrefix(accountScopeID, sessionID), limit, func(_ string, value []byte) error {
		artifactID := strings.TrimSpace(string(value))
		working, ok, err := s.GetArtifactV2Working(accountScopeID, artifactID)
		if err != nil {
			return err
		}
		if !ok || working.SessionID != sessionID {
			return errors.New("artifact v2 session index points to missing or foreign state")
		}
		out = append(out, working)
		return nil
	})
	return out, err
}

func (s *SessionStore) GetArtifactV2Part(accountScopeID, artifactID, partID string) (ArtifactV2Part, bool, error) {
	var out ArtifactV2Part
	ok, err := s.store.GetJSON(KeyArtifactV2Part(accountScopeID, artifactID, partID), &out)
	return out, ok, err
}
func (s *SessionStore) ListArtifactV2Parts(accountScopeID, artifactID string, limit int) ([]ArtifactV2Part, error) {
	if limit <= 0 || limit > 256 {
		limit = 256
	}
	out := make([]ArtifactV2Part, 0, limit)
	err := s.store.IteratePrefix(ArtifactV2PartPrefix(accountScopeID, artifactID), limit, func(_ string, value []byte) error {
		var part ArtifactV2Part
		if err := json.Unmarshal(value, &part); err != nil {
			return err
		}
		out = append(out, part)
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order == out[j].Order {
			return out[i].ID < out[j].ID
		}
		return out[i].Order < out[j].Order
	})
	return out, err
}
func (s *SessionStore) GetArtifactV2PartRevision(accountScopeID, artifactID, partID, revisionID string) (ArtifactV2PartRevision, bool, error) {
	var out ArtifactV2PartRevision
	ok, err := s.store.GetJSON(KeyArtifactV2PartRevision(accountScopeID, artifactID, partID, revisionID), &out)
	return out, ok, err
}
func (s *SessionStore) ListArtifactV2PartRevisions(accountScopeID, artifactID, partID string, limit int) ([]ArtifactV2PartRevision, error) {
	if limit <= 0 || limit > 256 {
		limit = 256
	}
	out := make([]ArtifactV2PartRevision, 0, limit)
	err := s.store.IteratePrefix(ArtifactV2PartRevisionPrefix(accountScopeID, artifactID, partID), limit, func(_ string, value []byte) error {
		var revision ArtifactV2PartRevision
		if err := json.Unmarshal(value, &revision); err != nil {
			return err
		}
		out = append(out, revision)
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].EventSeq == out[j].EventSeq {
			return out[i].ID < out[j].ID
		}
		return out[i].EventSeq < out[j].EventSeq
	})
	return out, err
}
func (s *SessionStore) GetArtifactV2Composition(accountScopeID, artifactID, compositionID string) (ArtifactV2Composition, bool, error) {
	var out ArtifactV2Composition
	ok, err := s.store.GetJSON(KeyArtifactV2Composition(accountScopeID, artifactID, compositionID), &out)
	return out, ok, err
}
func (s *SessionStore) ListArtifactV2Compositions(accountScopeID, artifactID string, limit int) ([]ArtifactV2Composition, error) {
	return listArtifactV2Records(s, ArtifactV2CompositionPrefix(accountScopeID, artifactID), limit, func(value []byte) (ArtifactV2Composition, error) {
		var out ArtifactV2Composition
		err := json.Unmarshal(value, &out)
		return out, err
	}, func(left, right ArtifactV2Composition) bool { return left.EventSeq < right.EventSeq })
}
func (s *SessionStore) GetArtifactV2Build(accountScopeID, artifactID, buildID string) (ArtifactV2BuildResult, bool, error) {
	var out ArtifactV2BuildResult
	ok, err := s.store.GetJSON(KeyArtifactV2Build(accountScopeID, artifactID, buildID), &out)
	return out, ok, err
}
func (s *SessionStore) ListArtifactV2Builds(accountScopeID, artifactID string, limit int) ([]ArtifactV2BuildResult, error) {
	return listArtifactV2Records(s, ArtifactV2BuildPrefix(accountScopeID, artifactID), limit, func(value []byte) (ArtifactV2BuildResult, error) {
		var out ArtifactV2BuildResult
		err := json.Unmarshal(value, &out)
		return out, err
	}, func(left, right ArtifactV2BuildResult) bool { return left.EventSeq < right.EventSeq })
}
func (s *SessionStore) GetArtifactV2Validation(accountScopeID, artifactID, validationID string) (ArtifactV2ValidationResult, bool, error) {
	var out ArtifactV2ValidationResult
	ok, err := s.store.GetJSON(KeyArtifactV2Validation(accountScopeID, artifactID, validationID), &out)
	return out, ok, err
}
func (s *SessionStore) ListArtifactV2Validations(accountScopeID, artifactID string, limit int) ([]ArtifactV2ValidationResult, error) {
	return listArtifactV2Records(s, ArtifactV2ValidationPrefix(accountScopeID, artifactID), limit, func(value []byte) (ArtifactV2ValidationResult, error) {
		var out ArtifactV2ValidationResult
		err := json.Unmarshal(value, &out)
		return out, err
	}, func(left, right ArtifactV2ValidationResult) bool { return left.EventSeq < right.EventSeq })
}
func (s *SessionStore) GetArtifactV2Derivative(accountScopeID, artifactID, derivativeID string) (ArtifactV2Derivative, bool, error) {
	var out ArtifactV2Derivative
	ok, err := s.store.GetJSON(KeyArtifactV2Derivative(accountScopeID, artifactID, derivativeID), &out)
	return out, ok, err
}
func (s *SessionStore) ListArtifactV2Derivatives(accountScopeID, artifactID string, limit int) ([]ArtifactV2Derivative, error) {
	return listArtifactV2Records(s, ArtifactV2DerivativePrefix(accountScopeID, artifactID), limit, func(value []byte) (ArtifactV2Derivative, error) {
		var out ArtifactV2Derivative
		err := json.Unmarshal(value, &out)
		return out, err
	}, func(left, right ArtifactV2Derivative) bool { return left.EventSeq < right.EventSeq })
}
func (s *SessionStore) GetArtifactV2Iteration(accountScopeID, artifactID, iterationID string) (ArtifactV2IterationRound, bool, error) {
	var out ArtifactV2IterationRound
	ok, err := s.store.GetJSON(KeyArtifactV2Iteration(accountScopeID, artifactID, iterationID), &out)
	return out, ok, err
}
func (s *SessionStore) ListArtifactV2Iterations(accountScopeID, artifactID string, limit int) ([]ArtifactV2IterationRound, error) {
	return listArtifactV2Records(s, ArtifactV2IterationPrefix(accountScopeID, artifactID), limit, func(value []byte) (ArtifactV2IterationRound, error) {
		var out ArtifactV2IterationRound
		err := json.Unmarshal(value, &out)
		return out, err
	}, func(left, right ArtifactV2IterationRound) bool { return left.EventSeq < right.EventSeq })
}
func (s *SessionStore) GetArtifactV2PublishedHead(accountScopeID, artifactID, publishedHeadID string) (ArtifactV2PublishedHead, bool, error) {
	var out ArtifactV2PublishedHead
	ok, err := s.store.GetJSON(KeyArtifactV2PublishedHead(accountScopeID, artifactID, publishedHeadID), &out)
	return out, ok, err
}
func (s *SessionStore) ListArtifactV2PublishedHeads(accountScopeID, artifactID string, limit int) ([]ArtifactV2PublishedHead, error) {
	return listArtifactV2Records(s, ArtifactV2PublishedHeadPrefix(accountScopeID, artifactID), limit, func(value []byte) (ArtifactV2PublishedHead, error) {
		var out ArtifactV2PublishedHead
		err := json.Unmarshal(value, &out)
		return out, err
	}, func(left, right ArtifactV2PublishedHead) bool { return left.EventSeq < right.EventSeq })
}

type artifactV2RecordDecoder[T any] func([]byte) (T, error)
type artifactV2RecordLess[T any] func(T, T) bool

func listArtifactV2Records[T any](s *SessionStore, prefix string, limit int, decode artifactV2RecordDecoder[T], less artifactV2RecordLess[T]) ([]T, error) {
	if limit <= 0 || limit > 256 {
		limit = 256
	}
	out := make([]T, 0, limit)
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		record, err := decode(value)
		if err != nil {
			return err
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out, nil
}

func artifactV2ProjectionFromWorking(working ArtifactV2WorkingArtifact, partCount int) ArtifactV2Projection {
	return ArtifactV2Projection{SchemaVersion: ArtifactV2SchemaVersion, ArtifactID: working.ID, SessionID: working.SessionID, Kind: working.Kind, State: working.State, Revision: working.Revision, EventSeq: working.EventSeq, PartCount: partCount, CompositionHead: cloneArtifactV2CompositionHead(working.CompositionHead), LatestBuildID: working.LatestBuildID, LatestValidationID: working.LatestValidationID, ActiveIterationID: working.ActiveIterationID, PublishedHead: cloneArtifactV2PublishedHeadReference(working.PublishedHead), LatestDiagnostic: cloneArtifactV2Diagnostic(working.LatestDiagnostic), UpdatedAt: working.UpdatedAt}
}

func cloneArtifactV2CompositionHead(in *ArtifactV2CompositionHead) *ArtifactV2CompositionHead {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
func cloneArtifactV2PublishedHeadReference(in *ArtifactV2PublishedHeadReference) *ArtifactV2PublishedHeadReference {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
func cloneArtifactV2Diagnostic(in *ArtifactV2Diagnostic) *ArtifactV2Diagnostic {
	if in == nil {
		return nil
	}
	out := *in
	out.PreservationProofs = append([]string(nil), in.PreservationProofs...)
	return &out
}

func setArtifactV2JSON(batch *pebble.Batch, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return batch.Set([]byte(key), payload, nil)
}
