package artifact

import (
	"context"
	"errors"

	"swarm/packages/swarmd/internal/artifactgit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (a *Authority) publishGitPartSelection(ctx context.Context, input SelectPartRevisionsInput, source pebblestore.SessionArtifactVariant, variant *pebblestore.SessionArtifactVariant, composition *pebblestore.SessionArtifactComposition) error {
	if source.RepositoryID == "" || source.CommitOID == "" { return errors.New("part selection source is missing exact Git identity") }
	repo, err := a.repository(ctx, source.RepositoryID)
	if err != nil { return err }
	official, err := repo.Official(ctx)
	if err != nil { return err }
	parents := []string{official}
	seenParents := map[string]bool{official: true}
	if !seenParents[source.CommitOID] { parents = append(parents, source.CommitOID); seenParents[source.CommitOID] = true }
	selections := make(map[string]artifactgit.Selection, len(input.Choices))
	for _, choice := range input.Choices {
		if choice.Revision.RepositoryID != source.RepositoryID || choice.Revision.CommitOID == "" { return errors.New("part selection crosses artifact Git repositories") }
		if _, readErr := repo.ReadCommit(ctx, choice.Revision.CommitOID); readErr != nil { return readErr }
		actualBlob, blobErr := repo.ReadBlobOID(ctx, choice.Revision.CommitOID, choice.Revision.PartID)
		if blobErr != nil || actualBlob != choice.Revision.BlobOID { return artifactgit.ErrIntegrity }
		if !seenParents[choice.Revision.CommitOID] { parents = append(parents, choice.Revision.CommitOID); seenParents[choice.Revision.CommitOID] = true }
		locked := choice.Locked
		selections[choice.PartID] = artifactgit.Selection{Commit: choice.Revision.CommitOID, PartID: choice.Revision.PartID, Lock: &locked}
	}
	candidateID := artifactGitID("merge", input.ArtifactStepID, input.VariantID)
	var commitOID string
	if len(parents) == 1 {
		changes := make(map[string]artifactgit.PartChange, len(input.Choices))
		for _, choice := range input.Choices { locked := choice.Locked; changes[choice.PartID] = artifactgit.PartChange{Lock: &locked} }
		commitOID, err = repo.Candidate(ctx, artifactgit.CandidateRequest{ID: candidateID, Base: official, Parts: changes, Message: "artifact exact part lock"})
	} else {
		commitOID, err = repo.Merge(ctx, artifactgit.MergeRequest{ID: candidateID, Parents: parents, Selections: selections, Message: "artifact multi-parent part selection"})
	}
	if err != nil { return err }
	commit, err := repo.ReadCommit(ctx, commitOID)
	if err != nil { return err }
	variant.RepositoryID, variant.CommitOID, variant.TreeOID = source.RepositoryID, commitOID, commit.Tree
	variant.ParentCommitOIDs, variant.CandidateRef, variant.GraphState = append([]string(nil), commit.Parents...), "refs/swarm/candidates/"+candidateID, pebblestore.SessionArtifactGraphAuthoritative
	composition.RepositoryID, composition.CommitOID, composition.TreeOID = source.RepositoryID, commitOID, commit.Tree
	composition.ParentCommitOIDs = append([]string(nil), commit.Parents...)
	_, err = repo.AdvanceOfficial(ctx, official, commitOID, artifactGitID("tx", input.RequestID))
	return err
}
