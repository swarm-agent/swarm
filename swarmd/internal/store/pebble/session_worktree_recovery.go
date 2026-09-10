package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble"
)

// WorktreeOwnership is a durable projection of canonical session mutations, not
// filesystem authorization. Git consumers must independently verify registration,
// resolved admin/common directories, source workspace generation and content.
// Records survive deletion and retain every prior owner; archive never releases.
type WorktreeOwnership struct {
	Path string `json:"path"`
	AccountScopeID string `json:"account_scope_id"`
	UserID string `json:"user_id"`
	OwnerSessionID string `json:"owner_session_id"`
	PreviousOwners []string `json:"previous_owners,omitempty"`
	Revision uint64 `json:"revision"`
	OperationID string `json:"operation_id,omitempty"`
	ClaimantSessionID string `json:"claimant_session_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

// WorktreeRecoveryMutation uses a caller-generated stable OperationID and exact
// inventory revision/owner. Evidence is a bounded digest of the Git consumer's
// independently authenticated snapshot. Publication must repeat that digest.
// Release only cancels a reservation; it never releases session ownership or
// deletes Git resources. Failed cleanup therefore remains safely reserved.
type WorktreeRecoveryMutation struct {
	Action string `json:"action"` // reserve, publish, release
	Path string `json:"path"`
	OwnerSessionID string `json:"owner_session_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	OperationID string `json:"operation_id"`
	Evidence string `json:"evidence"`
}

var ErrWorktreeRecoveryConflict = errors.New("worktree recovery evidence missing, unauthorized, stale or busy")

func worktreeOwnershipKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return "v3/worktree_ownership/" + hex.EncodeToString(sum[:])
}

func validWorktreePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && strings.TrimSpace(path) == path
}

// InspectWorktreeOwnership performs a bounded exact-path inventory lookup. A
// missing record is NOT an ownerless lane. No foreign attribution is returned.
func (s *SessionStore) InspectWorktreeOwnership(account, user string, paths []string) ([]WorktreeOwnership, error) {
	if account == "" || user == "" || len(paths) == 0 || len(paths) > 100 {
		return nil, ErrWorktreeRecoveryConflict
	}
	out := make([]WorktreeOwnership, 0, len(paths))
	for _, path := range paths {
		if !validWorktreePath(path) { return nil, ErrWorktreeRecoveryConflict }
		var record WorktreeOwnership
		ok, err := s.store.GetJSON(worktreeOwnershipKey(path), &record)
		if err != nil { return nil, err }
		if !ok || record.AccountScopeID != account || record.UserID != user { return nil, ErrWorktreeRecoveryConflict }
		out = append(out, record)
	}
	return out, nil
}

func (s *SessionStore) worktreeTransitionIdle(sessionID string) error {
	programs, err := s.ListTaskPrograms(sessionID)
	if err != nil { return err }
	for _, program := range programs {
		if program.State != TaskProgramStateRunning && program.State != TaskProgramStateDeclared { continue }
		for _, job := range program.Definition.Jobs {
			if job.AgentType == "designer" && (job.OutputMode == "" || job.OutputMode == "managed") { continue }
			return ErrWorktreeRecoveryConflict
		}
	}
	return nil
}

func (s *SessionStore) prepareWorktreeOwnership(input V3SessionMutationInput, next SessionSnapshot) (*WorktreeOwnership, error) {
	mutation := input.WorktreeRecovery
	if mutation == nil && input.Session == nil { return nil, nil }
	if mutation != nil && input.Session == nil {
		current, ok, err := s.GetSession(input.SessionID)
		if err != nil { return nil, err }
		if !ok { return nil, ErrWorktreeRecoveryConflict }
		next = current
	}
	path := next.WorktreeRootPath
	if mutation != nil { path = mutation.Path }
	if path == "" { return nil, nil }
	if !validWorktreePath(path) { return nil, ErrWorktreeRecoveryConflict }
	var record WorktreeOwnership
	found, err := s.store.GetJSON(worktreeOwnershipKey(path), &record)
	if err != nil { return nil, err }
	if found && (record.AccountScopeID != input.AccountScopeID || record.UserID != input.UserID) { return nil, ErrWorktreeRecoveryConflict }
	if mutation == nil {
		if !next.WorktreeEnabled { return nil, nil }
		if current, ok, err := s.GetSession(input.SessionID); err != nil { return nil, err } else if ok && (current.AccountScopeID != input.AccountScopeID || current.UserID != input.UserID) { return nil, ErrWorktreeRecoveryConflict }
		if found {
			if record.OwnerSessionID != input.SessionID || record.ClaimantSessionID != "" { return nil, ErrWorktreeRecoveryConflict }
			return nil, nil
		}
		// Legacy lanes require an explicit provenance migration; an adoption
		// request itself is not evidence of ownership.
		if input.Kind != V3SessionMutationCreateSession {
			current, ok, err := s.GetSession(input.SessionID)
			if err != nil { return nil, err }
			if ok && current.WorktreeRootPath == path { return nil, nil }
			return nil, ErrWorktreeRecoveryConflict
		}
		return &WorktreeOwnership{Path:path, AccountScopeID:input.AccountScopeID, UserID:input.UserID, OwnerSessionID:input.SessionID, Revision:1}, nil
	}
	if input.Kind != V3SessionMutationUpdateSettings || input.ExpectedLastEventSeq == nil || !found || mutation.ExpectedRevision != record.Revision || mutation.OwnerSessionID != record.OwnerSessionID || mutation.OperationID == "" || len(mutation.OperationID) > 128 || len(mutation.Evidence) != 64 {
		return nil, ErrWorktreeRecoveryConflict
	}
	if _, err := hex.DecodeString(mutation.Evidence); err != nil { return nil, ErrWorktreeRecoveryConflict }
	current, ok, err := s.GetSession(input.SessionID)
	if err != nil { return nil, err }
	if !ok || current.AccountScopeID != input.AccountScopeID || current.UserID != input.UserID { return nil, ErrWorktreeRecoveryConflict }
	if err := s.worktreeTransitionIdle(input.SessionID); err != nil { return nil, err }
	switch mutation.Action {
	case "reserve":
		if record.ClaimantSessionID != "" || record.OperationID == mutation.OperationID { return nil, ErrWorktreeRecoveryConflict }
		if next.WorktreeRootPath != current.WorktreeRootPath { return nil, ErrWorktreeRecoveryConflict }
		if record.OwnerSessionID != input.SessionID {
			if _, live, err := s.GetSession(record.OwnerSessionID); err != nil { return nil, err } else if live { return nil, ErrWorktreeRecoveryConflict }
			tombstone, exists, err := s.GetV3SessionTombstone(record.OwnerSessionID)
			if err != nil { return nil, err }
			// Absence alone is never proof that a writer has stopped.
			if !exists || !tombstone.Deleted || tombstone.AccountScopeID != input.AccountScopeID || tombstone.UserID != input.UserID { return nil, ErrWorktreeRecoveryConflict }
			if err := s.worktreeTransitionIdle(record.OwnerSessionID); err != nil { return nil, err }
		}
		record.OperationID, record.ClaimantSessionID, record.OperationState, record.Evidence = mutation.OperationID, input.SessionID, "reserved", mutation.Evidence
	case "publish", "release":
		if record.OperationState != "reserved" || record.ClaimantSessionID != input.SessionID || record.OperationID != mutation.OperationID || record.Evidence != mutation.Evidence { return nil, ErrWorktreeRecoveryConflict }
		if mutation.Action == "publish" {
			if input.Session == nil || !next.WorktreeEnabled || next.WorktreeRootPath != path || next.WorkspacePath != path { return nil, ErrWorktreeRecoveryConflict }
			if err := s.worktreeTransitionIdle(record.OwnerSessionID); err != nil { return nil, err }
			if record.OwnerSessionID != input.SessionID { record.PreviousOwners = append(record.PreviousOwners, record.OwnerSessionID) }
			record.OwnerSessionID, record.OperationState = input.SessionID, "published"
		} else {
			if next.WorktreeRootPath != current.WorktreeRootPath { return nil, ErrWorktreeRecoveryConflict }
			record.OperationState = "released"
		}
		record.ClaimantSessionID = ""
	default:
		return nil, ErrWorktreeRecoveryConflict
	}
	record.Revision++
	return &record, nil
}

func setWorktreeOwnershipInBatch(batch *pebble.Batch, record *WorktreeOwnership) error {
	if record == nil { return nil }
	payload, err := json.Marshal(record)
	if err != nil { return err }
	return batch.Set([]byte(worktreeOwnershipKey(record.Path)), payload, nil)
}
