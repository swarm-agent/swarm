package pebblestore

import (
	"strings"
	"testing"
)

func gitOID(character string) string { return strings.Repeat(character, 40) }

func TestArtifactGitProjectionAcceptsHistoricalForkAndMultiParentMerge(t *testing.T) {
	root := gitOID("a")
	left := gitOID("b")
	right := gitOID("c")
	merge := gitOID("d")
	input := V3SessionMutationInput{SessionID: "session-1"}
	fork := SessionArtifactGitTransaction{
		ID: "fork-tx", OwnerSessionID: input.SessionID, ArtifactChainID: "artifact-1", RepositoryID: "repo-1",
		TransactionRef: "refs/swarm/transactions/fork", CandidateRef: "refs/swarm/candidates/fork", OfficialRef: "refs/swarm/official/artifact-1",
		ExpectedOldOID: right, CommitOID: left, ParentCommitOIDs: []string{root}, State: "candidate",
	}
	if err := validateArtifactGitTransaction(fork, input); err != nil {
		t.Fatalf("historical fork projection: %v", err)
	}
	merged := fork
	merged.ID = "merge-tx"
	merged.TransactionRef = "refs/swarm/transactions/merge"
	merged.CandidateRef = ""
	merged.ExpectedOldOID = right
	merged.CommitOID = merge
	merged.ParentCommitOIDs = []string{left, right}
	merged.ResultingOfficial = merge
	merged.State = "committed"
	if err := validateArtifactGitTransaction(merged, input); err != nil {
		t.Fatalf("multi-parent merge projection: %v", err)
	}
}

func TestArtifactGitProjectionExactPartBlobAndLocks(t *testing.T) {
	commit := gitOID("a")
	blob := gitOID("b")
	reference := SessionArtifactPartRevisionReference{
		ArtifactChainID: "artifact-1", PartID: "hero", PartRevisionID: "hero-r1", OwnerSessionID: "session-1",
		RepositoryID: "repo-1", CommitOID: commit, BlobOID: blob, Size: 12, MediaType: "text/html",
	}
	if err := validateArtifactPartRevisionReference(reference); err != nil {
		t.Fatalf("exact part/blob projection: %v", err)
	}
	if err := validateArtifactGitLocks([]SessionArtifactGitLock{{PartID: "hero", RepositoryID: "repo-1", CommitOID: commit, BlobOID: blob}}, "repo-1"); err != nil {
		t.Fatalf("exact lock projection: %v", err)
	}
}

func TestArtifactGitTransactionProjectionSurvivesRestartAndRejectsOwnershipMismatch(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	transaction := SessionArtifactGitTransaction{
		Version: SessionArtifactVersion, ID: "tx-1", AccountScopeID: "account-1", UserID: "user-1", OwnerSessionID: "session-1",
		ArtifactChainID: "artifact-1", RepositoryID: "repo-1", TransactionRef: "refs/swarm/transactions/tx-1",
		CandidateRef: "refs/swarm/candidates/tx-1", OfficialRef: "refs/swarm/official/artifact-1", CommitOID: gitOID("a"), State: "candidate", EventSeq: 7,
	}
	if err := store.PutJSON(KeySessionArtifactGitTransaction(transaction.AccountScopeID, transaction.OwnerSessionID, transaction.ID), transaction); err != nil {
		t.Fatal(err)
	}
	restarted := NewSessionStore(store)
	got, ok, err := restarted.GetSessionArtifactGitTransaction("account-1", "user-1", "session-1", "tx-1")
	if err != nil || !ok || got.CommitOID != transaction.CommitOID || got.EventSeq != 7 {
		t.Fatalf("restart projection = %+v ok=%t err=%v", got, ok, err)
	}
	if _, ok, err := restarted.GetSessionArtifactGitTransaction("account-1", "other-user", "session-1", "tx-1"); err == nil || ok {
		t.Fatalf("ownership mismatch was accepted: ok=%t err=%v", ok, err)
	}
}
