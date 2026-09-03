package pebblestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type ArtifactV3Service struct {
	sessions *SessionStore
	root     string
	limits   ArtifactV3Limits
}

type ArtifactV3CreateInput struct {
	Owner         ArtifactV3Owner
	ArtifactID    string
	TransactionID string
	Project       ArtifactV3Project
	Message       string
	Build         ArtifactV3EvidenceProjection
	Preview       ArtifactV3EvidenceProjection
	NowUnixMs     int64
}

type ArtifactV3OpenTurnInput struct {
	Owner        ArtifactV3Owner
	ArtifactID   string
	TurnID       string
	ExpectedHead string
	TargetPartID string
	NowUnixMs    int64
}

type ArtifactV3SubmitCandidateInput struct {
	Owner         ArtifactV3Owner
	ArtifactID    string
	TurnID        string
	CandidateID   string
	TransactionID string
	ExpectedHead  string
	Project       ArtifactV3Project
	Message       string
	Build         ArtifactV3EvidenceProjection
	Preview       ArtifactV3EvidenceProjection
	NowUnixMs     int64
}

type ArtifactV3SelectInput struct {
	Owner         ArtifactV3Owner
	ArtifactID    string
	TurnID        string
	CandidateID   string
	TransactionID string
	ExpectedHead  string
	NowUnixMs     int64
}

func NewArtifactV3Service(sessions *SessionStore, root string, limits ArtifactV3Limits) (*ArtifactV3Service, error) {
	if sessions == nil || sessions.store == nil || strings.TrimSpace(root) == "" {
		return nil, errors.New("artifact v3 service requires session store and repository root")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &ArtifactV3Service{sessions: sessions, root: absolute, limits: limits.normalized()}, nil
}

func (s *ArtifactV3Service) Create(ctx context.Context, input ArtifactV3CreateInput) (ArtifactV3Projection, error) {
	if input.Owner.AccountScopeID == "" || input.Owner.UserID == "" || input.Owner.SessionID == "" || input.ArtifactID == "" || input.TransactionID == "" {
		return ArtifactV3Projection{}, ErrArtifactV3Invalid
	}
	if !artifactV3EvidencePrepared(input.Build) || !artifactV3EvidencePrepared(input.Preview) {
		return ArtifactV3Projection{}, errors.New("artifact v3 genesis requires complete build and preview evidence")
	}
	_, existedBefore, existenceErr := s.sessions.GetArtifactV3Repository(input.Owner.AccountScopeID, input.Owner.UserID, input.ArtifactID)
	if existenceErr != nil {
		return ArtifactV3Projection{}, ErrArtifactV3Unauthorized
	}
	if existedBefore {
		repository, openErr := s.open(ctx, input.Owner, input.ArtifactID)
		if openErr != nil {
			return ArtifactV3Projection{}, openErr
		}
		revision, revisionErr := repository.Genesis(ctx, ArtifactV3GenesisRequest{TransactionID: input.TransactionID, Project: input.Project, Message: input.Message})
		if revisionErr != nil {
			return ArtifactV3Projection{}, revisionErr
		}
		projected, ok, readErr := s.sessions.GetArtifactV3Revision(input.Owner.AccountScopeID, input.Owner.UserID, input.ArtifactID, revision.CommitOID)
		if readErr != nil || !ok || !artifactV3EvidenceReady(projected.Build, revision.CommitOID) || !artifactV3EvidenceReady(projected.Preview, revision.CommitOID) {
			return ArtifactV3Projection{}, ErrArtifactV3Integrity
		}
		stored, _, _ := s.sessions.GetArtifactV3Repository(input.Owner.AccountScopeID, input.Owner.UserID, input.ArtifactID)
		turnID, candidateID := "turn-"+input.TransactionID, "candidate-"+input.TransactionID
		turn, _, _ := s.sessions.GetArtifactV3Turn(input.Owner.AccountScopeID, input.Owner.UserID, input.ArtifactID, turnID)
		candidate, _, _ := s.sessions.GetArtifactV3Candidate(input.Owner.AccountScopeID, input.Owner.UserID, input.ArtifactID, turnID, candidateID)
		return ArtifactV3Projection{Repository: &stored, Revision: &projected, Turn: &turn, Candidate: &candidate}, nil
	}
	repository, err := s.open(ctx, input.Owner, input.ArtifactID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if _, headErr := repository.Head(ctx); headErr == nil {
		return ArtifactV3Projection{}, ErrArtifactV3Conflict
	} else if !errors.Is(headErr, ErrArtifactV3NotFound) {
		return ArtifactV3Projection{}, headErr
	}
	revision, err := repository.Genesis(ctx, ArtifactV3GenesisRequest{TransactionID: input.TransactionID, Project: input.Project, Message: input.Message})
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	build, preview := input.Build, input.Preview
	build.CommitOID, preview.CommitOID = revision.CommitOID, revision.CommitOID
	projection, err := s.revisionProjection(ctx, repository, input.Owner, input.ArtifactID, revision, "", build, preview, now)
	if err != nil {
		_ = repository.Delete()
		return ArtifactV3Projection{}, err
	}
	repositoryProjection := ArtifactV3RepositoryProjection{ArtifactID: input.ArtifactID, RepositoryID: input.ArtifactID, AccountScopeID: input.Owner.AccountScopeID, UserID: input.Owner.UserID, OwnerSessionID: input.Owner.SessionID, IntentReference: strings.TrimSpace(input.Message), HeadCommitOID: revision.CommitOID}
	turnID, candidateID := "turn-"+input.TransactionID, "candidate-"+input.TransactionID
	turn := ArtifactV3TurnProjection{ArtifactID: input.ArtifactID, TurnID: turnID, OwnerSessionID: input.Owner.SessionID, BaseCommitOID: revision.CommitOID, Status: "selected", SelectedCandidateID: candidateID}
	candidate := ArtifactV3CandidateProjection{ArtifactID: input.ArtifactID, TurnID: turnID, CandidateID: candidateID, OwnerSessionID: input.Owner.SessionID, CommitOID: revision.CommitOID, CandidateRef: "refs/heads/artifact", TransactionID: input.TransactionID, Status: "selected", Build: build, Preview: preview}
	applied, err := s.apply(input.Owner, input.TransactionID, V3SessionMutationArtifactV3GenesisCommitted, ArtifactV3Mutation{Repository: &repositoryProjection, Revision: &projection, Turn: &turn, Candidate: &candidate}, input.NowUnixMs)
	if err != nil {
		// Git is written before the durable V3 projection. Preserve the private
		// transaction ref when that handoff fails so Recover can deterministically
		// finish the exact commit instead of deleting the only recovery evidence.
		return ArtifactV3Projection{}, err
	}
	return applied, nil
}

func (s *ArtifactV3Service) OpenTurn(ctx context.Context, input ArtifactV3OpenTurnInput) (ArtifactV3Projection, error) {
	repository, err := s.open(ctx, input.Owner, input.ArtifactID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	head, err := repository.Head(ctx)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if head != input.ExpectedHead {
		return ArtifactV3Projection{}, ErrArtifactV3Conflict
	}
	turn := ArtifactV3TurnProjection{ArtifactID: input.ArtifactID, TurnID: input.TurnID, OwnerSessionID: input.Owner.SessionID, BaseCommitOID: head, TargetPartID: input.TargetPartID, Status: "open"}
	return s.apply(input.Owner, input.TurnID, V3SessionMutationArtifactV3TurnOpened, ArtifactV3Mutation{Turn: &turn, ExpectedHeadCommitOID: head}, input.NowUnixMs)
}

func (s *ArtifactV3Service) SubmitCandidate(ctx context.Context, input ArtifactV3SubmitCandidateInput) (ArtifactV3Projection, error) {
	repository, err := s.open(ctx, input.Owner, input.ArtifactID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	head, err := repository.Head(ctx)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if head != input.ExpectedHead {
		return ArtifactV3Projection{}, ErrArtifactV3Conflict
	}
	turn, ok, err := s.sessions.GetArtifactV3Turn(input.Owner.AccountScopeID, input.Owner.UserID, input.ArtifactID, input.TurnID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if !ok || turn.BaseCommitOID != head || (turn.Status != "open" && turn.Status != "awaiting_selection") {
		return ArtifactV3Projection{}, ErrArtifactV3Conflict
	}
	if _, _, validationErr := repository.validateProject(input.Project); validationErr != nil {
		return ArtifactV3Projection{}, validationErr
	}
	commitOID, err := repository.commitProject(ctx, input.Project, []string{head}, input.Message)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	candidate := ArtifactV3CandidateProjection{ArtifactID: input.ArtifactID, TurnID: input.TurnID, CandidateID: input.CandidateID, OwnerSessionID: input.Owner.SessionID, CommitOID: commitOID, TransactionID: input.TransactionID, Status: "ready", Build: input.Build, Preview: input.Preview}
	if !artifactV3EvidenceReady(candidate.Build, candidate.CommitOID) || !artifactV3EvidenceReady(candidate.Preview, candidate.CommitOID) {
		return ArtifactV3Projection{}, errors.New("artifact v3 candidate evidence does not bind the exact commit")
	}
	if existing, err := repository.Transaction(ctx, input.TransactionID); err == nil && existing.CommitOID != commitOID {
		return ArtifactV3Projection{}, ErrArtifactV3TxReuse
	} else if err != nil && !errors.Is(err, ErrArtifactV3NotFound) {
		return ArtifactV3Projection{}, err
	}
	candidateRef, _ := repository.CandidateRef(input.TurnID, input.CandidateID)
	if existingCommit, err := repository.ref(ctx, candidateRef); err == nil && existingCommit != commitOID {
		return ArtifactV3Projection{}, ErrArtifactV3Conflict
	} else if err != nil && !errors.Is(err, ErrArtifactV3NotFound) {
		return ArtifactV3Projection{}, err
	}
	if err := repository.atomicCreateRefs(ctx, input.TransactionID, candidateRef, commitOID); err != nil {
		return ArtifactV3Projection{}, err
	}
	candidate.CandidateRef = candidateRef
	revision, err := repository.ReadRevision(ctx, commitOID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	projectedRevision, err := s.revisionProjection(ctx, repository, input.Owner, input.ArtifactID, revision, head, input.Build, input.Preview, now)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	turn.Status = "awaiting_selection"
	return s.apply(input.Owner, input.TransactionID, V3SessionMutationArtifactV3CandidateCommitted, ArtifactV3Mutation{Revision: &projectedRevision, Turn: &turn, Candidate: &candidate, ExpectedHeadCommitOID: head}, input.NowUnixMs)
}

func (s *ArtifactV3Service) Select(ctx context.Context, input ArtifactV3SelectInput) (ArtifactV3Projection, error) {
	repository, err := s.open(ctx, input.Owner, input.ArtifactID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	candidate, ok, err := s.sessions.GetArtifactV3Candidate(input.Owner.AccountScopeID, input.Owner.UserID, input.ArtifactID, input.TurnID, input.CandidateID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if !ok || candidate.Status != "ready" || !artifactV3EvidenceReady(candidate.Build, candidate.CommitOID) || !artifactV3EvidenceReady(candidate.Preview, candidate.CommitOID) {
		return ArtifactV3Projection{}, errors.New("artifact v3 candidate is not selectable")
	}
	turn, ok, err := s.sessions.GetArtifactV3Turn(input.Owner.AccountScopeID, input.Owner.UserID, input.ArtifactID, input.TurnID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if !ok || turn.BaseCommitOID != input.ExpectedHead {
		return ArtifactV3Projection{}, ErrArtifactV3Conflict
	}
	revision, err := repository.Select(ctx, ArtifactV3SelectionRequest{TurnID: input.TurnID, CandidateID: input.CandidateID, TransactionID: input.TransactionID, ExpectedHead: input.ExpectedHead, Candidate: candidate.CommitOID})
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	repositoryProjection, ok, err := s.sessions.GetArtifactV3Repository(input.Owner.AccountScopeID, input.Owner.UserID, input.ArtifactID)
	if err != nil || !ok {
		return ArtifactV3Projection{}, err
	}
	repositoryProjection.HeadCommitOID = revision.CommitOID
	candidate.Status = "selected"
	turn.Status = "selected"
	turn.SelectedCandidateID = input.CandidateID
	return s.apply(input.Owner, input.TransactionID, V3SessionMutationArtifactV3HeadSelected, ArtifactV3Mutation{Repository: &repositoryProjection, Turn: &turn, Candidate: &candidate, ExpectedHeadCommitOID: input.ExpectedHead}, input.NowUnixMs)
}

func (s *ArtifactV3Service) Recover(ctx context.Context, owner ArtifactV3Owner, artifactID string) (ArtifactV3Projection, error) {
	repository, err := s.open(ctx, owner, artifactID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	head, err := repository.Head(ctx)
	if errors.Is(err, ErrArtifactV3NotFound) {
		// An interrupted first authoring turn may leave an owner-bound empty bare
		// repository before Genesis creates any commit. It carries no user-visible
		// revision or transaction to recover, so preserve it for the original turn
		// and do not fail daemon startup.
		return ArtifactV3Projection{}, nil
	}
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if err := repository.IntegrityCheck(ctx); err != nil {
		return ArtifactV3Projection{}, err
	}
	stored, ok, err := s.sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, artifactID)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if ok && stored.RepositoryID != artifactID {
		return ArtifactV3Projection{}, ErrArtifactV3Integrity
	}
	if ok && stored.HeadCommitOID == head {
		projected, revisionOK, readErr := s.sessions.GetArtifactV3Revision(owner.AccountScopeID, owner.UserID, artifactID, head)
		if readErr != nil || !revisionOK || !artifactV3EvidenceReady(projected.Build, head) || !artifactV3EvidenceReady(projected.Preview, head) {
			return ArtifactV3Projection{}, ErrArtifactV3Integrity
		}
		return ArtifactV3Projection{Repository: &stored, Revision: &projected}, nil
	}
	tx, err := s.findAppliedTransaction(ctx, repository, head)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	revision, err := repository.ReadRevision(ctx, head)
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	build, preview := recoveryEvidence(revision.CommitOID, revision.TreeOID, "build"), recoveryEvidence(revision.CommitOID, revision.TreeOID, "preview")
	if existing, found, readErr := s.sessions.GetArtifactV3Revision(owner.AccountScopeID, owner.UserID, artifactID, head); readErr == nil && found && artifactV3EvidenceReady(existing.Build, head) && artifactV3EvidenceReady(existing.Preview, head) {
		build, preview = existing.Build, existing.Preview
	}
	projectedRevision, err := s.revisionProjection(ctx, repository, owner, artifactID, revision, firstParent(revision), build, preview, time.Now().UnixMilli())
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	projection := ArtifactV3RepositoryProjection{ArtifactID: artifactID, RepositoryID: artifactID, AccountScopeID: owner.AccountScopeID, UserID: owner.UserID, OwnerSessionID: owner.SessionID, HeadCommitOID: head}
	if ok {
		projection.CreatedAt, projection.IntentReference = stored.CreatedAt, stored.IntentReference
	}
	return s.apply(owner, "recover-"+tx.ID, V3SessionMutationArtifactV3Recovered, ArtifactV3Mutation{Repository: &projection, Revision: &projectedRevision}, 0)
}

func (s *ArtifactV3Service) RecordCandidateTerminal(owner ArtifactV3Owner, artifactID, turnID, candidateID, transactionID, status, failureCode string, now int64) (ArtifactV3Projection, error) {
	if status != "failed" && status != "cancelled" {
		return ArtifactV3Projection{}, ErrArtifactV3Invalid
	}
	repository, ok, err := s.sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, artifactID)
	if err != nil || !ok {
		return ArtifactV3Projection{}, err
	}
	turn, turnOK, err := s.sessions.GetArtifactV3Turn(owner.AccountScopeID, owner.UserID, artifactID, turnID)
	if err != nil || !turnOK || (turn.Status != "open" && turn.Status != "awaiting_selection") {
		return ArtifactV3Projection{}, errors.New("artifact v3 candidate turn was not found or is closed")
	}
	candidate := ArtifactV3CandidateProjection{ArtifactID: artifactID, TurnID: turnID, CandidateID: candidateID, OwnerSessionID: owner.SessionID, TransactionID: transactionID, Status: status, FailureCode: failureCode}
	kind := V3SessionMutationArtifactV3CandidateFailed
	if status == "cancelled" {
		kind = V3SessionMutationArtifactV3CandidateCancelled
	}
	return s.apply(owner, transactionID, kind, ArtifactV3Mutation{Candidate: &candidate, ExpectedHeadCommitOID: repository.HeadCommitOID}, now)
}

func (s *ArtifactV3Service) open(ctx context.Context, owner ArtifactV3Owner, artifactID string) (*ArtifactV3Repository, error) {
	return OpenArtifactV3Repository(ctx, s.root, artifactID, owner, s.limits)
}
func (s *ArtifactV3Service) apply(owner ArtifactV3Owner, requestID, kind string, mutation ArtifactV3Mutation, now int64) (ArtifactV3Projection, error) {
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	identity, marshalErr := json.Marshal(mutation)
	if marshalErr != nil {
		return ArtifactV3Projection{}, marshalErr
	}
	sum := sha256.Sum256(append([]byte(kind+"\x00"+requestID+"\x00"), identity...))
	result, err := s.sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: owner.SessionID, UserID: owner.UserID, AccountScopeID: owner.AccountScopeID, ClientRequestID: requestID, IdempotencyKey: requestID, PayloadHash: hex.EncodeToString(sum[:]), Kind: kind, ArtifactV3: &mutation, NowUnixMs: now})
	if err != nil {
		return ArtifactV3Projection{}, err
	}
	if result.ArtifactV3 == nil {
		return ArtifactV3Projection{}, errors.New("artifact v3 projection missing from mutation result")
	}
	return *result.ArtifactV3, nil
}
func (s *ArtifactV3Service) revisionProjection(ctx context.Context, repository *ArtifactV3Repository, owner ArtifactV3Owner, artifactID string, revision ArtifactV3Revision, base string, build, preview ArtifactV3EvidenceProjection, now int64) (ArtifactV3RevisionProjection, error) {
	changed, err := repository.ChangedFiles(ctx, base, revision.CommitOID)
	if err != nil {
		return ArtifactV3RevisionProjection{}, err
	}
	if !artifactV3EvidenceReady(build, revision.CommitOID) || !artifactV3EvidenceReady(preview, revision.CommitOID) {
		return ArtifactV3RevisionProjection{}, errors.New("artifact v3 revision evidence does not bind the exact commit")
	}
	parts := make([]ArtifactV3PartProjection, 0, len(revision.Manifest.Parts))
	for _, part := range revision.Manifest.Parts {
		parts = append(parts, ArtifactV3PartProjection{ID: part.ID, Label: part.Label, LocatorKind: part.Locator.Kind, Path: part.Locator.Path, Value: part.Locator.Value, Paths: append([]string(nil), part.Locator.Paths...)})
	}
	return ArtifactV3RevisionProjection{ArtifactID: artifactID, RepositoryID: artifactID, OwnerSessionID: owner.SessionID, CommitOID: revision.CommitOID, TreeOID: revision.TreeOID, ManifestBlobOID: revision.ManifestBlobOID, ParentCommitOIDs: append([]string(nil), revision.Parents...), ChangedFiles: changed, Parts: parts, Build: build, Preview: preview, FileCount: revision.FileCount, TreeBytes: revision.TreeBytes, CreatedAt: now}, nil
}

func artifactV3EvidencePrepared(e ArtifactV3EvidenceProjection) bool {
	return e.Status == "succeeded" && strings.TrimSpace(e.DigestSHA256) != "" && strings.TrimSpace(e.Reference) != ""
}

func recoveryEvidence(commitOID, treeOID, kind string) ArtifactV3EvidenceProjection {
	return ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: commitOID, DigestSHA256: treeOID, Reference: "recovered-" + kind + "-" + commitOID}
}
func (s *ArtifactV3Service) findAppliedTransaction(ctx context.Context, repository *ArtifactV3Repository, head string) (ArtifactV3Transaction, error) {
	cursor := ""
	for {
		page, err := repository.ListRefs(ctx, "refs/swarm/transactions/", cursor, 500)
		if err != nil {
			return ArtifactV3Transaction{}, err
		}
		for _, ref := range page.Refs {
			if ref.CommitOID == head {
				id := strings.TrimPrefix(ref.Name, "refs/swarm/transactions/")
				tx, txErr := repository.Transaction(ctx, id)
				if txErr == nil && tx.State == ArtifactV3TransactionApplied {
					return tx, nil
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return ArtifactV3Transaction{}, fmt.Errorf("%w: head has no applied transaction", ErrArtifactV3Integrity)
}
func firstParent(revision ArtifactV3Revision) string {
	if len(revision.Parents) == 0 {
		return ""
	}
	return revision.Parents[0]
}
