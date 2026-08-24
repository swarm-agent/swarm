package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (a *Authority) ResolvePartTarget(principal Principal, source pebblestore.SessionArtifactSelectionReference, partID string) (pebblestore.SessionArtifactVariant, pebblestore.SessionArtifactComposition, pebblestore.SessionArtifactPartDefinition, pebblestore.SessionArtifactPartRevisionReference, error) {
	variant, err := a.GetReference(principal, source)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, err
	}
	partID = strings.TrimSpace(partID)
	if variant.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || variant.Composition == nil || partID == "" {
		return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, errors.New("artifact source does not expose an authoritative selected part composition")
	}
	composition := *variant.Composition
	if variant.ArtifactChainID == "" || composition.ArtifactChainID != variant.ArtifactChainID || composition.OwnerSessionID != variant.SessionID {
		return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, errors.New("artifact source composition identity is inconsistent")
	}
	for _, selected := range composition.Parts {
		if selected.PartID != partID {
			continue
		}
		definition, ok, err := a.metadata.GetSessionArtifactPartDefinition(principal.AccountScopeID, principal.UserID, selected.DefinitionOwnerSessionID, composition.ArtifactChainID, partID)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, err
		}
		if !ok || definition.GraphState != pebblestore.SessionArtifactGraphAuthoritative {
			return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, errors.New("selected part definition is unavailable or non-Git")
		}
		revision, ok, err := a.metadata.GetSessionArtifactPartRevision(principal.AccountScopeID, principal.UserID, selected.Revision.OwnerSessionID, selected.Revision.ArtifactChainID, selected.Revision.PartID, selected.Revision.PartRevisionID)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, err
		}
		if !ok || revision.GraphState != pebblestore.SessionArtifactGraphAuthoritative || revision.Reference() != selected.Revision {
			return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, errors.New("selected source part revision is unavailable, mismatched, or stale")
		}
		return variant, composition, definition, selected.Revision, nil
	}
	return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, errors.New("selected part is not present in the authenticated source composition")
}

func equalAuthoritativePartDefinition(left, right pebblestore.SessionArtifactPartDefinition) bool {
	return left.Version == right.Version && left.GraphState == right.GraphState && left.ArtifactChainID == right.ArtifactChainID && left.ID == right.ID && left.AccountScopeID == right.AccountScopeID && left.UserID == right.UserID && left.OwnerSessionID == right.OwnerSessionID && left.Label == right.Label && left.Description == right.Description && reflect.DeepEqual(left.Locator, right.Locator) && left.CreatedAt == right.CreatedAt && left.EventSeq == right.EventSeq
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (a *Authority) PublishPartReplacement(ctx context.Context, principal Principal, input PublishPartReplacementInput) (pebblestore.SessionArtifactVariant, error) {
	return a.PublishPartReplacements(ctx, principal, PublishPartReplacementsInput{
		RequestID: input.RequestID, CallID: input.CallID, CollectionID: input.CollectionID, VariantID: input.VariantID,
		ArtifactStepID: input.ArtifactStepID, IterationTurnID: input.ArtifactStepID, IterationGroupID: firstNonEmpty(input.CallID, input.ArtifactStepID),
		CandidateIndex: input.CandidateIndex, AutoAccept: input.AutoAccept, SourceArtifact: input.SourceArtifact,
		SourceComposition: input.SourceComposition, Replacements: []PartReplacementInput{{PartDefinition: input.PartDefinition,
			SourcePartRevision: input.SourcePartRevision, Filename: input.Filename, MediaType: input.MediaType, Body: input.Body, Locked: input.Locked}},
	})
}

// PublishPartReplacements atomically publishes one composition revision containing
// one or more changed parts. Every candidate in the call shares durable turn/group
// identity, while each revision retains exact ancestry.
func (a *Authority) PublishPartReplacements(ctx context.Context, principal Principal, input PublishPartReplacementsInput) (pebblestore.SessionArtifactVariant, error) {
	principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	input.RequestID, input.CollectionID, input.VariantID = strings.TrimSpace(input.RequestID), strings.TrimSpace(input.CollectionID), strings.TrimSpace(input.VariantID)
	input.ArtifactStepID, input.IterationTurnID, input.IterationGroupID = strings.TrimSpace(input.ArtifactStepID), strings.TrimSpace(input.IterationTurnID), strings.TrimSpace(input.IterationGroupID)
	if input.RequestID == "" || input.CollectionID == "" || input.VariantID == "" || input.ArtifactStepID == "" || input.IterationTurnID == "" || input.IterationGroupID == "" || input.CandidateIndex < 1 || len(input.Replacements) == 0 || len(input.Replacements) > pebblestore.SessionArtifactMaxParts {
		return pebblestore.SessionArtifactVariant{}, errors.New("part replacements require trusted source, destination, turn, group, and one or more bounded replacements")
	}
	sourceVariant, err := a.GetReference(principal, input.SourceArtifact)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if sourceVariant.Composition == nil || sourceVariant.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || !reflect.DeepEqual(*sourceVariant.Composition, input.SourceComposition) {
		return pebblestore.SessionArtifactVariant{}, errors.New("part replacement trusted source composition changed before publication")
	}
	source := *sourceVariant.Composition
	composition := source
	composition.Parts = append([]pebblestore.SessionArtifactCompositionPart(nil), source.Parts...)
	composition.Parent = nil
	composition.IterationTurnID, composition.IterationGroupID = input.IterationTurnID, input.IterationGroupID
	seed := sha256.Sum256([]byte("artifact-composition-turn-v1\x00" + principal.SessionID + "\x00" + input.VariantID + "\x00" + input.RequestID))
	composition.ID, composition.OwnerSessionID = "composition-"+hex.EncodeToString(seed[:12]), principal.SessionID

	definitions := make([]pebblestore.SessionArtifactPartDefinition, 0, len(input.Replacements))
	revisions := make([]pebblestore.SessionArtifactPartRevision, 0, len(input.Replacements))
	changed := make(map[string]struct{}, len(input.Replacements))
	for index, replacement := range input.Replacements {
		partID := strings.TrimSpace(replacement.PartDefinition.ID)
		if partID == "" || strings.TrimSpace(replacement.MediaType) == "" || len(replacement.Body) == 0 {
			return pebblestore.SessionArtifactVariant{}, errors.New("each replacement requires part identity, media type, and non-empty bytes")
		}
		if _, duplicate := changed[partID]; duplicate {
			return pebblestore.SessionArtifactVariant{}, errors.New("a multipart iteration cannot replace the same part twice")
		}
		changed[partID] = struct{}{}
		_, authenticatedComposition, definition, sourceRevision, resolveErr := a.ResolvePartTarget(principal, input.SourceArtifact, partID)
		if resolveErr != nil {
			return pebblestore.SessionArtifactVariant{}, resolveErr
		}
		if !reflect.DeepEqual(authenticatedComposition, source) || !equalAuthoritativePartDefinition(definition, replacement.PartDefinition) || sourceRevision != replacement.SourcePartRevision {
			return pebblestore.SessionArtifactVariant{}, errors.New("multipart replacement contains a stale or conflicting part selection")
		}
		for _, slot := range source.Parts {
			if slot.PartID == partID && slot.Locked {
				return pebblestore.SessionArtifactVariant{}, fmt.Errorf("artifact part %q is locked to an exact revision", partID)
			}
		}
		revisionSeed := sha256.Sum256([]byte(fmt.Sprintf("artifact-part-turn-v1\x00%s\x00%s\x00%d", principal.SessionID, input.RequestID, index)))
		revision := pebblestore.SessionArtifactPartRevision{ArtifactChainID: source.ArtifactChainID, PartID: partID, ID: "part-revision-" + hex.EncodeToString(revisionSeed[:12]), OwnerSessionID: principal.SessionID, MediaType: replacement.MediaType, IterationTurnID: input.IterationTurnID, IterationGroupID: input.IterationGroupID}
		for slotIndex := range composition.Parts {
			if composition.Parts[slotIndex].PartID == partID {
				composition.Parts[slotIndex].Revision, composition.Parts[slotIndex].Locked = revision.Reference(), replacement.Locked
			}
		}
		definitions, revisions = append(definitions, definition), append(revisions, revision)
	}
	for index, slot := range composition.Parts {
		if _, wasChanged := changed[slot.PartID]; !wasChanged && slot != source.Parts[index] {
			return pebblestore.SessionArtifactVariant{}, errors.New("multipart replacement changed an untouched exact part reference")
		}
	}
	return a.publishReplacementComposition(ctx, principal, input, sourceVariant, composition, definitions, revisions)
}

func (a *Authority) publishReplacementComposition(ctx context.Context, principal Principal, input PublishPartReplacementsInput, sourceVariant pebblestore.SessionArtifactVariant, composition pebblestore.SessionArtifactComposition, definitions []pebblestore.SessionArtifactPartDefinition, revisions []pebblestore.SessionArtifactPartRevision) (pebblestore.SessionArtifactVariant, error) {
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, input.CollectionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("part replacement destination collection was not found")
		}
		return pebblestore.SessionArtifactVariant{}, err
	}
	lineage := a.lineage(principal, CreateInput{SourceSessionID: input.SourceArtifact.SessionID, SourceCollectionID: input.SourceArtifact.CollectionID, SourceVariantID: input.SourceArtifact.VariantID, SourceEventSeq: input.SourceArtifact.EventSeq})
	parent := input.SourceArtifact
	variant := pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Filename: sourceVariant.Filename, MediaType: sourceVariant.MediaType, Presentation: sourceVariant.Presentation, OutputRequirements: cloneOutputRequirements(sourceVariant.OutputRequirements), AnimationProfile: cloneAnimationProfile(sourceVariant.AnimationProfile), Lineage: lineage, ArtifactChainID: composition.ArtifactChainID, ArtifactStepID: input.ArtifactStepID, GraphState: pebblestore.SessionArtifactGraphAuthoritative, ParentArtifact: &parent, RevisionNumber: sourceVariant.RevisionNumber + 1, RevisionRoundID: input.ArtifactStepID, CandidateIndex: input.CandidateIndex, AutoAccept: input.AutoAccept, PartDefinitions: definitions, Composition: &composition}
	revisions, err = a.publishGitReplacement(ctx, input, sourceVariant, &variant, &composition, revisions)
	if err != nil { return pebblestore.SessionArtifactVariant{}, fmt.Errorf("publish multipart Git candidate: %w", err) }
	variant.Composition = &composition
	compositionBytes, err := json.Marshal(composition)
	if err != nil { return pebblestore.SessionArtifactVariant{}, fmt.Errorf("encode replacement composition projection: %w", err) }
	digest := sha256.Sum256(compositionBytes)
	variant.DigestSHA256, variant.Size = hex.EncodeToString(digest[:]), int64(len(compositionBytes))
	kind := pebblestore.V3SessionMutationCreateArtifact
	if existing, exists, getErr := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID); getErr != nil {
		return pebblestore.SessionArtifactVariant{}, getErr
	} else if exists {
		if existing.Status != pebblestore.SessionArtifactStatusStaging || !artifactDestinationLineageCompatible(existing.Lineage, lineage) || existing.AutoAccept != input.AutoAccept {
			return pebblestore.SessionArtifactVariant{}, errors.New("part replacement destination was already published or has incompatible trusted lineage")
		}
		variant.CreatedAt, variant.UpdatedAt, variant.EventSeq, kind = existing.CreatedAt, existing.UpdatedAt, existing.EventSeq, pebblestore.V3SessionMutationUpdateArtifact
		if existing.Lineage != (pebblestore.SessionArtifactLineage{}) {
			variant.Lineage = existing.Lineage
		}
		variant.OutputRequirements = cloneOutputRequirements(existing.OutputRequirements)
		variant.AnimationProfile = cloneAnimationProfile(existing.AnimationProfile)
	}
	result, err := a.mutateArtifact(principal, input.RequestID+":parts", kind, collection, &variant, nil, definitions, revisions, &composition)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("persist replacement part composition: %w", err)
	}
	if result.Artifact == nil || result.Artifact.Variant == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("replacement part composition was not persisted")
	}
	created := *result.Artifact.Variant
	created.Status, created.DigestSHA256, created.Size = pebblestore.SessionArtifactStatusReady, variant.DigestSHA256, variant.Size
	finalized, err := a.mutateArtifact(principal, input.RequestID+":parts-ready:"+created.DigestSHA256, pebblestore.V3SessionMutationFinalizeArtifact, collection, &created, nil, nil, nil, nil)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("finalize replacement part candidate: %w", err)
	}
	if finalized.Artifact == nil || finalized.Artifact.Variant == nil || finalized.Artifact.Variant.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("replacement part candidate did not become ready")
	}
	return *finalized.Artifact.Variant, nil
}
