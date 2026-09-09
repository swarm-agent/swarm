package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	V3SessionMutationArtifactV3DraftSaved         = "artifact.v3.draft.saved"
	V3SessionMutationArtifactV3GenesisCommitted   = "artifact.v3.genesis.committed"
	V3SessionMutationArtifactV3TurnOpened         = "artifact.v3.turn.opened"
	V3SessionMutationArtifactV3CandidateCommitted = "artifact.v3.candidate.committed"
	V3SessionMutationArtifactV3CandidateFailed    = "artifact.v3.candidate.failed"
	V3SessionMutationArtifactV3CandidateCancelled = "artifact.v3.candidate.cancelled"
	V3SessionMutationArtifactV3HeadSelected       = "artifact.v3.head.selected"
	V3SessionMutationArtifactV3Recovered          = "artifact.v3.recovered"
)

// Draft source is private storage state, never part of session/realtime JSON.
type ArtifactV3DraftProjection struct {
	GrantID   string
	Grant     json.RawMessage
	State     json.RawMessage `json:"state,omitempty"`
	Digest    string
	Status    string
	ExpiresAt int64
	Sequence  uint64
	EventSeq  uint64
}

type ArtifactV3RepositoryProjection struct {
	Generations     []ArtifactV3GenerationMember         `json:"generations,omitempty"`
	Drafts          map[string]ArtifactV3DraftProjection `json:"-"`
	DraftStatus     string                               `json:"draft_status,omitempty"`
	Version         int                                  `json:"version"`
	ArtifactID      string                               `json:"artifact_id"`
	RepositoryID    string                               `json:"repository_id"`
	AccountScopeID  string                               `json:"account_scope_id"`
	UserID          string                               `json:"user_id"`
	OwnerSessionID  string                               `json:"owner_session_id"`
	IntentReference string                               `json:"intent_reference,omitempty"`
	HeadCommitOID   string                               `json:"head_commit_oid"`
	CreatedAt       int64                                `json:"created_at"`
	UpdatedAt       int64                                `json:"updated_at"`
	EventSeq        uint64                               `json:"event_seq"`
}

type ArtifactV3PartProjection struct {
	Temporal      *ArtifactV3TemporalScene `json:"temporal,omitempty"`
	CaptureTimeMS *int64                   `json:"capture_time_ms,omitempty"`
	ID            string                   `json:"id"`
	Label         string                   `json:"label"`
	LocatorKind   string                   `json:"locator_kind"`
	Path          string                   `json:"path,omitempty"`
	Value         string                   `json:"value,omitempty"`
	Paths         []string                 `json:"paths,omitempty"`
}

type ArtifactV3RevisionProjection struct {
	Version          int                          `json:"version"`
	ArtifactID       string                       `json:"artifact_id"`
	RepositoryID     string                       `json:"repository_id"`
	OwnerSessionID   string                       `json:"owner_session_id"`
	CommitOID        string                       `json:"commit_oid"`
	TreeOID          string                       `json:"tree_oid"`
	ManifestBlobOID  string                       `json:"manifest_blob_oid"`
	ParentCommitOIDs []string                     `json:"parent_commit_oids,omitempty"`
	ChangedFiles     []string                     `json:"changed_files,omitempty"`
	Parts            []ArtifactV3PartProjection   `json:"parts"`
	Build            ArtifactV3EvidenceProjection `json:"build"`
	Preview          ArtifactV3EvidenceProjection `json:"preview"`
	FileCount        int                          `json:"file_count"`
	TreeBytes        int64                        `json:"tree_bytes"`
	CreatedAt        int64                        `json:"created_at"`
	EventSeq         uint64                       `json:"event_seq"`
}

type ArtifactV3TurnProjection struct {
	RevisionIntent      string   `json:"revision_intent,omitempty"`
	Version             int      `json:"version"`
	ArtifactID          string   `json:"artifact_id"`
	TurnID              string   `json:"turn_id"`
	OwnerSessionID      string   `json:"owner_session_id"`
	BaseCommitOID       string   `json:"base_commit_oid"`
	TargetPartID        string   `json:"target_part_id,omitempty"`
	TargetPartIDs       []string `json:"target_part_ids,omitempty"`
	Status              string   `json:"status"`
	SelectedCandidateID string   `json:"selected_candidate_id,omitempty"`
	CreatedAt           int64    `json:"created_at"`
	UpdatedAt           int64    `json:"updated_at"`
	EventSeq            uint64   `json:"event_seq"`
}

type ArtifactV3SceneEvidence struct {
	PartID       string `json:"part_id"`
	SampleMS     int64  `json:"sample_ms"`
	DigestSHA256 string `json:"digest_sha256"`
}

type ArtifactV3EvidenceProjection struct {
	Scenes       []ArtifactV3SceneEvidence `json:"scenes,omitempty"`
	Status       string                    `json:"status"`
	CommitOID    string                    `json:"commit_oid"`
	DigestSHA256 string                    `json:"digest_sha256"`
	Reference    string                    `json:"reference"`
}

type ArtifactV3CandidateProjection struct {
	Version        int                          `json:"version"`
	ArtifactID     string                       `json:"artifact_id"`
	TurnID         string                       `json:"turn_id"`
	CandidateID    string                       `json:"candidate_id"`
	OwnerSessionID string                       `json:"owner_session_id"`
	CommitOID      string                       `json:"commit_oid,omitempty"`
	CandidateRef   string                       `json:"candidate_ref,omitempty"`
	TransactionID  string                       `json:"transaction_id"`
	Status         string                       `json:"status"`
	FailureCode    string                       `json:"failure_code,omitempty"`
	Build          ArtifactV3EvidenceProjection `json:"build"`
	Preview        ArtifactV3EvidenceProjection `json:"preview"`
	CreatedAt      int64                        `json:"created_at"`
	UpdatedAt      int64                        `json:"updated_at"`
	EventSeq       uint64                       `json:"event_seq"`
}

// ArtifactV3DraftResume is an explicit producer handoff, checked under the session mutation lock.
type ArtifactV3DraftResume struct {
	GrantID       string `json:"grant_id"`
	ProjectionSeq uint64 `json:"projection_seq"`
	ExpectedHead  string `json:"expected_head"`
	ProducerRunID string `json:"producer_run_id"`
}

type ArtifactV3Mutation struct {
	Resume                *ArtifactV3DraftResume          `json:"resume,omitempty"`
	Draft                 *ArtifactV3DraftProjection      `json:"draft,omitempty"`
	ExpectedDraftSequence uint64                          `json:"expected_draft_sequence,omitempty"`
	Repository            *ArtifactV3RepositoryProjection `json:"repository,omitempty"`
	Revision              *ArtifactV3RevisionProjection   `json:"revision,omitempty"`
	Turn                  *ArtifactV3TurnProjection       `json:"turn,omitempty"`
	Candidate             *ArtifactV3CandidateProjection  `json:"candidate,omitempty"`
	ExpectedHeadCommitOID string                          `json:"expected_head_commit_oid,omitempty"`
}

type ArtifactV3Projection struct {
	Repository *ArtifactV3RepositoryProjection `json:"repository,omitempty"`
	Revision   *ArtifactV3RevisionProjection   `json:"revision,omitempty"`
	Turn       *ArtifactV3TurnProjection       `json:"turn,omitempty"`
	Candidate  *ArtifactV3CandidateProjection  `json:"candidate,omitempty"`
}

type preparedArtifactV3Mutation struct{ Projection ArtifactV3Projection }

func KeyArtifactV3Repository(accountScopeID, artifactID string) string {
	return fmt.Sprintf("v3/artifact/repository/%s/%s", keyPart(accountScopeID), keyPart(artifactID))
}
func KeyArtifactV3Revision(accountScopeID, artifactID, commitOID string) string {
	return fmt.Sprintf("v3/artifact/revision/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(commitOID))
}
func KeyArtifactV3Turn(accountScopeID, artifactID, turnID string) string {
	return fmt.Sprintf("v3/artifact/turn/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(turnID))
}
func KeyArtifactV3Candidate(accountScopeID, artifactID, turnID, candidateID string) string {
	return fmt.Sprintf("v3/artifact/candidate/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(artifactID), keyPart(turnID), keyPart(candidateID))
}

func isArtifactV3MutationKind(kind string) bool {
	switch kind {
	case V3SessionMutationArtifactV3DraftSaved, V3SessionMutationArtifactV3GenesisCommitted, V3SessionMutationArtifactV3TurnOpened,
		V3SessionMutationArtifactV3CandidateCommitted, V3SessionMutationArtifactV3CandidateFailed,
		V3SessionMutationArtifactV3CandidateCancelled, V3SessionMutationArtifactV3HeadSelected,
		V3SessionMutationArtifactV3Recovered:
		return true
	default:
		return false
	}
}

func normalizeArtifactV3Mutation(input *V3SessionMutationInput) {
	if input == nil || input.ArtifactV3 == nil {
		return
	}
	m := input.ArtifactV3
	m.ExpectedHeadCommitOID = strings.ToLower(strings.TrimSpace(m.ExpectedHeadCommitOID))
	if m.Repository != nil {
		m.Repository.ArtifactID = strings.TrimSpace(m.Repository.ArtifactID)
		m.Repository.RepositoryID = strings.TrimSpace(m.Repository.RepositoryID)
		m.Repository.HeadCommitOID = strings.ToLower(strings.TrimSpace(m.Repository.HeadCommitOID))
	}
	if m.Revision != nil {
		m.Revision.ArtifactID = strings.TrimSpace(m.Revision.ArtifactID)
		m.Revision.CommitOID = strings.ToLower(strings.TrimSpace(m.Revision.CommitOID))
		m.Revision.TreeOID = strings.ToLower(strings.TrimSpace(m.Revision.TreeOID))
		m.Revision.ManifestBlobOID = strings.ToLower(strings.TrimSpace(m.Revision.ManifestBlobOID))
	}
	if m.Turn != nil {
		m.Turn.ArtifactID, m.Turn.TurnID = strings.TrimSpace(m.Turn.ArtifactID), strings.TrimSpace(m.Turn.TurnID)
	}
	if m.Candidate != nil {
		m.Candidate.ArtifactID, m.Candidate.TurnID, m.Candidate.CandidateID = strings.TrimSpace(m.Candidate.ArtifactID), strings.TrimSpace(m.Candidate.TurnID), strings.TrimSpace(m.Candidate.CandidateID)
	}
}

func validateArtifactV3MutationInput(input V3SessionMutationInput) error {
	if input.ArtifactV3 == nil {
		if isArtifactV3MutationKind(input.Kind) {
			return errors.New("artifact v3 mutation payload is required")
		}
		return nil
	}
	if !isArtifactV3MutationKind(input.Kind) {
		return errors.New("artifact v3 payload requires an artifact.v3 mutation kind")
	}
	m := input.ArtifactV3
	if m.Resume != nil && input.Kind != V3SessionMutationArtifactV3DraftSaved {
		return ErrArtifactV3Invalid
	}
	artifactID := ""
	for _, id := range []string{artifactV3RepositoryID(m.Repository), artifactV3RevisionID(m.Revision), artifactV3TurnID(m.Turn), artifactV3CandidateID(m.Candidate)} {
		if id == "" {
			continue
		}
		if artifactID != "" && id != artifactID {
			return errors.New("artifact v3 mutation contains mixed artifact identities")
		}
		artifactID = id
	}
	if artifactID == "" {
		return errors.New("artifact v3 identity is required")
	}
	if input.Kind == V3SessionMutationArtifactV3DraftSaved {
		if m.Repository == nil || m.Draft == nil || m.Revision != nil || m.Turn != nil || m.Candidate != nil {
			return ErrArtifactV3Invalid
		}
		d := m.Draft
		if d.GrantID == "" || len(d.GrantID) > 128 || !json.Valid(d.Grant) || len(d.Grant) > 16384 || len(d.State) > 384<<20 || (len(d.State) != 0 && !json.Valid(d.State)) || d.ExpiresAt <= input.NowUnixMs {
			return ErrArtifactV3Invalid
		}
		if d.Status != "creating" && d.Status != "fixing" && d.Status != "error" && d.Status != "publishing" && d.Status != "ready" {
			return ErrArtifactV3Invalid
		}
	}
	if m.Repository != nil {
		if m.Repository.OwnerSessionID != input.SessionID || m.Repository.AccountScopeID != input.AccountScopeID || m.Repository.UserID != input.UserID || m.Repository.RepositoryID == "" || (!validGitOID(m.Repository.HeadCommitOID) && !(input.Kind == V3SessionMutationArtifactV3DraftSaved && m.Repository.HeadCommitOID == "")) {
			return errors.New("artifact v3 repository ownership or Git identity is invalid")
		}
	}
	if (input.Kind == V3SessionMutationArtifactV3GenesisCommitted || input.Kind == V3SessionMutationArtifactV3Recovered || input.Kind == V3SessionMutationArtifactV3HeadSelected) && m.Repository == nil {
		return errors.New("artifact v3 head-changing mutation requires repository projection")
	}
	if input.Kind == V3SessionMutationArtifactV3GenesisCommitted && (m.Revision == nil || m.Turn == nil || m.Candidate == nil) {
		return errors.New("artifact v3 genesis requires revision, turn, and selected candidate projections")
	}
	if input.Kind == V3SessionMutationArtifactV3TurnOpened && m.Turn == nil {
		return errors.New("artifact v3 turn open requires turn projection")
	}
	if (input.Kind == V3SessionMutationArtifactV3CandidateCommitted || input.Kind == V3SessionMutationArtifactV3CandidateFailed || input.Kind == V3SessionMutationArtifactV3CandidateCancelled || input.Kind == V3SessionMutationArtifactV3HeadSelected) && m.Candidate == nil {
		return errors.New("artifact v3 candidate mutation requires candidate projection")
	}
	if input.Kind == V3SessionMutationArtifactV3CandidateCommitted && (m.Turn == nil || m.Turn.Status != "awaiting_selection") {
		return errors.New("artifact v3 candidate commit requires awaiting-selection turn projection")
	}
	if m.Revision != nil {
		if m.Revision.OwnerSessionID != input.SessionID || m.Revision.RepositoryID == "" || !validGitOID(m.Revision.CommitOID) || !validGitOID(m.Revision.TreeOID) || !validGitOID(m.Revision.ManifestBlobOID) || m.Revision.FileCount < 1 || m.Revision.TreeBytes < 0 {
			return errors.New("artifact v3 revision is incomplete")
		}
		if !artifactV3EvidenceReady(m.Revision.Build, m.Revision.CommitOID) || !artifactV3EvidenceReady(m.Revision.Preview, m.Revision.CommitOID) {
			return errors.New("artifact v3 revision requires exact build and preview evidence")
		}
		if err := validateCommitOIDs(m.Revision.ParentCommitOIDs); err != nil {
			return err
		}
		if len(m.Revision.Parts) > 16384 {
			return errors.New("artifact v3 part count exceeds bounds")
		}
		seenParts := map[string]bool{}
		for _, part := range m.Revision.Parts {
			if part.ID == "" || strings.TrimSpace(part.Label) == "" || seenParts[part.ID] {
				return errors.New("artifact v3 part projection is invalid")
			}
			seenParts[part.ID] = true
		}
	}
	if m.Turn != nil && (m.Turn.OwnerSessionID != input.SessionID || m.Turn.TurnID == "" || !validGitOID(m.Turn.BaseCommitOID) || (m.Turn.Status != "open" && m.Turn.Status != "awaiting_selection" && m.Turn.Status != "selected" && m.Turn.Status != "closed")) {
		return errors.New("artifact v3 turn is incomplete")
	}
	if input.Kind == V3SessionMutationArtifactV3CandidateCommitted && m.Revision == nil {
		return errors.New("artifact v3 committed candidate requires revision projection")
	}
	if m.Candidate != nil {
		if m.Candidate.OwnerSessionID != input.SessionID || m.Candidate.TurnID == "" || m.Candidate.CandidateID == "" || m.Candidate.TransactionID == "" || (m.Candidate.Status != "ready" && m.Candidate.Status != "selected" && m.Candidate.Status != "failed" && m.Candidate.Status != "cancelled") {
			return errors.New("artifact v3 candidate is incomplete")
		}
		if (m.Candidate.Status == "ready" || m.Candidate.Status == "selected") && (!validGitOID(m.Candidate.CommitOID) || !validArtifactGitRef(m.Candidate.CandidateRef)) {
			return errors.New("artifact v3 ready candidate Git identity is incomplete")
		}
		if input.Kind == V3SessionMutationArtifactV3GenesisCommitted && (m.Candidate.Status != "selected" || m.Candidate.CommitOID != m.Repository.HeadCommitOID || m.Turn.Status != "selected" || !artifactV3EvidenceReady(m.Candidate.Build, m.Candidate.CommitOID) || !artifactV3EvidenceReady(m.Candidate.Preview, m.Candidate.CommitOID)) {
			return errors.New("artifact v3 genesis selected candidate does not match head or evidence")
		}
	}
	return nil
}

func artifactV3RepositoryID(v *ArtifactV3RepositoryProjection) string {
	if v == nil {
		return ""
	}
	return v.ArtifactID
}
func artifactV3RevisionID(v *ArtifactV3RevisionProjection) string {
	if v == nil {
		return ""
	}
	return v.ArtifactID
}
func artifactV3TurnID(v *ArtifactV3TurnProjection) string {
	if v == nil {
		return ""
	}
	return v.ArtifactID
}
func artifactV3CandidateID(v *ArtifactV3CandidateProjection) string {
	if v == nil {
		return ""
	}
	return v.ArtifactID
}

func (s *SessionStore) prepareArtifactV3Mutation(input V3SessionMutationInput, seq uint64, now int64) (preparedArtifactV3Mutation, error) {
	if input.ArtifactV3 == nil {
		return preparedArtifactV3Mutation{}, nil
	}
	storedSession, ok, err := s.GetSession(input.SessionID)
	if err != nil {
		return preparedArtifactV3Mutation{}, err
	}
	if !ok || storedSession.AccountScopeID != input.AccountScopeID || storedSession.UserID != input.UserID {
		return preparedArtifactV3Mutation{}, errors.New("artifact v3 mutation session ownership does not match")
	}
	m := *input.ArtifactV3
	p := ArtifactV3Projection{}
	artifactID := artifactV3RepositoryID(m.Repository)
	if artifactID == "" {
		artifactID = artifactV3RevisionID(m.Revision)
	}
	if artifactID == "" {
		artifactID = artifactV3TurnID(m.Turn)
	}
	if artifactID == "" {
		artifactID = artifactV3CandidateID(m.Candidate)
	}
	current, currentOK, err := s.GetArtifactV3Repository(input.AccountScopeID, input.UserID, artifactID)
	if err != nil {
		return preparedArtifactV3Mutation{}, err
	}
	if !currentOK && input.Kind != V3SessionMutationArtifactV3GenesisCommitted && input.Kind != V3SessionMutationArtifactV3Recovered && input.Kind != V3SessionMutationArtifactV3DraftSaved {
		return preparedArtifactV3Mutation{}, errors.New("artifact v3 repository was not found")
	}
	if currentOK && input.Kind == V3SessionMutationArtifactV3GenesisCommitted && current.HeadCommitOID != "" && current.HeadCommitOID != m.Repository.HeadCommitOID {
		return preparedArtifactV3Mutation{}, errors.New("artifact v3 genesis conflicts with existing head")
	}
	if currentOK && current.OwnerSessionID != input.SessionID {
		return preparedArtifactV3Mutation{}, ErrArtifactV3Unauthorized
	}
	if m.ExpectedHeadCommitOID != "" && (!currentOK || current.HeadCommitOID != m.ExpectedHeadCommitOID) {
		return preparedArtifactV3Mutation{}, errors.New("artifact v3 head compare-and-swap is stale")
	}
	if m.Repository != nil {
		copy := *m.Repository
		copy.Version = 3
		copy.EventSeq = seq
		copy.UpdatedAt = now
		copy.Drafts = current.Drafts
		if err := s.prepareArtifactV3Generation(input, current, &copy); err != nil {
			return preparedArtifactV3Mutation{}, err
		}
		if input.Kind == V3SessionMutationArtifactV3DraftSaved {
			d := *m.Draft
			previous, found := current.Drafts[d.GrantID]
			if m.Resume != nil {
				if err := s.validateArtifactV3DraftResume(input, current, d, now); err != nil {
					return preparedArtifactV3Mutation{}, err
				}
				previous = current.Drafts[m.Resume.GrantID]
				found = true
				// Renewal changes only the grant identity/expiry and producer binding.
				previous.Grant, previous.ExpiresAt = d.Grant, d.ExpiresAt
			}
			if copy.HeadCommitOID != current.HeadCommitOID || (found && (previous.Sequence != m.ExpectedDraftSequence || previous.ExpiresAt <= now || string(previous.Grant) != string(d.Grant) || previous.ExpiresAt != d.ExpiresAt)) || (!found && m.ExpectedDraftSequence != 0) {
				return preparedArtifactV3Mutation{}, ErrArtifactV3Conflict
			}
			// Drafts include immutable completed history, not just active producers.
			// Permit bounded repeat rounds while retaining the aggregate byte cap below.
			if !found && len(current.Drafts) >= 256 {
				return preparedArtifactV3Mutation{}, fmt.Errorf("%w: artifact retained draft limit (256) reached", ErrArtifactV3Invalid)
			}
			copy.Drafts = make(map[string]ArtifactV3DraftProjection, len(current.Drafts)+1)
			for key, value := range current.Drafts {
				copy.Drafts[key] = value
			}
			if m.Resume != nil {
				delete(copy.Drafts, m.Resume.GrantID)
			}
			d.Sequence = m.ExpectedDraftSequence + 1
			d.EventSeq = seq
			copy.Drafts[d.GrantID] = d
			var storedBytes int64
			for _, value := range copy.Drafts {
				storedBytes += int64(len(value.State) + len(value.Grant))
			}
			if storedBytes > 384<<20 {
				return preparedArtifactV3Mutation{}, ErrArtifactV3Invalid
			}
			copy.DraftStatus = d.Status
		}
		if currentOK {
			copy.CreatedAt = current.CreatedAt
			if copy.RepositoryID != current.RepositoryID {
				return preparedArtifactV3Mutation{}, errors.New("artifact v3 repository identity is immutable")
			}
		} else if copy.CreatedAt == 0 {
			copy.CreatedAt = now
		}
		p.Repository = &copy
	}
	if m.Revision != nil {
		copy := *m.Revision
		copy.Version = 3
		copy.EventSeq = seq
		if copy.CreatedAt == 0 {
			copy.CreatedAt = now
		}
		p.Revision = &copy
	}
	if m.Turn != nil {
		copy := *m.Turn
		copy.Version = 3
		copy.EventSeq = seq
		copy.UpdatedAt = now
		if copy.CreatedAt == 0 {
			copy.CreatedAt = now
		}
		if existing, found, readErr := s.GetArtifactV3Turn(input.AccountScopeID, input.UserID, artifactID, copy.TurnID); readErr != nil {
			return preparedArtifactV3Mutation{}, readErr
		} else if found {
			candidateTransition := input.Kind == V3SessionMutationArtifactV3CandidateCommitted && (existing.Status == "open" || existing.Status == "awaiting_selection") && copy.Status == "awaiting_selection" && existing.BaseCommitOID == copy.BaseCommitOID
			selectTransition := input.Kind == V3SessionMutationArtifactV3HeadSelected && existing.Status == "awaiting_selection" && copy.Status == "selected" && existing.BaseCommitOID == copy.BaseCommitOID
			if existing.RevisionIntent != copy.RevisionIntent || existing.BaseCommitOID != copy.BaseCommitOID || !reflect.DeepEqual(existing.TargetPartIDs, copy.TargetPartIDs) || existing.TargetPartID != copy.TargetPartID || (!candidateTransition && !selectTransition && existing.Status != copy.Status) {
				return preparedArtifactV3Mutation{}, errors.New("artifact v3 turn identity is immutable")
			}
		}
		p.Turn = &copy
	}
	if m.Candidate != nil {
		copy := *m.Candidate
		copy.Version = 3
		copy.EventSeq = seq
		copy.UpdatedAt = now
		if copy.CreatedAt == 0 {
			copy.CreatedAt = now
		}
		_, found, readErr := s.GetArtifactV3Turn(input.AccountScopeID, input.UserID, artifactID, copy.TurnID)
		if readErr != nil && currentOK {
			return preparedArtifactV3Mutation{}, readErr
		}
		if !found && p.Turn != nil && p.Turn.TurnID == copy.TurnID {
			found = true
		}
		if !found {
			return preparedArtifactV3Mutation{}, errors.New("artifact v3 candidate turn was not found")
		}
		if existing, candidateFound, candidateErr := s.GetArtifactV3Candidate(input.AccountScopeID, input.UserID, artifactID, copy.TurnID, copy.CandidateID); candidateErr != nil {
			return preparedArtifactV3Mutation{}, candidateErr
		} else if candidateFound {
			selectTransition := input.Kind == V3SessionMutationArtifactV3HeadSelected && existing.Status == "ready" && copy.Status == "selected" && existing.CommitOID == copy.CommitOID
			if !selectTransition {
				return preparedArtifactV3Mutation{}, errors.New("artifact v3 candidate identity already exists")
			}
		}
		if (copy.Status == "ready" || input.Kind == V3SessionMutationArtifactV3HeadSelected) && (!artifactV3EvidenceReady(copy.Build, copy.CommitOID) || !artifactV3EvidenceReady(copy.Preview, copy.CommitOID)) {
			return preparedArtifactV3Mutation{}, errors.New("artifact v3 candidate requires complete build and preview evidence")
		}
		if input.Kind == V3SessionMutationArtifactV3HeadSelected {
			if m.Repository == nil || m.Repository.HeadCommitOID != copy.CommitOID {
				return preparedArtifactV3Mutation{}, errors.New("artifact v3 selection repository head does not match candidate")
			}
			copy.Status = "selected"
		}
		p.Candidate = &copy
	}
	return preparedArtifactV3Mutation{Projection: p}, nil
}

func artifactV3EvidenceReady(e ArtifactV3EvidenceProjection, commit string) bool {
	return e.Status == "succeeded" && e.CommitOID == commit && e.DigestSHA256 != "" && e.Reference != ""
}

func setArtifactV3MutationInBatch(batch *pebble.Batch, accountScopeID string, prepared preparedArtifactV3Mutation) error {
	p := prepared.Projection
	if p.Repository != nil {
		for _, member := range p.Repository.Generations {
			raw, err := json.Marshal(member)
			if err != nil {
				return err
			}
			if err := batch.Set([]byte(artifactV3GenerationKey(accountScopeID, p.Repository.OwnerSessionID, member.WaveID, member.Index)), raw, nil); err != nil {
				return err
			}
		}
	}
	for key, value := range map[string]any{
		func() string {
			if p.Repository == nil {
				return ""
			}
			return KeyArtifactV3Repository(accountScopeID, p.Repository.ArtifactID)
		}(): artifactV3StoredRepository(p.Repository),
		func() string {
			if p.Revision == nil {
				return ""
			}
			return KeyArtifactV3Revision(accountScopeID, p.Revision.ArtifactID, p.Revision.CommitOID)
		}(): p.Revision,
		func() string {
			if p.Turn == nil {
				return ""
			}
			return KeyArtifactV3Turn(accountScopeID, p.Turn.ArtifactID, p.Turn.TurnID)
		}(): p.Turn,
		func() string {
			if p.Candidate == nil {
				return ""
			}
			return KeyArtifactV3Candidate(accountScopeID, p.Candidate.ArtifactID, p.Candidate.TurnID, p.Candidate.CandidateID)
		}(): p.Candidate,
	} {
		if key == "" || value == nil {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(key), raw, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) GetArtifactV3Repository(accountScopeID, userID, artifactID string) (ArtifactV3RepositoryProjection, bool, error) {
	if s == nil || s.store == nil {
		return ArtifactV3RepositoryProjection{}, false, errors.New("session store is not configured")
	}
	var value ArtifactV3RepositoryProjection
	stored := struct {
		*ArtifactV3RepositoryProjection
		PrivateDrafts map[string]ArtifactV3DraftProjection `json:"private_drafts,omitempty"`
	}{ArtifactV3RepositoryProjection: &value}
	ok, err := s.store.GetJSON(KeyArtifactV3Repository(accountScopeID, artifactID), &stored)
	value.Drafts = stored.PrivateDrafts
	if err != nil || !ok {
		return value, ok, err
	}
	if value.AccountScopeID != accountScopeID || value.UserID != userID {
		return ArtifactV3RepositoryProjection{}, false, errors.New("artifact v3 repository ownership does not match")
	}
	return value, true, nil
}
func (s *SessionStore) GetArtifactV3Revision(accountScopeID, userID, artifactID, commitOID string) (ArtifactV3RevisionProjection, bool, error) {
	var value ArtifactV3RevisionProjection
	repository, ok, err := s.GetArtifactV3Repository(accountScopeID, userID, artifactID)
	if err != nil || !ok {
		return value, false, err
	}
	ok, err = s.store.GetJSON(KeyArtifactV3Revision(accountScopeID, artifactID, commitOID), &value)
	if err != nil || !ok {
		return value, ok, err
	}
	if value.OwnerSessionID != repository.OwnerSessionID || value.RepositoryID != repository.RepositoryID {
		return ArtifactV3RevisionProjection{}, false, errors.New("artifact v3 revision ownership does not match")
	}
	return value, true, nil
}
func (s *SessionStore) GetArtifactV3Turn(accountScopeID, userID, artifactID, turnID string) (ArtifactV3TurnProjection, bool, error) {
	var value ArtifactV3TurnProjection
	repository, ok, err := s.GetArtifactV3Repository(accountScopeID, userID, artifactID)
	if err != nil || !ok {
		return value, false, err
	}
	ok, err = s.store.GetJSON(KeyArtifactV3Turn(accountScopeID, artifactID, turnID), &value)
	if err != nil || !ok {
		return value, ok, err
	}
	if value.OwnerSessionID != repository.OwnerSessionID {
		return ArtifactV3TurnProjection{}, false, errors.New("artifact v3 turn ownership does not match")
	}
	return value, true, nil
}
func (s *SessionStore) GetArtifactV3Candidate(accountScopeID, userID, artifactID, turnID, candidateID string) (ArtifactV3CandidateProjection, bool, error) {
	var value ArtifactV3CandidateProjection
	repository, ok, err := s.GetArtifactV3Repository(accountScopeID, userID, artifactID)
	if err != nil || !ok {
		return value, false, err
	}
	ok, err = s.store.GetJSON(KeyArtifactV3Candidate(accountScopeID, artifactID, turnID, candidateID), &value)
	if err != nil || !ok {
		return value, ok, err
	}
	if value.OwnerSessionID != repository.OwnerSessionID {
		return ArtifactV3CandidateProjection{}, false, errors.New("artifact v3 candidate ownership does not match")
	}
	return value, true, nil
}

// ListArtifactV3CandidateProjections enumerates durable slots, including failed
// slots with no Git ref. It authenticates the repository before scanning and
// fails closed rather than silently truncating the bounded Studio history.
func (s *SessionStore) ListArtifactV3CandidateProjections(accountScopeID, userID, artifactID string) ([]ArtifactV3CandidateProjection, error) {
	repository, ok, err := s.GetArtifactV3Repository(accountScopeID, userID, artifactID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrArtifactV3NotFound
	}
	prefix := fmt.Sprintf("v3/artifact/candidate/%s/%s/", keyPart(accountScopeID), keyPart(artifactID))
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := make([]ArtifactV3CandidateProjection, 0)
	for iter.First(); iter.Valid(); iter.Next() {
		if len(out) >= 4096 {
			return nil, errors.New("artifact v3 candidate history exceeds bounded limit")
		}
		var value ArtifactV3CandidateProjection
		if err := json.Unmarshal(iter.Value(), &value); err != nil {
			return nil, err
		}
		if value.OwnerSessionID != repository.OwnerSessionID || value.ArtifactID != artifactID || string(iter.Key()) != KeyArtifactV3Candidate(accountScopeID, artifactID, value.TurnID, value.CandidateID) {
			return nil, ErrArtifactV3Integrity
		}
		out = append(out, value)
	}
	return out, iter.Error()
}

// Only this storage envelope contains private draft bytes. Public projections,
// including canonical session events and idempotent results, omit them.
func artifactV3StoredRepository(repository *ArtifactV3RepositoryProjection) any {
	if repository == nil {
		return nil
	}
	return struct {
		*ArtifactV3RepositoryProjection
		PrivateDrafts map[string]ArtifactV3DraftProjection `json:"private_drafts,omitempty"`
	}{repository, repository.Drafts}
}

// ListArtifactV3Repositories uses persisted identities, including unpublished
// drafts. Ambient Git directories are never the catalog authority.
func (s *SessionStore) ListArtifactV3Repositories(account, user, session string, limit int) ([]ArtifactV3RepositoryProjection, error) {
	owner, found, err := s.GetSession(session)
	if err != nil {
		return nil, err
	}
	if !found || owner.AccountScopeID != account || owner.UserID != user {
		return nil, ErrArtifactV3Unauthorized
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	prefix := fmt.Sprintf("v3/artifact/repository/%s/", keyPart(account))
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := make([]ArtifactV3RepositoryProjection, 0)
	scanned := 0
	for iter.First(); iter.Valid(); iter.Next() {
		scanned++
		if scanned > 10000 {
			return nil, ErrArtifactV3Invalid
		}
		var value ArtifactV3RepositoryProjection
		if err := json.Unmarshal(iter.Value(), &value); err != nil {
			return nil, err
		}
		if value.AccountScopeID != account || string(iter.Key()) != KeyArtifactV3Repository(account, value.ArtifactID) {
			return nil, ErrArtifactV3Integrity
		}
		if value.UserID == user && value.OwnerSessionID == session {
			// Read the private envelope for the service's safe draft projection.
			// Draft bytes remain excluded from public JSON.
			var envelope struct {
				PrivateDrafts map[string]ArtifactV3DraftProjection `json:"private_drafts,omitempty"`
			}
			if err := json.Unmarshal(iter.Value(), &envelope); err != nil {
				return nil, err
			}
			value.Drafts = envelope.PrivateDrafts
			out = append(out, value)
			if len(out) == limit {
				break
			}
		}
	}
	return out, iter.Error()
}

// validateArtifactV3DraftResume runs inside ApplySessionMutation's serialization.
// JSON envelopes are private; compare all immutable fields, not a partial DTO.
func (s *SessionStore) validateArtifactV3DraftResume(input V3SessionMutationInput, repository ArtifactV3RepositoryProjection, next ArtifactV3DraftProjection, now int64) error {
	m := input.ArtifactV3
	r := m.Resume
	old, exists := repository.Drafts[r.GrantID]
	if !exists || repository.HeadCommitOID != r.ExpectedHead || old.Sequence != m.ExpectedDraftSequence || repository.EventSeq != r.ProjectionSeq || next.GrantID == old.GrantID || next.Digest != old.Digest || next.ExpiresAt <= now || next.ExpiresAt > now+int64(time.Hour/time.Millisecond) {
		return ErrArtifactV3Conflict
	}
	if _, exists := repository.Drafts[next.GrantID]; exists {
		return ErrArtifactV3Conflict
	}
	var before, after map[string]json.RawMessage
	if json.Unmarshal(old.State, &before) != nil || json.Unmarshal(next.State, &after) != nil {
		return ErrArtifactV3Integrity
	}
	var producerSession, producerRun string
	if json.Unmarshal(before["ProducerSessionID"], &producerSession) != nil || json.Unmarshal(before["ProducerRunID"], &producerRun) != nil || producerSession == "" || producerRun == "" || producerRun == r.ProducerRunID {
		return ErrArtifactV3Unauthorized
	}
	var publishing bool
	if json.Unmarshal(before["Publishing"], &publishing) != nil || string(before["Finished"]) != "null" {
		return ErrArtifactV3Conflict
	}
	if err := s.ValidateArtifactV3DraftProducer(input.AccountScopeID, input.UserID, input.SessionID, producerSession); err != nil {
		return err
	}
	previous, found, err := s.GetV3SessionRunIntent(producerSession, producerRun)
	if err != nil {
		return err
	}
	if !found || previous.UserID != input.UserID || previous.AccountScopeID != input.AccountScopeID || !isV3RunIntentTerminal(previous.Status) {
		return ErrArtifactV3Conflict
	}
	active, found, err := s.GetV3SessionActiveRunIntent(input.SessionID)
	if err != nil {
		return err
	}
	if !found || active.RunID != r.ProducerRunID || active.UserID != input.UserID || active.AccountScopeID != input.AccountScopeID {
		return ErrArtifactV3Unauthorized
	}
	var nextSession string
	if json.Unmarshal(after["ProducerSessionID"], &nextSession) != nil || nextSession != input.SessionID {
		return ErrArtifactV3Unauthorized
	}
	var nextRun string
	if json.Unmarshal(after["ProducerRunID"], &nextRun) != nil || nextRun != r.ProducerRunID || (!publishing && string(after["Gate"]) != "null") {
		return ErrArtifactV3Unauthorized
	}
	// Only the authenticated terminal-producer handoff may renew the budget.
	// Missing Attempt is allowed for older envelopes, but a supplied value must be zero.
	if raw, exists := after["Attempt"]; exists && !publishing {
		var attempt int
		if json.Unmarshal(raw, &attempt) != nil || attempt != 0 {
			return ErrArtifactV3Conflict
		}
	}
	if !publishing {
		// The only permitted history change is archiving the exact current gate
		// under the same eight-entry bound before invalidating current validation.
		if gate := before["Gate"]; len(gate) != 0 && string(gate) != "null" {
			var history []json.RawMessage
			if raw := before["History"]; len(raw) != 0 && json.Unmarshal(raw, &history) != nil {
				return ErrArtifactV3Integrity
			}
			history = append(history, gate)
			if len(history) > 8 {
				history = history[len(history)-8:]
			}
			before["History"], _ = json.Marshal(history)
		}
		delete(before, "Attempt")
		delete(after, "Attempt")
		delete(before, "Gate")
		delete(after, "Gate")
	}
	// A frozen publication handoff preserves every validated byte and gate;
	// only its terminal producer may change. It cannot become an editable draft.
	delete(before, "ProducerSessionID")
	delete(after, "ProducerSessionID")
	delete(before, "ProducerRunID")
	delete(after, "ProducerRunID")
	left, _ := json.Marshal(before)
	right, _ := json.Marshal(after)
	if string(left) != string(right) {
		return ErrArtifactV3Conflict
	}
	// Unmarshal into fresh maps so removed fields cannot survive decoding.
	after = nil
	if json.Unmarshal(next.Grant, &after) != nil {
		return ErrArtifactV3Integrity
	}
	var id string
	var expiry int64
	if json.Unmarshal(after["ID"], &id) != nil || id != next.GrantID || json.Unmarshal(after["ExpiresAt"], &expiry) != nil || expiry != next.ExpiresAt {
		return ErrArtifactV3Invalid
	}
	// Re-decode the grant into a fresh map as well.
	before = nil
	if json.Unmarshal(old.Grant, &before) != nil {
		return ErrArtifactV3Integrity
	}
	delete(before, "ID")
	delete(after, "ID")
	delete(before, "ExpiresAt")
	delete(after, "ExpiresAt")
	left, _ = json.Marshal(before)
	right, _ = json.Marshal(after)
	if string(left) != string(right) {
		return ErrArtifactV3Unauthorized
	}
	return nil
}

// ValidateArtifactV3DraftProducer authenticates durable task lineage, not caller
// claims. Resume also invokes it under the canonical mutation serialization.
func (s *SessionStore) ValidateArtifactV3DraftProducer(account, user, owner, producer string) error {
	if account == "" || user == "" || owner == "" || producer == "" {
		return ErrArtifactV3Unauthorized
	}
	if producer == owner {
		return nil
	}
	child, found, err := s.GetSession(producer)
	if err != nil {
		return err
	}
	if !found || child.AccountScopeID != account || child.UserID != user {
		return ErrArtifactV3Unauthorized
	}
	parent, parentOK := child.Metadata["parent_session_id"].(string)
	kind, kindOK := child.Metadata["lineage_kind"].(string)
	agent, agentOK := child.Metadata["subagent"].(string)
	// Delegation persists the code-owned profile ID (system-designer). The
	// older short name remains valid for already-retained drafts. Keep this
	// allowlist exact: display labels and arbitrary profile names are not authority.
	if !parentOK || parent != owner || !kindOK || kind != "delegated_subagent" || !agentOK || (agent != "designer" && agent != "system-designer") {
		return ErrArtifactV3Unauthorized
	}
	return nil
}
