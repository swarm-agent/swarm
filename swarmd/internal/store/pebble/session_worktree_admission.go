package pebblestore

import (
	"encoding/json"

	"github.com/cockroachdb/pebble"
)

// WorktreeAdmissionEvidence is trusted in-process evidence, never a decoded tool
// argument. The caller must verify Git registration and resolved filesystem
// identity before supplying it. Allocated means this operation actually created
// the lane; legacy means authenticated same-owner history was validated.
// The store independently verifies identities and all retained history claims.
type WorktreeAdmissionEvidence struct {
	Kind           string // allocated or legacy
	Path           string
	SourcePath     string
	OwnerSessionID string
	Branch         string
}

func (s *SessionStore) validateWorktreeAdmission(input V3SessionMutationInput, next SessionSnapshot) error {
	e := input.WorktreeAdmission
	if e == nil || (e.Kind != "allocated" && e.Kind != "legacy") ||
		e.Path != next.WorktreeRootPath || e.OwnerSessionID != input.SessionID ||
		!validWorktreePath(e.SourcePath) || e.SourcePath != next.WorkspacePath ||
		e.Branch == "" || e.Branch != next.WorktreeBranch {
		return ErrWorktreeRecoveryConflict
	}
	for key, expected := range map[string]string{
		"swarm_v3_worktree_owner_session_id": e.OwnerSessionID,
		"swarm_v3_source_workspace_path":     e.SourcePath,
		"swarm_v3_runtime_workspace_path":    e.Path,
	} {
		if value := v3LibraryMetadataString(next.Metadata, key); value != "" && value != expected {
			return ErrWorktreeRecoveryConflict
		}
	}
	current, exists, err := s.GetSession(input.SessionID)
	if err != nil {
		return err
	}
	if exists && (current.WorkspacePath != e.SourcePath || current.AccountScopeID != input.AccountScopeID || current.UserID != input.UserID) {
		return ErrWorktreeRecoveryConflict
	}
	if e.Kind == "allocated" {
		// Allocation is positive caller evidence, not inferred from an absent
		// claim. Historical conflicts still take precedence over that evidence.
		return s.validateWorktreeHistoryClaims(input, false)
	}
	return s.validateWorktreeHistoryClaims(input, true)
}

// A bounded complete scan includes every principal: principal-filtered discovery
// cannot disprove foreign ownership. Incomplete backfill or an exceeded bound
// fails closed. This runs under the canonical mutation lock.
func (s *SessionStore) validateWorktreeHistoryClaims(input V3SessionMutationInput, requireOwner bool) error {
	var meta repositoryHistoryMeta
	ok, err := s.store.GetJSON(repositoryHistoryMetaKey, &meta)
	if err != nil {
		return err
	}
	if !ok || !meta.Ready {
		return ErrRepositoryHistoryNotReady
	}
	prefix := "v3/repository_history/rows/"
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return err
	}
	defer iter.Close()
	matched := false
	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
		if count > 10000 {
			return ErrWorktreeRecoveryConflict
		}
		var row SessionRepositoryHistory
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			return err
		}
		owner := row.Session
		if owner.WorktreeRootPath != input.WorktreeAdmission.Path {
			continue
		}
		if owner.ID != input.SessionID || owner.AccountScopeID != input.AccountScopeID || owner.UserID != input.UserID ||
			owner.WorkspacePath != input.WorktreeAdmission.SourcePath || owner.WorktreeBranch != input.WorktreeAdmission.Branch || !owner.WorktreeEnabled || row.Deleted {
			return ErrWorktreeRecoveryConflict
		}
		matched = true
	}
	if err := iter.Error(); err != nil {
		return err
	}
	if requireOwner && !matched {
		return ErrWorktreeRecoveryConflict
	}
	return nil
}

// Retained integration destinations remain claims even after a program stops.
func (s *SessionStore) validateRetainedWorktreeProgramClaims(path, owner string) error {
	prefix := "task_program/"
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return err
	}
	defer iter.Close()
	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
		if count > 10000 {
			return ErrWorktreeRecoveryConflict
		}
		var program TaskProgramRecord
		if err := json.Unmarshal(iter.Value(), &program); err != nil {
			return err
		}
		if program.RepositoryLane != nil && program.RepositoryLane.WorkspacePath == path && program.ParentSessionID != owner {
			return ErrWorktreeRecoveryConflict
		}
		for _, job := range program.Jobs {
			if job.WorkspacePath == path && job.ChildSessionID != owner && job.CurrentSessionID != owner {
				return ErrWorktreeRecoveryConflict
			}
		}
	}
	return iter.Error()
}
