package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"swarm/packages/swarmd/internal/artifactgit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func gitConstruction(in pebblestore.SessionArtifactConstruction) artifactgit.Construction {
	out := artifactgit.Construction{Kind: in.Kind, Entries: make([]artifactgit.ConstructionEntry, 0, len(in.Entries))}
	for _, entry := range in.Entries {
		out.Entries = append(out.Entries, artifactgit.ConstructionEntry{PartID: entry.PartID, Path: entry.Path})
	}
	return out
}

func (a *Authority) publishGitInitialComposition(ctx context.Context, input CreateInitialCompositionInput, variant *pebblestore.SessionArtifactVariant, composition *pebblestore.SessionArtifactComposition, revisions []pebblestore.SessionArtifactPartRevision) ([]pebblestore.SessionArtifactPartRevision, error) {
	variant.RepositoryID = input.ArtifactChainID
	repo, err := a.repository(ctx, variant.RepositoryID)
	if err != nil {
		return nil, err
	}
	parts := make(map[string]artifactgit.BlobInput, len(input.Parts))
	for _, part := range input.Parts {
		parts[part.Definition.ID] = artifactgit.BlobInput{MediaType: part.MediaType, Bytes: part.Body}
	}
	commitOID, err := repo.Genesis(ctx, artifactgit.Genesis{MediaType: variant.MediaType, Parts: parts, Construction: gitConstruction(composition.Construction)})
	if err != nil {
		return nil, err
	}
	commit, err := repo.ReadCommit(ctx, commitOID)
	if err != nil {
		return nil, err
	}
	variant.CommitOID, variant.TreeOID, variant.GraphState = commitOID, commit.Tree, pebblestore.SessionArtifactGraphAuthoritative
	composition.RepositoryID, composition.CommitOID, composition.TreeOID = variant.RepositoryID, commitOID, commit.Tree
	composition.ParentCommitOIDs = append([]string(nil), commit.Parents...)
	for index := range revisions {
		blobOID, blobErr := repo.ReadBlobOID(ctx, commitOID, revisions[index].PartID)
		if blobErr != nil {
			return nil, blobErr
		}
		body, readErr := repo.ReadBlob(ctx, commitOID, revisions[index].PartID)
		if readErr != nil {
			return nil, readErr
		}
		digest := sha256.Sum256(body)
		revisions[index].RepositoryID, revisions[index].CommitOID, revisions[index].BlobOID = variant.RepositoryID, commitOID, blobOID
		revisions[index].ParentCommitOIDs = append([]string(nil), commit.Parents...)
		revisions[index].DigestSHA256, revisions[index].Size = hex.EncodeToString(digest[:]), int64(len(body))
		for slotIndex := range composition.Parts {
			if composition.Parts[slotIndex].PartID == revisions[index].PartID {
				composition.Parts[slotIndex].Revision = revisions[index].Reference()
			}
		}
	}
	return revisions, nil
}

func (a *Authority) publishGitReplacement(ctx context.Context, input PublishPartReplacementsInput, source pebblestore.SessionArtifactVariant, variant *pebblestore.SessionArtifactVariant, composition *pebblestore.SessionArtifactComposition, revisions []pebblestore.SessionArtifactPartRevision) ([]pebblestore.SessionArtifactPartRevision, error) {
	if source.RepositoryID == "" || source.CommitOID == "" {
		return nil, errors.New("multipart source is missing exact Git identity")
	}
	repo, err := a.repository(ctx, source.RepositoryID)
	if err != nil {
		return nil, err
	}
	changes := make(map[string]artifactgit.PartChange, len(input.Replacements))
	for _, replacement := range input.Replacements {
		locked := replacement.Locked
		changes[replacement.PartDefinition.ID] = artifactgit.PartChange{MediaType: replacement.MediaType, Bytes: replacement.Body, Lock: &locked}
	}
	candidateID := artifactGitID("candidate", input.ArtifactStepID, input.VariantID)
	parents := []string{source.CommitOID}
	expectedOfficial := source.CommitOID
	if input.AutoAccept {
		expectedOfficial, err = repo.Official(ctx)
		if err == nil && expectedOfficial != source.CommitOID {
			parents = []string{expectedOfficial, source.CommitOID}
		}
	}
	if err != nil {
		return nil, err
	}
	commitOID, err := repo.Candidate(ctx, artifactgit.CandidateRequest{ID: candidateID, Base: source.CommitOID, Parents: parents, Parts: changes, Message: "artifact multipart candidate"})
	if err != nil {
		return nil, err
	}
	commit, err := repo.ReadCommit(ctx, commitOID)
	if err != nil {
		return nil, err
	}
	variant.RepositoryID, variant.CommitOID, variant.TreeOID = source.RepositoryID, commitOID, commit.Tree
	variant.ParentCommitOIDs, variant.CandidateRef, variant.GraphState = append([]string(nil), commit.Parents...), "refs/swarm/candidates/"+candidateID, pebblestore.SessionArtifactGraphAuthoritative
	composition.RepositoryID, composition.CommitOID, composition.TreeOID = source.RepositoryID, commitOID, commit.Tree
	composition.ParentCommitOIDs = append([]string(nil), commit.Parents...)
	changed := make(map[string]int, len(revisions))
	for index := range revisions {
		changed[revisions[index].PartID] = index
	}
	for slotIndex := range composition.Parts {
		partID := composition.Parts[slotIndex].PartID
		blobOID, blobErr := repo.ReadBlobOID(ctx, commitOID, partID)
		if blobErr != nil {
			return nil, blobErr
		}
		if index, ok := changed[partID]; ok {
			body, readErr := repo.ReadBlob(ctx, commitOID, partID)
			if readErr != nil {
				return nil, readErr
			}
			digest := sha256.Sum256(body)
			revisions[index].RepositoryID, revisions[index].CommitOID, revisions[index].BlobOID = source.RepositoryID, commitOID, blobOID
			revisions[index].ParentCommitOIDs = append([]string(nil), commit.Parents...)
			revisions[index].DigestSHA256, revisions[index].Size = hex.EncodeToString(digest[:]), int64(len(body))
			composition.Parts[slotIndex].Revision = revisions[index].Reference()
			continue
		}
		// Unchanged slots retain their immutable revision identity. Verify that the
		// new tree structurally reuses the exact projected blob.
		if composition.Parts[slotIndex].Revision.BlobOID != blobOID {
			return nil, artifactgit.ErrIntegrity
		}
	}
	if input.AutoAccept {
		if _, err = repo.AdvanceOfficial(ctx, expectedOfficial, commitOID, artifactGitID("tx", input.RequestID)); err != nil {
			return nil, err
		}
	}
	return revisions, nil
}

func (a *Authority) readGitPartRevision(ctx context.Context, revision pebblestore.SessionArtifactPartRevision, maxBytes int64) ([]byte, error) {
	if revision.RepositoryID == "" || revision.CommitOID == "" || revision.BlobOID == "" {
		return nil, errors.New("artifact part revision is missing exact Git identity")
	}
	repo, err := a.repository(ctx, revision.RepositoryID)
	if err != nil {
		return nil, err
	}
	actual, err := repo.ReadBlobOID(ctx, revision.CommitOID, revision.PartID)
	if err != nil {
		return nil, err
	}
	if actual != revision.BlobOID {
		return nil, artifactgit.ErrIntegrity
	}
	body, err := repo.ReadBlob(ctx, revision.CommitOID, revision.PartID)
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 || int64(len(body)) > maxBytes {
		return nil, errors.New("artifact part exceeds read byte bound")
	}
	return body, nil
}
