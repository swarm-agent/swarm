package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/artifactgit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func artifactGitID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func (a *Authority) publishGitVariant(ctx context.Context, principal Principal, input CreateInput, variant *pebblestore.SessionArtifactVariant, body []byte) error {
	chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, variant.CollectionID, variant.ID)
	var base string
	if input.SourceSessionID != "" {
		source, err := a.GetReference(principal, pebblestore.SessionArtifactSelectionReference{SessionID: input.SourceSessionID, CollectionID: input.SourceCollectionID, VariantID: input.SourceVariantID, EventSeq: input.SourceEventSeq})
		if err != nil {
			return err
		}
		if source.RepositoryID == "" || source.CommitOID == "" {
			return errors.New("source artifact is missing exact Git identity")
		}
		chainID, variant.RepositoryID, base = source.ArtifactChainID, source.RepositoryID, source.CommitOID
	}
	if variant.RepositoryID == "" {
		variant.RepositoryID = chainID
	}
	repo, err := a.repository(ctx, variant.RepositoryID)
	if err != nil {
		return err
	}
	if base == "" {
		variant.CommitOID, err = repo.Genesis(ctx, artifactgit.Genesis{MediaType: variant.MediaType, Content: &artifactgit.BlobInput{MediaType: variant.MediaType, Bytes: body}})
	} else {
		candidateID := artifactGitID("candidate", input.ArtifactStepID, variant.ID)
		parents := []string{base}
		expectedOfficial := base
		if input.AutoAccept {
			// A user may continue directly from any exact ready iteration, including a
			// sibling candidate that is not the current official head. Join that base
			// with the current official history and CAS from the actual official ref;
			// never retry the impossible stale CAS against the selected candidate.
			expectedOfficial, err = repo.Official(ctx)
			if err == nil && expectedOfficial != base {
				parents = []string{expectedOfficial, base}
			}
		}
		if err == nil {
			variant.CommitOID, err = repo.Candidate(ctx, artifactgit.CandidateRequest{ID: candidateID, Base: base, Parents: parents, Content: &artifactgit.BlobInput{MediaType: variant.MediaType, Bytes: body}, Message: "artifact candidate"})
		}
		variant.CandidateRef, variant.ParentCommitOIDs = "refs/swarm/candidates/"+candidateID, append([]string(nil), parents...)
		if err == nil && input.AutoAccept {
			_, err = repo.AdvanceOfficial(ctx, expectedOfficial, variant.CommitOID, artifactGitID("tx", input.RequestID))
		}
	}
	if err != nil {
		return fmt.Errorf("publish artifact Git commit: %w", err)
	}
	commit, err := repo.ReadCommit(ctx, variant.CommitOID)
	if err != nil {
		return err
	}
	variant.ArtifactChainID, variant.TreeOID, variant.GraphState = chainID, commit.Tree, pebblestore.SessionArtifactGraphAuthoritative
	return nil
}

func (a *Authority) readGitVariant(ctx context.Context, variant pebblestore.SessionArtifactVariant, maxBytes int64) ([]byte, error) {
	if variant.RepositoryID == "" || variant.CommitOID == "" {
		return nil, errors.New("artifact is missing exact Git identity")
	}
	repo, err := a.repository(ctx, variant.RepositoryID)
	if err != nil {
		return nil, err
	}
	body, err := repo.ReadBlob(ctx, variant.CommitOID, "content")
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 || int64(len(body)) > maxBytes {
		return nil, errors.New("artifact exceeds read byte bound")
	}
	return body, nil
}

func (a *Authority) attachGitProjection(ctx context.Context, principal Principal, requestID, kind string, mutation *pebblestore.V3ArtifactMutation) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errors.New("artifact request id is required")
	}
	variant := mutation.Variant
	if variant == nil && mutation.Selection != nil {
		selected, ok, err := a.metadata.GetSessionArtifactVariant(principal.AccountScopeID, principal.SessionID, mutation.Collection.ID, mutation.Selection.VariantID)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("selected artifact Git projection was not found")
		}
		variant = &selected
	}
	if variant == nil || variant.RepositoryID == "" || variant.CommitOID == "" || variant.ArtifactChainID == "" {
		return errors.New("artifact mutation is missing exact Git identity")
	}
	parents := append([]string(nil), variant.ParentCommitOIDs...)
	expected := ""
	if len(parents) > 0 {
		expected = parents[0]
	}
	state, resulting := "candidate", ""
	// Candidate/staging projection never claims an official move. The terminal
	// mutation records the already-completed Git CAS so crash replay can reconcile.
	if kind == pebblestore.V3SessionMutationFinalizeArtifact && (len(parents) == 0 || variant.AutoAccept) {
		state, resulting = "committed", variant.CommitOID
	}
	selectionCASCompleted := false
	if kind == pebblestore.V3SessionMutationSelectArtifact && (mutation.Selection == nil || mutation.Selection.Action != "use") {
		if variant.AutoAccept {
			state, resulting = "committed", variant.CommitOID
		} else {
			repo, err := a.repository(ctx, variant.RepositoryID)
			if err != nil {
				return err
			}
			currentOfficial, officialErr := repo.Official(ctx)
			if officialErr != nil {
				return officialErr
			}
			if currentOfficial == variant.CommitOID {
				chain, ok, chainErr := a.metadata.GetSessionArtifactChain(principal.AccountScopeID, principal.UserID, variant.ArtifactChainID)
				if chainErr != nil {
					return chainErr
				}
				if !ok {
					return errors.New("artifact Git chain projection was not found")
				}
				expected, state, resulting = chain.OfficialCommitOID, "committed", variant.CommitOID
			} else {
				chain, ok, err := a.metadata.GetSessionArtifactChain(principal.AccountScopeID, principal.UserID, variant.ArtifactChainID)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("artifact Git chain projection was not found")
				}
				expected = chain.OfficialCommitOID
				txID := artifactGitID("tx", requestID)
				if _, err = repo.AdvanceOfficial(ctx, expected, variant.CommitOID, txID); err != nil {
					return fmt.Errorf("advance artifact official ref from %q to %q with transaction %q: %w", expected, variant.CommitOID, txID, err)
				}
				state, resulting, selectionCASCompleted = "committed", variant.CommitOID, true
			}
		}
	}
	txID := artifactGitID("tx", requestID)
	repo, err := a.repository(ctx, variant.RepositoryID)
	if err != nil {
		return err
	}
	// Every projected transaction must have an authoritative immutable Git ref.
	// AdvanceOfficial creates it atomically with official; all other mutations
	// record it explicitly. Selection can replay after that atomic CAS, so an
	// existing exact transaction is accepted while a missing ref is a conflict.
	if kind == pebblestore.V3SessionMutationSelectArtifact && state == "committed" && resulting != "" && selectionCASCompleted {
		committed, txErr := repo.Transaction(ctx, txID)
		if txErr != nil || committed != variant.CommitOID {
			return artifactgit.ErrConflict
		}
	} else if err := repo.RecordTransaction(ctx, txID, variant.CommitOID); err != nil {
		return err
	}
	mutation.Transaction = &pebblestore.SessionArtifactGitTransaction{ID: txID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, OwnerSessionID: principal.SessionID, ArtifactChainID: variant.ArtifactChainID, RepositoryID: variant.RepositoryID, TransactionRef: "refs/swarm/transactions/" + txID, CandidateRef: variant.CandidateRef, OfficialRef: "refs/heads/official", ExpectedOldOID: expected, CommitOID: variant.CommitOID, ParentCommitOIDs: parents, ResultingOfficial: resulting, State: state}
	return nil
}
