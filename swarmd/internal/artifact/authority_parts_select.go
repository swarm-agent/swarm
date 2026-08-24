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

// SelectPartRevisions publishes an immutable complete composition from exact
// existing part revisions. It changes no part bytes and advances the canonical
// chain head through the same one-candidate auto-accept path as AI publication.
func (a *Authority) SelectPartRevisions(ctx context.Context, principal Principal, input SelectPartRevisionsInput) (pebblestore.SessionArtifactVariant, error) {
	_, principal, err := a.owned(principal)
	if err != nil { return pebblestore.SessionArtifactVariant{}, err }
	input.RequestID, input.CollectionID, input.VariantID, input.ArtifactStepID = strings.TrimSpace(input.RequestID), strings.TrimSpace(input.CollectionID), strings.TrimSpace(input.VariantID), strings.TrimSpace(input.ArtifactStepID)
	if input.RequestID == "" || input.CollectionID == "" || input.VariantID == "" || input.ArtifactStepID == "" || len(input.Choices) == 0 || len(input.Choices) > pebblestore.SessionArtifactMaxParts {
		return pebblestore.SessionArtifactVariant{}, errors.New("part selection requires source, destination, step, and one or more bounded exact choices")
	}
	sourceVariant, err := a.GetReference(principal, input.SourceArtifact)
	if err != nil { return pebblestore.SessionArtifactVariant{}, err }
	if sourceVariant.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || sourceVariant.Composition == nil || !reflect.DeepEqual(*sourceVariant.Composition, input.SourceComposition) {
		return pebblestore.SessionArtifactVariant{}, errors.New("part selection source composition is stale")
	}
	source := *sourceVariant.Composition
	existing, existingDestination, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, input.CollectionID, input.VariantID)
	if err != nil { return pebblestore.SessionArtifactVariant{}, err }
	chain, ok, err := a.metadata.GetSessionArtifactChain(principal.AccountScopeID, principal.UserID, source.ArtifactChainID)
	if err != nil { return pebblestore.SessionArtifactVariant{}, err }
	if !existingDestination && (!ok || chain.GraphState != pebblestore.SessionArtifactGraphAuthoritative || chain.Head.SessionID != input.SourceArtifact.SessionID || chain.Head.CollectionID != input.SourceArtifact.CollectionID || chain.Head.VariantID != input.SourceArtifact.VariantID || chain.Head.EventSeq != input.SourceArtifact.EventSeq) {
		return pebblestore.SessionArtifactVariant{}, errors.New("part selection source is stale relative to the canonical chain head")
	}
	composition := source
	composition.Parts = append([]pebblestore.SessionArtifactCompositionPart(nil), source.Parts...)
	composition.Parent = &pebblestore.SessionArtifactCompositionReference{ArtifactChainID: source.ArtifactChainID, CompositionID: source.ID, OwnerSessionID: source.OwnerSessionID, EventSeq: source.EventSeq}
	seed := sha256.Sum256([]byte("artifact-part-selection-v1\x00" + principal.SessionID + "\x00" + input.RequestID))
	composition.ID, composition.OwnerSessionID = "composition-"+hex.EncodeToString(seed[:12]), principal.SessionID
	composition.IterationTurnID, composition.IterationGroupID = input.ArtifactStepID, input.ArtifactStepID

	seen := make(map[string]struct{}, len(input.Choices))
	for _, choice := range input.Choices {
		partID := strings.TrimSpace(choice.PartID)
		if partID == "" || choice.RevisionEventSeq == 0 { return pebblestore.SessionArtifactVariant{}, errors.New("each part selection requires stable part and revision event identities") }
		if _, duplicate := seen[partID]; duplicate { return pebblestore.SessionArtifactVariant{}, fmt.Errorf("part selection contains duplicate part %q", partID) }
		seen[partID] = struct{}{}
		slotIndex := -1
		for index := range composition.Parts { if composition.Parts[index].PartID == partID { slotIndex = index; break } }
		if slotIndex < 0 { return pebblestore.SessionArtifactVariant{}, fmt.Errorf("part selection %q is outside the source composition", partID) }
		definition, definitionOK, definitionErr := a.metadata.GetSessionArtifactPartDefinition(principal.AccountScopeID, principal.UserID, composition.Parts[slotIndex].DefinitionOwnerSessionID, source.ArtifactChainID, partID)
		if definitionErr != nil { return pebblestore.SessionArtifactVariant{}, definitionErr }
		if !definitionOK || definition.GraphState != pebblestore.SessionArtifactGraphAuthoritative { return pebblestore.SessionArtifactVariant{}, fmt.Errorf("part selection %q definition is unavailable", partID) }
		revision, revisionOK, revisionErr := a.metadata.GetSessionArtifactPartRevision(principal.AccountScopeID, principal.UserID, choice.Revision.OwnerSessionID, choice.Revision.ArtifactChainID, choice.Revision.PartID, choice.Revision.PartRevisionID)
		if revisionErr != nil { return pebblestore.SessionArtifactVariant{}, revisionErr }
		if !revisionOK || revision.GraphState != pebblestore.SessionArtifactGraphAuthoritative || revision.EventSeq != choice.RevisionEventSeq || revision.Reference() != choice.Revision || revision.ArtifactChainID != source.ArtifactChainID || revision.PartID != partID {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("part selection %q revision is stale, mixed, or mismatched", partID)
		}
		composition.Parts[slotIndex].Revision, composition.Parts[slotIndex].Locked = choice.Revision, choice.Locked
	}
	for index, slot := range composition.Parts { if _, changed := seen[slot.PartID]; !changed && slot != source.Parts[index] { return pebblestore.SessionArtifactVariant{}, errors.New("part selection changed an untouched exact part") } }
	if existingDestination {
		if existing.Status == pebblestore.SessionArtifactStatusReady && existing.Composition != nil && reflect.DeepEqual(existing.Composition.Parts, composition.Parts) && existing.Composition.Parent != nil && *existing.Composition.Parent == *composition.Parent { return existing, nil }
		return pebblestore.SessionArtifactVariant{}, errors.New("part selection idempotency destination conflicts with the exact request")
	}

	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, input.CollectionID)
	if err != nil { return pebblestore.SessionArtifactVariant{}, err }
	if !ok { collection = pebblestore.SessionArtifactCollection{ID: input.CollectionID, Name: "Part selections", Description: "Accepted exact part selections"} }
	compositionBytes, err := json.Marshal(composition)
	if err != nil { return pebblestore.SessionArtifactVariant{}, err }
	digest := sha256.Sum256(compositionBytes)
	lineage := a.lineage(principal, CreateInput{SourceSessionID: input.SourceArtifact.SessionID, SourceCollectionID: input.SourceArtifact.CollectionID, SourceVariantID: input.SourceArtifact.VariantID, SourceEventSeq: input.SourceArtifact.EventSeq})
	variant := pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: input.CollectionID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Filename: sourceVariant.Filename, MediaType: sourceVariant.MediaType, DigestSHA256: hex.EncodeToString(digest[:]), Size: int64(len(compositionBytes)), Presentation: sourceVariant.Presentation, OutputRequirements: cloneOutputRequirements(sourceVariant.OutputRequirements), AnimationProfile: cloneAnimationProfile(sourceVariant.AnimationProfile), Lineage: lineage, ArtifactChainID: source.ArtifactChainID, ArtifactStepID: input.ArtifactStepID, RevisionRoundID: input.ArtifactStepID, CandidateIndex: 1, AutoAccept: true, Composition: &composition}
	result, err := a.mutateArtifact(principal, input.RequestID+":part-selection", pebblestore.V3SessionMutationCreateArtifact, collection, &variant, nil, nil, nil, &composition)
	if err != nil { return pebblestore.SessionArtifactVariant{}, fmt.Errorf("persist part selection composition: %w", err) }
	if result.Artifact == nil || result.Artifact.Variant == nil { return pebblestore.SessionArtifactVariant{}, errors.New("part selection composition was not persisted") }
	created := *result.Artifact.Variant
	created.Status = pebblestore.SessionArtifactStatusReady
	finalized, err := a.mutateArtifact(principal, input.RequestID+":part-selection-ready:"+created.DigestSHA256, pebblestore.V3SessionMutationFinalizeArtifact, collection, &created, nil, nil, nil, nil)
	if err != nil { return pebblestore.SessionArtifactVariant{}, fmt.Errorf("finalize part selection composition: %w", err) }
	if finalized.Artifact == nil || finalized.Artifact.Variant == nil || finalized.Artifact.Variant.Status != pebblestore.SessionArtifactStatusReady { return pebblestore.SessionArtifactVariant{}, errors.New("part selection composition did not become ready") }
	return *finalized.Artifact.Variant, nil
}
