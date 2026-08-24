package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
)

// applyV3ArtifactMutationForTest supplies the projection of an already
// completed test Git transaction. Artifact lifecycle tests exercise Pebble's
// projection/reconciliation boundary; the bare repository itself is covered by
// artifact and artifactgit package tests.
func applyV3ArtifactMutationForTest(sessions *SessionStore, input V3SessionMutationInput) (V3SessionMutationResult, error) {
	if input.Artifact != nil && input.Artifact.Transaction == nil && isV3ArtifactMutationKind(input.Kind) {
		mutation := artifactMutationWithGitProjection(input.SessionID, input.ClientRequestID, *input.Artifact)
		chainID := mutation.Transaction.ArtifactChainID
		var existing SessionArtifactVariant
		var existingOK bool
		if mutation.Selection != nil {
			existing, existingOK, _ = sessions.GetSessionArtifactVariant(input.AccountScopeID, input.SessionID, mutation.Selection.CollectionID, mutation.Selection.VariantID)
		} else if mutation.Variant != nil {
			existing, existingOK, _ = sessions.GetSessionArtifactVariant(input.AccountScopeID, input.SessionID, mutation.Collection.ID, mutation.Variant.ID)
		}
		if existingOK {
			chainID = existing.ArtifactChainID
			mutation.Transaction.RepositoryID = existing.RepositoryID
			if chain, chainOK, chainErr := sessions.GetSessionArtifactChain(input.AccountScopeID, input.UserID, existing.ArtifactChainID); chainErr == nil && chainOK {
				mutation.Transaction.OfficialRef = chain.OfficialRef
				mutation.Transaction.ExpectedOldOID = chain.OfficialCommitOID
			}
			if input.Kind == V3SessionMutationSelectArtifact {
				mutation.Transaction.CommitOID = existing.CommitOID
				mutation.Transaction.ParentCommitOIDs = append([]string(nil), existing.ParentCommitOIDs...)
				mutation.Transaction.State = "committed"
				mutation.Transaction.ResultingOfficial = mutation.Transaction.CommitOID
				mutation.Transaction.CandidateRef = ""
			}
		} else if mutation.Variant != nil {
			lineage := mutation.Variant.Lineage
			if lineage.SourceSessionID != "" && lineage.SourceCollectionID != "" && lineage.SourceVariantID != "" {
				if source, ok, err := sessions.GetSessionArtifactVariant(input.AccountScopeID, lineage.SourceSessionID, lineage.SourceCollectionID, lineage.SourceVariantID); err == nil && ok {
					chainID = source.ArtifactChainID
					mutation.Transaction.RepositoryID = source.RepositoryID
					if chain, chainOK, chainErr := sessions.GetSessionArtifactChain(input.AccountScopeID, input.UserID, source.ArtifactChainID); chainErr == nil && chainOK {
						mutation.Transaction.OfficialRef = chain.OfficialRef
					}
				}
			} else {
				chainID = artifactChainIDForRoot(SessionArtifactSelectionReference{SessionID: input.SessionID, CollectionID: mutation.Collection.ID, VariantID: mutation.Variant.ID})
			}
		}
		mutation.Transaction.ArtifactChainID = chainID
		if mutation.Composition != nil {
			mutation.Composition.RepositoryID = mutation.Transaction.RepositoryID
			for index := range mutation.PartRevisions {
				mutation.PartRevisions[index].RepositoryID = mutation.Transaction.RepositoryID
			}
			for index := range mutation.Composition.Parts {
				mutation.Composition.Parts[index].Revision.RepositoryID = mutation.Transaction.RepositoryID
			}
			if mutation.Variant != nil {
				composition := *mutation.Composition
				composition.Parts = append([]SessionArtifactCompositionPart(nil), mutation.Composition.Parts...)
				mutation.Variant.Composition = &composition
			}
		}
		if mutation.Transaction.OfficialRef == "" {
			refSum := sha256.Sum256([]byte(chainID))
			mutation.Transaction.OfficialRef = "refs/swarm/official/" + hex.EncodeToString(refSum[:])
		}
		input.Artifact = &mutation
	}
	return sessions.ApplyV3SessionMutation(input)
}

func artifactMutationWithGitProjection(sessionID, requestID string, mutation V3ArtifactMutation) V3ArtifactMutation {
	if mutation.Transaction != nil {
		return mutation
	}
	sum := sha256.Sum256([]byte(sessionID + "\x00" + requestID))
	oid := hex.EncodeToString(sum[:])
	chainID := "chain-" + mutation.Collection.ID
	if mutation.Variant != nil && mutation.Variant.ArtifactChainID != "" {
		chainID = mutation.Variant.ArtifactChainID
	}
	if mutation.Composition != nil && mutation.Composition.ArtifactChainID != "" {
		chainID = mutation.Composition.ArtifactChainID
	}
	mutation.Transaction = &SessionArtifactGitTransaction{
		ID:              "tx-" + oid[:24],
		OwnerSessionID:  sessionID,
		ArtifactChainID: chainID,
		RepositoryID:    "artifact-test-repository",
		TransactionRef:  "refs/swarm/transactions/" + oid,
		CandidateRef:    "refs/swarm/candidates/" + oid,
		OfficialRef:     "",
		CommitOID:       oid,
		State:           "candidate",
	}
	for index := range mutation.PartRevisions {
		revision := &mutation.PartRevisions[index]
		revision.RepositoryID = mutation.Transaction.RepositoryID
		revision.CommitOID = oid
		if revision.BlobOID == "" {
			blob := sha256.Sum256([]byte(oid + "\x00" + revision.PartID + "\x00" + revision.ID))
			revision.BlobOID = hex.EncodeToString(blob[:])
		}
	}
	if mutation.Composition != nil {
		if mutation.Composition.ArtifactChainID != "" {
			mutation.Transaction.ArtifactChainID = mutation.Composition.ArtifactChainID
		}
		mutation.Composition.RepositoryID = mutation.Transaction.RepositoryID
		mutation.Composition.CommitOID = oid
		if mutation.Composition.TreeOID == "" {
			tree := sha256.Sum256([]byte(oid + "\x00tree"))
			mutation.Composition.TreeOID = hex.EncodeToString(tree[:])
		}
		for partIndex := range mutation.Composition.Parts {
			part := &mutation.Composition.Parts[partIndex]
			part.Revision.RepositoryID = mutation.Transaction.RepositoryID
			part.Revision.CommitOID = oid
			if part.Revision.BlobOID == "" {
				blob := sha256.Sum256([]byte(oid + "\x00" + part.PartID + "\x00" + part.Revision.PartRevisionID))
				part.Revision.BlobOID = hex.EncodeToString(blob[:])
			}
			for revisionIndex := range mutation.PartRevisions {
				revision := mutation.PartRevisions[revisionIndex]
				if revision.PartID == part.PartID && revision.ID == part.Revision.PartRevisionID {
					part.Revision = revision.Reference()
					break
				}
			}
		}
		if mutation.Variant != nil {
			composition := *mutation.Composition
			composition.Parts = append([]SessionArtifactCompositionPart(nil), mutation.Composition.Parts...)
			mutation.Variant.Composition = &composition
		}
	}
	return mutation
}
