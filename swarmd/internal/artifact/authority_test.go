package artifact

import (
	"errors"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type authorityMetadata struct {
	collection       pebblestore.SessionArtifactCollection
	sourceCollection pebblestore.SessionArtifactCollection
	variant          pebblestore.SessionArtifactVariant
	sourceVariant    pebblestore.SessionArtifactVariant
	readyCalls       int
	failReady        bool
	partDefinitions  map[string]pebblestore.SessionArtifactPartDefinition
	partRevisions    map[string]pebblestore.SessionArtifactPartRevision
	compositions     map[string]pebblestore.SessionArtifactComposition
	chainHead        pebblestore.SessionArtifactSelectionReference
}

func (m *authorityMetadata) GetSessionArtifactCollection(_, sessionID, id string) (pebblestore.SessionArtifactCollection, bool, error) {
	if m.sourceCollection.SessionID == sessionID && m.sourceCollection.ID == id {
		return m.sourceCollection, true, nil
	}
	return m.collection, m.collection.SessionID == sessionID && m.collection.ID == id, nil
}
func (m *authorityMetadata) GetSessionArtifactVariant(_, sessionID, collectionID, id string) (pebblestore.SessionArtifactVariant, bool, error) {
	if m.sourceVariant.SessionID == sessionID && m.sourceVariant.CollectionID == collectionID && m.sourceVariant.ID == id {
		return m.sourceVariant, true, nil
	}
	return m.variant, m.variant.SessionID == sessionID && m.variant.CollectionID == collectionID && m.variant.ID == id, nil
}
func (m *authorityMetadata) GetSessionArtifactVariantByID(_, _, id string) (pebblestore.SessionArtifactVariant, bool, error) {
	return m.variant, m.variant.ID == id, nil
}
func (m *authorityMetadata) ListSessionArtifactCollections(_, _, _ string, _ int) ([]pebblestore.SessionArtifactCollection, error) {
	if m.collection.ID == "" {
		return nil, nil
	}
	return []pebblestore.SessionArtifactCollection{m.collection}, nil
}
func (m *authorityMetadata) ListSessionArtifactVariants(_, _, collectionID string, _ int) ([]pebblestore.SessionArtifactVariant, error) {
	if m.variant.CollectionID != collectionID {
		return nil, nil
	}
	return []pebblestore.SessionArtifactVariant{m.variant}, nil
}
func (m *authorityMetadata) SearchSessionArtifactCatalog(_, _ string, _ pebblestore.SessionArtifactCatalogOptions) (pebblestore.SessionArtifactCatalogPage, error) {
	return pebblestore.SessionArtifactCatalogPage{}, nil
}
func (m *authorityMetadata) GetSessionArtifactPartDefinition(_, _, owner, chain, part string) (pebblestore.SessionArtifactPartDefinition, bool, error) {
	got, ok := m.partDefinitions[owner+"\x00"+chain+"\x00"+part]
	return got, ok, nil
}
func (m *authorityMetadata) GetSessionArtifactPartRevision(_, _, owner, chain, part, revision string) (pebblestore.SessionArtifactPartRevision, bool, error) {
	got, ok := m.partRevisions[owner+"\x00"+chain+"\x00"+part+"\x00"+revision]
	return got, ok, nil
}
func (m *authorityMetadata) GetSessionArtifactComposition(_, _, owner, chain, composition string) (pebblestore.SessionArtifactComposition, bool, error) {
	got, ok := m.compositions[owner+"\x00"+chain+"\x00"+composition]
	return got, ok, nil
}
func (m *authorityMetadata) GetSessionArtifactChain(_, _, chain string) (pebblestore.SessionArtifactChain, bool, error) {
	if m.chainHead.VariantID == "" {
		return pebblestore.SessionArtifactChain{}, false, nil
	}
	return pebblestore.SessionArtifactChain{GraphState: pebblestore.SessionArtifactGraphAuthoritative, ID: chain, Head: m.chainHead}, true, nil
}
func (m *authorityMetadata) ApplySessionMutation(input pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	projection := pebblestore.V3ArtifactProjection{Collection: input.Artifact.Collection, Variant: input.Artifact.Variant, Selection: input.Artifact.Selection}
	switch input.Kind {
	case pebblestore.V3SessionMutationCreateArtifact, pebblestore.V3SessionMutationUpdateArtifact:
		m.collection = input.Artifact.Collection
		m.collection.AccountScopeID, m.collection.SessionID, m.collection.Status = input.AccountScopeID, input.SessionID, pebblestore.SessionArtifactStatusStaging
		m.collection.VariantCount, m.collection.StagingCount = 1, 1
		m.variant = *input.Artifact.Variant
		m.variant.AccountScopeID, m.variant.SessionID, m.variant.CollectionID, m.variant.Status = input.AccountScopeID, input.SessionID, m.collection.ID, pebblestore.SessionArtifactStatusStaging
		m.variant.EventSeq, m.variant.ProjectionReservation = 1, input.Artifact.ProjectionOnly
		projection.Collection, projection.Variant = m.collection, &m.variant
		if m.partDefinitions == nil {
			m.partDefinitions = map[string]pebblestore.SessionArtifactPartDefinition{}
		}
		if m.partRevisions == nil {
			m.partRevisions = map[string]pebblestore.SessionArtifactPartRevision{}
		}
		if m.compositions == nil {
			m.compositions = map[string]pebblestore.SessionArtifactComposition{}
		}
		normalizedDefinitions := make([]pebblestore.SessionArtifactPartDefinition, 0, len(input.Artifact.PartDefinitions))
		for _, definition := range input.Artifact.PartDefinitions {
			definition.AccountScopeID, definition.UserID, definition.GraphState = input.AccountScopeID, input.UserID, pebblestore.SessionArtifactGraphAuthoritative
			m.partDefinitions[definition.OwnerSessionID+"\x00"+definition.ArtifactChainID+"\x00"+definition.ID] = definition
			normalizedDefinitions = append(normalizedDefinitions, definition)
		}
		for _, revision := range input.Artifact.PartRevisions {
			revision.AccountScopeID, revision.UserID, revision.GraphState, revision.EventSeq = input.AccountScopeID, input.UserID, pebblestore.SessionArtifactGraphAuthoritative, 1
			m.partRevisions[revision.OwnerSessionID+"\x00"+revision.ArtifactChainID+"\x00"+revision.PartID+"\x00"+revision.ID] = revision
		}
		if input.Artifact.Composition != nil {
			composition := *input.Artifact.Composition
			composition.AccountScopeID, composition.UserID, composition.GraphState, composition.EventSeq = input.AccountScopeID, input.UserID, pebblestore.SessionArtifactGraphAuthoritative, 1
			m.compositions[composition.OwnerSessionID+"\x00"+composition.ArtifactChainID+"\x00"+composition.ID] = composition
			m.variant.PartGraphState, m.variant.Composition, m.variant.PartDefinitions = pebblestore.SessionArtifactGraphAuthoritative, &composition, normalizedDefinitions
			projection.Variant = &m.variant
			projection.PartDefinitions, projection.PartRevisions, projection.Composition = normalizedDefinitions, input.Artifact.PartRevisions, &composition
		}
	case pebblestore.V3SessionMutationFinalizeArtifact:
		m.readyCalls++
		if m.failReady {
			return pebblestore.V3SessionMutationResult{}, errors.New("metadata unavailable")
		}
		currentComposition, currentDefinitions, currentPartGraphState := m.variant.Composition, m.variant.PartDefinitions, m.variant.PartGraphState
		m.variant = *input.Artifact.Variant
		if m.variant.Composition == nil {
			m.variant.Composition, m.variant.PartDefinitions, m.variant.PartGraphState = currentComposition, currentDefinitions, currentPartGraphState
		}
		m.variant.Status = pebblestore.SessionArtifactStatusReady
		if m.variant.EventSeq == 0 {
			m.variant.EventSeq = 1
		}
		if m.variant.AutoAccept {
			m.chainHead = pebblestore.SessionArtifactSelectionReference{SessionID: m.variant.SessionID, CollectionID: m.variant.CollectionID, VariantID: m.variant.ID, EventSeq: m.variant.EventSeq}
		}
		m.collection.Status = pebblestore.SessionArtifactStatusReady
		m.collection.StagingCount, m.collection.ReadyCount = 0, 1
		projection.Collection, projection.Variant, projection.Composition = m.collection, &m.variant, m.variant.Composition
	case pebblestore.V3SessionMutationFailArtifact:
		m.variant.Status, m.variant.FailureCode = pebblestore.SessionArtifactStatusFailed, input.Artifact.Variant.FailureCode
	case pebblestore.V3SessionMutationDeleteArtifactVariant:
		m.variant = pebblestore.SessionArtifactVariant{}
	case pebblestore.V3SessionMutationDeleteArtifactCollection:
		m.variant, m.collection = pebblestore.SessionArtifactVariant{}, pebblestore.SessionArtifactCollection{}
	}
	return pebblestore.V3SessionMutationResult{Artifact: &projection}, nil
}
