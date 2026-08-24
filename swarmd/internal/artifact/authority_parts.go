package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// CreateInitialComposition stores every initial part independently before one
// canonical V3 mutation atomically publishes definitions, exact revisions,
// composition, artifact step, projection, and realtime outbox state.
func (a *Authority) CreateInitialComposition(ctx context.Context, principal Principal, input CreateInitialCompositionInput) (pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.CollectionID, input.VariantID = strings.TrimSpace(input.CollectionID), strings.TrimSpace(input.VariantID)
	input.CollectionName, input.CollectionDescription = strings.TrimSpace(input.CollectionName), strings.TrimSpace(input.CollectionDescription)
	input.Filename, input.MediaType = strings.TrimSpace(input.Filename), strings.TrimSpace(input.MediaType)
	input.ArtifactChainID = strings.TrimSpace(input.ArtifactChainID)
	input.CompositionID = strings.TrimSpace(input.CompositionID)
	expectedChainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, input.CollectionID, input.VariantID)
	if input.RequestID == "" || input.CollectionID == "" || input.VariantID == "" || input.ArtifactChainID != expectedChainID || input.CompositionID == "" || len(input.Parts) < 2 || len(input.Parts) > pebblestore.SessionArtifactMaxParts {
		return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition requires a server-owned destination, chain, composition, and 2 or more bounded parts")
	}
	if len(input.Body) != 0 || len(input.CreateInput.Parts) != 0 {
		return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition cannot combine monolithic bytes or locator-only parts with real parts")
	}
	if input.SourceSessionID != "" || input.SourceCollectionID != "" || input.SourceVariantID != "" || input.SourceEventSeq != 0 {
		return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition cannot claim source lineage")
	}
	if input.Filename == "" || input.MediaType == "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition requires filename and complete media type")
	}
	if err := applyArtifactOutputRequirementsToPresentation(&input.Presentation, input.OutputRequirements); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	collection := pebblestore.SessionArtifactCollection{}
	existingStaging := false
	if existing, ok, getErr := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, input.CollectionID, input.VariantID); getErr != nil {
		return pebblestore.SessionArtifactVariant{}, getErr
	} else if ok {
		if existing.Status != pebblestore.SessionArtifactStatusStaging || existing.PartGraphState == pebblestore.SessionArtifactGraphAuthoritative || existing.Composition != nil {
			return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition destination already exists outside an empty staging placeholder")
		}
		lineage := a.lineage(principal, input.CreateInput)
		lineageCompatible := existing.Lineage == (pebblestore.SessionArtifactLineage{}) || artifactDestinationLineageCompatible(existing.Lineage, lineage)
		requirementsCompatible := equalOutputRequirements(existing.OutputRequirements, input.OutputRequirements) || (existing.OutputRequirements != nil && input.OutputRequirements == nil)
		animationCompatible := equalAnimationProfile(existing.AnimationProfile, input.AnimationProfile) || (existing.AnimationProfile != nil && input.AnimationProfile == nil)
		if !lineageCompatible || !requirementsCompatible || !animationCompatible || !artifactPresentationRequirementsCompatible(existing.Presentation, input.Presentation, existing.OutputRequirements) {
			return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition destination has incompatible trusted lineage, requirements, or presentation")
		}
		storedCollection, collectionOK, collectionErr := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, input.CollectionID)
		if collectionErr != nil {
			return pebblestore.SessionArtifactVariant{}, collectionErr
		}
		if !collectionOK {
			return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition staging collection metadata is missing")
		}
		collection, existingStaging = storedCollection, true
		input.OutputRequirements = cloneOutputRequirements(existing.OutputRequirements)
		input.AnimationProfile = cloneAnimationProfile(existing.AnimationProfile)
		if input.OutputRequirements != nil {
			input.Presentation.Width, input.Presentation.Height = input.OutputRequirements.Width, input.OutputRequirements.Height
		}
	}
	definitions := make([]pebblestore.SessionArtifactPartDefinition, 0, len(input.Parts))
	revisions := make([]pebblestore.SessionArtifactPartRevision, 0, len(input.Parts))
	composition := pebblestore.SessionArtifactComposition{ID: input.CompositionID, ArtifactChainID: input.ArtifactChainID, OwnerSessionID: principal.SessionID, Construction: input.Construction}
	if composition.Construction.Kind == "" {
		entries := make([]pebblestore.SessionArtifactConstructionEntry, 0, len(input.Parts))
		for _, part := range input.Parts {
			entries = append(entries, pebblestore.SessionArtifactConstructionEntry{PartID: strings.TrimSpace(part.Definition.ID)})
		}
		composition.Construction = pebblestore.SessionArtifactConstruction{Kind: "concat-v1", Entries: entries}
	}
	constructionValidation := composition
	constructionValidation.Parts = make([]pebblestore.SessionArtifactCompositionPart, 0, len(input.Parts))
	for _, part := range input.Parts {
		constructionValidation.Parts = append(constructionValidation.Parts, pebblestore.SessionArtifactCompositionPart{PartID: strings.TrimSpace(part.Definition.ID)})
	}
	if err := pebblestore.ValidateArtifactConstruction(constructionValidation); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	seen := make(map[string]struct{}, len(input.Parts))
	for _, part := range input.Parts {
		definition := part.Definition
		definition.ID, definition.Label, definition.Description = strings.TrimSpace(definition.ID), strings.TrimSpace(definition.Label), strings.TrimSpace(definition.Description)
		definition.ArtifactChainID = input.ArtifactChainID
		definition.OwnerSessionID = principal.SessionID
		if definition.ID == "" || definition.Label == "" || len(definition.Label) > 256 || len(definition.Description) > 2048 {
			return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition part identity or metadata is incomplete")
		}
		if _, duplicate := seen[definition.ID]; duplicate {
			return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition has duplicate stable part ids")
		}
		seen[definition.ID] = struct{}{}
		mediaType, _, mediaErr := mime.ParseMediaType(strings.TrimSpace(part.MediaType))
		if mediaErr != nil || mediaType == "" || len(part.Body) == 0 {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("initial artifact part %q requires non-empty independent bytes and media type", definition.ID)
		}
		revision := pebblestore.SessionArtifactPartRevision{ArtifactChainID: input.ArtifactChainID, PartID: definition.ID, ID: strings.TrimSpace(part.RevisionID), OwnerSessionID: principal.SessionID, MediaType: strings.ToLower(mediaType)}
		if revision.ID == "" {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("initial artifact part %q requires a server-owned revision identity", definition.ID)
		}
		revision.MediaType = strings.ToLower(mediaType)
		definitions = append(definitions, definition)
		revisions = append(revisions, revision)
		composition.Parts = append(composition.Parts, pebblestore.SessionArtifactCompositionPart{PartID: definition.ID, DefinitionOwnerSessionID: principal.SessionID, Revision: revision.Reference()})
	}
	lineage := a.lineage(principal, input.CreateInput)
	collectionLineage := lineage
	collectionLineage.SourceSessionID, collectionLineage.SourceCollectionID, collectionLineage.SourceVariantID, collectionLineage.SourceEventSeq = "", "", "", 0
	if !existingStaging {
		collection = pebblestore.SessionArtifactCollection{ID: strings.TrimSpace(input.CollectionID), Name: strings.TrimSpace(input.CollectionName), Description: strings.TrimSpace(input.CollectionDescription), Lineage: collectionLineage, Presentation: input.Presentation}
	}
	variantLineage := lineage
	if existingStaging {
		if existing, ok, _ := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, input.CollectionID, input.VariantID); ok && existing.Lineage != (pebblestore.SessionArtifactLineage{}) {
			variantLineage = existing.Lineage
		}
	}
	variant := pebblestore.SessionArtifactVariant{ID: strings.TrimSpace(input.VariantID), CollectionID: collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Filename: strings.TrimSpace(input.Filename), MediaType: strings.TrimSpace(input.MediaType), Presentation: input.Presentation, OutputRequirements: cloneOutputRequirements(input.OutputRequirements), AnimationProfile: cloneAnimationProfile(input.AnimationProfile), Lineage: variantLineage, ArtifactChainID: input.ArtifactChainID, ArtifactStepID: strings.TrimSpace(input.ArtifactStepID), RevisionRoundID: strings.TrimSpace(input.ArtifactStepID), CandidateIndex: input.CandidateIndex, AutoAccept: input.AutoAccept, PartDefinitions: definitions, Composition: &composition}
	if variant.ArtifactStepID == "" {
		variant.ArtifactStepID = input.CompositionID
		variant.RevisionRoundID = input.CompositionID
	}
	if variant.CandidateIndex < 1 {
		variant.CandidateIndex = 1
	}
	revisions, err = a.publishGitInitialComposition(ctx, input, &variant, &composition, revisions)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("publish initial artifact Git composition: %w", err)
	}
	variant.Composition = &composition
	compositionBytes, err := json.Marshal(composition)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("encode initial artifact composition projection: %w", err)
	}
	compositionDigest := sha256.Sum256(compositionBytes)
	variant.DigestSHA256, variant.Size = hex.EncodeToString(compositionDigest[:]), int64(len(compositionBytes))
	mutationKind := pebblestore.V3SessionMutationCreateArtifact
	if existingStaging {
		mutationKind = pebblestore.V3SessionMutationUpdateArtifact
	}
	result, err := a.mutateArtifact(principal, input.RequestID+":composition", mutationKind, collection, &variant, nil, definitions, revisions, &composition)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("persist initial artifact composition: %w", err)
	}
	if result.Artifact == nil || result.Artifact.Variant == nil || result.Artifact.Composition == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition was not persisted")
	}
	created := *result.Artifact.Variant
	created.Status, created.DigestSHA256, created.Size = pebblestore.SessionArtifactStatusReady, variant.DigestSHA256, variant.Size
	finalized, err := a.mutateArtifact(principal, input.RequestID+":composition-ready:"+created.DigestSHA256, pebblestore.V3SessionMutationFinalizeArtifact, collection, &created, nil, nil, nil, nil)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("finalize initial artifact composition: %w", err)
	}
	if finalized.Artifact == nil || finalized.Artifact.Variant == nil || finalized.Artifact.Variant.Status != pebblestore.SessionArtifactStatusReady || finalized.Artifact.Variant.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || finalized.Artifact.Variant.Composition == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("initial artifact composition did not become an authoritative ready variant")
	}
	return *finalized.Artifact.Variant, nil
}

func (a *Authority) ReadPartRevision(ctx context.Context, principal Principal, reference pebblestore.SessionArtifactPartRevisionReference, maxBytes int64) ([]byte, pebblestore.SessionArtifactPartRevision, error) {
	ownedPrincipal, err := a.owned(principal)
	if err != nil {
		return nil, pebblestore.SessionArtifactPartRevision{}, err
	}
	principal = ownedPrincipal
	revision, ok, err := a.metadata.GetSessionArtifactPartRevision(principal.AccountScopeID, principal.UserID, reference.OwnerSessionID, reference.ArtifactChainID, reference.PartID, reference.PartRevisionID)
	if err != nil {
		return nil, pebblestore.SessionArtifactPartRevision{}, err
	}
	if !ok || revision.Reference() != reference || revision.GraphState != pebblestore.SessionArtifactGraphAuthoritative {
		return nil, pebblestore.SessionArtifactPartRevision{}, errors.New("artifact part revision reference is missing, non-Git, or stale")
	}
	body, err := a.readGitPartRevision(ctx, revision, maxBytes)
	return body, revision, err
}

func (a *Authority) mutateArtifact(principal Principal, requestID, kind string, collection pebblestore.SessionArtifactCollection, variant *pebblestore.SessionArtifactVariant, selection *pebblestore.SessionArtifactSelectionReference, definitions []pebblestore.SessionArtifactPartDefinition, revisions []pebblestore.SessionArtifactPartRevision, composition *pebblestore.SessionArtifactComposition) (pebblestore.V3SessionMutationResult, error) {
	return a.mutateWithArtifact(principal, requestID, kind, pebblestore.V3ArtifactMutation{Collection: collection, Variant: variant, Selection: selection, PartDefinitions: definitions, PartRevisions: revisions, Composition: composition})
}
