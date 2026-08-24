package pebblestore

import (
	"errors"
	"fmt"
	"strings"
)

// SessionArtifactGitTransaction is the durable reconciliation record for one
// transaction already performed by the authoritative bare Git repository. It
// records CAS intent/result and exact refs, but does not perform or emulate Git.
type SessionArtifactGitTransaction struct {
	Version           int      `json:"version"`
	ID                string   `json:"id"`
	AccountScopeID    string   `json:"account_scope_id"`
	UserID            string   `json:"user_id"`
	OwnerSessionID    string   `json:"owner_session_id"`
	ArtifactChainID   string   `json:"artifact_chain_id"`
	RepositoryID      string   `json:"repository_id"`
	TransactionRef    string   `json:"transaction_ref"`
	CandidateRef      string   `json:"candidate_ref,omitempty"`
	OfficialRef       string   `json:"official_ref"`
	ExpectedOldOID    string   `json:"expected_old_oid,omitempty"`
	CommitOID         string   `json:"commit_oid"`
	ParentCommitOIDs  []string `json:"parent_commit_oids,omitempty"`
	ResultingOfficial string   `json:"resulting_official_oid,omitempty"`
	State             string   `json:"state"`
	CreatedAt         int64    `json:"created_at"`
	EventSeq          uint64   `json:"event_seq"`
}

// SessionArtifactGitLock projects a lock recorded in Git metadata. Exact blob
// and commit identities make the lock portable and independently verifiable.
type SessionArtifactGitLock struct {
	PartID       string `json:"part_id"`
	RepositoryID string `json:"repository_id"`
	CommitOID    string `json:"commit_oid"`
	BlobOID      string `json:"blob_oid"`
}

func KeySessionArtifactGitTransaction(accountScopeID, ownerSessionID, transactionID string) string {
	return fmt.Sprintf("v3/session_artifact/git_transactions/%s/%s/%s", keyPart(accountScopeID), keyPart(ownerSessionID), keyPart(transactionID))
}

func SessionArtifactGitTransactionSessionPrefix(accountScopeID, ownerSessionID string) string {
	return fmt.Sprintf("v3/session_artifact/git_transactions/%s/%s/", keyPart(accountScopeID), keyPart(ownerSessionID))
}

func (s *SessionStore) GetSessionArtifactGitTransaction(accountScopeID, userID, ownerSessionID, transactionID string) (SessionArtifactGitTransaction, bool, error) {
	if s == nil || s.store == nil {
		return SessionArtifactGitTransaction{}, false, errors.New("session store is not configured")
	}
	accountScopeID, userID, ownerSessionID, transactionID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), strings.TrimSpace(ownerSessionID), strings.TrimSpace(transactionID)
	if accountScopeID == "" || userID == "" || ownerSessionID == "" || transactionID == "" {
		return SessionArtifactGitTransaction{}, false, nil
	}
	var transaction SessionArtifactGitTransaction
	ok, err := s.store.GetJSON(KeySessionArtifactGitTransaction(accountScopeID, ownerSessionID, transactionID), &transaction)
	if err != nil || !ok {
		return SessionArtifactGitTransaction{}, ok, err
	}
	if transaction.AccountScopeID != accountScopeID || transaction.UserID != userID || transaction.OwnerSessionID != ownerSessionID || transaction.ID != transactionID {
		return SessionArtifactGitTransaction{}, false, errors.New("artifact Git transaction ownership metadata is inconsistent")
	}
	return transaction, true, nil
}

func normalizeArtifactGitTransaction(transaction *SessionArtifactGitTransaction) {
	if transaction == nil {
		return
	}
	transaction.ID = strings.TrimSpace(transaction.ID)
	transaction.OwnerSessionID = strings.TrimSpace(transaction.OwnerSessionID)
	transaction.ArtifactChainID = strings.TrimSpace(transaction.ArtifactChainID)
	transaction.RepositoryID = strings.TrimSpace(transaction.RepositoryID)
	transaction.TransactionRef = strings.TrimSpace(transaction.TransactionRef)
	transaction.CandidateRef = strings.TrimSpace(transaction.CandidateRef)
	transaction.OfficialRef = strings.TrimSpace(transaction.OfficialRef)
	transaction.ExpectedOldOID = strings.ToLower(strings.TrimSpace(transaction.ExpectedOldOID))
	transaction.CommitOID = strings.ToLower(strings.TrimSpace(transaction.CommitOID))
	transaction.ResultingOfficial = strings.ToLower(strings.TrimSpace(transaction.ResultingOfficial))
	transaction.State = strings.ToLower(strings.TrimSpace(transaction.State))
	for index := range transaction.ParentCommitOIDs {
		transaction.ParentCommitOIDs[index] = strings.ToLower(strings.TrimSpace(transaction.ParentCommitOIDs[index]))
	}
}

func validateArtifactRepositoryID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || value == "." || value == ".." || strings.ContainsAny(value, `/\\`) {
		return errors.New("artifact Git repository identity is invalid")
	}
	return nil
}

func validGitOID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}

func validArtifactGitRef(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < len("refs/x") || len(value) > 512 || !strings.HasPrefix(value, "refs/") || strings.HasSuffix(value, "/") || strings.Contains(value, "..") || strings.ContainsAny(value, " ~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validateCommitOIDs(values []string) error {
	if len(values) > SessionArtifactMaxCommitParents {
		return errors.New("artifact Git commit parent count exceeds bounds")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validGitOID(value) {
			return errors.New("artifact Git commit parent oid is invalid")
		}
		if _, ok := seen[value]; ok {
			return errors.New("artifact Git commit parents contain a duplicate")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateArtifactGitTransaction(transaction SessionArtifactGitTransaction, input V3SessionMutationInput) error {
	if transaction.ID == "" || transaction.OwnerSessionID != input.SessionID || transaction.ArtifactChainID == "" {
		return errors.New("artifact Git transaction identity or ownership is incomplete")
	}
	if err := validateArtifactID("Git transaction", transaction.ID); err != nil {
		return err
	}
	if err := validateArtifactRepositoryID(transaction.RepositoryID); err != nil {
		return err
	}
	if !validArtifactGitRef(transaction.TransactionRef) || !validArtifactGitRef(transaction.OfficialRef) || (transaction.CandidateRef != "" && !validArtifactGitRef(transaction.CandidateRef)) {
		return errors.New("artifact Git transaction ref is invalid")
	}
	if !validGitOID(transaction.CommitOID) || (transaction.ExpectedOldOID != "" && !validGitOID(transaction.ExpectedOldOID)) || (transaction.ResultingOfficial != "" && !validGitOID(transaction.ResultingOfficial)) {
		return errors.New("artifact Git transaction oid is invalid")
	}
	if err := validateCommitOIDs(transaction.ParentCommitOIDs); err != nil {
		return err
	}
	if transaction.State != "candidate" && transaction.State != "committed" && transaction.State != "deleted" {
		return errors.New("artifact Git transaction state is invalid")
	}
	if transaction.State == "committed" && transaction.ResultingOfficial != transaction.CommitOID {
		return errors.New("committed artifact Git transaction must project its commit as the official result")
	}
	return nil
}

func validateArtifactGitLocks(locks []SessionArtifactGitLock, repositoryID string) error {
	if len(locks) > SessionArtifactMaxLocks {
		return errors.New("artifact Git lock count exceeds bounds")
	}
	seen := make(map[string]struct{}, len(locks))
	for _, lock := range locks {
		if err := validateArtifactID("lock part", lock.PartID); err != nil {
			return err
		}
		if lock.RepositoryID != repositoryID || !validGitOID(lock.CommitOID) || !validGitOID(lock.BlobOID) {
			return errors.New("artifact Git lock does not identify an exact repository commit and blob")
		}
		if _, ok := seen[lock.PartID]; ok {
			return errors.New("artifact Git locks contain a duplicate part")
		}
		seen[lock.PartID] = struct{}{}
	}
	return nil
}
