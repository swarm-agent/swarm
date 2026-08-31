package artifactv2

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	AuthorCapabilityClass = "designer_author_v2"
	DefaultPolicyRevision = "artifact-v2-policy-1"
	DefaultConstruction   = "artifact-v2-compose-1"
	DefaultCompiler       = "artifact-v2-compiler-1"
	DefaultValidator      = "artifact-v2-validator-1"
	maxAuthorPartBytes    = 8 << 20
	maxAuthorParts        = 64
)

// PolicySnapshot is resolved by trusted orchestration and copied into a grant.
// No field in artifact_v2_author can replace it.
type PolicySnapshot struct {
	Revision         string `json:"revision"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	AspectRatio      string `json:"aspect_ratio,omitempty"`
	Orientation      string `json:"orientation,omitempty"`
	Preset           string `json:"preset,omitempty"`
	AnimationProfile string `json:"animation_profile,omitempty"`
	MaxPartBytes     int    `json:"max_part_bytes"`
	MaxParts         int    `json:"max_parts"`
}

// AuthorGrant is server-created run context. Durable destination, ownership,
// policy and candidate routing are intentionally absent from model arguments.
type AuthorGrant struct {
	ID                   string
	ArtifactID           string
	OwnerSessionID       string
	ProducerSessionID    string
	ProducerRunID        string
	TaskCallID           string
	IterationID          string
	CandidateSlotID      string
	AllowedActions       []string
	EditablePartIDs      []string
	AllowPartDeclaration bool
	ExpiresAt            int64
	Policy               PolicySnapshot
}

func (g AuthorGrant) Allows(action string) bool {
	for _, allowed := range g.AllowedActions {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(action)) {
			return true
		}
	}
	return false
}

func (g AuthorGrant) Editable(partID string) bool {
	if len(g.EditablePartIDs) == 0 {
		return true
	}
	for _, allowed := range g.EditablePartIDs {
		if strings.TrimSpace(allowed) == strings.TrimSpace(partID) {
			return true
		}
	}
	return false
}

type AuthorPartDeclaration struct {
	Key, Label, Role, MediaClass, LocatorKind, LocatorValue string
	Order                                                   int
}

type AuthorPartWrite struct {
	PartID, ExpectedBaseRevisionID, MediaType string
	ExpectedCompositionHeadRevision           uint64
	Body                                      []byte
}

type AuthorIterationTarget struct {
	PartID, Label string
}

type AuthorIterationCandidate struct {
	ArtifactID, SlotID string
}

type AuthorIterationContext struct {
	ArtifactID      string
	IterationID     string
	OwnerSessionID  string
	BaseComposition pebblestore.ArtifactV2Composition
	Targets         []AuthorIterationTarget
	CandidateCount  int
}

type AuthorContext struct {
	State            string                                 `json:"state"`
	Revision         uint64                                 `json:"revision"`
	CompositionHead  *pebblestore.ArtifactV2CompositionHead `json:"composition_head,omitempty"`
	Parts            []AuthorContextPart                    `json:"parts"`
	LatestDiagnostic *pebblestore.ArtifactV2Diagnostic      `json:"latest_diagnostic,omitempty"`
	EditablePartIDs  []string                               `json:"editable_part_ids,omitempty"`
	CanDeclareParts  bool                                   `json:"can_declare_parts"`
	Output           PolicySnapshot                         `json:"output"`
}

type AuthorContextPart struct {
	ID                string `json:"id"`
	Key               string `json:"key"`
	Label             string `json:"label"`
	Role              string `json:"role,omitempty"`
	MediaClass        string `json:"media_class"`
	CurrentRevisionID string `json:"current_revision_id,omitempty"`
	DigestSHA256      string `json:"digest_sha256,omitempty"`
	Locked            bool   `json:"locked,omitempty"`
}

type CompileInput struct {
	Artifact    pebblestore.ArtifactV2WorkingArtifact
	Composition pebblestore.ArtifactV2Composition
	Parts       []CompilePart
	Policy      PolicySnapshot
}

type CompilePart struct {
	Definition pebblestore.ArtifactV2Part
	Revision   pebblestore.ArtifactV2PartRevision
	Body       []byte
}

type CompileProduct struct {
	Bytes                      []byte
	MediaType                  string
	CompilerVersion            string
	TemplateVersion            string
	DurationMS                 int
	FPS                        int
	RepresentativeTimestampsMS []int
	Diagnostics                []pebblestore.ArtifactV2Diagnostic
}

type ValidationInput struct {
	Artifact    pebblestore.ArtifactV2WorkingArtifact
	Composition pebblestore.ArtifactV2Composition
	Build       pebblestore.ArtifactV2BuildResult
	Product     CompileProduct
	Parts       []CompilePart
	Policy      PolicySnapshot
}

type ValidationProduct struct {
	Status           string
	ValidatorVersion string
	RendererSnapshot string
	Diagnostics      []pebblestore.ArtifactV2Diagnostic
	EvidenceDigests  []string
}

type Compiler interface {
	Compile(context.Context, CompileInput) (CompileProduct, error)
}

type Validator interface {
	Validate(context.Context, ValidationInput) (ValidationProduct, error)
}

// AuthorService is the only model-facing application capability. It delegates
// durable domain changes to Service and never exposes publication or selection.
type AuthorService struct {
	core      *Service
	compiler  Compiler
	validator Validator
	now       func() time.Time
}

func (s *AuthorService) AllocateWorking(ctx context.Context, principal Principal, requestID, kind, intentReference string, policy PolicySnapshot) (pebblestore.ArtifactV2WorkingArtifact, error) {
	if s == nil || s.core == nil {
		return pebblestore.ArtifactV2WorkingArtifact{}, errors.New("artifact v2 author service is not configured")
	}
	policy = normalizedPolicy(policy)
	return s.core.CreateWorking(ctx, principal, CreateWorkingInput{RequestID: strings.TrimSpace(requestID), ArtifactKind: firstNonEmpty(kind, "creative"), PolicyRevision: policy.Revision, CapabilityClass: AuthorCapabilityClass, IntentReference: strings.TrimSpace(intentReference)})
}

func NewAuthorService(core *Service, compiler Compiler, validator Validator) *AuthorService {
	if compiler == nil {
		compiler = DeterministicCompiler{}
	}
	if validator == nil {
		validator = DeterministicValidator{}
	}
	return &AuthorService{core: core, compiler: compiler, validator: validator, now: time.Now}
}

func (s *AuthorService) PrepareIteration(ctx context.Context, principal Principal, requestID, artifactID string, expectedWorkingRevision, expectedHeadRevision uint64, targets []AuthorIterationTarget, candidateCount int) (AuthorIterationContext, error) {
	if s == nil || s.core == nil {
		return AuthorIterationContext{}, errors.New("artifact v2 author service is not configured")
	}
	principal, working, err := s.core.load(principal, artifactID)
	if err != nil {
		return AuthorIterationContext{}, err
	}
	if working.Revision != expectedWorkingRevision || working.CompositionHead == nil || working.CompositionHead.HeadRevision != expectedHeadRevision || working.State != pebblestore.ArtifactV2StatePublishedView || working.PublishedHead == nil {
		return AuthorIterationContext{}, errors.New("artifact v2 iteration source is stale or unpublished")
	}
	base, ok, err := s.core.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, working.CompositionHead.CompositionID)
	if err != nil || !ok || base.DigestSHA256 != working.CompositionHead.DigestSHA256 {
		return AuthorIterationContext{}, errors.New("artifact v2 iteration base composition is unavailable")
	}
	if candidateCount < 1 || candidateCount > 16 || len(targets) == 0 {
		return AuthorIterationContext{}, errors.New("artifact v2 iteration targets or candidate count are invalid")
	}
	partIDs := make([]string, 0, len(targets))
	seen := map[string]bool{}
	for i := range targets {
		targets[i].PartID = strings.TrimSpace(targets[i].PartID)
		targets[i].Label = strings.TrimSpace(targets[i].Label)
		if targets[i].PartID == "" || seen[targets[i].PartID] {
			return AuthorIterationContext{}, errors.New("artifact v2 iteration target set is invalid")
		}
		seen[targets[i].PartID] = true
		if part, found, readErr := s.core.store.GetArtifactV2Part(principal.AccountScopeID, working.ID, targets[i].PartID); readErr != nil || !found {
			return AuthorIterationContext{}, errors.New("artifact v2 iteration target was not found")
		} else if targets[i].Label == "" {
			targets[i].Label = part.Label
		}
		partIDs = append(partIDs, targets[i].PartID)
	}
	round, err := s.core.OpenIteration(ctx, principal, OpenIterationInput{RequestID: strings.TrimSpace(requestID), ArtifactID: working.ID, ExpectedWorkingRevision: working.Revision, RequestedCandidates: uint64(candidateCount), TargetPartIDs: partIDs})
	if err != nil {
		return AuthorIterationContext{}, err
	}
	return AuthorIterationContext{ArtifactID: working.ID, IterationID: round.ID, OwnerSessionID: working.SessionID, BaseComposition: base, Targets: append([]AuthorIterationTarget(nil), targets...), CandidateCount: candidateCount}, nil
}

func (s *AuthorService) AllocateIterationCandidate(ctx context.Context, principal Principal, requestID, intentReference string, iteration AuthorIterationContext, slotIndex int, policy PolicySnapshot) (pebblestore.ArtifactV2WorkingArtifact, error) {
	if slotIndex < 1 || slotIndex > iteration.CandidateCount {
		return pebblestore.ArtifactV2WorkingArtifact{}, errors.New("artifact v2 iteration candidate slot is invalid")
	}
	return s.AllocateWorking(ctx, principal, strings.TrimSpace(requestID), "managed_creative_candidate", intentReference, policy)
}

func (s *AuthorService) FinalizeIterationCandidate(ctx context.Context, principal Principal, iteration AuthorIterationContext, candidate AuthorIterationCandidate, requestID string) error {
	principal, err := s.core.owned(principal)
	if err != nil {
		return err
	}
	working, ok, err := s.core.store.GetArtifactV2Working(principal.AccountScopeID, strings.TrimSpace(candidate.ArtifactID))
	if err != nil || !ok || working.UserID != principal.UserID || working.SessionID != iteration.OwnerSessionID || working.State != pebblestore.ArtifactV2StatePublishedView || working.CompositionHead == nil {
		return errors.New("artifact v2 iteration candidate is not an exact published owned composition")
	}
	candidateComposition, ok, err := s.core.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, working.CompositionHead.CompositionID)
	if err != nil || !ok {
		return errors.New("artifact v2 iteration candidate composition is unavailable")
	}
	baseParts := make(map[string]pebblestore.ArtifactV2CompositionPart, len(iteration.BaseComposition.Parts))
	candidateParts := make(map[string]pebblestore.ArtifactV2CompositionPart, len(candidateComposition.Parts))
	for _, selected := range iteration.BaseComposition.Parts {
		part, found, readErr := s.core.store.GetArtifactV2Part(principal.AccountScopeID, iteration.ArtifactID, selected.PartID)
		if readErr != nil || !found {
			return errors.New("artifact v2 iteration base part definition is unavailable")
		}
		baseParts[part.Key] = selected
	}
	for _, selected := range candidateComposition.Parts {
		part, found, readErr := s.core.store.GetArtifactV2Part(principal.AccountScopeID, working.ID, selected.PartID)
		if readErr != nil || !found {
			return errors.New("artifact v2 iteration candidate part definition is unavailable")
		}
		candidateParts[part.Key] = selected
	}
	if len(baseParts) != len(candidateParts) {
		return errors.New("artifact v2 iteration candidate did not preserve the complete part set")
	}
	targets := map[string]bool{}
	for _, target := range iteration.Targets {
		targets[target.PartID] = true
	}
	mergedParts := append([]pebblestore.ArtifactV2CompositionPart(nil), iteration.BaseComposition.Parts...)
	imported := make([]pebblestore.ArtifactV2PartRevision, 0, len(targets))
	for index := range mergedParts {
		basePart := mergedParts[index]
		baseDefinition, found, readErr := s.core.store.GetArtifactV2Part(principal.AccountScopeID, iteration.ArtifactID, basePart.PartID)
		if readErr != nil || !found {
			return errors.New("artifact v2 iteration base part definition is unavailable")
		}
		candidatePart, exists := candidateParts[baseDefinition.Key]
		if !exists {
			return errors.New("artifact v2 iteration candidate omitted a required part")
		}
		if !targets[basePart.PartID] {
			if candidatePart.DigestSHA256 != basePart.DigestSHA256 || candidatePart.Locked != basePart.Locked {
				return errors.New("artifact v2 iteration candidate changed a preserved part")
			}
			continue
		}
		if basePart.Locked {
			return errors.New("artifact v2 iteration target is locked")
		}
		sourceRevision, found, readErr := s.core.store.GetArtifactV2PartRevision(principal.AccountScopeID, working.ID, candidatePart.PartID, candidatePart.PartRevisionID)
		if readErr != nil || !found || sourceRevision.Blob.DigestSHA256 != candidatePart.DigestSHA256 || sourceRevision.Blob.DigestSHA256 == basePart.DigestSHA256 {
			return errors.New("artifact v2 iteration candidate target revision is missing, stale, or unchanged")
		}
		importID := deterministicID("prev2", iteration.ArtifactID, iteration.IterationID, candidate.SlotID, basePart.PartID)
		imported = append(imported, pebblestore.ArtifactV2PartRevision{ID: importID, ArtifactID: iteration.ArtifactID, PartID: basePart.PartID, ParentRevisionID: basePart.PartRevisionID, ProducerRunID: sourceRevision.ProducerRunID, CapabilityGrant: sourceRevision.CapabilityGrant, Blob: sourceRevision.Blob})
		mergedParts[index] = pebblestore.ArtifactV2CompositionPart{PartID: basePart.PartID, PartRevisionID: importID, DigestSHA256: sourceRevision.Blob.DigestSHA256}
	}
	owner, found, err := s.core.store.GetArtifactV2Working(principal.AccountScopeID, iteration.ArtifactID)
	if err != nil || !found || owner.SessionID != iteration.OwnerSessionID || owner.UserID != principal.UserID || owner.ActiveIterationID != iteration.IterationID {
		return errors.New("artifact v2 iteration owner is stale or unavailable")
	}
	round, found, err := s.core.store.GetArtifactV2Iteration(principal.AccountScopeID, iteration.ArtifactID, iteration.IterationID)
	if err != nil || !found {
		return errors.New("artifact v2 iteration round is unavailable")
	}
	candidateRecord := pebblestore.ArtifactV2Composition{ConstructionVersion: iteration.BaseComposition.ConstructionVersion, Parts: mergedParts}
	candidateRecord.ID = deterministicID("compv2", owner.ID, round.ID, candidate.SlotID, requestID)
	candidateRecord.ArtifactID, candidateRecord.ParentCompositionID, candidateRecord.PolicyRevision = owner.ID, round.BaseCompositionID, owner.PolicyRevision
	candidateRecord.DigestSHA256 = pebblestore.ArtifactV2CompositionDigest(candidateRecord.PolicyRevision, candidateRecord.ConstructionVersion, candidateRecord.Parts)
	round.Candidates = append(round.Candidates, pebblestore.ArtifactV2IterationCandidate{SlotID: strings.TrimSpace(candidate.SlotID), CompositionID: candidateRecord.ID, Status: "ready"})
	round.Revision, round.UpdatedAt, round.Status = round.Revision+1, s.now().UnixMilli(), pebblestore.ArtifactV2IterationGenerating
	kind := pebblestore.V3SessionMutationArtifactV2IterationCandidateAppended
	if len(round.Candidates) == round.RequestedCandidates {
		round.Status, kind = pebblestore.ArtifactV2IterationAwaitingSelection, pebblestore.V3SessionMutationArtifactV2IterationAwaitingSelection
	}
	next := owner
	next.Revision = owner.Revision + 1
	expectedWorking, expectedRound := owner.Revision, round.Revision-1
	_, err = s.core.commitMutation(ctx, principal, strings.TrimSpace(requestID), kind, next, pebblestore.ArtifactV2Mutation{Working: &next, PartRevisions: imported, Composition: &candidateRecord, Iteration: &round, ExpectedWorkingRevision: &expectedWorking, ExpectedIterationRevision: &expectedRound}, "", iteration.IterationID)
	return err
}

func (s *AuthorService) Inspect(principal Principal, grant AuthorGrant) (AuthorContext, error) {
	principal, working, err := s.authorized(principal, grant, "inspect_context")
	if err != nil {
		return AuthorContext{}, err
	}
	parts, err := s.core.store.ListArtifactV2Parts(principal.AccountScopeID, working.ID, maxAuthorParts)
	if err != nil {
		return AuthorContext{}, err
	}
	current := map[string]pebblestore.ArtifactV2CompositionPart{}
	if working.CompositionHead != nil {
		composition, ok, err := s.core.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, working.CompositionHead.CompositionID)
		if err != nil || !ok {
			return AuthorContext{}, errors.New("artifact v2 author context composition is unavailable")
		}
		for _, selected := range composition.Parts {
			current[selected.PartID] = selected
		}
	}
	out := AuthorContext{State: working.State, Revision: working.Revision, CompositionHead: cloneCompositionHead(working.CompositionHead), LatestDiagnostic: cloneDiagnostic(working.LatestDiagnostic), EditablePartIDs: append([]string(nil), grant.EditablePartIDs...), CanDeclareParts: grant.AllowPartDeclaration, Output: normalizedPolicy(grant.Policy)}
	for _, part := range parts {
		entry := AuthorContextPart{ID: part.ID, Key: part.Key, Label: part.Label, Role: part.Role, MediaClass: part.MediaClass}
		if selected, ok := current[part.ID]; ok {
			entry.CurrentRevisionID, entry.DigestSHA256, entry.Locked = selected.PartRevisionID, selected.DigestSHA256, selected.Locked
		} else if revisions, err := s.core.store.ListArtifactV2PartRevisions(principal.AccountScopeID, working.ID, part.ID, 256); err != nil {
			return AuthorContext{}, err
		} else if len(revisions) != 0 {
			latest := revisions[len(revisions)-1]
			entry.CurrentRevisionID, entry.DigestSHA256 = latest.ID, latest.Blob.DigestSHA256
		}
		out.Parts = append(out.Parts, entry)
	}
	return out, nil
}

func (s *AuthorService) DeclareParts(ctx context.Context, principal Principal, grant AuthorGrant, requestID string, declarations []AuthorPartDeclaration) (AuthorContext, error) {
	principal, working, err := s.authorized(principal, grant, "declare_parts")
	if err != nil {
		return AuthorContext{}, err
	}
	if !grant.AllowPartDeclaration || len(declarations) == 0 || len(declarations) > normalizedPolicy(grant.Policy).MaxParts {
		return AuthorContext{}, errors.New("artifact v2 author part declaration is not permitted or is out of bounds")
	}
	existing, err := s.core.store.ListArtifactV2Parts(principal.AccountScopeID, working.ID, maxAuthorParts)
	if err != nil {
		return AuthorContext{}, err
	}
	if len(existing) != 0 {
		return AuthorContext{}, errors.New("artifact v2 author part declaration is already complete")
	}
	for index, declaration := range declarations {
		part, err := s.core.DeclarePart(ctx, principal, DeclarePartInput{RequestID: fmt.Sprintf("%s:part:%d", strings.TrimSpace(requestID), index+1), ArtifactID: working.ID, ExpectedRevision: working.Revision, Key: declaration.Key, Label: declaration.Label, Role: declaration.Role, MediaClass: declaration.MediaClass, LocatorKind: declaration.LocatorKind, LocatorValue: declaration.LocatorValue, Order: declaration.Order})
		if err != nil {
			return AuthorContext{}, err
		}
		_ = part
		working, err = s.core.mustWorking(principal, working.ID)
		if err != nil {
			return AuthorContext{}, err
		}
	}
	return s.Inspect(principal, grant)
}

func (s *AuthorService) WritePart(ctx context.Context, principal Principal, grant AuthorGrant, requestID string, write AuthorPartWrite) (AuthorContext, error) {
	principal, working, err := s.authorized(principal, grant, "write_part")
	if err != nil {
		return AuthorContext{}, err
	}
	if !grant.Editable(write.PartID) {
		return AuthorContext{}, errors.New("artifact v2 author part is outside the editable target set")
	}
	actualHeadRevision := uint64(0)
	if working.CompositionHead != nil {
		actualHeadRevision = working.CompositionHead.HeadRevision
	}
	if write.ExpectedCompositionHeadRevision != actualHeadRevision {
		return AuthorContext{}, errors.New("artifact v2 author composition head is stale")
	}
	policy := normalizedPolicy(grant.Policy)
	if len(write.Body) == 0 || len(write.Body) > policy.MaxPartBytes {
		return AuthorContext{}, errors.New("artifact v2 author part bytes are empty or exceed the capability bound")
	}
	baseID, locked, err := s.currentPartRevision(principal, working, write.PartID)
	if err != nil {
		return AuthorContext{}, err
	}
	if locked {
		return AuthorContext{}, errors.New("artifact v2 author cannot replace a locked part")
	}
	if strings.TrimSpace(write.ExpectedBaseRevisionID) != baseID {
		return AuthorContext{}, errors.New("artifact v2 author base part revision is stale")
	}
	revision, err := s.core.AppendPartRevision(ctx, principal, AppendPartRevisionInput{RequestID: strings.TrimSpace(requestID) + ":bytes", ArtifactID: working.ID, PartID: write.PartID, ExpectedWorkingRevision: working.Revision, ExpectedBaseRevisionID: baseID, CapabilityGrantID: grant.ID, MediaType: write.MediaType, Body: append([]byte(nil), write.Body...)})
	if err != nil {
		return AuthorContext{}, err
	}
	working, err = s.core.mustWorking(principal, working.ID)
	if err != nil {
		return AuthorContext{}, err
	}
	if err := s.advanceFromLatest(ctx, principal, grant, strings.TrimSpace(requestID)+":composition", working, revision.PartID, revision.ID); err != nil {
		return AuthorContext{}, err
	}
	return s.Inspect(principal, grant)
}

func (s *AuthorService) RequestBuild(ctx context.Context, principal Principal, grant AuthorGrant, requestID string) (AuthorCandidateReference, error) {
	principal, working, err := s.authorized(principal, grant, "request_build")
	if err != nil {
		return AuthorCandidateReference{}, err
	}
	if working.CompositionHead == nil {
		return AuthorCandidateReference{}, errors.New("artifact v2 author build requires a complete composition")
	}
	composition, ok, err := s.core.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, working.CompositionHead.CompositionID)
	if err != nil || !ok {
		return AuthorCandidateReference{}, errors.New("artifact v2 author composition was not found")
	}
	compileInput := CompileInput{Artifact: working, Composition: composition, Policy: normalizedPolicy(grant.Policy)}
	for _, selected := range composition.Parts {
		part, ok, err := s.core.store.GetArtifactV2Part(principal.AccountScopeID, working.ID, selected.PartID)
		if err != nil || !ok {
			return AuthorCandidateReference{}, errors.New("artifact v2 author build part was not found")
		}
		revision, ok, err := s.core.store.GetArtifactV2PartRevision(principal.AccountScopeID, working.ID, selected.PartID, selected.PartRevisionID)
		if err != nil || !ok || revision.Blob.DigestSHA256 != selected.DigestSHA256 {
			return AuthorCandidateReference{}, errors.New("artifact v2 author build part revision is stale")
		}
		body, err := s.core.blobs.GetExact(ctx, principal, revision.Blob)
		if err != nil {
			return AuthorCandidateReference{}, err
		}
		compileInput.Parts = append(compileInput.Parts, CompilePart{Definition: part, Revision: revision, Body: body})
	}
	product, compileErr := s.compiler.Compile(ctx, compileInput)
	product.Diagnostics = safeDiagnostics(product.Diagnostics)
	buildStatus := pebblestore.ArtifactV2BuildSucceeded
	if compileErr != nil || len(product.Bytes) == 0 {
		buildStatus = pebblestore.ArtifactV2BuildFailed
		if len(product.Diagnostics) == 0 {
			product.Diagnostics = []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("compile_failed", "compile", "error", "infrastructure", "The server compiler could not build this exact composition.")}
		}
	}
	build := pebblestore.ArtifactV2BuildResult{CompilerVersion: firstNonEmpty(product.CompilerVersion, DefaultCompiler), TemplateVersion: product.TemplateVersion, SourceDigests: sourceDigests(compileInput.Parts), RepresentativeTimestampsMS: append([]int(nil), product.RepresentativeTimestampsMS...), DurationMS: product.DurationMS, FPS: product.FPS, Status: buildStatus, Diagnostics: product.Diagnostics, CompletedAt: s.now().UnixMilli()}
	if buildStatus == pebblestore.ArtifactV2BuildSucceeded {
		receipt, err := s.core.blobs.PutImmutable(ctx, principal, working.ID, "compiled-output", firstNonEmpty(product.MediaType, "application/octet-stream"), product.Bytes)
		if err != nil {
			return AuthorCandidateReference{}, err
		}
		build.Output = &receipt
		build.OutputDigestSHA256 = receipt.DigestSHA256
	}
	storedBuild, err := s.core.RecordBuild(ctx, principal, RecordBuildInput{RequestID: strings.TrimSpace(requestID) + ":build", ArtifactID: working.ID, ExpectedWorkingRevision: working.Revision, Build: build})
	if err != nil {
		return AuthorCandidateReference{}, err
	}
	if storedBuild.Status != pebblestore.ArtifactV2BuildSucceeded {
		ref, refErr := s.candidateReference(principal, working.ID)
		if refErr == nil && len(storedBuild.Diagnostics) != 0 {
			ref.Diagnostic = cloneDiagnostic(&storedBuild.Diagnostics[0])
		}
		return ref, refErr
	}
	working, err = s.core.mustWorking(principal, working.ID)
	if err != nil {
		return AuthorCandidateReference{}, err
	}
	validationProduct, validationErr := s.validator.Validate(ctx, ValidationInput{Artifact: working, Composition: composition, Build: storedBuild, Product: product, Parts: append([]CompilePart(nil), compileInput.Parts...), Policy: normalizedPolicy(grant.Policy)})
	validationProduct.Diagnostics = safeDiagnostics(validationProduct.Diagnostics)
	if validationProduct.ValidatorVersion == "" {
		validationProduct.ValidatorVersion = DefaultValidator
	}
	if validationErr != nil {
		validationProduct.Status = pebblestore.ArtifactV2ValidationFailedToRun
		if len(validationProduct.Diagnostics) == 0 {
			validationProduct.Diagnostics = []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("validation_unavailable", "policy", "error", "infrastructure", "Validation could not run for this exact build.")}
		}
	}
	if validationProduct.Status == "" {
		validationProduct.Status = pebblestore.ArtifactV2ValidationValid
	}
	validation := pebblestore.ArtifactV2ValidationResult{BuildID: storedBuild.ID, ValidatorVersion: validationProduct.ValidatorVersion, CompilerVersion: storedBuild.CompilerVersion, TemplateVersion: storedBuild.TemplateVersion, SourceDigests: append([]string(nil), storedBuild.SourceDigests...), RepresentativeTimestampsMS: append([]int(nil), storedBuild.RepresentativeTimestampsMS...), DurationMS: storedBuild.DurationMS, FPS: storedBuild.FPS, RendererSnapshot: validationProduct.RendererSnapshot, Status: validationProduct.Status, Diagnostics: validationProduct.Diagnostics, EvidenceDigests: append([]string(nil), validationProduct.EvidenceDigests...), CompletedAt: s.now().UnixMilli()}
	if _, err := s.core.RecordValidation(ctx, principal, RecordValidationInput{RequestID: strings.TrimSpace(requestID) + ":validation", ArtifactID: working.ID, ExpectedWorkingRevision: working.Revision, Validation: validation}); err != nil {
		return AuthorCandidateReference{}, err
	}
	return s.candidateReference(principal, working.ID)
}

type AuthorCandidateReference struct {
	ArtifactID      string                                 `json:"artifact_id"`
	State           string                                 `json:"state"`
	Revision        uint64                                 `json:"revision"`
	CompositionHead *pebblestore.ArtifactV2CompositionHead `json:"composition_head,omitempty"`
	BuildID         string                                 `json:"build_id,omitempty"`
	ValidationID    string                                 `json:"validation_id,omitempty"`
	PublishedHeadID string                                 `json:"published_head_id,omitempty"`
	Diagnostic      *pebblestore.ArtifactV2Diagnostic      `json:"diagnostic,omitempty"`
}

func (s *AuthorService) SubmitCandidate(ctx context.Context, principal Principal, grant AuthorGrant, requestID string) (AuthorCandidateReference, error) {
	principal, working, err := s.authorized(principal, grant, "submit_candidate")
	if err != nil {
		return AuthorCandidateReference{}, err
	}
	ref, err := s.candidateReference(principal, working.ID)
	if err != nil {
		return ref, err
	}
	if ref.State == pebblestore.ArtifactV2StatePublishedView && ref.PublishedHeadID != "" {
		return ref, nil
	}
	if ref.State != pebblestore.ArtifactV2StateReady || ref.CompositionHead == nil || ref.BuildID == "" || ref.ValidationID == "" {
		return ref, errors.New("artifact v2 candidate is not backed by exact valid build evidence")
	}
	published, err := s.core.Publish(ctx, principal, PublishInput{RequestID: strings.TrimSpace(requestID) + ":publish", ArtifactID: working.ID, ExpectedWorkingRevision: working.Revision, AuthorizingActor: "designer_submission"})
	if err != nil {
		return ref, err
	}
	ref, err = s.candidateReference(principal, working.ID)
	if err == nil {
		ref.PublishedHeadID = published.ID
	}
	return ref, err
}

func (s *AuthorService) MarkFailed(ctx context.Context, principal Principal, grant AuthorGrant, requestID, code, message string) error {
	principal, working, err := s.authorizedAny(principal, grant)
	if err != nil {
		return err
	}
	diagnostic := safeDiagnostic(code, "policy", "error", "repairable", message)
	next := working
	next.State, next.Revision, next.LatestDiagnostic = pebblestore.ArtifactV2StateInvalid, working.Revision+1, &diagnostic
	expected := working.Revision
	_, err = s.core.commitMutation(ctx, principal, requestID, pebblestore.V3SessionMutationArtifactV2CandidateFailed, next, pebblestore.ArtifactV2Mutation{Working: &next, ExpectedWorkingRevision: &expected}, "", grant.TaskCallID)
	return err
}

func (s *AuthorService) candidateReference(principal Principal, artifactID string) (AuthorCandidateReference, error) {
	working, err := s.core.mustWorking(principal, artifactID)
	if err != nil {
		return AuthorCandidateReference{}, err
	}
	ref := AuthorCandidateReference{ArtifactID: working.ID, State: working.State, Revision: working.Revision, CompositionHead: cloneCompositionHead(working.CompositionHead), BuildID: working.LatestBuildID, ValidationID: working.LatestValidationID, Diagnostic: cloneDiagnostic(working.LatestDiagnostic)}
	if working.PublishedHead != nil {
		ref.PublishedHeadID = working.PublishedHead.PublishedHeadID
	}
	return ref, nil
}

func (s *AuthorService) authorized(principal Principal, grant AuthorGrant, action string) (Principal, pebblestore.ArtifactV2WorkingArtifact, error) {
	principal, working, err := s.authorizedAny(principal, grant)
	if err != nil {
		return Principal{}, working, err
	}
	if !grant.Allows(action) {
		return Principal{}, working, errors.New("artifact v2 author action is not granted")
	}
	return principal, working, nil
}

func (s *AuthorService) authorizedAny(principal Principal, grant AuthorGrant) (Principal, pebblestore.ArtifactV2WorkingArtifact, error) {
	if s == nil || s.core == nil || strings.TrimSpace(grant.ID) == "" || strings.TrimSpace(grant.ArtifactID) == "" || strings.TrimSpace(grant.OwnerSessionID) == "" || strings.TrimSpace(grant.ProducerSessionID) == "" || strings.TrimSpace(principal.RunID) == "" || strings.TrimSpace(grant.ProducerRunID) != strings.TrimSpace(principal.RunID) || strings.TrimSpace(principal.SessionID) != strings.TrimSpace(grant.OwnerSessionID) || strings.TrimSpace(principal.ActorClass) != "designer" {
		return Principal{}, pebblestore.ArtifactV2WorkingArtifact{}, errors.New("artifact v2 author capability context is incomplete or mismatched")
	}
	if grant.ExpiresAt <= s.now().UnixMilli() {
		return Principal{}, pebblestore.ArtifactV2WorkingArtifact{}, errors.New("artifact v2 author capability has expired")
	}
	principal, working, err := s.core.load(principal, grant.ArtifactID)
	if err != nil {
		return Principal{}, working, err
	}
	if working.CapabilityClass != AuthorCapabilityClass || working.PolicyRevision != normalizedPolicy(grant.Policy).Revision {
		return Principal{}, working, errors.New("artifact v2 author capability policy does not match the working artifact")
	}
	return principal, working, nil
}

func (s *AuthorService) currentPartRevision(principal Principal, working pebblestore.ArtifactV2WorkingArtifact, partID string) (string, bool, error) {
	if _, ok, err := s.core.store.GetArtifactV2Part(principal.AccountScopeID, working.ID, strings.TrimSpace(partID)); err != nil || !ok {
		return "", false, errors.New("artifact v2 author part was not found")
	}
	if working.CompositionHead == nil {
		revisions, err := s.core.store.ListArtifactV2PartRevisions(principal.AccountScopeID, working.ID, strings.TrimSpace(partID), 256)
		if err != nil {
			return "", false, err
		}
		if len(revisions) != 0 {
			return revisions[len(revisions)-1].ID, false, nil
		}
		return "", false, nil
	}
	composition, ok, err := s.core.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, working.CompositionHead.CompositionID)
	if err != nil || !ok {
		return "", false, errors.New("artifact v2 author current composition was not found")
	}
	for _, selected := range composition.Parts {
		if selected.PartID == strings.TrimSpace(partID) {
			return selected.PartRevisionID, selected.Locked, nil
		}
	}
	return "", false, nil
}

func (s *AuthorService) advanceFromLatest(ctx context.Context, principal Principal, grant AuthorGrant, requestID string, working pebblestore.ArtifactV2WorkingArtifact, changedPartID, changedRevisionID string) error {
	parts, err := s.core.store.ListArtifactV2Parts(principal.AccountScopeID, working.ID, normalizedPolicy(grant.Policy).MaxParts)
	if err != nil {
		return err
	}
	selected := map[string]pebblestore.ArtifactV2CompositionPart{}
	headRevision := uint64(0)
	if working.CompositionHead != nil {
		headRevision = working.CompositionHead.HeadRevision
		composition, ok, err := s.core.store.GetArtifactV2Composition(principal.AccountScopeID, working.ID, working.CompositionHead.CompositionID)
		if err != nil || !ok {
			return errors.New("artifact v2 author current composition was not found")
		}
		for _, item := range composition.Parts {
			selected[item.PartID] = item
		}
	}
	revision, ok, err := s.core.store.GetArtifactV2PartRevision(principal.AccountScopeID, working.ID, changedPartID, changedRevisionID)
	if err != nil || !ok {
		return errors.New("artifact v2 author changed revision was not found")
	}
	selected[changedPartID] = pebblestore.ArtifactV2CompositionPart{PartID: changedPartID, PartRevisionID: changedRevisionID, DigestSHA256: revision.Blob.DigestSHA256}
	for _, part := range parts {
		if _, ok := selected[part.ID]; ok {
			continue
		}
		revisions, err := s.core.store.ListArtifactV2PartRevisions(principal.AccountScopeID, working.ID, part.ID, 256)
		if err != nil {
			return err
		}
		if len(revisions) == 0 {
			return nil
		}
		latest := revisions[len(revisions)-1]
		selected[part.ID] = pebblestore.ArtifactV2CompositionPart{PartID: part.ID, PartRevisionID: latest.ID, DigestSHA256: latest.Blob.DigestSHA256}
	}
	selections := make([]CompositionSelection, 0, len(parts))
	for _, part := range parts {
		item, ok := selected[part.ID]
		if !ok {
			return nil
		}
		selections = append(selections, CompositionSelection{PartID: item.PartID, PartRevisionID: item.PartRevisionID, Locked: item.Locked})
	}
	_, err = s.core.AdvanceComposition(ctx, principal, AdvanceCompositionInput{RequestID: requestID, ArtifactID: working.ID, ExpectedWorkingRevision: working.Revision, ExpectedCompositionHeadRevision: headRevision, ConstructionVersion: DefaultConstruction, Selections: selections})
	return err
}

func normalizedPolicy(policy PolicySnapshot) PolicySnapshot {
	policy.Revision = strings.TrimSpace(policy.Revision)
	if policy.Revision == "" {
		policy.Revision = DefaultPolicyRevision
	}
	if policy.MaxPartBytes <= 0 || policy.MaxPartBytes > maxAuthorPartBytes {
		policy.MaxPartBytes = maxAuthorPartBytes
	}
	if policy.MaxParts <= 0 || policy.MaxParts > maxAuthorParts {
		policy.MaxParts = maxAuthorParts
	}
	return policy
}

func safeDiagnostics(in []pebblestore.ArtifactV2Diagnostic) []pebblestore.ArtifactV2Diagnostic {
	if len(in) > 16 {
		in = in[:16]
	}
	out := make([]pebblestore.ArtifactV2Diagnostic, 0, len(in))
	for _, diagnostic := range in {
		out = append(out, safeDiagnostic(diagnostic.Code, diagnostic.Phase, diagnostic.Severity, diagnostic.RetryClass, diagnostic.SafeMessage))
		out[len(out)-1].PartID = bounded(diagnostic.PartID, 128)
		out[len(out)-1].AuthoredLocator = bounded(diagnostic.AuthoredLocator, 256)
		out[len(out)-1].FrameSlotOrTime = bounded(diagnostic.FrameSlotOrTime, 128)
		out[len(out)-1].Bounds = bounded(diagnostic.Bounds, 128)
		if len(diagnostic.PreservationProofs) > 8 {
			diagnostic.PreservationProofs = diagnostic.PreservationProofs[:8]
		}
		for _, proof := range diagnostic.PreservationProofs {
			out[len(out)-1].PreservationProofs = append(out[len(out)-1].PreservationProofs, bounded(proof, 128))
		}
	}
	return out
}

func safeDiagnostic(code, phase, severity, retryClass, message string) pebblestore.ArtifactV2Diagnostic {
	return pebblestore.ArtifactV2Diagnostic{Code: safeToken(code, "unknown"), Phase: safeToken(phase, "policy"), Severity: safeToken(severity, "error"), RetryClass: safeToken(retryClass, "terminal"), SafeMessage: bounded(message, 512)}
}

func safeToken(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return bounded(b.String(), 64)
}

func bounded(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func cloneCompositionHead(in *pebblestore.ArtifactV2CompositionHead) *pebblestore.ArtifactV2CompositionHead {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
func cloneDiagnostic(in *pebblestore.ArtifactV2Diagnostic) *pebblestore.ArtifactV2Diagnostic {
	if in == nil {
		return nil
	}
	out := *in
	out.PreservationProofs = append([]string(nil), in.PreservationProofs...)
	return &out
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// DeterministicCompiler is the launch baseline for static creative parts. Later
// specialized compilers replace this behind the same V2-owned interface.
type DeterministicCompiler struct{}

func (DeterministicCompiler) Compile(_ context.Context, input CompileInput) (CompileProduct, error) {
	if len(input.Parts) == 0 {
		return CompileProduct{}, errors.New("composition has no parts")
	}
	sort.SliceStable(input.Parts, func(i, j int) bool { return input.Parts[i].Definition.Order < input.Parts[j].Definition.Order })
	mediaType := input.Parts[0].Revision.Blob.MediaType
	var body []byte
	for index, part := range input.Parts {
		if index > 0 {
			body = append(body, '\n')
		}
		body = append(body, part.Body...)
		if part.Revision.Blob.MediaType != mediaType {
			mediaType = "application/octet-stream"
		}
	}
	return CompileProduct{Bytes: body, MediaType: mediaType, CompilerVersion: DefaultCompiler, TemplateVersion: DefaultConstruction}, nil
}

type DeterministicValidator struct{}

func (DeterministicValidator) Validate(_ context.Context, input ValidationInput) (ValidationProduct, error) {
	if len(input.Product.Bytes) == 0 || input.Build.Output == nil {
		return ValidationProduct{Status: pebblestore.ArtifactV2ValidationInvalid, ValidatorVersion: DefaultValidator, Diagnostics: []pebblestore.ArtifactV2Diagnostic{safeDiagnostic("empty_build", "policy", "error", "repairable", "The compiled result is empty.")}}, nil
	}
	return ValidationProduct{Status: pebblestore.ArtifactV2ValidationValid, ValidatorVersion: DefaultValidator, EvidenceDigests: []string{input.Build.Output.DigestSHA256}}, nil
}
