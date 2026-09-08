// Package artifactv2 is the sole application authority for new managed
// creative writes. It intentionally depends only on audited storage and V3
// mutation primitives, never the retired managed-write authority.
package artifactv2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/artifactgit"
	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Principal struct {
	AccountScopeID string
	UserID         string
	SessionID      string
	RunID          string
	ActorClass     string
}

type SessionResolver interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
}

type EventCommitter interface {
	ApplySessionMutation(pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error)
}

type Store interface {
	GetArtifactV2Working(accountScopeID, artifactID string) (pebblestore.ArtifactV2WorkingArtifact, bool, error)
	ListArtifactV2Parts(accountScopeID, artifactID string, limit int) ([]pebblestore.ArtifactV2Part, error)
	GetArtifactV2Part(accountScopeID, artifactID, partID string) (pebblestore.ArtifactV2Part, bool, error)
	GetArtifactV2PartRevision(accountScopeID, artifactID, partID, revisionID string) (pebblestore.ArtifactV2PartRevision, bool, error)
	ListArtifactV2PartRevisions(accountScopeID, artifactID, partID string, limit int) ([]pebblestore.ArtifactV2PartRevision, error)
	GetArtifactV2Composition(accountScopeID, artifactID, compositionID string) (pebblestore.ArtifactV2Composition, bool, error)
	GetArtifactV2Build(accountScopeID, artifactID, buildID string) (pebblestore.ArtifactV2BuildResult, bool, error)
	GetArtifactV2Validation(accountScopeID, artifactID, validationID string) (pebblestore.ArtifactV2ValidationResult, bool, error)
	GetArtifactV2Derivative(accountScopeID, artifactID, derivativeID string) (pebblestore.ArtifactV2Derivative, bool, error)
	ListArtifactV2Derivatives(accountScopeID, artifactID string, limit int) ([]pebblestore.ArtifactV2Derivative, error)
	GetArtifactV2Iteration(accountScopeID, artifactID, iterationID string) (pebblestore.ArtifactV2IterationRound, bool, error)
	GetArtifactV2PublishedHead(accountScopeID, artifactID, publishedHeadID string) (pebblestore.ArtifactV2PublishedHead, bool, error)
}

type BlobStore interface {
	PutImmutable(ctx context.Context, principal Principal, artifactID, partID, mediaType string, body []byte) (pebblestore.ArtifactV2BlobReceipt, error)
	GetExact(ctx context.Context, principal Principal, receipt pebblestore.ArtifactV2BlobReceipt) ([]byte, error)
}

// ReadyReference is the exact Artifact V2 read/render boundary. It is not the
// legacy collection/variant reference schema and no V1 writer accepts it.
type ReadyReference struct {
	ArtifactID      string `json:"artifact_id"`
	PublishedHeadID string `json:"published_head_id"`
	CompositionID   string `json:"composition_id"`
	BuildID         string `json:"build_id"`
	ValidationID    string `json:"validation_id"`
	EventSeq        uint64 `json:"event_seq"`
	DigestSHA256    string `json:"digest_sha256"`
	PolicyRevision  string `json:"policy_revision"`
}

type Service struct {
	sessions SessionResolver
	store    Store
	commit   EventCommitter
	blobs    BlobStore
	now      func() time.Time
	newID    func(string) (string, error)
}

func NewService(sessions SessionResolver, store Store, commit EventCommitter, blobs BlobStore) *Service {
	return &Service{sessions: sessions, store: store, commit: commit, blobs: blobs, now: time.Now, newID: randomID}
}

type CreateWorkingInput struct {
	RequestID       string
	ArtifactKind    string
	PolicyRevision  string
	CapabilityClass string
	IntentReference string
	CausationID     string
	CorrelationID   string
}

func (s *Service) CreateWorking(ctx context.Context, principal Principal, input CreateWorkingInput) (pebblestore.ArtifactV2WorkingArtifact, error) {
	principal, err := s.owned(principal)
	if err != nil {
		return pebblestore.ArtifactV2WorkingArtifact{}, err
	}
	input.RequestID, input.ArtifactKind, input.PolicyRevision, input.CapabilityClass = trim(input.RequestID), trim(input.ArtifactKind), trim(input.PolicyRevision), trim(input.CapabilityClass)
	if input.RequestID == "" || input.ArtifactKind == "" || input.PolicyRevision == "" || input.CapabilityClass == "" {
		return pebblestore.ArtifactV2WorkingArtifact{}, errors.New("artifact v2 create requires request, kind, policy revision, and capability class")
	}
	artifactID := deterministicID("artv2", principal.AccountScopeID, principal.SessionID, input.RequestID)
	working := pebblestore.ArtifactV2WorkingArtifact{SchemaVersion: pebblestore.ArtifactV2SchemaVersion, ID: artifactID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: principal.SessionID, Kind: input.ArtifactKind, State: pebblestore.ArtifactV2StateAllocated, PolicyRevision: input.PolicyRevision, CapabilityClass: input.CapabilityClass, IntentReference: trim(input.IntentReference), CreationRequestID: input.RequestID, Revision: 1}
	result, err := s.commitMutation(ctx, principal, input.RequestID, pebblestore.V3SessionMutationArtifactV2WorkingCreated, working, pebblestore.ArtifactV2Mutation{Working: &working}, input.CausationID, input.CorrelationID)
	if err != nil {
		return pebblestore.ArtifactV2WorkingArtifact{}, err
	}
	if result.ArtifactV2 == nil {
		return pebblestore.ArtifactV2WorkingArtifact{}, errors.New("artifact v2 create committed without projection")
	}
	return s.mustWorking(principal, artifactID)
}

type DeclarePartInput struct {
	RequestID        string
	ArtifactID       string
	ExpectedRevision uint64
	Key              string
	Label            string
	Role             string
	MediaClass       string
	LocatorKind      string
	LocatorValue     string
	Order            int
}

func (s *Service) DeclarePart(ctx context.Context, principal Principal, input DeclarePartInput) (pebblestore.ArtifactV2Part, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2Part{}, err
	}
	if input.ExpectedRevision != working.Revision {
		return pebblestore.ArtifactV2Part{}, errors.New("artifact v2 working revision is stale")
	}
	if input.RequestID == "" || trim(input.Key) == "" || trim(input.MediaClass) == "" {
		return pebblestore.ArtifactV2Part{}, errors.New("artifact v2 part declaration is incomplete")
	}
	partID := deterministicID("partv2", working.ID, input.RequestID, input.Key)
	part := pebblestore.ArtifactV2Part{SchemaVersion: pebblestore.ArtifactV2SchemaVersion, ID: partID, ArtifactID: working.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: principal.SessionID, Key: trim(input.Key), Label: trim(input.Label), Role: trim(input.Role), MediaClass: trim(input.MediaClass), LocatorKind: trim(input.LocatorKind), LocatorValue: trim(input.LocatorValue), Order: input.Order}
	next := working
	next.State, next.Revision = pebblestore.ArtifactV2StateAuthoring, working.Revision+1
	expected := working.Revision
	_, err = s.commitMutation(ctx, principal, input.RequestID, pebblestore.V3SessionMutationArtifactV2PartDeclared, next, pebblestore.ArtifactV2Mutation{Working: &next, Part: &part, ExpectedWorkingRevision: &expected}, "", "")
	if err != nil {
		return pebblestore.ArtifactV2Part{}, err
	}
	stored, ok, err := s.store.GetArtifactV2Part(principal.AccountScopeID, working.ID, partID)
	if err != nil || !ok {
		if err != nil {
			return pebblestore.ArtifactV2Part{}, err
		}
		return pebblestore.ArtifactV2Part{}, errors.New("artifact v2 part was not persisted")
	}
	return stored, nil
}

type AppendPartRevisionInput struct {
	RequestID               string
	ArtifactID              string
	PartID                  string
	ExpectedWorkingRevision uint64
	ExpectedBaseRevisionID  string
	CapabilityGrantID       string
	MediaType               string
	Body                    []byte
}

func (s *Service) AppendPartRevision(ctx context.Context, principal Principal, input AppendPartRevisionInput) (pebblestore.ArtifactV2PartRevision, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2PartRevision{}, err
	}
	if input.ExpectedWorkingRevision != working.Revision {
		return pebblestore.ArtifactV2PartRevision{}, errors.New("artifact v2 working revision is stale")
	}
	part, ok, err := s.store.GetArtifactV2Part(principal.AccountScopeID, working.ID, trim(input.PartID))
	if err != nil || !ok {
		if err != nil {
			return pebblestore.ArtifactV2PartRevision{}, err
		}
		return pebblestore.ArtifactV2PartRevision{}, errors.New("artifact v2 part was not found")
	}
	if input.RequestID == "" || trim(input.MediaType) == "" || len(input.Body) == 0 {
		return pebblestore.ArtifactV2PartRevision{}, errors.New("artifact v2 part revision requires request, media type, and non-empty bytes")
	}
	parentID := trim(input.ExpectedBaseRevisionID)
	if parentID != "" {
		if _, ok, err := s.store.GetArtifactV2PartRevision(principal.AccountScopeID, working.ID, part.ID, parentID); err != nil || !ok {
			if err != nil {
				return pebblestore.ArtifactV2PartRevision{}, err
			}
			return pebblestore.ArtifactV2PartRevision{}, errors.New("artifact v2 base part revision was not found")
		}
	}
	revisionID := deterministicID("prev2", working.ID, part.ID, input.RequestID)
	if existing, ok, err := s.store.GetArtifactV2PartRevision(principal.AccountScopeID, working.ID, part.ID, revisionID); err != nil {
		return pebblestore.ArtifactV2PartRevision{}, err
	} else if ok {
		return existing, nil
	}
	receipt, err := s.blobs.PutImmutable(ctx, principal, working.ID, part.ID, trim(input.MediaType), input.Body)
	if err != nil {
		return pebblestore.ArtifactV2PartRevision{}, err
	}
	revision := pebblestore.ArtifactV2PartRevision{SchemaVersion: pebblestore.ArtifactV2SchemaVersion, ID: revisionID, ArtifactID: working.ID, PartID: part.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: principal.SessionID, ParentRevisionID: parentID, ProducerRunID: principal.RunID, CapabilityGrant: trim(input.CapabilityGrantID), Blob: receipt}
	next := working
	next.State, next.Revision = pebblestore.ArtifactV2StateAuthoring, working.Revision+1
	expected := working.Revision
	_, err = s.commitMutation(ctx, principal, input.RequestID, pebblestore.V3SessionMutationArtifactV2PartRevisionAppended, next, pebblestore.ArtifactV2Mutation{Working: &next, PartRevision: &revision, ExpectedWorkingRevision: &expected}, "", "")
	if err != nil {
		return pebblestore.ArtifactV2PartRevision{}, err
	}
	stored, ok, err := s.store.GetArtifactV2PartRevision(principal.AccountScopeID, working.ID, part.ID, revisionID)
	if err != nil || !ok {
		if err != nil {
			return pebblestore.ArtifactV2PartRevision{}, err
		}
		return pebblestore.ArtifactV2PartRevision{}, errors.New("artifact v2 part revision was not persisted")
	}
	return stored, nil
}

type CompositionSelection struct {
	PartID, PartRevisionID string
	Locked                 bool
}
type AdvanceCompositionInput struct {
	RequestID                       string
	ArtifactID                      string
	ExpectedWorkingRevision         uint64
	ExpectedCompositionHeadRevision uint64
	ConstructionVersion             string
	Selections                      []CompositionSelection
	// AllowLockedPartChanges is reserved for authenticated user application
	// commands. Designer capabilities never set it.
	AllowLockedPartChanges bool
}

func (s *Service) AdvanceComposition(ctx context.Context, principal Principal, input AdvanceCompositionInput) (pebblestore.ArtifactV2Composition, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2Composition{}, err
	}
	if input.ExpectedWorkingRevision != working.Revision {
		return pebblestore.ArtifactV2Composition{}, errors.New("artifact v2 working revision is stale")
	}
	if input.RequestID == "" || trim(input.ConstructionVersion) == "" || len(input.Selections) == 0 {
		return pebblestore.ArtifactV2Composition{}, errors.New("artifact v2 composition input is incomplete")
	}
	parts := make([]pebblestore.ArtifactV2CompositionPart, 0, len(input.Selections))
	for _, selected := range input.Selections {
		revision, ok, err := s.store.GetArtifactV2PartRevision(principal.AccountScopeID, working.ID, trim(selected.PartID), trim(selected.PartRevisionID))
		if err != nil || !ok {
			if err != nil {
				return pebblestore.ArtifactV2Composition{}, err
			}
			return pebblestore.ArtifactV2Composition{}, errors.New("artifact v2 composition part revision was not found")
		}
		parts = append(parts, pebblestore.ArtifactV2CompositionPart{PartID: revision.PartID, PartRevisionID: revision.ID, DigestSHA256: revision.Blob.DigestSHA256, Locked: selected.Locked})
	}
	compositionID := deterministicID("compv2", working.ID, input.RequestID)
	composition := pebblestore.ArtifactV2Composition{SchemaVersion: pebblestore.ArtifactV2SchemaVersion, ID: compositionID, ArtifactID: working.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: principal.SessionID, PolicyRevision: working.PolicyRevision, ConstructionVersion: trim(input.ConstructionVersion), Parts: parts}
	if working.CompositionHead != nil {
		composition.ParentCompositionID = working.CompositionHead.CompositionID
	}
	composition.DigestSHA256 = pebblestore.ArtifactV2CompositionDigest(composition.PolicyRevision, composition.ConstructionVersion, composition.Parts)
	next := working
	next.State, next.Revision = pebblestore.ArtifactV2StateAuthoring, working.Revision+1
	expectedWorking, expectedHead := working.Revision, input.ExpectedCompositionHeadRevision
	_, err = s.commitMutation(ctx, principal, input.RequestID, pebblestore.V3SessionMutationArtifactV2CompositionHeadAdvanced, next, pebblestore.ArtifactV2Mutation{Working: &next, Composition: &composition, ExpectedWorkingRevision: &expectedWorking, ExpectedCompositionHeadRevision: &expectedHead, AdvanceCompositionHead: true, AllowLockedPartChanges: input.AllowLockedPartChanges}, "", "")
	if err != nil {
		return pebblestore.ArtifactV2Composition{}, err
	}
	stored, ok, err := s.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, compositionID)
	if err != nil || !ok {
		if err != nil {
			return pebblestore.ArtifactV2Composition{}, err
		}
		return pebblestore.ArtifactV2Composition{}, errors.New("artifact v2 composition was not persisted")
	}
	return stored, nil
}

type RecordBuildInput struct {
	RequestID, ArtifactID   string
	ExpectedWorkingRevision uint64
	Build                   pebblestore.ArtifactV2BuildResult
}

func (s *Service) RecordBuild(ctx context.Context, principal Principal, input RecordBuildInput) (pebblestore.ArtifactV2BuildResult, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return input.Build, err
	}
	if working.CompositionHead == nil || input.ExpectedWorkingRevision != working.Revision {
		return input.Build, errors.New("artifact v2 build working state is stale or has no composition")
	}
	b := input.Build
	b.ID, b.ArtifactID, b.CompositionID, b.CompositionDigest, b.PolicyRevision = trim(b.ID), working.ID, working.CompositionHead.CompositionID, working.CompositionHead.DigestSHA256, working.PolicyRevision
	if b.ID == "" {
		b.ID = deterministicID("buildv2", working.ID, input.RequestID)
	}
	b.Revision = 1
	if b.CreatedAt == 0 {
		b.CreatedAt = s.now().UnixMilli()
	}
	if b.CompilerVersion == "" || !validBuildStatus(b.Status) {
		return b, errors.New("artifact v2 build compiler version or status is invalid")
	}
	next := working
	next.Revision, next.LatestBuildID = working.Revision+1, b.ID
	if b.Status == pebblestore.ArtifactV2BuildSucceeded {
		next.State = pebblestore.ArtifactV2StateValidating
	} else if b.Status == pebblestore.ArtifactV2BuildFailed {
		next.State = pebblestore.ArtifactV2StateInvalid
	} else {
		next.State = pebblestore.ArtifactV2StateBuilding
	}
	expected := working.Revision
	_, err = s.commitMutation(ctx, principal, input.RequestID, artifactV2BuildEventKind(b.Status), next, pebblestore.ArtifactV2Mutation{Working: &next, Build: &b, ExpectedWorkingRevision: &expected}, "", "")
	if err != nil {
		return b, err
	}
	stored, _, err := s.store.GetArtifactV2Build(principal.AccountScopeID, working.ID, b.ID)
	return stored, err
}

type RecordValidationInput struct {
	RequestID, ArtifactID   string
	ExpectedWorkingRevision uint64
	Validation              pebblestore.ArtifactV2ValidationResult
}

func (s *Service) RecordValidation(ctx context.Context, principal Principal, input RecordValidationInput) (pebblestore.ArtifactV2ValidationResult, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return input.Validation, err
	}
	if working.CompositionHead == nil || input.ExpectedWorkingRevision != working.Revision {
		return input.Validation, errors.New("artifact v2 validation working state is stale or has no composition")
	}
	v := input.Validation
	v.ID, v.ArtifactID, v.CompositionID, v.CompositionDigest, v.PolicyRevision = trim(v.ID), working.ID, working.CompositionHead.CompositionID, working.CompositionHead.DigestSHA256, working.PolicyRevision
	if v.ID == "" {
		v.ID = deterministicID("valv2", working.ID, input.RequestID)
	}
	v.Revision = 1
	if v.CreatedAt == 0 {
		v.CreatedAt = s.now().UnixMilli()
	}
	build, ok, err := s.store.GetArtifactV2Build(principal.AccountScopeID, working.ID, v.BuildID)
	if err != nil || !ok {
		if err != nil {
			return v, err
		}
		return v, errors.New("artifact v2 validation build was not found")
	}
	if build.Status != pebblestore.ArtifactV2BuildSucceeded || build.CompositionID != v.CompositionID || build.CompositionDigest != v.CompositionDigest || build.PolicyRevision != v.PolicyRevision || v.CompilerVersion != build.CompilerVersion || v.TemplateVersion != build.TemplateVersion || !equalStrings(v.SourceDigests, build.SourceDigests) || !equalInts(v.RepresentativeTimestampsMS, build.RepresentativeTimestampsMS) || v.DurationMS != build.DurationMS || v.FPS != build.FPS || v.ValidatorVersion == "" || !validValidationStatus(v.Status) {
		return v, errors.New("artifact v2 validation evidence does not match the exact successful build")
	}
	next := working
	next.Revision, next.LatestValidationID = working.Revision+1, v.ID
	if v.Status == pebblestore.ArtifactV2ValidationValid {
		next.State = pebblestore.ArtifactV2StateReady
	} else if v.Status == pebblestore.ArtifactV2ValidationInvalid || v.Status == pebblestore.ArtifactV2ValidationFailedToRun {
		next.State = pebblestore.ArtifactV2StateInvalid
	} else {
		next.State = pebblestore.ArtifactV2StateValidating
	}
	if len(v.Diagnostics) > 0 {
		d := v.Diagnostics[0]
		next.LatestDiagnostic = &d
	}
	expected := working.Revision
	_, err = s.commitMutation(ctx, principal, input.RequestID, artifactV2ValidationEventKind(v.Status), next, pebblestore.ArtifactV2Mutation{Working: &next, Validation: &v, ExpectedWorkingRevision: &expected}, "", "")
	if err != nil {
		return v, err
	}
	stored, _, err := s.store.GetArtifactV2Validation(principal.AccountScopeID, working.ID, v.ID)
	return stored, err
}

type OpenIterationInput struct {
	RequestID, ArtifactID                        string
	ExpectedWorkingRevision, RequestedCandidates uint64
	TargetPartIDs                                []string
}

func (s *Service) OpenIteration(ctx context.Context, principal Principal, input OpenIterationInput) (pebblestore.ArtifactV2IterationRound, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2IterationRound{}, err
	}
	if input.ExpectedWorkingRevision != working.Revision || working.CompositionHead == nil || input.RequestedCandidates < 1 || input.RequestedCandidates > 16 || len(input.TargetPartIDs) == 0 {
		return pebblestore.ArtifactV2IterationRound{}, errors.New("artifact v2 iteration input is incomplete or stale")
	}
	seen := map[string]bool{}
	for i, id := range input.TargetPartIDs {
		id = trim(id)
		if id == "" || seen[id] {
			return pebblestore.ArtifactV2IterationRound{}, errors.New("artifact v2 iteration target set is invalid")
		}
		seen[id], input.TargetPartIDs[i] = true, id
		if _, ok, err := s.store.GetArtifactV2Part(principal.AccountScopeID, working.ID, id); err != nil || !ok {
			if err != nil {
				return pebblestore.ArtifactV2IterationRound{}, err
			}
			return pebblestore.ArtifactV2IterationRound{}, errors.New("artifact v2 iteration target was not found")
		}
	}
	id := deterministicID("roundv2", working.ID, input.RequestID)
	round := pebblestore.ArtifactV2IterationRound{SchemaVersion: pebblestore.ArtifactV2SchemaVersion, ID: id, ArtifactID: working.ID, BaseCompositionID: working.CompositionHead.CompositionID, BaseCompositionDigest: working.CompositionHead.DigestSHA256, TargetPartIDs: append([]string(nil), input.TargetPartIDs...), RequestedCandidates: int(input.RequestedCandidates), Status: pebblestore.ArtifactV2IterationOpen, Revision: 1, CreatedAt: s.now().UnixMilli(), UpdatedAt: s.now().UnixMilli()}
	next := working
	next.Revision, next.State, next.ActiveIterationID = working.Revision+1, pebblestore.ArtifactV2StateIterating, id
	expected := working.Revision
	_, err = s.commitMutation(ctx, principal, input.RequestID, pebblestore.V3SessionMutationArtifactV2IterationOpened, next, pebblestore.ArtifactV2Mutation{Working: &next, Iteration: &round, ExpectedWorkingRevision: &expected}, "", "")
	if err != nil {
		return round, err
	}
	stored, ok, err := s.store.GetArtifactV2Iteration(principal.AccountScopeID, working.ID, id)
	if err != nil || !ok {
		if err != nil {
			return round, err
		}
		return round, errors.New("artifact v2 iteration was not persisted")
	}
	return stored, nil
}

type AppendIterationCandidateInput struct {
	RequestID, ArtifactID, IterationID, SlotID         string
	ExpectedWorkingRevision, ExpectedIterationRevision uint64
	Composition                                        pebblestore.ArtifactV2Composition
}

func (s *Service) AppendIterationCandidate(ctx context.Context, principal Principal, input AppendIterationCandidateInput) (pebblestore.ArtifactV2IterationRound, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2IterationRound{}, err
	}
	round, ok, err := s.store.GetArtifactV2Iteration(principal.AccountScopeID, working.ID, trim(input.IterationID))
	if err != nil || !ok {
		return round, errors.New("artifact v2 iteration was not found")
	}
	if input.ExpectedWorkingRevision != working.Revision || input.ExpectedIterationRevision != round.Revision || round.Status == pebblestore.ArtifactV2IterationSelected || len(round.Candidates) >= round.RequestedCandidates {
		return round, errors.New("artifact v2 iteration candidate append is stale or closed")
	}
	for _, candidate := range round.Candidates {
		if candidate.SlotID == trim(input.SlotID) {
			return round, errors.New("artifact v2 iteration slot already has a candidate")
		}
	}
	candidate := input.Composition
	candidate.ID = deterministicID("compv2", working.ID, round.ID, input.SlotID, input.RequestID)
	candidate.ArtifactID, candidate.ParentCompositionID, candidate.PolicyRevision = working.ID, round.BaseCompositionID, working.PolicyRevision
	if candidate.ConstructionVersion == "" || len(candidate.Parts) == 0 {
		return round, errors.New("artifact v2 iteration candidate composition is incomplete")
	}
	candidate.DigestSHA256 = pebblestore.ArtifactV2CompositionDigest(candidate.PolicyRevision, candidate.ConstructionVersion, candidate.Parts)
	base, ok, err := s.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, round.BaseCompositionID)
	if err != nil || !ok {
		return round, errors.New("artifact v2 iteration base composition was not found")
	}
	targets, baseByPart := map[string]bool{}, map[string]pebblestore.ArtifactV2CompositionPart{}
	for _, id := range round.TargetPartIDs {
		targets[id] = true
	}
	for _, part := range base.Parts {
		baseByPart[part.PartID] = part
	}
	for _, part := range candidate.Parts {
		basePart, exists := baseByPart[part.PartID]
		if !exists || (!targets[part.PartID] && part != basePart) || (basePart.Locked && part != basePart) {
			return round, errors.New("artifact v2 iteration candidate changed a preserved or locked part")
		}
	}
	round.Candidates = append(round.Candidates, pebblestore.ArtifactV2IterationCandidate{SlotID: trim(input.SlotID), CompositionID: candidate.ID, Status: "ready"})
	round.Revision, round.UpdatedAt, round.Status = round.Revision+1, s.now().UnixMilli(), pebblestore.ArtifactV2IterationGenerating
	kind := pebblestore.V3SessionMutationArtifactV2IterationCandidateAppended
	if len(round.Candidates) == round.RequestedCandidates {
		round.Status, kind = pebblestore.ArtifactV2IterationAwaitingSelection, pebblestore.V3SessionMutationArtifactV2IterationAwaitingSelection
	}
	next := working
	next.Revision = working.Revision + 1
	expectedWorking, expectedRound := working.Revision, input.ExpectedIterationRevision
	_, err = s.commitMutation(ctx, principal, input.RequestID, kind, next, pebblestore.ArtifactV2Mutation{Working: &next, Iteration: &round, Composition: &candidate, ExpectedWorkingRevision: &expectedWorking, ExpectedIterationRevision: &expectedRound}, "", "")
	if err != nil {
		return round, err
	}
	stored, _, err := s.store.GetArtifactV2Iteration(principal.AccountScopeID, working.ID, round.ID)
	return stored, err
}

type SelectIterationCandidateInput struct {
	RequestID, ArtifactID, IterationID, SlotID         string
	ExpectedWorkingRevision, ExpectedIterationRevision uint64
}

func (s *Service) SelectIterationCandidate(ctx context.Context, principal Principal, input SelectIterationCandidateInput) (pebblestore.ArtifactV2Composition, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2Composition{}, err
	}
	round, ok, err := s.store.GetArtifactV2Iteration(principal.AccountScopeID, working.ID, trim(input.IterationID))
	if err != nil || !ok {
		return pebblestore.ArtifactV2Composition{}, errors.New("artifact v2 iteration was not found")
	}
	if input.ExpectedWorkingRevision != working.Revision || input.ExpectedIterationRevision != round.Revision || round.Status != pebblestore.ArtifactV2IterationAwaitingSelection {
		return pebblestore.ArtifactV2Composition{}, errors.New("artifact v2 iteration selection is stale or not ready")
	}
	var compositionID string
	for _, candidate := range round.Candidates {
		if candidate.SlotID == trim(input.SlotID) && candidate.Status == "ready" {
			compositionID = candidate.CompositionID
			break
		}
	}
	if compositionID == "" {
		return pebblestore.ArtifactV2Composition{}, errors.New("artifact v2 iteration candidate was not found")
	}
	composition, ok, err := s.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, compositionID)
	if err != nil || !ok {
		return composition, errors.New("artifact v2 iteration candidate composition was not found")
	}
	round.Status, round.SelectedSlotID, round.Revision, round.UpdatedAt = pebblestore.ArtifactV2IterationSelected, trim(input.SlotID), round.Revision+1, s.now().UnixMilli()
	next := working
	next.Revision, next.State, next.ActiveIterationID = working.Revision+1, pebblestore.ArtifactV2StateReady, ""
	headRevision := uint64(1)
	if working.CompositionHead != nil {
		headRevision = working.CompositionHead.HeadRevision + 1
	}
	next.CompositionHead = &pebblestore.ArtifactV2CompositionHead{CompositionID: composition.ID, HeadRevision: headRevision, DigestSHA256: composition.DigestSHA256}
	expectedWorking, expectedRound := working.Revision, input.ExpectedIterationRevision
	_, err = s.commitMutation(ctx, principal, input.RequestID, pebblestore.V3SessionMutationArtifactV2IterationSelected, next, pebblestore.ArtifactV2Mutation{Working: &next, Iteration: &round, ExpectedWorkingRevision: &expectedWorking, ExpectedIterationRevision: &expectedRound}, "", "")
	if err != nil {
		return composition, err
	}
	return composition, nil
}

type CreateDerivativeInput struct {
	RequestID, ArtifactID   string
	ExpectedWorkingRevision uint64
	Kind                    string
	Renderer                MotionRenderer
}

func (s *Service) CreateDerivative(ctx context.Context, principal Principal, input CreateDerivativeInput) (pebblestore.ArtifactV2Derivative, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2Derivative{}, err
	}
	if input.ExpectedWorkingRevision != working.Revision || working.CompositionHead == nil || working.LatestBuildID == "" || working.LatestValidationID == "" {
		return pebblestore.ArtifactV2Derivative{}, errors.New("artifact v2 derivative evidence is incomplete or stale")
	}
	if input.Kind != "preview" && input.Kind != "fallback" && input.Kind != "mp4" {
		return pebblestore.ArtifactV2Derivative{}, errors.New("artifact v2 derivative kind is not allowlisted")
	}
	build, ok, err := s.store.GetArtifactV2Build(principal.AccountScopeID, working.ID, working.LatestBuildID)
	if err != nil || !ok || build.Output == nil {
		return pebblestore.ArtifactV2Derivative{}, errors.New("artifact v2 derivative build is missing")
	}
	validation, ok, err := s.store.GetArtifactV2Validation(principal.AccountScopeID, working.ID, working.LatestValidationID)
	if err != nil || !ok || validation.Status != pebblestore.ArtifactV2ValidationValid || validation.BuildID != build.ID || validation.CompositionID != working.CompositionHead.CompositionID || validation.CompositionDigest != working.CompositionHead.DigestSHA256 || validation.PolicyRevision != working.PolicyRevision {
		return pebblestore.ArtifactV2Derivative{}, errors.New("artifact v2 derivative validation is stale or mismatched")
	}
	derivative := pebblestore.ArtifactV2Derivative{ID: deterministicID("deriv2", working.ID, input.RequestID), ArtifactID: working.ID, CompositionID: build.CompositionID, CompositionDigest: build.CompositionDigest, BuildID: build.ID, ValidationID: validation.ID, PolicyRevision: working.PolicyRevision, Kind: input.Kind, Status: "failed"}
	if existing, ok, err := s.store.GetArtifactV2Derivative(principal.AccountScopeID, working.ID, derivative.ID); err != nil {
		return derivative, err
	} else if ok {
		return existing, nil
	}
	source, err := s.blobs.GetExact(ctx, principal, *build.Output)
	if err != nil {
		return derivative, err
	}
	if input.Renderer == nil {
		return s.recordDerivative(ctx, principal, working, input.RequestID, derivative, []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("animation_renderer_unavailable", "infrastructure", "error", "infrastructure", "Trusted derivative rendering is unavailable.")})
	}
	var rendered MotionRenderResult
	if input.Kind == "mp4" {
		rendered, err = input.Renderer.Render(ctx, source, build.DurationMS, build.FPS)
	} else {
		rendered, err = input.Renderer.Preflight(ctx, source, build.DurationMS, build.FPS)
	}
	if err != nil {
		var captureErr *htmlcapture.Error
		code := "animation_renderer_failed"
		if errors.As(err, &captureErr) {
			code = captureErr.Code
		}
		return s.recordDerivative(ctx, principal, working, input.RequestID, derivative, []pebblestore.ArtifactV2Diagnostic{safeDiagnostic(code, motionFailurePhase(code), "error", motionRetryClass(code), motionSafeMessage(code))})
	}
	var output []byte
	mediaType := "image/png"
	if input.Kind == "mp4" {
		output, mediaType = rendered.MP4, "video/mp4"
	} else if input.Kind == "fallback" {
		if len(rendered.Frames) > 0 {
			output = rendered.Frames[0].PNG
		} else {
			output = rendered.PreviewPNG
		}
	} else {
		output = rendered.PreviewPNG
	}
	if len(output) == 0 {
		return s.recordDerivative(ctx, principal, working, input.RequestID, derivative, []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("animation_derivative_empty", "frame", "error", "infrastructure", "Trusted rendering returned no derivative bytes.")})
	}
	receipt, err := s.blobs.PutImmutable(ctx, principal, working.ID, "derivative-"+derivative.ID, mediaType, output)
	if err != nil {
		return derivative, err
	}
	derivative.Status, derivative.Output = "ready", &receipt
	return s.recordDerivative(ctx, principal, working, input.RequestID, derivative, nil)
}

func (s *Service) recordDerivative(ctx context.Context, principal Principal, working pebblestore.ArtifactV2WorkingArtifact, requestID string, derivative pebblestore.ArtifactV2Derivative, diagnostics []pebblestore.ArtifactV2Diagnostic) (pebblestore.ArtifactV2Derivative, error) {
	derivative.Diagnostics = safeDiagnostics(diagnostics)
	next := working
	next.Revision = working.Revision + 1
	expected := working.Revision
	kind := pebblestore.V3SessionMutationArtifactV2DerivativeCreated
	if derivative.Status == "failed" {
		kind = pebblestore.V3SessionMutationArtifactV2DerivativeFailed
	}
	_, err := s.commitMutation(ctx, principal, requestID+":derivative", kind, next, pebblestore.ArtifactV2Mutation{Working: &next, Derivative: &derivative, ExpectedWorkingRevision: &expected}, "", "")
	if err != nil {
		return derivative, err
	}
	stored, _, err := s.store.GetArtifactV2Derivative(principal.AccountScopeID, working.ID, derivative.ID)
	return stored, err
}

type PublishInput struct {
	RequestID, ArtifactID   string
	ExpectedWorkingRevision uint64
	AuthorizingActor        string
}

func (s *Service) Publish(ctx context.Context, principal Principal, input PublishInput) (pebblestore.ArtifactV2PublishedHead, error) {
	principal, working, err := s.load(principal, input.ArtifactID)
	if err != nil {
		return pebblestore.ArtifactV2PublishedHead{}, err
	}
	if input.ExpectedWorkingRevision != working.Revision || working.CompositionHead == nil || working.LatestBuildID == "" || working.LatestValidationID == "" {
		return pebblestore.ArtifactV2PublishedHead{}, errors.New("artifact v2 publication evidence is incomplete or stale")
	}
	build, ok, err := s.store.GetArtifactV2Build(principal.AccountScopeID, working.ID, working.LatestBuildID)
	if err != nil || !ok {
		return pebblestore.ArtifactV2PublishedHead{}, errors.New("artifact v2 publication build is missing")
	}
	validation, ok, err := s.store.GetArtifactV2Validation(principal.AccountScopeID, working.ID, working.LatestValidationID)
	if err != nil || !ok {
		return pebblestore.ArtifactV2PublishedHead{}, errors.New("artifact v2 publication validation is missing")
	}
	if build.Status != pebblestore.ArtifactV2BuildSucceeded || validation.Status != pebblestore.ArtifactV2ValidationValid || build.CompositionID != working.CompositionHead.CompositionID || validation.CompositionID != working.CompositionHead.CompositionID || build.CompositionDigest != working.CompositionHead.DigestSHA256 || validation.CompositionDigest != working.CompositionHead.DigestSHA256 || build.PolicyRevision != working.PolicyRevision || validation.PolicyRevision != working.PolicyRevision {
		return pebblestore.ArtifactV2PublishedHead{}, errors.New("artifact v2 publication evidence is stale or mismatched")
	}
	id := deterministicID("pubv2", working.ID, input.RequestID)
	p := pebblestore.ArtifactV2PublishedHead{SchemaVersion: pebblestore.ArtifactV2SchemaVersion, ID: id, ArtifactID: working.ID, CompositionID: working.CompositionHead.CompositionID, CompositionDigest: working.CompositionHead.DigestSHA256, BuildID: build.ID, ValidationID: validation.ID, PolicyRevision: working.PolicyRevision, AuthorizingActor: trim(input.AuthorizingActor), Revision: 1, CreatedAt: s.now().UnixMilli()}
	if working.PublishedHead != nil {
		p.PreviousHeadID = working.PublishedHead.PublishedHeadID
	}
	next := working
	next.Revision, next.State = working.Revision+1, pebblestore.ArtifactV2StatePublishedView
	next.PublishedHead = &pebblestore.ArtifactV2PublishedHeadReference{PublishedHeadID: p.ID, CompositionID: p.CompositionID, DigestSHA256: p.CompositionDigest}
	expected := working.Revision
	_, err = s.commitMutation(ctx, principal, input.RequestID, pebblestore.V3SessionMutationArtifactV2PublishedHeadCreated, next, pebblestore.ArtifactV2Mutation{Working: &next, PublishedHead: &p, ExpectedWorkingRevision: &expected}, "", "")
	if err != nil {
		return p, err
	}
	stored, _, err := s.store.GetArtifactV2PublishedHead(principal.AccountScopeID, working.ID, p.ID)
	return stored, err
}

func (s *Service) ReadBuildOutput(ctx context.Context, principal Principal, artifactID, buildID string) ([]byte, string, error) {
	principal, working, err := s.load(principal, artifactID)
	if err != nil {
		return nil, "", err
	}
	build, ok, err := s.store.GetArtifactV2Build(principal.AccountScopeID, working.ID, trim(buildID))
	if err != nil || !ok || build.Output == nil || build.ArtifactID != working.ID || build.PolicyRevision != working.PolicyRevision {
		return nil, "", errors.New("artifact v2 build output was not found")
	}
	body, err := s.blobs.GetExact(ctx, principal, *build.Output)
	if err != nil {
		return nil, "", err
	}
	return body, build.Output.MediaType, nil
}

func (s *Service) ReadReadyPart(ctx context.Context, principal Principal, ref ReadyReference, partID string) ([]byte, error) {
	principal, working, err := s.load(principal, ref.ArtifactID)
	if err != nil {
		return nil, err
	}
	resolved, err := s.ResolveReady(principal, ref.ArtifactID, ref.PublishedHeadID)
	if err != nil || resolved != ref {
		return nil, errors.New("artifact v2 ready reference is stale or mismatched")
	}
	composition, ok, err := s.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, ref.CompositionID)
	if err != nil || !ok {
		return nil, errors.New("artifact v2 ready composition was not found")
	}
	for _, selected := range composition.Parts {
		if selected.PartID != trim(partID) {
			continue
		}
		revision, ok, err := s.store.GetArtifactV2PartRevision(principal.AccountScopeID, working.ID, selected.PartID, selected.PartRevisionID)
		if err != nil || !ok || revision.Blob.DigestSHA256 != selected.DigestSHA256 {
			return nil, errors.New("artifact v2 ready part revision is missing or stale")
		}
		return s.blobs.GetExact(ctx, principal, revision.Blob)
	}
	return nil, errors.New("artifact v2 ready part was not found")
}

func (s *Service) ResolveReady(principal Principal, artifactID, publishedHeadID string) (ReadyReference, error) {
	principal, working, err := s.load(principal, artifactID)
	if err != nil {
		return ReadyReference{}, err
	}
	p, ok, err := s.store.GetArtifactV2PublishedHead(principal.AccountScopeID, working.ID, trim(publishedHeadID))
	if err != nil || !ok {
		return ReadyReference{}, errors.New("artifact v2 published head was not found")
	}
	v, ok, err := s.store.GetArtifactV2Validation(principal.AccountScopeID, working.ID, p.ValidationID)
	if err != nil || !ok || v.Status != pebblestore.ArtifactV2ValidationValid || v.CompositionDigest != p.CompositionDigest || v.PolicyRevision != p.PolicyRevision {
		return ReadyReference{}, errors.New("artifact v2 published head is not backed by matching valid evidence")
	}
	return ReadyReference{ArtifactID: working.ID, PublishedHeadID: p.ID, CompositionID: p.CompositionID, BuildID: p.BuildID, ValidationID: p.ValidationID, EventSeq: p.EventSeq, DigestSHA256: p.CompositionDigest, PolicyRevision: p.PolicyRevision}, nil
}

func (s *Service) owned(principal Principal) (Principal, error) {
	principal.AccountScopeID, principal.UserID, principal.SessionID = trim(principal.AccountScopeID), trim(principal.UserID), trim(principal.SessionID)
	if principal.AccountScopeID == "" || principal.UserID == "" || principal.SessionID == "" {
		return Principal{}, errors.New("artifact v2 trusted principal is incomplete")
	}
	session, ok, err := s.sessions.GetSession(principal.SessionID)
	if err != nil || !ok {
		if err != nil {
			return Principal{}, err
		}
		return Principal{}, errors.New("artifact v2 owner session was not found")
	}
	if session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
		return Principal{}, errors.New("artifact v2 owner session does not match trusted principal")
	}
	return principal, nil
}
func (s *Service) load(principal Principal, artifactID string) (Principal, pebblestore.ArtifactV2WorkingArtifact, error) {
	principal, err := s.owned(principal)
	if err != nil {
		return Principal{}, pebblestore.ArtifactV2WorkingArtifact{}, err
	}
	working, ok, err := s.store.GetArtifactV2Working(principal.AccountScopeID, trim(artifactID))
	if err != nil || !ok {
		if err != nil {
			return Principal{}, working, err
		}
		return Principal{}, working, errors.New("artifact v2 working artifact was not found")
	}
	if working.SessionID != principal.SessionID || working.UserID != principal.UserID {
		return Principal{}, working, errors.New("artifact v2 working artifact ownership does not match")
	}
	return principal, working, nil
}
func (s *Service) mustWorking(principal Principal, artifactID string) (pebblestore.ArtifactV2WorkingArtifact, error) {
	working, ok, err := s.store.GetArtifactV2Working(principal.AccountScopeID, artifactID)
	if err != nil || !ok {
		if err != nil {
			return working, err
		}
		return working, errors.New("artifact v2 working artifact was not persisted")
	}
	return working, nil
}
func (s *Service) commitMutation(_ context.Context, principal Principal, requestID, kind string, working pebblestore.ArtifactV2WorkingArtifact, mutation pebblestore.ArtifactV2Mutation, causationID, correlationID string) (pebblestore.V3SessionMutationResult, error) {
	payload, err := json.Marshal(mutation)
	if err != nil {
		return pebblestore.V3SessionMutationResult{}, err
	}
	sum := sha256.Sum256(payload)
	return s.commit.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: principal.SessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: trim(requestID), IdempotencyKey: trim(requestID), PayloadHash: hex.EncodeToString(sum[:]), Kind: kind, EventType: kind, CausationID: trim(causationID), CorrelationID: trim(correlationID), ArtifactV2: &mutation, NowUnixMs: s.now().UnixMilli()})
}
func artifactV2BuildEventKind(status string) string {
	switch status {
	case pebblestore.ArtifactV2BuildQueued:
		return pebblestore.V3SessionMutationArtifactV2BuildQueued
	case pebblestore.ArtifactV2BuildRunning:
		return pebblestore.V3SessionMutationArtifactV2BuildStarted
	case pebblestore.ArtifactV2BuildSucceeded:
		return pebblestore.V3SessionMutationArtifactV2BuildSucceeded
	case pebblestore.ArtifactV2BuildFailed:
		return pebblestore.V3SessionMutationArtifactV2BuildFailed
	default:
		return pebblestore.V3SessionMutationArtifactV2BuildCancelled
	}
}
func artifactV2ValidationEventKind(status string) string {
	switch status {
	case pebblestore.ArtifactV2ValidationQueued:
		return pebblestore.V3SessionMutationArtifactV2ValidationQueued
	case pebblestore.ArtifactV2ValidationRunning:
		return pebblestore.V3SessionMutationArtifactV2ValidationStarted
	case pebblestore.ArtifactV2ValidationValid:
		return pebblestore.V3SessionMutationArtifactV2ValidationValid
	case pebblestore.ArtifactV2ValidationInvalid:
		return pebblestore.V3SessionMutationArtifactV2ValidationInvalid
	case pebblestore.ArtifactV2ValidationFailedToRun:
		return pebblestore.V3SessionMutationArtifactV2ValidationFailedToRun
	default:
		return pebblestore.V3SessionMutationArtifactV2ValidationCancelled
	}
}
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
func validBuildStatus(status string) bool {
	switch status {
	case pebblestore.ArtifactV2BuildQueued, pebblestore.ArtifactV2BuildRunning, pebblestore.ArtifactV2BuildSucceeded, pebblestore.ArtifactV2BuildFailed, pebblestore.ArtifactV2BuildCancelled:
		return true
	}
	return false
}
func validValidationStatus(status string) bool {
	switch status {
	case pebblestore.ArtifactV2ValidationQueued, pebblestore.ArtifactV2ValidationRunning, pebblestore.ArtifactV2ValidationValid, pebblestore.ArtifactV2ValidationInvalid, pebblestore.ArtifactV2ValidationFailedToRun, pebblestore.ArtifactV2ValidationCancelled:
		return true
	}
	return false
}
func trim(value string) string { return strings.TrimSpace(value) }
func deterministicID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "_" + hex.EncodeToString(sum[:16])
}
func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate artifact v2 id: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// GitBlobStore is the audited private immutable-byte adapter. It imports only
// artifactgit and exposes no official/candidate/transaction ref operation.
type GitRepositoryOpener interface {
	Repository(ctx context.Context, repositoryID string) (*artifactgit.Repository, error)
}
type GitBlobStore struct{ opener GitRepositoryOpener }

func NewGitBlobStore(opener GitRepositoryOpener) *GitBlobStore { return &GitBlobStore{opener: opener} }
func (b *GitBlobStore) PutImmutable(ctx context.Context, principal Principal, artifactID, partID, mediaType string, body []byte) (pebblestore.ArtifactV2BlobReceipt, error) {
	repositoryID := deterministicID("artv2blob", principal.AccountScopeID, principal.SessionID, artifactID, partID, digest(body))
	repo, err := b.opener.Repository(ctx, repositoryID)
	if err != nil {
		return pebblestore.ArtifactV2BlobReceipt{}, err
	}
	commit, err := repo.Genesis(ctx, artifactgit.Genesis{MediaType: trim(mediaType), Content: &artifactgit.BlobInput{MediaType: trim(mediaType), Bytes: body}})
	if err != nil {
		return pebblestore.ArtifactV2BlobReceipt{}, err
	}
	blobOID, err := repo.ReadBlobOID(ctx, commit, "content")
	if err != nil {
		return pebblestore.ArtifactV2BlobReceipt{}, err
	}
	return pebblestore.ArtifactV2BlobReceipt{RepositoryID: repositoryID, CommitOID: commit, BlobOID: blobOID, DigestSHA256: digest(body), Size: int64(len(body)), MediaType: trim(mediaType)}, nil
}
func (b *GitBlobStore) GetExact(ctx context.Context, principal Principal, receipt pebblestore.ArtifactV2BlobReceipt) ([]byte, error) {
	repo, err := b.opener.Repository(ctx, receipt.RepositoryID)
	if err != nil {
		return nil, err
	}
	body, err := repo.ReadBlob(ctx, receipt.CommitOID, "content")
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != receipt.Size || digest(body) != receipt.DigestSHA256 {
		return nil, errors.New("artifact v2 blob receipt integrity mismatch")
	}
	return body, nil
}
func digest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
