package artifact

import (
	"bytes"
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
	var selected *pebblestore.SessionArtifactCompositionPart
	for index := range composition.Parts {
		if composition.Parts[index].PartID == partID {
			copy := composition.Parts[index]
			selected = &copy
			break
		}
	}
	if selected == nil {
		return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, errors.New("selected part is not present in the authenticated source composition")
	}
	definition, ok, err := a.metadata.GetSessionArtifactPartDefinition(principal.AccountScopeID, principal.UserID, selected.DefinitionOwnerSessionID, composition.ArtifactChainID, partID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, err
	}
	if !ok || definition.GraphState != pebblestore.SessionArtifactGraphAuthoritative {
		return pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactComposition{}, pebblestore.SessionArtifactPartDefinition{}, pebblestore.SessionArtifactPartRevisionReference{}, errors.New("selected part definition is unavailable or legacy")
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

func equalAuthoritativePartDefinition(left, right pebblestore.SessionArtifactPartDefinition) bool {
	return left.Version == right.Version && left.GraphState == right.GraphState && left.ArtifactChainID == right.ArtifactChainID && left.ID == right.ID && left.AccountScopeID == right.AccountScopeID && left.UserID == right.UserID && left.OwnerSessionID == right.OwnerSessionID && left.Label == right.Label && left.Description == right.Description && reflect.DeepEqual(left.Locator, right.Locator) && left.CreatedAt == right.CreatedAt && left.EventSeq == right.EventSeq
}

func (a *Authority) PublishPartReplacement(ctx context.Context, principal Principal, input PublishPartReplacementInput) (pebblestore.SessionArtifactVariant, error) {
	service, principal, err := a.owned(principal)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	input.RequestID, input.CollectionID, input.VariantID = strings.TrimSpace(input.RequestID), strings.TrimSpace(input.CollectionID), strings.TrimSpace(input.VariantID)
	input.ArtifactStepID, input.Filename, input.MediaType = strings.TrimSpace(input.ArtifactStepID), strings.TrimSpace(input.Filename), strings.TrimSpace(input.MediaType)
	if input.RequestID == "" || input.CollectionID == "" || input.VariantID == "" || input.ArtifactStepID == "" || input.CandidateIndex < 1 || input.Filename == "" || input.MediaType == "" || len(input.Body) == 0 {
		return pebblestore.SessionArtifactVariant{}, errors.New("part replacement requires trusted source, destination, step, metadata, and non-empty bytes")
	}
	sourceVariant, sourceComposition, definition, sourceRevision, err := a.ResolvePartTarget(principal, input.SourceArtifact, input.PartDefinition.ID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	if !reflect.DeepEqual(sourceComposition, input.SourceComposition) || !equalAuthoritativePartDefinition(definition, input.PartDefinition) || sourceRevision != input.SourcePartRevision {
		return pebblestore.SessionArtifactVariant{}, errors.New("part replacement trusted source composition changed before publication")
	}
	if sourceVariant.ArtifactChainID != sourceComposition.ArtifactChainID {
		return pebblestore.SessionArtifactVariant{}, errors.New("part replacement source chain is inconsistent")
	}
	revisionIDSeed := sha256.Sum256([]byte("artifact-part-candidate-v1\x00" + principal.SessionID + "\x00" + input.VariantID + "\x00" + input.RequestID))
	revision := pebblestore.SessionArtifactPartRevision{ArtifactChainID: sourceComposition.ArtifactChainID, PartID: definition.ID, ID: "part-revision-" + hex.EncodeToString(revisionIDSeed[:12]), OwnerSessionID: principal.SessionID, MediaType: input.MediaType, Parent: &sourceRevision}
	staged, err := service.StagePart(ctx, revision, bytes.NewReader(input.Body))
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("stage replacement part: %w", err)
	}
	blob, err := service.FinalizePart(ctx, staged, staged.DigestSHA256, staged.Size)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("finalize replacement part: %w", err)
	}
	revision.DigestSHA256, revision.Size, revision.MediaType = blob.DigestSHA256, blob.Size, blob.MediaType
	composition := sourceComposition
	compositionIDSeed := sha256.Sum256([]byte("artifact-composition-candidate-v1\x00" + principal.SessionID + "\x00" + input.VariantID + "\x00" + input.ArtifactStepID))
	composition.ID, composition.OwnerSessionID = "composition-"+hex.EncodeToString(compositionIDSeed[:12]), principal.SessionID
	replaced := 0
	for index := range composition.Parts {
		if composition.Parts[index].PartID == definition.ID {
			composition.Parts[index].Revision = revision.Reference()
			replaced++
		}
	}
	if replaced != 1 || len(composition.Parts) != len(sourceComposition.Parts) {
		return pebblestore.SessionArtifactVariant{}, errors.New("part replacement must substitute exactly one source composition slot")
	}
	for index := range composition.Parts {
		if composition.Parts[index].PartID != definition.ID && composition.Parts[index] != sourceComposition.Parts[index] {
			return pebblestore.SessionArtifactVariant{}, errors.New("part replacement changed an untouched exact part reference")
		}
	}
	lineage := a.lineage(principal, CreateInput{SourceSessionID: input.SourceArtifact.SessionID, SourceCollectionID: input.SourceArtifact.CollectionID, SourceVariantID: input.SourceArtifact.VariantID, SourceEventSeq: input.SourceArtifact.EventSeq})
	compositionBytes, err := json.Marshal(composition)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("encode replacement composition projection: %w", err)
	}
	compositionDigest := sha256.Sum256(compositionBytes)
	collection, ok, err := a.metadata.GetSessionArtifactCollection(principal.AccountScopeID, principal.SessionID, input.CollectionID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("part replacement destination collection was not found")
		}
		return pebblestore.SessionArtifactVariant{}, err
	}
	variant := pebblestore.SessionArtifactVariant{ID: input.VariantID, CollectionID: collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Filename: sourceVariant.Filename, MediaType: sourceVariant.MediaType, DigestSHA256: hex.EncodeToString(compositionDigest[:]), Size: int64(len(compositionBytes)), Presentation: sourceVariant.Presentation, OutputRequirements: cloneOutputRequirements(sourceVariant.OutputRequirements), AnimationProfile: cloneAnimationProfile(sourceVariant.AnimationProfile), Lineage: lineage, ArtifactChainID: sourceComposition.ArtifactChainID, ArtifactStepID: input.ArtifactStepID, RevisionRoundID: input.ArtifactStepID, CandidateIndex: input.CandidateIndex, AutoAccept: input.AutoAccept, PartDefinitions: []pebblestore.SessionArtifactPartDefinition{definition}, Composition: &composition}
	mutationKind := pebblestore.V3SessionMutationCreateArtifact
	if existing, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	} else if ok {
		if existing.Status != pebblestore.SessionArtifactStatusStaging || !artifactDestinationLineageCompatible(existing.Lineage, lineage) || existing.AutoAccept != input.AutoAccept {
			return pebblestore.SessionArtifactVariant{}, errors.New("part replacement destination was already published or has incompatible trusted lineage")
		}
		variant.CreatedAt, variant.UpdatedAt, variant.EventSeq = existing.CreatedAt, existing.UpdatedAt, existing.EventSeq
		mutationKind = pebblestore.V3SessionMutationUpdateArtifact
	}
	result, err := a.mutateArtifact(principal, input.RequestID+":part", mutationKind, collection, &variant, nil, nil, []pebblestore.SessionArtifactPartRevision{revision}, &composition)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("persist replacement part composition: %w", err)
	}
	if result.Artifact == nil || result.Artifact.Variant == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("replacement part composition was not persisted")
	}
	created := *result.Artifact.Variant
	created.Status, created.DigestSHA256, created.Size = pebblestore.SessionArtifactStatusReady, hex.EncodeToString(compositionDigest[:]), int64(len(compositionBytes))
	finalized, err := a.mutateArtifact(principal, input.RequestID+":part-ready:"+created.DigestSHA256, pebblestore.V3SessionMutationFinalizeArtifact, collection, &created, nil, nil, nil, nil)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("finalize replacement part candidate: %w", err)
	}
	if finalized.Artifact == nil || finalized.Artifact.Variant == nil || finalized.Artifact.Variant.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, errors.New("replacement part candidate did not become ready")
	}
	return *finalized.Artifact.Variant, nil
}
