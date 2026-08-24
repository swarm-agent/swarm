package pebblestore

import (
	"errors"
	"fmt"
	"reflect"
)

func (s *SessionStore) prepareAuthoritativeArtifactParts(input V3SessionMutationInput, seq uint64, now int64) ([]SessionArtifactPartDefinition, []SessionArtifactPartRevision, *SessionArtifactComposition, error) {
	mutation := input.Artifact
	if mutation == nil {
		return nil, nil, nil, nil
	}
	if mutation.Composition == nil {
		if len(mutation.PartDefinitions) != 0 || len(mutation.PartRevisions) != 0 {
			return nil, nil, nil, errors.New("artifact part definitions and revisions require an exact composition")
		}
		return nil, nil, nil, nil
	}
	if (input.Kind != V3SessionMutationCreateArtifact && input.Kind != V3SessionMutationUpdateArtifact) || mutation.Variant == nil {
		return nil, nil, nil, errors.New("artifact composition is accepted only while creating or filling an immutable staging revision")
	}
	composition := *mutation.Composition
	if composition.OwnerSessionID != input.SessionID {
		return nil, nil, nil, errors.New("artifact composition owner must match the mutation session")
	}
	if mutation.Variant.Composition == nil || !reflect.DeepEqual(*mutation.Variant.Composition, composition) {
		return nil, nil, nil, errors.New("artifact variant must carry the exact submitted composition")
	}
	if mutation.Variant.ArtifactChainID != "" && mutation.Variant.ArtifactChainID != composition.ArtifactChainID {
		return nil, nil, nil, errors.New("artifact variant and composition chain identities conflict")
	}

	definitions := make([]SessionArtifactPartDefinition, len(mutation.PartDefinitions))
	definitionByID := make(map[string]SessionArtifactPartDefinition, len(definitions))
	for index, definition := range mutation.PartDefinitions {
		definition.Version = SessionArtifactVersion
		definition.GraphState = SessionArtifactGraphAuthoritative
		definition.AccountScopeID = input.AccountScopeID
		definition.UserID = input.UserID
		definition.CreatedAt = now
		definition.EventSeq = seq
		if existing, ok, err := s.GetSessionArtifactPartDefinition(input.AccountScopeID, input.UserID, definition.OwnerSessionID, definition.ArtifactChainID, definition.ID); err != nil {
			return nil, nil, nil, err
		} else if ok {
			definition = existing
		} else if definition.OwnerSessionID != input.SessionID {
			return nil, nil, nil, errors.New("new artifact part definitions must be owned by the mutation session")
		}
		definitions[index] = definition
		definitionByID[definition.ID] = definition
	}

	revisions := make([]SessionArtifactPartRevision, len(mutation.PartRevisions))
	revisionByKey := make(map[string]SessionArtifactPartRevision, len(revisions))
	for index, revision := range mutation.PartRevisions {
		owner, ok, err := s.GetSession(revision.OwnerSessionID)
		if err != nil {
			return nil, nil, nil, err
		}
		if !ok || owner.AccountScopeID != input.AccountScopeID || owner.UserID != input.UserID {
			return nil, nil, nil, errors.New("artifact part revision owner is not authenticated")
		}
		if revision.ArtifactChainID != composition.ArtifactChainID {
			return nil, nil, nil, errors.New("artifact part revision is outside the composition chain")
		}
		revision.Version = SessionArtifactVersion
		revision.GraphState = SessionArtifactGraphAuthoritative
		revision.AccountScopeID = input.AccountScopeID
		revision.UserID = input.UserID
		revision.CreatedAt = now
		revision.EventSeq = seq
		if _, ok, err := s.GetSessionArtifactPartRevision(input.AccountScopeID, input.UserID, revision.OwnerSessionID, revision.ArtifactChainID, revision.PartID, revision.ID); err != nil {
			return nil, nil, nil, err
		} else if ok {
			return nil, nil, nil, fmt.Errorf("artifact part revision %q is immutable", revision.ID)
		}
		if revision.Parent != nil {
			parent, ok, err := s.GetSessionArtifactPartRevision(input.AccountScopeID, input.UserID, revision.Parent.OwnerSessionID, revision.Parent.ArtifactChainID, revision.Parent.PartID, revision.Parent.PartRevisionID)
			if err != nil {
				return nil, nil, nil, err
			}
			if !ok || !exactPartRevisionReference(*revision.Parent, parent) {
				return nil, nil, nil, errors.New("artifact part revision parent is missing or does not match exact immutable metadata")
			}
		}
		revisions[index] = revision
		revisionByKey[partRevisionLookupKey(revision.OwnerSessionID, revision.ArtifactChainID, revision.PartID, revision.ID)] = revision
	}

	for _, part := range composition.Parts {
		definition, ok := definitionByID[part.PartID]
		if !ok {
			var err error
			definition, ok, err = s.GetSessionArtifactPartDefinition(input.AccountScopeID, input.UserID, part.DefinitionOwnerSessionID, composition.ArtifactChainID, part.PartID)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		if !ok || definition.GraphState != SessionArtifactGraphAuthoritative || definition.ArtifactChainID != composition.ArtifactChainID || definition.OwnerSessionID != part.DefinitionOwnerSessionID {
			return nil, nil, nil, errors.New("artifact composition references a missing or unauthenticated part definition")
		}
		revision, ok := revisionByKey[partRevisionLookupKey(part.Revision.OwnerSessionID, part.Revision.ArtifactChainID, part.Revision.PartID, part.Revision.PartRevisionID)]
		if !ok {
			var err error
			revision, ok, err = s.GetSessionArtifactPartRevision(input.AccountScopeID, input.UserID, part.Revision.OwnerSessionID, part.Revision.ArtifactChainID, part.Revision.PartID, part.Revision.PartRevisionID)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		if !ok || revision.GraphState != SessionArtifactGraphAuthoritative || !exactPartRevisionReference(part.Revision, revision) {
			return nil, nil, nil, errors.New("artifact composition references a missing or mismatched exact part revision")
		}
	}
	if _, ok, err := s.GetSessionArtifactComposition(input.AccountScopeID, input.UserID, composition.OwnerSessionID, composition.ArtifactChainID, composition.ID); err != nil {
		return nil, nil, nil, err
	} else if ok {
		return nil, nil, nil, fmt.Errorf("artifact composition %q is immutable", composition.ID)
	}
	composition.Version = SessionArtifactVersion
	composition.GraphState = SessionArtifactGraphAuthoritative
	composition.AccountScopeID = input.AccountScopeID
	composition.UserID = input.UserID
	composition.CreatedAt = now
	composition.EventSeq = seq
	return definitions, revisions, &composition, nil
}

func partRevisionLookupKey(ownerSessionID, chainID, partID, revisionID string) string {
	return ownerSessionID + "\x00" + chainID + "\x00" + partID + "\x00" + revisionID
}
