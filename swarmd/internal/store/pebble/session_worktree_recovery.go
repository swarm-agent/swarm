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
	Path              string              `json:"path"`
	AccountScopeID    string              `json:"account_scope_id"`
	UserID            string              `json:"user_id"`
	OwnerSessionID    string              `json:"owner_session_id"`
	PreviousOwners    []string            `json:"previous_owners,omitempty"`
	Revision          uint64              `json:"revision"`
	OperationID       string              `json:"operation_id,omitempty"`
	ClaimantSessionID string              `json:"claimant_session_id,omitempty"`
	OperationState    string              `json:"operation_state,omitempty"`
	Evidence          string              `json:"evidence,omitempty"`
	CopySource        *WorktreeCopySource `json:"copy_source,omitempty"`
	DestinationPath   string              `json:"destination_path,omitempty"`
	DestinationBranch string              `json:"destination_branch,omitempty"`
	DestinationBase   string              `json:"destination_base,omitempty"`
}

// WorktreeCopySource is immutable attribution captured at copy publication.
// Copies reference their immediate source; ownership history is never flattened.
type WorktreeCopySource struct {
	Path           string `json:"path"`
	OwnerSessionID string `json:"owner_session_id"`
	Revision       uint64 `json:"revision"`
	Evidence       string `json:"evidence"`
}

// WorktreeRecoveryMutation uses a caller-generated stable OperationID and exact
// inventory revision/owner. Evidence is a bounded digest of the Git consumer's
// independently authenticated snapshot. Publication must repeat that digest.
// Release only cancels a reservation; it never releases session ownership or
// deletes Git resources. Failed cleanup therefore remains safely reserved.
type WorktreeRecoveryMutation struct {
	SourcePath        string `json:"source_path,omitempty"` // exact authenticated source for legacy migration
	Action            string `json:"action"`                // reserve, reserve_copy, publish, publish_copy, release
	Path              string `json:"path"`
	OwnerSessionID    string `json:"owner_session_id"`
	ExpectedRevision  uint64 `json:"expected_revision"`
	OperationID       string `json:"operation_id"`
	Evidence          string `json:"evidence"`
	DestinationPath   string `json:"destination_path,omitempty"`
	DestinationBranch string `json:"destination_branch,omitempty"`
	DestinationBase   string `json:"destination_base,omitempty"`
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
		if !validWorktreePath(path) {
			return nil, ErrWorktreeRecoveryConflict
		}
		var record WorktreeOwnership
		ok, err := s.store.GetJSON(worktreeOwnershipKey(path), &record)
		if err != nil {
			return nil, err
		}
		if !ok || record.AccountScopeID != account || record.UserID != user {
			return nil, ErrWorktreeRecoveryConflict
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *SessionStore) worktreeTransitionIdle(sessionID string) error {
	programs, err := s.ListTaskPrograms(sessionID)
	if err != nil {
		return err
	}
	for _, program := range programs {
		if program.State != TaskProgramStateRunning && program.State != TaskProgramStateDeclared {
			continue
		}
		for _, job := range program.Definition.Jobs {
			if job.AgentType == "designer" && (job.OutputMode == "" || job.OutputMode == "managed") {
				continue
			}
			return ErrWorktreeRecoveryConflict
		}
	}
	return nil
}

func (s *SessionStore) prepareWorktreeOwnership(input V3SessionMutationInput, next SessionSnapshot) ([]WorktreeOwnership, error) {
	// Run admission must not start a writer against a reserved source lane.
	if input.RunIntent != nil && (input.RunIntent.Status == V3RunIntentPendingExecutor || input.RunIntent.Status == V3RunIntentRunning) {
		current, exists, err := s.GetSession(input.SessionID)
		if err != nil {
			return nil, err
		}
		if exists && current.WorktreeRootPath != "" {
			var claim WorktreeOwnership
			found, err := s.store.GetJSON(worktreeOwnershipKey(current.WorktreeRootPath), &claim)
			if err != nil {
				return nil, err
			}
			if found && claim.ClaimantSessionID != "" {
				return nil, ErrWorktreeRecoveryConflict
			}
		}
	}
	mutation := input.WorktreeRecovery
	if mutation == nil && input.Session != nil {
		current, exists, err := s.GetSession(input.SessionID)
		if err != nil {
			return nil, err
		}
		if exists && current.WorktreeRootPath != "" {
			var claim WorktreeOwnership
			found, err := s.store.GetJSON(worktreeOwnershipKey(current.WorktreeRootPath), &claim)
			if err != nil {
				return nil, err
			}
			if found && claim.ClaimantSessionID != "" {
				return nil, ErrWorktreeRecoveryConflict
			}
		}
	}
	if mutation == nil && input.Session == nil {
		return nil, nil
	}
	if mutation != nil && input.Session == nil {
		current, ok, err := s.GetSession(input.SessionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrWorktreeRecoveryConflict
		}
		next = current
	}
	path := next.WorktreeRootPath
	if mutation != nil {
		path = mutation.Path
	}
	if path == "" {
		return nil, nil
	}
	if !validWorktreePath(path) {
		return nil, ErrWorktreeRecoveryConflict
	}
	claimOwner := input.SessionID
	if mutation != nil && (mutation.Action == "reserve_copy" || mutation.Action == "publish_copy" || mutation.Action == "journal_copy" || mutation.Action == "release") {
		claimOwner = mutation.OwnerSessionID
	}
	if err := s.validateRetainedWorktreeProgramClaims(path, claimOwner); err != nil {
		return nil, err
	}
	var record WorktreeOwnership
	found, err := s.store.GetJSON(worktreeOwnershipKey(path), &record)
	if err != nil {
		return nil, err
	}
	if !found && mutation != nil && (mutation.Action == "reserve" || mutation.Action == "reserve_copy") {
		// Migration is part of this reservation's atomic V3 batch, never a
		// discovery side effect or a missing-record ownership inference.
		claims, err := s.InspectRecoveryOwnership(input.AccountScopeID, input.UserID, mutation.SourcePath, path)
		if err != nil {
			return nil, err
		}
		if len(claims) != 1 {
			return nil, ErrWorktreeRecoveryConflict
		}
		record, found = claims[0], true
	}
	if found && (record.AccountScopeID != input.AccountScopeID || record.UserID != input.UserID) {
		return nil, ErrWorktreeRecoveryConflict
	}
	if mutation == nil {
		if !next.WorktreeEnabled {
			return nil, nil
		}
		if current, ok, err := s.GetSession(input.SessionID); err != nil {
			return nil, err
		} else if ok && (current.AccountScopeID != input.AccountScopeID || current.UserID != input.UserID) {
			return nil, ErrWorktreeRecoveryConflict
		}
		if found {
			if record.OwnerSessionID != input.SessionID || record.ClaimantSessionID != "" {
				return nil, ErrWorktreeRecoveryConflict
			}
			return nil, nil
		}
		// A create event is not allocation evidence. Every first claim,
		// including legacy same-owner updates, crosses this admission check.
		if err := s.validateWorktreeAdmission(input, next); err != nil {
			return nil, err
		}
		return []WorktreeOwnership{{Path: path, AccountScopeID: input.AccountScopeID, UserID: input.UserID, OwnerSessionID: input.SessionID, Revision: 1}}, nil
	}
	if input.Kind != V3SessionMutationUpdateSettings || input.ExpectedLastEventSeq == nil || !found || mutation.ExpectedRevision != record.Revision || mutation.OwnerSessionID != record.OwnerSessionID || mutation.OperationID == "" || len(mutation.OperationID) > 128 || len(mutation.Evidence) != 64 {
		return nil, ErrWorktreeRecoveryConflict
	}
	if _, err := hex.DecodeString(mutation.Evidence); err != nil {
		return nil, ErrWorktreeRecoveryConflict
	}
	current, ok, err := s.GetSession(input.SessionID)
	if err != nil {
		return nil, err
	}
	if !ok || current.AccountScopeID != input.AccountScopeID || current.UserID != input.UserID {
		return nil, ErrWorktreeRecoveryConflict
	}
	if err := s.worktreeTransitionIdle(input.SessionID); err != nil {
		return nil, err
	}
	switch mutation.Action {
	case "reserve", "reserve_copy":
		if record.ClaimantSessionID != "" || record.OperationID == mutation.OperationID {
			return nil, ErrWorktreeRecoveryConflict
		}
		if next.WorktreeRootPath != current.WorktreeRootPath || next.WorkspacePath != current.WorkspacePath {
			return nil, ErrWorktreeRecoveryConflict
		}
		if mutation.Action == "reserve_copy" {
			if err := s.worktreeSourceIdle(record.OwnerSessionID, input.SessionID); err != nil {
				return nil, err
			}
		}
		if record.OwnerSessionID != input.SessionID && mutation.Action != "reserve_copy" {
			if _, live, err := s.GetSession(record.OwnerSessionID); err != nil {
				return nil, err
			} else if live {
				return nil, ErrWorktreeRecoveryConflict
			}
			tombstone, exists, err := s.GetV3SessionTombstone(record.OwnerSessionID)
			if err != nil {
				return nil, err
			}
			// Absence alone is never proof that a writer has stopped.
			if !exists || !tombstone.Deleted || tombstone.AccountScopeID != input.AccountScopeID || tombstone.UserID != input.UserID {
				return nil, ErrWorktreeRecoveryConflict
			}
			if state, exists, err := s.GetV3SessionRunState(record.OwnerSessionID); err != nil {
				return nil, err
			} else if record.OwnerSessionID != input.SessionID && exists && state.Active {
				return nil, ErrWorktreeRecoveryConflict
			}
			if err := s.worktreeTransitionIdle(record.OwnerSessionID); err != nil {
				return nil, err
			}
		}
		record.DestinationPath, record.DestinationBranch, record.DestinationBase = "", "", ""
		record.OperationID, record.ClaimantSessionID, record.OperationState, record.Evidence = mutation.OperationID, input.SessionID, "reserved", mutation.Evidence
		if mutation.Action == "reserve_copy" {
			record.OperationState = "reserved_copy"
		}
	case "journal_copy":
		if record.OperationState != "reserved_copy" || record.ClaimantSessionID != input.SessionID || record.OperationID != mutation.OperationID || record.Evidence != mutation.Evidence || record.DestinationPath != "" || !validWorktreePath(mutation.DestinationPath) || mutation.DestinationPath == path || mutation.DestinationBranch == "" || !artifactV3OIDPattern.MatchString(mutation.DestinationBase) || input.Session != nil {
			return nil, ErrWorktreeRecoveryConflict
		}
		var existing WorktreeOwnership
		if exists, err := s.store.GetJSON(worktreeOwnershipKey(mutation.DestinationPath), &existing); err != nil {
			return nil, err
		} else if exists {
			return nil, ErrWorktreeRecoveryConflict
		}
		if err := s.validateRetainedWorktreeProgramClaims(mutation.DestinationPath, input.SessionID); err != nil {
			return nil, err
		}
		record.DestinationPath, record.DestinationBranch, record.DestinationBase = mutation.DestinationPath, mutation.DestinationBranch, mutation.DestinationBase
		record.Revision++
		destination := WorktreeOwnership{Path: mutation.DestinationPath, AccountScopeID: input.AccountScopeID, UserID: input.UserID, OwnerSessionID: input.SessionID, Revision: 1, OperationID: mutation.OperationID, ClaimantSessionID: input.SessionID, OperationState: "allocating_copy", Evidence: mutation.Evidence}
		return []WorktreeOwnership{record, destination}, nil
	case "publish", "publish_copy", "release":
		if (record.OperationState != "reserved" && record.OperationState != "reserved_copy") || record.ClaimantSessionID != input.SessionID || record.OperationID != mutation.OperationID || record.Evidence != mutation.Evidence {
			return nil, ErrWorktreeRecoveryConflict
		}
		if mutation.Action == "publish_copy" {
			if record.DestinationPath == "" || next.WorktreeRootPath != record.DestinationPath || next.WorktreeBranch != record.DestinationBranch || input.WorktreeAdmission == nil || v3LibraryMetadataString(next.Metadata, "swarm_v3_worktree_base_commit") != record.DestinationBase {
				return nil, ErrWorktreeRecoveryConflict
			}
			if record.OperationState != "reserved_copy" || input.Session == nil || next.WorkspacePath != current.WorkspacePath || !next.WorktreeEnabled || next.WorktreeRootPath == path || input.WorktreeAdmission == nil || input.WorktreeAdmission.Kind != "allocated" {
				return nil, ErrWorktreeRecoveryConflict
			}
			if err := s.worktreeSourceIdle(record.OwnerSessionID, input.SessionID); err != nil {
				return nil, err
			}
			if err := s.validateWorktreeAdmission(input, next); err != nil {
				return nil, err
			}
			if !validWorktreePath(next.WorktreeRootPath) {
				return nil, ErrWorktreeRecoveryConflict
			}
			if err := s.validateRetainedWorktreeProgramClaims(next.WorktreeRootPath, input.SessionID); err != nil {
				return nil, err
			}
			var existing WorktreeOwnership
			if exists, err := s.store.GetJSON(worktreeOwnershipKey(next.WorktreeRootPath), &existing); err != nil {
				return nil, err
			} else if !exists || existing.AccountScopeID != input.AccountScopeID || existing.UserID != input.UserID || existing.OwnerSessionID != input.SessionID || existing.ClaimantSessionID != input.SessionID || existing.OperationID != mutation.OperationID || existing.OperationState != "allocating_copy" {
				return nil, ErrWorktreeRecoveryConflict
			}
			destination := []WorktreeOwnership{{Path: next.WorktreeRootPath, AccountScopeID: input.AccountScopeID, UserID: input.UserID, OwnerSessionID: input.SessionID, Revision: existing.Revision + 1}}
			destination[0].CopySource = &WorktreeCopySource{Path: path, OwnerSessionID: record.OwnerSessionID, Revision: record.Revision, Evidence: record.Evidence}
			record.OperationState, record.ClaimantSessionID = "copied", ""
			record.Revision++
			return append(destination, record), nil
		}
		if mutation.Action == "publish" {
			if record.OperationState != "reserved" {
				return nil, ErrWorktreeRecoveryConflict
			}
			if input.Session == nil || !next.WorktreeEnabled || next.WorktreeRootPath != path || next.WorkspacePath != current.WorkspacePath {
				return nil, ErrWorktreeRecoveryConflict
			}
			if state, exists, err := s.GetV3SessionRunState(record.OwnerSessionID); err != nil {
				return nil, err
			} else if record.OwnerSessionID != input.SessionID && exists && state.Active {
				return nil, ErrWorktreeRecoveryConflict
			}
			if err := s.worktreeTransitionIdle(record.OwnerSessionID); err != nil {
				return nil, err
			}
			if record.OwnerSessionID != input.SessionID {
				if len(record.PreviousOwners) >= 128 {
					return nil, ErrWorktreeRecoveryConflict
				}
				record.PreviousOwners = append(record.PreviousOwners, record.OwnerSessionID)
			}
			record.OwnerSessionID, record.OperationState = input.SessionID, "published"
		} else {
			if next.WorktreeRootPath != current.WorktreeRootPath || next.WorkspacePath != current.WorkspacePath {
				return nil, ErrWorktreeRecoveryConflict
			}
			record.OperationState = "released"
			if record.DestinationPath != "" {
				var destination WorktreeOwnership
				found, err := s.store.GetJSON(worktreeOwnershipKey(record.DestinationPath), &destination)
				if err != nil {
					return nil, err
				}
				if !found || destination.ClaimantSessionID != input.SessionID || destination.OperationID != mutation.OperationID || destination.OperationState != "allocating_copy" {
					return nil, ErrWorktreeRecoveryConflict
				}
				destination.OperationState, destination.ClaimantSessionID = "retained_copy", ""
				destination.Revision++
				record.ClaimantSessionID = ""
				record.Revision++
				return []WorktreeOwnership{record, destination}, nil
			}
		}
		record.ClaimantSessionID = ""
	default:
		return nil, ErrWorktreeRecoveryConflict
	}
	record.Revision++
	return []WorktreeOwnership{record}, nil
}

func setWorktreeOwnershipInBatch(batch *pebble.Batch, records []WorktreeOwnership) error {
	for _, record := range records {
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(worktreeOwnershipKey(record.Path)), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) worktreeSourceIdle(owner, claimant string) error {
	if owner != claimant {
		_, live, err := s.GetSession(owner)
		if err != nil {
			return err
		}
		if !live {
			tombstone, exists, err := s.GetV3SessionTombstone(owner)
			if err != nil {
				return err
			}
			if !exists || !tombstone.Deleted {
				return ErrWorktreeRecoveryConflict
			}
		}
		state, exists, err := s.GetV3SessionRunState(owner)
		if err != nil {
			return err
		}
		if exists && state.Active {
			return ErrWorktreeRecoveryConflict
		}
	}
	return s.worktreeTransitionIdle(owner)
}

// Called under the session and ownership locks, matching V3 reservation order.
// Managed-only designs do not write repository lanes.
func (s *SessionStore) checkProgramRecoveryFence(record TaskProgramRecord) error {
	paths := []string{}
	for _, job := range record.Definition.Jobs {
		if job.AgentType == "designer" && (job.OutputMode == "" || job.OutputMode == "managed") {
			continue
		}
		paths = append(paths, job.WorkspacePath)
	}
	if len(paths) == 0 {
		return nil
	}
	current, exists, err := s.GetSession(record.ParentSessionID)
	if err != nil {
		return err
	}
	if exists {
		paths = append(paths, current.WorktreeRootPath)
	}
	if record.RepositoryLane != nil {
		paths = append(paths, record.RepositoryLane.WorkspacePath)
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		var claim WorktreeOwnership
		found, err := s.store.GetJSON(worktreeOwnershipKey(path), &claim)
		if err != nil {
			return err
		}
		if found && claim.ClaimantSessionID != "" {
			return ErrWorktreeRecoveryConflict
		}
	}
	return nil
}
