package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	V3SessionMutationArtifactV3GenesisCommitted   = "artifact.v3.genesis.committed"
	V3SessionMutationArtifactV3TurnOpened         = "artifact.v3.turn.opened"
	V3SessionMutationArtifactV3CandidateCommitted = "artifact.v3.candidate.committed"
	V3SessionMutationArtifactV3CandidateFailed    = "artifact.v3.candidate.failed"
	V3SessionMutationArtifactV3CandidateCancelled = "artifact.v3.candidate.cancelled"
	V3SessionMutationArtifactV3HeadSelected       = "artifact.v3.head.selected"
	V3SessionMutationArtifactV3Recovered          = "artifact.v3.recovered"
)

type ArtifactV3RepositoryProjection struct {
	Version        int    `json:"version"`
	ArtifactID     string `json:"artifact_id"`
	RepositoryID   string `json:"repository_id"`
	AccountScopeID string `json:"account_scope_id"`
	UserID         string `json:"user_id"`
	OwnerSessionID string `json:"owner_session_id"`
	HeadCommitOID  string `json:"head_commit_oid"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	EventSeq       uint64 `json:"event_seq"`
}

type ArtifactV3PartProjection struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	LocatorKind string   `json:"locator_kind"`
	Path        string   `json:"path,omitempty"`
	Value       string   `json:"value,omitempty"`
	Paths       []string `json:"paths,omitempty"`
}

type ArtifactV3RevisionProjection struct {
	Version         int                        `json:"version"`
	ArtifactID      string                     `json:"artifact_id"`
	RepositoryID    string                     `json:"repository_id"`
	OwnerSessionID  string                     `json:"owner_session_id"`
	CommitOID       string                     `json:"commit_oid"`
	TreeOID         string                     `json:"tree_oid"`
	ManifestBlobOID string                     `json:"manifest_blob_oid"`
	ParentCommitOIDs []string                  `json:"parent_commit_oids,omitempty"`
	ChangedFiles    []string                   `json:"changed_files,omitempty"`
	Parts           []ArtifactV3PartProjection `json:"parts"`
	FileCount       int                        `json:"file_count"`
	TreeBytes       int64                      `json:"tree_bytes"`
	CreatedAt       int64                      `json:"created_at"`
	EventSeq        uint64                     `json:"event_seq"`
}

type ArtifactV3TurnProjection struct {
	Version        int    `json:"version"`
	ArtifactID     string `json:"artifact_id"`
	TurnID         string `json:"turn_id"`
	OwnerSessionID string `json:"owner_session_id"`
	BaseCommitOID  string `json:"base_commit_oid"`
	TargetPartID   string `json:"target_part_id,omitempty"`
	Status         string `json:"status"`
	SelectedCandidateID string `json:"selected_candidate_id,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	EventSeq       uint64 `json:"event_seq"`
}

type ArtifactV3EvidenceProjection struct {
	Status       string `json:"status"`
	CommitOID    string `json:"commit_oid"`
	DigestSHA256 string `json:"digest_sha256"`
	Reference    string `json:"reference"`
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

type ArtifactV3Mutation struct {
	Repository *ArtifactV3RepositoryProjection `json:"repository,omitempty"`
	Revision   *ArtifactV3RevisionProjection   `json:"revision,omitempty"`
	Turn       *ArtifactV3TurnProjection       `json:"turn,omitempty"`
	Candidate  *ArtifactV3CandidateProjection  `json:"candidate,omitempty"`
	ExpectedHeadCommitOID string                `json:"expected_head_commit_oid,omitempty"`
}

type ArtifactV3Projection struct {
	Repository *ArtifactV3RepositoryProjection `json:"repository,omitempty"`
	Revision   *ArtifactV3RevisionProjection   `json:"revision,omitempty"`
	Turn       *ArtifactV3TurnProjection       `json:"turn,omitempty"`
	Candidate  *ArtifactV3CandidateProjection  `json:"candidate,omitempty"`
}

type preparedArtifactV3Mutation struct { Projection ArtifactV3Projection }

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
	case V3SessionMutationArtifactV3GenesisCommitted, V3SessionMutationArtifactV3TurnOpened,
		V3SessionMutationArtifactV3CandidateCommitted, V3SessionMutationArtifactV3CandidateFailed,
		V3SessionMutationArtifactV3CandidateCancelled, V3SessionMutationArtifactV3HeadSelected,
		V3SessionMutationArtifactV3Recovered:
		return true
	default:
		return false
	}
}

func normalizeArtifactV3Mutation(input *V3SessionMutationInput) {
	if input == nil || input.ArtifactV3 == nil { return }
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
	if m.Turn != nil { m.Turn.ArtifactID, m.Turn.TurnID = strings.TrimSpace(m.Turn.ArtifactID), strings.TrimSpace(m.Turn.TurnID) }
	if m.Candidate != nil { m.Candidate.ArtifactID, m.Candidate.TurnID, m.Candidate.CandidateID = strings.TrimSpace(m.Candidate.ArtifactID), strings.TrimSpace(m.Candidate.TurnID), strings.TrimSpace(m.Candidate.CandidateID) }
}

func validateArtifactV3MutationInput(input V3SessionMutationInput) error {
	if input.ArtifactV3 == nil {
		if isArtifactV3MutationKind(input.Kind) { return errors.New("artifact v3 mutation payload is required") }
		return nil
	}
	if !isArtifactV3MutationKind(input.Kind) { return errors.New("artifact v3 payload requires an artifact.v3 mutation kind") }
	m := input.ArtifactV3
	artifactID := ""
	for _, id := range []string{artifactV3RepositoryID(m.Repository), artifactV3RevisionID(m.Revision), artifactV3TurnID(m.Turn), artifactV3CandidateID(m.Candidate)} {
		if id == "" { continue }
		if artifactID != "" && id != artifactID { return errors.New("artifact v3 mutation contains mixed artifact identities") }
		artifactID = id
	}
	if artifactID == "" { return errors.New("artifact v3 identity is required") }
	if m.Repository != nil {
		if m.Repository.OwnerSessionID != input.SessionID || m.Repository.AccountScopeID != input.AccountScopeID || m.Repository.UserID != input.UserID || m.Repository.RepositoryID == "" || !validGitOID(m.Repository.HeadCommitOID) { return errors.New("artifact v3 repository ownership or Git identity is invalid") }
	}
	if (input.Kind == V3SessionMutationArtifactV3GenesisCommitted || input.Kind == V3SessionMutationArtifactV3Recovered || input.Kind == V3SessionMutationArtifactV3HeadSelected) && m.Repository == nil { return errors.New("artifact v3 head-changing mutation requires repository projection") }
	if input.Kind == V3SessionMutationArtifactV3GenesisCommitted && m.Revision == nil { return errors.New("artifact v3 genesis requires revision projection") }
	if input.Kind == V3SessionMutationArtifactV3TurnOpened && m.Turn == nil { return errors.New("artifact v3 turn open requires turn projection") }
	if (input.Kind == V3SessionMutationArtifactV3CandidateCommitted || input.Kind == V3SessionMutationArtifactV3CandidateFailed || input.Kind == V3SessionMutationArtifactV3CandidateCancelled || input.Kind == V3SessionMutationArtifactV3HeadSelected) && m.Candidate == nil { return errors.New("artifact v3 candidate mutation requires candidate projection") }
	if m.Revision != nil {
		if m.Revision.OwnerSessionID != input.SessionID || m.Revision.RepositoryID == "" || !validGitOID(m.Revision.CommitOID) || !validGitOID(m.Revision.TreeOID) || !validGitOID(m.Revision.ManifestBlobOID) || m.Revision.FileCount < 1 || m.Revision.TreeBytes < 0 { return errors.New("artifact v3 revision is incomplete") }
		if err := validateCommitOIDs(m.Revision.ParentCommitOIDs); err != nil { return err }
		if len(m.Revision.Parts) > 16384 { return errors.New("artifact v3 part count exceeds bounds") }
		seenParts := map[string]bool{}; for _, part := range m.Revision.Parts { if part.ID == "" || strings.TrimSpace(part.Label) == "" || seenParts[part.ID] { return errors.New("artifact v3 part projection is invalid") }; seenParts[part.ID] = true }
	}
	if m.Turn != nil && (m.Turn.OwnerSessionID != input.SessionID || m.Turn.TurnID == "" || !validGitOID(m.Turn.BaseCommitOID) || (m.Turn.Status != "open" && m.Turn.Status != "selected" && m.Turn.Status != "closed")) { return errors.New("artifact v3 turn is incomplete") }
	if input.Kind == V3SessionMutationArtifactV3CandidateCommitted && m.Revision == nil { return errors.New("artifact v3 committed candidate requires revision projection") }
	if m.Candidate != nil {
		if m.Candidate.OwnerSessionID != input.SessionID || m.Candidate.TurnID == "" || m.Candidate.CandidateID == "" || m.Candidate.TransactionID == "" || (m.Candidate.Status != "ready" && m.Candidate.Status != "selected" && m.Candidate.Status != "failed" && m.Candidate.Status != "cancelled") { return errors.New("artifact v3 candidate is incomplete") }
		if (m.Candidate.Status == "ready" || m.Candidate.Status == "selected") && (!validGitOID(m.Candidate.CommitOID) || !validArtifactGitRef(m.Candidate.CandidateRef)) { return errors.New("artifact v3 ready candidate Git identity is incomplete") }
	}
	return nil
}

func artifactV3RepositoryID(v *ArtifactV3RepositoryProjection) string { if v == nil { return "" }; return v.ArtifactID }
func artifactV3RevisionID(v *ArtifactV3RevisionProjection) string { if v == nil { return "" }; return v.ArtifactID }
func artifactV3TurnID(v *ArtifactV3TurnProjection) string { if v == nil { return "" }; return v.ArtifactID }
func artifactV3CandidateID(v *ArtifactV3CandidateProjection) string { if v == nil { return "" }; return v.ArtifactID }

func (s *SessionStore) prepareArtifactV3Mutation(input V3SessionMutationInput, seq uint64, now int64) (preparedArtifactV3Mutation, error) {
	if input.ArtifactV3 == nil { return preparedArtifactV3Mutation{}, nil }
	storedSession, ok, err := s.GetSession(input.SessionID)
	if err != nil { return preparedArtifactV3Mutation{}, err }
	if !ok || storedSession.AccountScopeID != input.AccountScopeID || storedSession.UserID != input.UserID { return preparedArtifactV3Mutation{}, errors.New("artifact v3 mutation session ownership does not match") }
	m := *input.ArtifactV3
	p := ArtifactV3Projection{}
	artifactID := artifactV3RepositoryID(m.Repository)
	if artifactID == "" { artifactID = artifactV3RevisionID(m.Revision) }
	if artifactID == "" { artifactID = artifactV3TurnID(m.Turn) }
	if artifactID == "" { artifactID = artifactV3CandidateID(m.Candidate) }
	current, currentOK, err := s.GetArtifactV3Repository(input.AccountScopeID, input.UserID, artifactID)
	if err != nil { return preparedArtifactV3Mutation{}, err }
	if !currentOK && input.Kind != V3SessionMutationArtifactV3GenesisCommitted && input.Kind != V3SessionMutationArtifactV3Recovered { return preparedArtifactV3Mutation{}, errors.New("artifact v3 repository was not found") }
	if currentOK && input.Kind == V3SessionMutationArtifactV3GenesisCommitted && current.HeadCommitOID != m.Repository.HeadCommitOID { return preparedArtifactV3Mutation{}, errors.New("artifact v3 genesis conflicts with existing head") }
	if m.ExpectedHeadCommitOID != "" && (!currentOK || current.HeadCommitOID != m.ExpectedHeadCommitOID) { return preparedArtifactV3Mutation{}, errors.New("artifact v3 head compare-and-swap is stale") }
	if m.Repository != nil {
		copy := *m.Repository; copy.Version = 3; copy.EventSeq = seq; copy.UpdatedAt = now
		if currentOK { copy.CreatedAt = current.CreatedAt; if copy.RepositoryID != current.RepositoryID { return preparedArtifactV3Mutation{}, errors.New("artifact v3 repository identity is immutable") } } else if copy.CreatedAt == 0 { copy.CreatedAt = now }
		p.Repository = &copy
	}
	if m.Revision != nil { copy := *m.Revision; copy.Version = 3; copy.EventSeq = seq; if copy.CreatedAt == 0 { copy.CreatedAt = now }; p.Revision = &copy }
	if m.Turn != nil {
		copy := *m.Turn; copy.Version = 3; copy.EventSeq = seq; copy.UpdatedAt = now; if copy.CreatedAt == 0 { copy.CreatedAt = now }
		if existing, found, readErr := s.GetArtifactV3Turn(input.AccountScopeID, input.UserID, artifactID, copy.TurnID); readErr != nil { return preparedArtifactV3Mutation{}, readErr } else if found {
			selectTransition := input.Kind == V3SessionMutationArtifactV3HeadSelected && existing.Status == "open" && copy.Status == "selected" && existing.BaseCommitOID == copy.BaseCommitOID
			if !selectTransition && (existing.BaseCommitOID != copy.BaseCommitOID || existing.Status != copy.Status || existing.TargetPartID != copy.TargetPartID) { return preparedArtifactV3Mutation{}, errors.New("artifact v3 turn identity is immutable") }
		}
		p.Turn = &copy
	}
	if m.Candidate != nil {
		copy := *m.Candidate; copy.Version = 3; copy.EventSeq = seq; copy.UpdatedAt = now; if copy.CreatedAt == 0 { copy.CreatedAt = now }
		turn, found, readErr := s.GetArtifactV3Turn(input.AccountScopeID, input.UserID, artifactID, copy.TurnID); if readErr != nil { return preparedArtifactV3Mutation{}, readErr }; if !found { return preparedArtifactV3Mutation{}, errors.New("artifact v3 candidate turn was not found") }
		if existing, candidateFound, candidateErr := s.GetArtifactV3Candidate(input.AccountScopeID, input.UserID, artifactID, copy.TurnID, copy.CandidateID); candidateErr != nil { return preparedArtifactV3Mutation{}, candidateErr } else if candidateFound {
			selectTransition := input.Kind == V3SessionMutationArtifactV3HeadSelected && existing.Status == "ready" && copy.Status == "selected" && existing.CommitOID == copy.CommitOID
			if !selectTransition { return preparedArtifactV3Mutation{}, errors.New("artifact v3 candidate identity already exists") }
		}
		if (copy.Status == "ready" || input.Kind == V3SessionMutationArtifactV3HeadSelected) && (!artifactV3EvidenceReady(copy.Build, copy.CommitOID) || !artifactV3EvidenceReady(copy.Preview, copy.CommitOID)) { return preparedArtifactV3Mutation{}, errors.New("artifact v3 candidate requires complete build and preview evidence") }
		if input.Kind == V3SessionMutationArtifactV3HeadSelected {
			if turn.BaseCommitOID != m.ExpectedHeadCommitOID { return preparedArtifactV3Mutation{}, errors.New("artifact v3 turn base does not match expected head") }
			if m.Repository == nil || m.Repository.HeadCommitOID != copy.CommitOID { return preparedArtifactV3Mutation{}, errors.New("artifact v3 selection repository head does not match candidate") }
			copy.Status = "selected"
		}
		p.Candidate = &copy
	}
	return preparedArtifactV3Mutation{Projection: p}, nil
}

func artifactV3EvidenceReady(e ArtifactV3EvidenceProjection, commit string) bool { return e.Status == "succeeded" && e.CommitOID == commit && e.DigestSHA256 != "" && e.Reference != "" }

func setArtifactV3MutationInBatch(batch *pebble.Batch, accountScopeID string, prepared preparedArtifactV3Mutation) error {
	p := prepared.Projection
	for key, value := range map[string]any{
		func() string { if p.Repository == nil { return "" }; return KeyArtifactV3Repository(accountScopeID, p.Repository.ArtifactID) }(): p.Repository,
		func() string { if p.Revision == nil { return "" }; return KeyArtifactV3Revision(accountScopeID, p.Revision.ArtifactID, p.Revision.CommitOID) }(): p.Revision,
		func() string { if p.Turn == nil { return "" }; return KeyArtifactV3Turn(accountScopeID, p.Turn.ArtifactID, p.Turn.TurnID) }(): p.Turn,
		func() string { if p.Candidate == nil { return "" }; return KeyArtifactV3Candidate(accountScopeID, p.Candidate.ArtifactID, p.Candidate.TurnID, p.Candidate.CandidateID) }(): p.Candidate,
	} {
		if key == "" || value == nil { continue }; raw, err := json.Marshal(value); if err != nil { return err }; if err := batch.Set([]byte(key), raw, nil); err != nil { return err }
	}
	return nil
}

func (s *SessionStore) GetArtifactV3Repository(accountScopeID, userID, artifactID string) (ArtifactV3RepositoryProjection, bool, error) {
	if s == nil || s.store == nil { return ArtifactV3RepositoryProjection{}, false, errors.New("session store is not configured") }
	var value ArtifactV3RepositoryProjection; ok, err := s.store.GetJSON(KeyArtifactV3Repository(accountScopeID, artifactID), &value); if err != nil || !ok { return value, ok, err }; if value.AccountScopeID != accountScopeID || value.UserID != userID { return ArtifactV3RepositoryProjection{}, false, errors.New("artifact v3 repository ownership does not match") }; return value, true, nil
}
func (s *SessionStore) GetArtifactV3Revision(accountScopeID, userID, artifactID, commitOID string) (ArtifactV3RevisionProjection, bool, error) {
	var value ArtifactV3RevisionProjection; repository, ok, err := s.GetArtifactV3Repository(accountScopeID, userID, artifactID); if err != nil || !ok { return value, false, err }; ok, err = s.store.GetJSON(KeyArtifactV3Revision(accountScopeID, artifactID, commitOID), &value); if err != nil || !ok { return value, ok, err }; if value.OwnerSessionID != repository.OwnerSessionID || value.RepositoryID != repository.RepositoryID { return ArtifactV3RevisionProjection{}, false, errors.New("artifact v3 revision ownership does not match") }; return value, true, nil
}
func (s *SessionStore) GetArtifactV3Turn(accountScopeID, userID, artifactID, turnID string) (ArtifactV3TurnProjection, bool, error) {
	var value ArtifactV3TurnProjection; repository, ok, err := s.GetArtifactV3Repository(accountScopeID, userID, artifactID); if err != nil || !ok { return value, false, err }; ok, err = s.store.GetJSON(KeyArtifactV3Turn(accountScopeID, artifactID, turnID), &value); if err != nil || !ok { return value, ok, err }; if value.OwnerSessionID != repository.OwnerSessionID { return ArtifactV3TurnProjection{}, false, errors.New("artifact v3 turn ownership does not match") }; return value, true, nil
}
func (s *SessionStore) GetArtifactV3Candidate(accountScopeID, userID, artifactID, turnID, candidateID string) (ArtifactV3CandidateProjection, bool, error) {
	var value ArtifactV3CandidateProjection; repository, ok, err := s.GetArtifactV3Repository(accountScopeID, userID, artifactID); if err != nil || !ok { return value, false, err }; ok, err = s.store.GetJSON(KeyArtifactV3Candidate(accountScopeID, artifactID, turnID, candidateID), &value); if err != nil || !ok { return value, ok, err }; if value.OwnerSessionID != repository.OwnerSessionID { return ArtifactV3CandidateProjection{}, false, errors.New("artifact v3 candidate ownership does not match") }; return value, true, nil
}
