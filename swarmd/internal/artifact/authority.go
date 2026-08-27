package artifact

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
	"strings"
	"time"

	"swarm/packages/swarmd/internal/artifactgit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// MetadataStore is the canonical V3 metadata boundary used by Authority.
// Implementations must commit event, projection, idempotency, and realtime
// outbox state atomically, as SessionStore.ApplyV3SessionMutation does.
type MetadataStore interface {
	GetSessionArtifactCollection(accountScopeID, sessionID, collectionID string) (pebblestore.SessionArtifactCollection, bool, error)
	GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error)
	GetSessionArtifactVariantByID(accountScopeID, sessionID, variantID string) (pebblestore.SessionArtifactVariant, bool, error)
	ListSessionArtifactCollections(accountScopeID, sessionID, status string, limit int) ([]pebblestore.SessionArtifactCollection, error)
	ListSessionArtifactVariants(accountScopeID, sessionID, collectionID string, limit int) ([]pebblestore.SessionArtifactVariant, error)
	SearchSessionArtifactCatalog(accountScopeID, userID string, options pebblestore.SessionArtifactCatalogOptions) (pebblestore.SessionArtifactCatalogPage, error)
	GetSessionArtifactPartDefinition(accountScopeID, userID, ownerSessionID, chainID, partID string) (pebblestore.SessionArtifactPartDefinition, bool, error)
	GetSessionArtifactPartRevision(accountScopeID, userID, ownerSessionID, chainID, partID, revisionID string) (pebblestore.SessionArtifactPartRevision, bool, error)
	GetSessionArtifactComposition(accountScopeID, userID, ownerSessionID, chainID, compositionID string) (pebblestore.SessionArtifactComposition, bool, error)
	GetSessionArtifactChain(accountScopeID, userID, chainID string) (pebblestore.SessionArtifactChain, bool, error)
	ApplySessionMutation(pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error)
}

// Principal is trusted run/session context. None of these ownership or lineage
// fields are accepted from artifact content or model-authored metadata.
type Principal struct {
	SessionID               string
	AccountScopeID          string
	UserID                  string
	RunID                   string
	PlanID                  string
	CheckpointID            string
	AttemptID               string
	TaskCallID              string
	ProgramID               string
	ProgramJobID            string
	ChildSessionID          string
	IterationGroupID        string
	IterationGroup          string
	IterationID             string
	IterationIndex          int
	IterationLabel          string
	IterationTheme          string
	IterationSectionID      string
	IterationSectionLabel   string
	IterationSectionStartMs int64
	IterationSectionEndMs   int64
	PartID                  string
	PartLabel               string
	PartKind                string
	SelectedReviewTargetIDs string
}

type CreateInput struct {
	RequestID             string
	CollectionID          string
	CollectionName        string
	CollectionDescription string
	VariantID             string
	Filename              string
	MediaType             string
	Role                  string
	Presentation          pebblestore.SessionArtifactPresentation
	OutputRequirements    *pebblestore.SessionArtifactOutputRequirements
	AnimationProfile      *pebblestore.SessionArtifactAnimationProfile
	Parts                 []pebblestore.SessionArtifactPart
	SourceSessionID       string
	SourceCollectionID    string
	SourceVariantID       string
	SourceEventSeq        uint64
	VideoProjectID        string
	VideoRevisionID       string
	VideoRevisionEventSeq uint64
	ArtifactStepID        string
	CandidateIndex        int
	AutoAccept            bool
	Body                  []byte
}

type InitialPartInput struct {
	Definition pebblestore.SessionArtifactPartDefinition
	RevisionID string
	MediaType  string
	Body       []byte
}

type CreateInitialCompositionInput struct {
	CreateInput
	ArtifactChainID string
	CompositionID   string
	Construction    pebblestore.SessionArtifactConstruction
	Parts           []InitialPartInput
}

type PartReplacementInput struct {
	PartDefinition     pebblestore.SessionArtifactPartDefinition
	SourcePartRevision pebblestore.SessionArtifactPartRevisionReference
	Filename           string
	MediaType          string
	Body               []byte
	Locked             bool
}

type PublishPartReplacementsInput struct {
	RequestID         string
	CallID            string
	CollectionID      string
	VariantID         string
	ArtifactStepID    string
	IterationTurnID   string
	IterationGroupID  string
	CandidateIndex    int
	AutoAccept        bool
	SourceArtifact    pebblestore.SessionArtifactSelectionReference
	SourceComposition pebblestore.SessionArtifactComposition
	Replacements      []PartReplacementInput
}

type PartRevisionChoiceInput struct {
	PartID           string
	Revision         pebblestore.SessionArtifactPartRevisionReference
	RevisionEventSeq uint64
	Locked           bool
}

type SelectPartRevisionsInput struct {
	RequestID         string
	CollectionID      string
	VariantID         string
	ArtifactStepID    string
	SourceArtifact    pebblestore.SessionArtifactSelectionReference
	SourceComposition pebblestore.SessionArtifactComposition
	Choices           []PartRevisionChoiceInput
}

type PublishPartReplacementInput struct {
	RequestID          string
	CallID             string
	CollectionID       string
	VariantID          string
	ArtifactStepID     string
	CandidateIndex     int
	AutoAccept         bool
	SourceArtifact     pebblestore.SessionArtifactSelectionReference
	SourceComposition  pebblestore.SessionArtifactComposition
	PartDefinition     pebblestore.SessionArtifactPartDefinition
	SourcePartRevision pebblestore.SessionArtifactPartRevisionReference
	Filename           string
	MediaType          string
	Presentation       pebblestore.SessionArtifactPresentation
	OutputRequirements *pebblestore.SessionArtifactOutputRequirements
	AnimationProfile   *pebblestore.SessionArtifactAnimationProfile
	Body               []byte
	Locked             bool
}

type CreatePackageInput struct {
	CreateInput
	Entries []PackageEntry
}

type CreateFileInput struct {
	CreateInput
	SourcePath string
	Package    bool
}

// MaterializeBatchItem is one exact authenticated ready source. Destination
// names are derived only from each trusted variant's bounded filename.
type MaterializeBatchItem struct {
	Reference pebblestore.SessionArtifactSelectionReference
}

type Authority struct {
	registry *Registry
	metadata MetadataStore
	now      func() time.Time
}

func NewAuthority(registry *Registry, metadata MetadataStore) *Authority {
	return &Authority{registry: registry, metadata: metadata, now: time.Now}
}

func (a *Authority) Create(ctx context.Context, principal Principal, input CreateInput) (pebblestore.SessionArtifactVariant, error) {
	return a.create(ctx, principal, input, nil, "")
}

// Reserve publishes a durable projection-only staging variant before expensive
// byte production begins. The same CreateInput can later be passed to Create to
// atomically finalize the reserved identity with immutable bytes.
func (a *Authority) Reserve(principal Principal, input CreateInput) (pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.CollectionID, input.VariantID = strings.TrimSpace(input.CollectionID), strings.TrimSpace(input.VariantID)
	input.SourceSessionID = strings.TrimSpace(input.SourceSessionID)
	input.SourceCollectionID, input.SourceVariantID = strings.TrimSpace(input.SourceCollectionID), strings.TrimSpace(input.SourceVariantID)
	input.Role = strings.TrimSpace(input.Role)
	if input.Role != "" && input.Role != pebblestore.SessionArtifactRoleRenderOnly {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact role is unsupported")
	}
	if input.RequestID == "" || input.CollectionID == "" || input.VariantID == "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact reservation requires request, collection, and variant ids")
	}
	if input.SourceSessionID != "" || input.SourceEventSeq != 0 {
		ref := pebblestore.SessionArtifactSelectionReference{SessionID: input.SourceSessionID, CollectionID: input.SourceCollectionID, VariantID: input.SourceVariantID, EventSeq: input.SourceEventSeq}
		if _, err := a.GetReference(principal, ref); err != nil {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("resolve source artifact: %w", err)
		}
	} else if input.SourceCollectionID != "" || input.SourceVariantID != "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("source artifact lineage requires source_session_id and source_event_seq")
	}
	if err := applyArtifactOutputRequirementsToPresentation(&input.Presentation, input.OutputRequirements); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	lineage := a.lineage(principal, input)
	collectionLineage := lineage
	collectionLineage.SourceSessionID, collectionLineage.SourceCollectionID, collectionLineage.SourceVariantID, collectionLineage.SourceEventSeq = "", "", "", 0
	collectionLineage.ProgramJobID, collectionLineage.ChildSessionID = "", ""
	collectionLineage.IterationID, collectionLineage.IterationIndex, collectionLineage.IterationLabel, collectionLineage.IterationTheme = "", 0, "", ""
	collectionLineage.IterationSectionID, collectionLineage.IterationSectionLabel, collectionLineage.IterationSectionStartMs, collectionLineage.IterationSectionEndMs = "", "", 0, 0
	collectionLineage.PartID, collectionLineage.PartLabel, collectionLineage.PartKind = "", "", ""
	collectionLineage.SelectedReviewTargetIDs = ""
	collectionLineage.VideoProjectID, collectionLineage.VideoRevisionID, collectionLineage.VideoRevisionEventSeq = "", "", 0
	collection := pebblestore.SessionArtifactCollection{ID: input.CollectionID, Name: strings.TrimSpace(input.CollectionName), Description: strings.TrimSpace(input.CollectionDescription), Lineage: collectionLineage, Presentation: input.Presentation}
	variant := pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, Filename: strings.TrimSpace(input.Filename), MediaType: strings.TrimSpace(input.MediaType), Role: strings.TrimSpace(input.Role), Presentation: input.Presentation, OutputRequirements: cloneOutputRequirements(input.OutputRequirements), AnimationProfile: cloneAnimationProfile(input.AnimationProfile), Parts: append([]pebblestore.SessionArtifactPart(nil), input.Parts...), Lineage: lineage, ArtifactStepID: strings.TrimSpace(input.ArtifactStepID), RevisionRoundID: strings.TrimSpace(input.ArtifactStepID), CandidateIndex: input.CandidateIndex, AutoAccept: input.AutoAccept}
	if existing, ok, getErr := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID); getErr != nil {
		return pebblestore.SessionArtifactVariant{}, getErr
	} else if ok {
		compatible := artifactDestinationLineageCompatible(existing.Lineage, lineage) && existing.Filename == variant.Filename && existing.MediaType == variant.MediaType && existing.Role == variant.Role && equalOutputRequirements(existing.OutputRequirements, variant.OutputRequirements) && equalAnimationProfile(existing.AnimationProfile, variant.AnimationProfile)
		if !compatible {
			return pebblestore.SessionArtifactVariant{}, errors.New("artifact reservation conflicts with an existing variant")
		}
		switch existing.Status {
		case pebblestore.SessionArtifactStatusStaging, pebblestore.SessionArtifactStatusReady, pebblestore.SessionArtifactStatusFailed, pebblestore.SessionArtifactStatusUnavailable:
			return existing, nil
		default:
			return pebblestore.SessionArtifactVariant{}, errors.New("artifact reservation conflicts with an existing variant")
		}
	}
	result, err := a.mutateWithArtifact(principal, input.RequestID+":reserve", pebblestore.V3SessionMutationCreateArtifact, pebblestore.V3ArtifactMutation{ProjectionOnly: true, Collection: collection, Variant: &variant})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if result.Artifact == nil || result.Artifact.Variant == nil || result.Artifact.Variant.Status != pebblestore.SessionArtifactStatusStaging {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact reservation was not durably staged")
	}
	return *result.Artifact.Variant, nil
}

func (a *Authority) CreatePackage(ctx context.Context, principal Principal, input CreatePackageInput) (pebblestore.SessionArtifactVariant, error) {
	entries := make([]PackageEntry, len(input.Entries))
	copy(entries, input.Entries)
	return a.create(ctx, principal, input.CreateInput, entries, "")
}

func (a *Authority) CreateFromFile(ctx context.Context, principal Principal, input CreateFileInput) (pebblestore.SessionArtifactVariant, error) {
	if input.Package {
		return a.create(ctx, principal, input.CreateInput, []PackageEntry{}, strings.TrimSpace(input.SourcePath))
	}
	return a.create(ctx, principal, input.CreateInput, nil, strings.TrimSpace(input.SourcePath))
}

// PublishWorkspace creates one new immutable variant from a trusted workspace
// source. Caller-supplied variant IDs are never accepted on this path.
func (a *Authority) PublishWorkspace(ctx context.Context, principal Principal, input CreateFileInput) (pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	input.VariantID = strings.TrimSpace(input.VariantID)
	if input.VariantID == "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("workspace publication requires a trusted generated variant id")
	}
	if existing, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(input.CollectionID), input.VariantID); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	} else if ok {
		return pebblestore.SessionArtifactVariant{}, errors.New("workspace publication variant already exists; retry with a new trusted tool call")
	} else if existing.ID != "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("workspace publication variant identity is inconsistent")
	}
	return a.CreateFromFile(ctx, principal, input)
}

func (a *Authority) create(ctx context.Context, principal Principal, input CreateInput, packageEntries []PackageEntry, sourcePath string) (pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	input.SourceSessionID = strings.TrimSpace(input.SourceSessionID)
	input.SourceCollectionID = strings.TrimSpace(input.SourceCollectionID)
	input.SourceVariantID = strings.TrimSpace(input.SourceVariantID)
	input.Role = strings.TrimSpace(input.Role)
	if input.Role != "" && input.Role != pebblestore.SessionArtifactRoleRenderOnly {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact role is unsupported")
	}
	if input.SourceSessionID != "" || input.SourceEventSeq != 0 {
		ref := pebblestore.SessionArtifactSelectionReference{SessionID: input.SourceSessionID, CollectionID: input.SourceCollectionID, VariantID: input.SourceVariantID, EventSeq: input.SourceEventSeq}
		if _, err := a.GetReference(principal, ref); err != nil {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("resolve source artifact: %w", err)
		}
	} else if input.SourceCollectionID != "" || input.SourceVariantID != "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("source artifact lineage requires source_session_id and source_event_seq")
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact request id is required")
	}
	lineage := a.lineage(principal, input)
	if strings.TrimSpace(input.ArtifactStepID) == "" {
		digest := sha256.Sum256([]byte("artifact-step-v1\x00" + principal.SessionID + "\x00" + input.RequestID))
		input.ArtifactStepID = "artifact-step-" + hex.EncodeToString(digest[:12])
	}
	if input.CandidateIndex < 1 {
		input.CandidateIndex = 1
	}
	collectionLineage := lineage
	collectionLineage.SourceSessionID, collectionLineage.SourceCollectionID, collectionLineage.SourceVariantID, collectionLineage.SourceEventSeq = "", "", "", 0
	collectionLineage.ProgramJobID, collectionLineage.ChildSessionID = "", ""
	collectionLineage.IterationID, collectionLineage.IterationIndex, collectionLineage.IterationLabel, collectionLineage.IterationTheme = "", 0, "", ""
	collectionLineage.IterationSectionID, collectionLineage.IterationSectionLabel, collectionLineage.IterationSectionStartMs, collectionLineage.IterationSectionEndMs = "", "", 0, 0
	collectionLineage.PartID, collectionLineage.PartLabel, collectionLineage.PartKind = "", "", ""
	collectionLineage.SelectedReviewTargetIDs = ""
	collectionLineage.VideoProjectID, collectionLineage.VideoRevisionID, collectionLineage.VideoRevisionEventSeq = "", "", 0
	if err := applyArtifactOutputRequirementsToPresentation(&input.Presentation, input.OutputRequirements); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	collection := pebblestore.SessionArtifactCollection{ID: strings.TrimSpace(input.CollectionID), Name: strings.TrimSpace(input.CollectionName), Description: strings.TrimSpace(input.CollectionDescription), Lineage: collectionLineage, Presentation: input.Presentation}
	variant := pebblestore.SessionArtifactVariant{ID: strings.TrimSpace(input.VariantID), CollectionID: collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Filename: strings.TrimSpace(input.Filename), MediaType: strings.TrimSpace(input.MediaType), Role: strings.TrimSpace(input.Role), Presentation: input.Presentation, OutputRequirements: cloneOutputRequirements(input.OutputRequirements), AnimationProfile: cloneAnimationProfile(input.AnimationProfile), Parts: append([]pebblestore.SessionArtifactPart(nil), input.Parts...), Lineage: lineage, ArtifactStepID: strings.TrimSpace(input.ArtifactStepID), RevisionRoundID: strings.TrimSpace(input.ArtifactStepID), CandidateIndex: input.CandidateIndex, AutoAccept: input.AutoAccept}
	existingStaging := false
	if existing, ok, getErr := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID); getErr != nil {
		return pebblestore.SessionArtifactVariant{}, getErr
	} else if ok {
		lineageCompatible := existing.Lineage == (pebblestore.SessionArtifactLineage{}) || artifactDestinationLineageCompatible(existing.Lineage, lineage)
		roleCompatible := existing.Role == variant.Role
		presentationCompatible := artifactPresentationRequirementsCompatible(existing.Presentation, variant.Presentation, existing.OutputRequirements)
		if existing.OutputRequirements != nil && variant.OutputRequirements == nil {
			if (variant.Presentation.Width != 0 && variant.Presentation.Width != existing.OutputRequirements.Width) || (variant.Presentation.Height != 0 && variant.Presentation.Height != existing.OutputRequirements.Height) {
				return pebblestore.SessionArtifactVariant{}, errors.New("artifact presentation dimensions conflict with output requirements")
			}
			variant.Presentation.Width, variant.Presentation.Height = existing.OutputRequirements.Width, existing.OutputRequirements.Height
			presentationCompatible = artifactPresentationRequirementsCompatible(existing.Presentation, variant.Presentation, existing.OutputRequirements)
		}
		if existing.Status == pebblestore.SessionArtifactStatusReady {
			readyRequirementsCompatible := equalOutputRequirements(existing.OutputRequirements, variant.OutputRequirements)
			if existing.OutputRequirements != nil && variant.OutputRequirements == nil {
				readyRequirementsCompatible = true
			}
			readyAnimationCompatible := equalAnimationProfile(existing.AnimationProfile, variant.AnimationProfile)
			if existing.AnimationProfile != nil && variant.AnimationProfile == nil {
				readyAnimationCompatible = true
			}
			if !lineageCompatible || !roleCompatible || !readyRequirementsCompatible || !readyAnimationCompatible || !presentationCompatible {
				return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q already exists with incompatible metadata, lineage, requirements, or presentation", variant.ID)
			}
			return existing, nil
		}
		metadataCompatible := (existing.Filename == "" && existing.MediaType == "") || (existing.Filename == variant.Filename && existing.MediaType == variant.MediaType)
		requirementsCompatible := equalOutputRequirements(existing.OutputRequirements, variant.OutputRequirements)
		animationCompatible := equalAnimationProfile(existing.AnimationProfile, variant.AnimationProfile)
		// Managed preallocation is the durable requirement authority. A caller may
		// omit a trusted snapshot only when finalizing that same staging row.
		if existing.OutputRequirements != nil && variant.OutputRequirements == nil {
			requirementsCompatible = true
		}
		if existing.AnimationProfile != nil && variant.AnimationProfile == nil {
			animationCompatible = true
		}
		if existing.Status != pebblestore.SessionArtifactStatusStaging || !metadataCompatible || !roleCompatible || !lineageCompatible || !requirementsCompatible || !animationCompatible || !presentationCompatible {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q already exists with incompatible status, metadata, or lineage, or with incompatible requirements or presentation", variant.ID)
		}
		storedCollection, collectionOK, collectionErr := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, collection.ID)
		if collectionErr != nil {
			return pebblestore.SessionArtifactVariant{}, collectionErr
		}
		if !collectionOK {
			return pebblestore.SessionArtifactVariant{}, errors.New("artifact staging collection metadata is missing")
		}
		collection, existingStaging = storedCollection, true
		// Keep the caller's filename/media/presentation for byte staging. The
		// preallocated canonical row contributes identity and immutable lineage,
		// then the terminal mutation merges the produced metadata into it.
		variant.Version, variant.AccountScopeID, variant.SessionID, variant.Status = existing.Version, existing.AccountScopeID, existing.SessionID, existing.Status
		variant.CreatedAt, variant.UpdatedAt, variant.EventSeq = existing.CreatedAt, existing.UpdatedAt, existing.EventSeq
		if existing.Lineage == (pebblestore.SessionArtifactLineage{}) {
			variant.Lineage = lineage
		} else {
			// The placeholder is allocated before the child provider run exists, so
			// preserve its stable trusted destination lineage instead of trying to
			// mutate it with run/plan/attempt metadata discovered during execution.
			variant.Lineage = existing.Lineage
		}
		if collection.Lineage == (pebblestore.SessionArtifactLineage{}) {
			collection.Lineage = collectionLineage
		}
		variant.OutputRequirements = cloneOutputRequirements(existing.OutputRequirements)
		variant.AnimationProfile = cloneAnimationProfile(existing.AnimationProfile)
	}
	var gitBody []byte
	if packageEntries != nil {
		entries := packageEntries
		if sourcePath != "" {
			entries, err = packageDirectoryEntries(sourcePath, a.registry.limits)
		}
		if err == nil {
			gitBody, err = canonicalPackageEntries(ctx, a.registry.limits, entries)
		}
	} else if sourcePath != "" {
		gitBody, err = readArtifactFile(ctx, a.registry.limits, variant, sourcePath)
	} else {
		gitBody = append([]byte(nil), input.Body...)
	}
	var digest string
	var size int64
	if err == nil {
		gitBody, digest, size, err = canonicalArtifactBytes(ctx, a.registry.limits, variant, gitBody)
	}
	if err != nil {
		failed, mutationErr := a.recordFailure(principal, input.RequestID, collection, variant, "ingress_failed")
		if mutationErr != nil {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("%v; persist artifact failure: %w", err, mutationErr)
		}
		return failed, fmt.Errorf("validate artifact ingress: %w", err)
	}
	variant.DigestSHA256, variant.Size = digest, size
	if err := a.publishGitVariant(ctx, principal, input, &variant, gitBody); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !existingStaging {
		if _, err := a.mutate(principal, input.RequestID+":stage", pebblestore.V3SessionMutationCreateArtifact, collection, &variant, nil); err != nil {
			return pebblestore.SessionArtifactVariant{}, err
		}
	}
	// Requirements are the trusted target contract. Byte staging/finalization does
	// not inspect binary pixel dimensions, so preserve the exact target metadata.
	variant.Presentation.Width, variant.Presentation.Height = 0, 0
	if err := applyArtifactOutputRequirementsToPresentation(&variant.Presentation, variant.OutputRequirements); err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("preserve finalized artifact output requirements: %w", err)
	}
	collection.Lineage = collectionLineage
	if _, err := a.mutate(principal, input.RequestID+":ready:"+variant.DigestSHA256, pebblestore.V3SessionMutationFinalizeArtifact, collection, &variant, nil); err != nil {
		// Finalized bytes may outlive a failed metadata write, but ready metadata is
		// never published. A retry with the same request and bytes safely converges.
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("persist finalized artifact metadata: %w", err)
	}
	stored, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok || stored.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact ready metadata was not persisted")
	}
	return stored, nil
}

func (a *Authority) List(principal Principal, status string, limit int) ([]pebblestore.SessionArtifactCollection, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return nil, err
	}
	return a.metadata.ListSessionArtifactCollections(principal.AccountScopeID, principal.SessionID, status, limit)
}

func (a *Authority) ListVariants(principal Principal, collectionID string, limit int) ([]pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return nil, err
	}
	return a.metadata.ListSessionArtifactVariants(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID), limit)
}

// SearchCatalog returns a flattened, paginated library across every session
// owned by the authenticated account and user. Ownership comes exclusively from
// the trusted principal and durable session records.
func (a *Authority) SearchCatalog(principal Principal, options pebblestore.SessionArtifactCatalogOptions) (pebblestore.SessionArtifactCatalogPage, error) {
	owned, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactCatalogPage{}, err
	}
	return a.metadata.SearchSessionArtifactCatalog(owned.AccountScopeID, owned.UserID, options)
}

func (a *Authority) Get(principal Principal, variantID string) (pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	variant, ok, err := a.metadata.GetSessionArtifactVariantByID(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(variantID))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q was not found", variantID)
	}
	return variant, nil
}

func (a *Authority) ReadVariant(ctx context.Context, principal Principal, variant pebblestore.SessionArtifactVariant, maxBytes int64) ([]byte, error) {
	owned, err := a.owned(principal)
	if err != nil {
		return nil, err
	}
	if variant.AccountScopeID != owned.AccountScopeID {
		return nil, errors.New("artifact variant ownership does not match")
	}
	if variant.SessionID != owned.SessionID {
		if _, err := a.registry.OwnedSession(variant.SessionID, owned.AccountScopeID, owned.UserID); err != nil {
			return nil, errors.New("artifact variant ownership does not match")
		}
	}
	if variant.Status != pebblestore.SessionArtifactStatusReady {
		return nil, ErrNotReady
	}
	if variant.PartGraphState == pebblestore.SessionArtifactGraphAuthoritative && variant.Composition != nil {
		return a.constructComposition(ctx, principal, *variant.Composition, maxBytes)
	}
	return a.readGitVariant(ctx, variant, maxBytes)
}

func (a *Authority) Read(ctx context.Context, principal Principal, variantID string, maxBytes int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return nil, pebblestore.SessionArtifactVariant{}, err
	}
	variant, err := a.Get(principal, variantID)
	if err != nil {
		return nil, pebblestore.SessionArtifactVariant{}, err
	}
	data, err := a.ReadVariant(ctx, principal, variant, maxBytes)
	return data, variant, err
}

// GetReference resolves an attached opaque reference without changing the
// trusted current-run principal. The source session must belong to the same
// authenticated account and user, and the reference must still identify the
// exact ready variant event that was attached to the message.
func (a *Authority) GetReference(principal Principal, ref pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error) {
	if _, err := a.owned(principal); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.CollectionID = strings.TrimSpace(ref.CollectionID)
	ref.VariantID = strings.TrimSpace(ref.VariantID)
	if ref.SessionID == "" || ref.CollectionID == "" || ref.VariantID == "" || ref.EventSeq == 0 {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference requires session_id, collection_id, variant_id, and event_seq")
	}
	if _, err := a.registry.OwnedSession(ref.SessionID, principal.AccountScopeID, principal.UserID); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, ref.SessionID, ref.CollectionID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference was not found")
	}
	if collection.AccountScopeID != principal.AccountScopeID || collection.SessionID != ref.SessionID || collection.ID != ref.CollectionID {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference ownership is inconsistent")
	}
	variant, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, ref.SessionID, ref.CollectionID, ref.VariantID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference was not found")
	}
	if variant.AccountScopeID != principal.AccountScopeID || variant.SessionID != ref.SessionID || variant.CollectionID != ref.CollectionID || variant.ID != ref.VariantID {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference ownership is inconsistent")
	}
	if variant.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference is not ready")
	}
	readySequence := variant.EventSeq == ref.EventSeq
	selectedSequence := collection.SelectedVariantID == variant.ID && collection.EventSeq == ref.EventSeq
	if !readySequence && !selectedSequence {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact source reference is stale")
	}
	return variant, nil
}

// ReadReference reads bounded bytes through the source session's authenticated
// storage authority. It never exposes or accepts a filesystem path.
func (a *Authority) ReadReference(ctx context.Context, principal Principal, ref pebblestore.SessionArtifactSelectionReference, maxBytes int64) ([]byte, pebblestore.SessionArtifactVariant, error) {
	variant, err := a.GetReference(principal, ref)
	if err != nil {
		return nil, pebblestore.SessionArtifactVariant{}, err
	}
	data, err := a.ReadVariant(ctx, principal, variant, maxBytes)
	return data, variant, err
}

func (a *Authority) constructComposition(ctx context.Context, principal Principal, composition pebblestore.SessionArtifactComposition, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("artifact composition read requires a positive byte bound")
	}
	slots := make(map[string]pebblestore.SessionArtifactPartRevisionReference, len(composition.Parts))
	for _, slot := range composition.Parts {
		slots[slot.PartID] = slot.Revision
	}
	var assembled bytes.Buffer
	var archive *zip.Writer
	if composition.Construction.Kind == "package-v1" {
		archive = zip.NewWriter(&assembled)
	}
	if composition.Construction.Kind != "concat-v1" && archive == nil {
		return nil, errors.New("artifact composition construction kind is unsupported")
	}
	for _, entry := range composition.Construction.Entries {
		reference, ok := slots[entry.PartID]
		if !ok {
			return nil, errors.New("artifact construction references an unknown part")
		}
		part, _, err := a.ReadPartRevision(ctx, principal, reference, maxBytes-int64(assembled.Len()))
		if err != nil {
			return nil, err
		}
		if archive == nil {
			_, err = assembled.Write(part)
		} else {
			var writer io.Writer
			writer, err = archive.CreateHeader(&zip.FileHeader{Name: entry.Path, Method: zip.Store})
			if err == nil {
				_, err = writer.Write(part)
			}
		}
		if err != nil {
			return nil, err
		}
		if int64(assembled.Len()) > maxBytes {
			return nil, errors.New("constructed artifact composition exceeds read byte bound")
		}
	}
	if archive != nil {
		if err := archive.Close(); err != nil {
			return nil, err
		}
	}
	if int64(assembled.Len()) > maxBytes {
		return nil, errors.New("constructed artifact composition exceeds read byte bound")
	}
	return assembled.Bytes(), nil
}

func (a *Authority) ReadPackageReference(ctx context.Context, principal Principal, ref pebblestore.SessionArtifactSelectionReference, entryName string, maxBytes int64) ([]PackageManifestEntry, []byte, pebblestore.SessionArtifactVariant, error) {
	variant, err := a.GetReference(principal, ref)
	if err != nil {
		return nil, nil, pebblestore.SessionArtifactVariant{}, err
	}
	body, err := a.ReadVariant(ctx, principal, variant, a.registry.limits.MaxVideoArtifactBytes)
	if err != nil {
		return nil, nil, pebblestore.SessionArtifactVariant{}, err
	}
	manifest, data, err := readPackageBytes(a.registry.limits, body, entryName, maxBytes)
	return manifest, data, variant, err
}

// MaterializeReference verifies an exact authenticated ready reference before
// copying its managed bytes into the trusted current workspace root.
func (a *Authority) MaterializeReference(ctx context.Context, principal Principal, ref pebblestore.SessionArtifactSelectionReference, workspaceRoot, destination string, overwrite bool) (Materialized, error) {
	variant, err := a.GetReference(principal, ref)
	if err != nil {
		return Materialized{}, err
	}
	body, err := a.ReadVariant(ctx, principal, variant, a.registry.limits.MaxVideoArtifactBytes)
	if err != nil {
		return Materialized{}, err
	}
	return MaterializeBytes(ctx, a.registry.limits, variant, body, workspaceRoot, destination, overwrite)
}

// MaterializeBatchReferences authenticates the complete reference set before the
// filesystem batch preflight and atomic destination-directory publication.
func (a *Authority) MaterializeBatchReferences(ctx context.Context, principal Principal, items []MaterializeBatchItem, workspaceRoot, destination string, overwrite bool) ([]Materialized, []pebblestore.SessionArtifactVariant, error) {
	if len(items) == 0 || len(items) > MaxMaterializeBatchItems {
		return nil, nil, fmt.Errorf("artifact materialization batch must contain 1 to %d items", MaxMaterializeBatchItems)
	}
	inputs := make([]BatchMaterializeInput, 0, len(items))
	variants := make([]pebblestore.SessionArtifactVariant, 0, len(items))
	for _, item := range items {
		variant, err := a.GetReference(principal, item.Reference)
		if err != nil {
			return nil, nil, err
		}
		body, err := a.ReadVariant(ctx, principal, variant, a.registry.limits.MaxVideoArtifactBytes)
		if err != nil {
			return nil, nil, err
		}
		inputs = append(inputs, BatchMaterializeInput{Variant: variant, Body: body})
		variants = append(variants, variant)
	}
	materialized, err := MaterializeBatch(ctx, a.registry.limits, inputs, workspaceRoot, destination, overwrite)
	if err != nil {
		return nil, nil, err
	}
	return materialized, variants, nil
}

func (a *Authority) Select(principal Principal, requestID, collectionID, variantID string) (pebblestore.SessionArtifactSelectionReference, error) {
	return a.SelectAction(principal, requestID, collectionID, variantID, "select")
}

func (a *Authority) SelectAction(principal Principal, requestID, collectionID, variantID, action string) (pebblestore.SessionArtifactSelectionReference, error) {
	return a.SelectReference(principal, requestID, collectionID, pebblestore.SessionArtifactSelectionReference{VariantID: variantID, Action: action})
}

func (a *Authority) SelectReference(principal Principal, requestID, collectionID string, incoming pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactSelectionReference, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactSelectionReference{}, err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID))
	if err != nil {
		return pebblestore.SessionArtifactSelectionReference{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactSelectionReference{}, fmt.Errorf("artifact collection %q was not found", collectionID)
	}
	incoming.Action = strings.ToLower(strings.TrimSpace(incoming.Action))
	if incoming.Action != "select" && incoming.Action != "use" {
		return pebblestore.SessionArtifactSelectionReference{}, errors.New("artifact selection action must be select or use")
	}
	incoming.SessionID, incoming.CollectionID, incoming.VariantID = principal.SessionID, collection.ID, strings.TrimSpace(incoming.VariantID)
	selection := &incoming
	selected, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, selection.VariantID)
	if err != nil {
		return pebblestore.SessionArtifactSelectionReference{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactSelectionReference{}, errors.New("selected artifact was not found")
	}
	result, err := a.mutateWithArtifact(principal, requestID, pebblestore.V3SessionMutationSelectArtifact, pebblestore.V3ArtifactMutation{Collection: collection, Variant: &selected, Selection: selection})
	if err != nil {
		return pebblestore.SessionArtifactSelectionReference{}, err
	}
	if result.Artifact == nil || result.Artifact.Selection == nil {
		return pebblestore.SessionArtifactSelectionReference{}, errors.New("artifact selection was not persisted")
	}
	return *result.Artifact.Selection, nil
}

func (a *Authority) UpdateProgress(principal Principal, requestID, collectionID, variantID string, progress pebblestore.SessionArtifactProgress) (pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact collection %q was not found", collectionID)
	}
	variant, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, strings.TrimSpace(variantID))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q was not found", variantID)
	}
	if variant.Status != pebblestore.SessionArtifactStatusStaging || !variant.ProjectionReservation {
		return variant, nil
	}
	variant.Progress = &progress
	result, err := a.mutateWithArtifact(principal, requestID, pebblestore.V3SessionMutationUpdateArtifact, pebblestore.V3ArtifactMutation{ProjectionOnly: true, Collection: collection, Variant: &variant})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if result.Artifact == nil || result.Artifact.Variant == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact progress metadata was not persisted")
	}
	return *result.Artifact.Variant, nil
}

func (a *Authority) MarkFailed(principal Principal, requestID, collectionID, variantID, failureCode string) (pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact collection %q was not found", collectionID)
	}
	variant, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, strings.TrimSpace(variantID))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact variant %q was not found", variantID)
	}
	if variant.Status == pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("ready artifact variant is immutable")
	}
	variant.FailureCode = strings.TrimSpace(failureCode)
	if variant.FailureCode == "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact failure code is required")
	}
	mutation := pebblestore.V3ArtifactMutation{Collection: collection, Variant: &variant}
	if variant.ProjectionReservation || variant.GraphState == "" {
		mutation.ProjectionOnly = true
	}
	if _, err := a.mutateWithArtifact(principal, requestID, pebblestore.V3SessionMutationFailArtifact, mutation); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	stored, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact failure metadata was not persisted")
	}
	return stored, nil
}

func (a *Authority) DeleteVariant(principal Principal, requestID, collectionID, variantID string) error {
	principal, err := a.owned(principal)
	if err != nil {
		return err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	variant, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, strings.TrimSpace(variantID))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if variant.CandidateRef != "" {
		repo, repoErr := a.repository(context.Background(), variant.RepositoryID)
		if repoErr != nil {
			return repoErr
		}
		if repoErr = repo.DeleteCandidate(context.Background(), variant.CandidateRef, variant.CommitOID); repoErr != nil {
			return repoErr
		}
	}
	if _, err = a.mutate(principal, requestID, pebblestore.V3SessionMutationDeleteArtifactVariant, collection, &variant, nil); err != nil {
		return err
	}
	return nil
}

func (a *Authority) DeleteCollection(principal Principal, requestID, collectionID string) error {
	principal, err := a.owned(principal)
	if err != nil {
		return err
	}
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, strings.TrimSpace(collectionID))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if _, err = a.mutate(principal, requestID, pebblestore.V3SessionMutationDeleteArtifactCollection, collection, nil, nil); err != nil {
		return err
	}
	return nil
}

func (a *Authority) recordFailure(principal Principal, requestID string, collection pebblestore.SessionArtifactCollection, variant pebblestore.SessionArtifactVariant, code string) (pebblestore.SessionArtifactVariant, error) {
	variant.FailureCode = code
	if _, err := a.mutate(principal, requestID+":failed:"+code, pebblestore.V3SessionMutationFailArtifact, collection, &variant, nil); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	stored, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !ok {
		return pebblestore.SessionArtifactVariant{}, errors.New("artifact failure metadata was not persisted")
	}
	return stored, nil
}

func (a *Authority) owned(principal Principal) (Principal, error) {
	if a == nil || a.registry == nil || a.metadata == nil {
		return Principal{}, errors.New("artifact authority is not configured")
	}
	principal.SessionID, principal.AccountScopeID, principal.UserID = strings.TrimSpace(principal.SessionID), strings.TrimSpace(principal.AccountScopeID), strings.TrimSpace(principal.UserID)
	if principal.SessionID == "" || principal.AccountScopeID == "" || principal.UserID == "" {
		return Principal{}, errors.New("trusted artifact session ownership is required")
	}
	session, err := a.registry.OwnedSession(principal.SessionID, principal.AccountScopeID, principal.UserID)
	if err != nil {
		return Principal{}, err
	}
	principal.AccountScopeID, principal.UserID = session.AccountScopeID, session.UserID
	return principal, nil
}

func (a *Authority) repository(ctx context.Context, repositoryID string) (*artifactgit.Repository, error) {
	if a == nil || a.registry == nil {
		return nil, errors.New("artifact authority is not configured")
	}
	return a.registry.Repository(ctx, repositoryID)
}

func cloneOutputRequirements(input *pebblestore.SessionArtifactOutputRequirements) *pebblestore.SessionArtifactOutputRequirements {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func equalOutputRequirements(left, right *pebblestore.SessionArtifactOutputRequirements) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneAnimationProfile(input *pebblestore.SessionArtifactAnimationProfile) *pebblestore.SessionArtifactAnimationProfile {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func equalAnimationProfile(left, right *pebblestore.SessionArtifactAnimationProfile) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func applyArtifactOutputRequirementsToPresentation(presentation *pebblestore.SessionArtifactPresentation, requirements *pebblestore.SessionArtifactOutputRequirements) error {
	if requirements == nil {
		return nil
	}
	if presentation == nil {
		return errors.New("artifact presentation is required")
	}
	if (presentation.Width != 0 && presentation.Width != requirements.Width) || (presentation.Height != 0 && presentation.Height != requirements.Height) {
		return errors.New("artifact presentation dimensions conflict with output requirements")
	}
	presentation.Width, presentation.Height = requirements.Width, requirements.Height
	return nil
}

func artifactPresentationRequirementsCompatible(existing, incoming pebblestore.SessionArtifactPresentation, requirements *pebblestore.SessionArtifactOutputRequirements) bool {
	if requirements == nil {
		return true
	}
	return existing.Width == requirements.Width && existing.Height == requirements.Height && incoming.Width == requirements.Width && incoming.Height == requirements.Height
}

func artifactDestinationLineageCompatible(existing, incoming pebblestore.SessionArtifactLineage) bool {
	// A managed destination is reserved before the child provider run is
	// allocated. Execution-attempt identifiers are therefore unavailable on the
	// placeholder and are not part of destination identity. Every parent/task/
	// program/child/iteration/source field remains immutable and must match.
	existing.RunID, existing.PlanID, existing.CheckpointID, existing.AttemptID = "", "", "", ""
	incoming.RunID, incoming.PlanID, incoming.CheckpointID, incoming.AttemptID = "", "", "", ""
	return existing == incoming
}

func (a *Authority) lineage(principal Principal, input CreateInput) pebblestore.SessionArtifactLineage {
	childSessionID := strings.TrimSpace(principal.ChildSessionID)
	sourceSessionID := strings.TrimSpace(input.SourceSessionID)
	if sourceSessionID == "" {
		sourceSessionID = principal.SessionID
		if childSessionID != "" {
			sourceSessionID = childSessionID
		}
	}
	return pebblestore.SessionArtifactLineage{
		ParentSessionID: principal.SessionID, SourceSessionID: sourceSessionID,
		SourceCollectionID: strings.TrimSpace(input.SourceCollectionID), SourceVariantID: strings.TrimSpace(input.SourceVariantID), SourceEventSeq: input.SourceEventSeq,
		TaskCallID: strings.TrimSpace(principal.TaskCallID), ProgramID: strings.TrimSpace(principal.ProgramID), ProgramJobID: strings.TrimSpace(principal.ProgramJobID),
		ChildSessionID: childSessionID, IterationGroupID: strings.TrimSpace(principal.IterationGroupID), IterationGroup: strings.TrimSpace(principal.IterationGroup),
		IterationID: strings.TrimSpace(principal.IterationID), IterationIndex: principal.IterationIndex, IterationLabel: strings.TrimSpace(principal.IterationLabel), IterationTheme: strings.TrimSpace(principal.IterationTheme),
		IterationSectionID: strings.TrimSpace(principal.IterationSectionID), IterationSectionLabel: strings.TrimSpace(principal.IterationSectionLabel), IterationSectionStartMs: principal.IterationSectionStartMs, IterationSectionEndMs: principal.IterationSectionEndMs,
		PartID: strings.TrimSpace(principal.PartID), PartLabel: strings.TrimSpace(principal.PartLabel), PartKind: strings.TrimSpace(principal.PartKind),
		SelectedReviewTargetIDs: strings.TrimSpace(principal.SelectedReviewTargetIDs),
		RunID:                   strings.TrimSpace(principal.RunID), PlanID: strings.TrimSpace(principal.PlanID), CheckpointID: strings.TrimSpace(principal.CheckpointID), AttemptID: strings.TrimSpace(principal.AttemptID),
		VideoProjectID: strings.TrimSpace(input.VideoProjectID), VideoRevisionID: strings.TrimSpace(input.VideoRevisionID), VideoRevisionEventSeq: input.VideoRevisionEventSeq,
	}
}

func (a *Authority) mutate(principal Principal, requestID, kind string, collection pebblestore.SessionArtifactCollection, variant *pebblestore.SessionArtifactVariant, selection *pebblestore.SessionArtifactSelectionReference) (pebblestore.V3SessionMutationResult, error) {
	return a.mutateWithArtifact(principal, requestID, kind, pebblestore.V3ArtifactMutation{Collection: collection, Variant: variant, Selection: selection})
}

func (a *Authority) mutateWithArtifact(principal Principal, requestID, kind string, artifactMutation pebblestore.V3ArtifactMutation) (pebblestore.V3SessionMutationResult, error) {
	requestID = strings.TrimSpace(requestID)
	if artifactMutation.Transaction == nil && !artifactMutation.ProjectionOnly {
		if err := a.attachGitProjection(context.Background(), principal, requestID, kind, &artifactMutation); err != nil {
			return pebblestore.V3SessionMutationResult{}, err
		}
	}
	if requestID == "" {
		return pebblestore.V3SessionMutationResult{}, errors.New("artifact request id is required")
	}
	payload := struct {
		Kind     string                         `json:"kind"`
		Artifact pebblestore.V3ArtifactMutation `json:"artifact"`
	}{kind, artifactMutation}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return pebblestore.V3SessionMutationResult{}, err
	}
	hash := sha256.Sum256(encoded)
	payloadHash := hex.EncodeToString(hash[:])
	keyHash := sha256.Sum256([]byte(strings.Join([]string{"managed-artifact", principal.SessionID, requestID, kind}, "\x00")))
	key := "managed-artifact-" + hex.EncodeToString(keyHash[:18])
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	return a.metadata.ApplySessionMutation(pebblestore.V3SessionMutationInput{SessionID: principal.SessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: kind, Artifact: &artifactMutation, NowUnixMs: now.UnixMilli()})
}
