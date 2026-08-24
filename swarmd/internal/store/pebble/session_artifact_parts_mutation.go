package pebblestore

import (
	"errors"
	"fmt"
	"path"
	"reflect"
	"strings"
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
	if err := ValidateArtifactConstruction(composition); err != nil {
		return nil, nil, nil, err
	}
	if input.Artifact.Transaction == nil || composition.RepositoryID != input.Artifact.Transaction.RepositoryID || composition.CommitOID != input.Artifact.Transaction.CommitOID || !reflect.DeepEqual(composition.ParentCommitOIDs, input.Artifact.Transaction.ParentCommitOIDs) {
		return nil, nil, nil, errors.New("artifact composition must match the exact authoritative Git transaction")
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
		if revision.ArtifactChainID != composition.ArtifactChainID || revision.RepositoryID != composition.RepositoryID || revision.CommitOID != composition.CommitOID {
			return nil, nil, nil, errors.New("artifact part revision is outside the composition Git commit")
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

func ValidateArtifactConstruction(composition SessionArtifactComposition) error {
	if len(composition.Parts) == 0 || len(composition.Parts) > SessionArtifactMaxParts {
		return errors.New("artifact composition requires a bounded part graph")
	}
	parts := make(map[string]struct{}, len(composition.Parts))
	for _, part := range composition.Parts {
		if _, exists := parts[part.PartID]; exists {
			return errors.New("artifact composition contains duplicate part ids")
		}
		parts[part.PartID] = struct{}{}
	}
	construction := composition.Construction
	if construction.Kind != "concat-v1" && construction.Kind != "package-v1" {
		return errors.New("artifact composition construction kind is unsupported")
	}
	if len(construction.Entries) != len(composition.Parts) {
		return errors.New("artifact construction must reference every part exactly once")
	}
	seen := make(map[string]struct{}, len(construction.Entries))
	paths := make(map[string]struct{}, len(construction.Entries))
	for _, entry := range construction.Entries {
		if _, ok := parts[entry.PartID]; !ok {
			return errors.New("artifact construction references an unknown part")
		}
		if _, duplicate := seen[entry.PartID]; duplicate {
			return errors.New("artifact construction references a part more than once")
		}
		seen[entry.PartID] = struct{}{}
		if construction.Kind == "concat-v1" {
			if entry.Path != "" {
				return errors.New("concat artifact construction cannot declare package paths")
			}
			continue
		}
		clean := path.Clean(strings.TrimSpace(entry.Path))
		if clean == "." || clean != entry.Path || path.IsAbs(clean) || strings.HasPrefix(clean, "../") || strings.Contains(clean, "\\") {
			return errors.New("package artifact construction contains an unsafe path")
		}
		if _, duplicate := paths[clean]; duplicate {
			return errors.New("package artifact construction contains duplicate paths")
		}
		paths[clean] = struct{}{}
	}
	return nil
}

func partRevisionLookupKey(ownerSessionID, chainID, partID, revisionID string) string {
	return ownerSessionID + "\x00" + chainID + "\x00" + partID + "\x00" + revisionID
}
