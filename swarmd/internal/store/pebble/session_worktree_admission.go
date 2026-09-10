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
	// DelegatedCoder explicitly attests an isolated Coder allocation whose
	// WorkspacePath is the runtime root, not the original source. Never set
	// for shared Finder lanes or based solely on decoded session metadata.
	DelegatedCoder bool
}

func (s *SessionStore) validateWorktreeAdmission(input V3SessionMutationInput, next SessionSnapshot) error {
	e := input.WorktreeAdmission
	if agent := v3LibraryMetadataString(next.Metadata, "subagent"); agent == "finder" || agent == "system-finder" {
		return ErrWorktreeRecoveryConflict
	}
	if e == nil || (e.Kind != "allocated" && e.Kind != "legacy") ||
		e.Path != next.WorktreeRootPath || e.OwnerSessionID != input.SessionID ||
		!validWorktreePath(e.SourcePath) || !worktreeAdmissionSourceMatches(e, next) ||
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
	if exists && (current.AccountScopeID != input.AccountScopeID || current.UserID != input.UserID) {
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
			!worktreeAdmissionHistorySourceMatches(input.WorktreeAdmission, row) || owner.WorktreeBranch != input.WorktreeAdmission.Branch || !owner.WorktreeEnabled || row.Deleted {
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

// Historical rows are canonical projections, not live session snapshots:
// repositoryHistoricalWorktrees places the runtime lane in WorkspacePath and
// retains the original source in metadata and an additional workspace grant.
// Only the store-owned row discriminator permits this representation.
func worktreeAdmissionHistorySourceMatches(e *WorktreeAdmissionEvidence, row SessionRepositoryHistory) bool {
	if !row.HistoricalWorktree {
		return worktreeAdmissionSourceMatches(e, row.Session)
	}
	owner := row.Session
	if owner.WorkspacePath != e.Path || owner.WorktreeRootPath != e.Path ||
		v3LibraryMetadataString(owner.Metadata, "swarm_v3_source_workspace_path") != e.SourcePath {
		return false
	}
	for _, grant := range owner.WorkspaceGrants {
		if grant.Kind == WorkspaceGrantAdditional && grant.Path == e.SourcePath && grant.WorkspaceID != "" && grant.WorkspaceGeneration > 0 {
			return true
		}
	}
	return false
}

func worktreeAdmissionSourceMatches(e *WorktreeAdmissionEvidence, snapshot SessionSnapshot) bool {
	if !e.DelegatedCoder {
		return snapshot.WorkspacePath == e.SourcePath
	}
	return snapshot.WorkspacePath == e.Path &&
		v3LibraryMetadataString(snapshot.Metadata, "swarm_v3_source_workspace_path") == e.SourcePath &&
		v3LibraryMetadataString(snapshot.Metadata, "swarm_v3_runtime_workspace_path") == e.Path &&
		v3LibraryMetadataString(snapshot.Metadata, "swarm_v3_worktree_owner_session_id") == e.OwnerSessionID
}

// InspectRecoveryOwnership authorizes legacy content inspection from complete
// canonical history. This does not persist ownership: reservation revalidates the
// same evidence under the V3 mutation lock and publishes the claim atomically.
func (s *SessionStore) InspectRecoveryOwnership(account, user, source, path string) ([]WorktreeOwnership, error) {
	if account == "" || user == "" || !validWorktreePath(source) || !validWorktreePath(path) {
		return nil, ErrWorktreeRecoveryConflict
	}
	var record WorktreeOwnership
	found, err := s.store.GetJSON(worktreeOwnershipKey(path), &record)
	if err != nil {
		return nil, err
	}
	if found {
		return s.InspectWorktreeOwnership(account, user, []string{path})
	}
	var meta repositoryHistoryMeta
	ready, err := s.store.GetJSON(repositoryHistoryMetaKey, &meta)
	if err != nil {
		return nil, err
	}
	if !ready || !meta.Ready {
		return nil, ErrRepositoryHistoryNotReady
	}
	prefix := "v3/repository_history/rows/"
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
		if count > 10000 {
			return nil, ErrWorktreeRecoveryConflict
		}
		var row SessionRepositoryHistory
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			return nil, err
		}
		owner := row.Session
		if owner.WorktreeRootPath != path {
			continue
		}
		if owner.ID == "" || owner.AccountScopeID != account || owner.UserID != user || !owner.WorktreeEnabled || owner.WorkspacePath != source || owner.WorktreeBranch == "" || (record.OwnerSessionID != "" && record.OwnerSessionID != owner.ID) {
			return nil, ErrWorktreeRecoveryConflict
		}
		for key, expected := range map[string]string{"swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": path, "swarm_v3_worktree_owner_session_id": owner.ID} {
			if value := v3LibraryMetadataString(owner.Metadata, key); value != "" && value != expected {
				return nil, ErrWorktreeRecoveryConflict
			}
		}
		record = WorktreeOwnership{Path: path, AccountScopeID: account, UserID: user, OwnerSessionID: owner.ID, Revision: 1}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	if record.OwnerSessionID == "" {
		return nil, ErrWorktreeRecoveryConflict
	}
	return []WorktreeOwnership{record}, nil
}
