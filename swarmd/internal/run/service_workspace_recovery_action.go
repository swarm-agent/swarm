package run

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Serializes recovery filesystem producers in this daemon. Durable reservations
// survive restarts; cancellation cannot release a reservation under a live producer.
var recoveryProducerMu sync.Mutex

type recoveryArguments struct {
	Owner                        string
	Revision                     uint64
	HEAD, Fingerprint, Operation string
	Files                        []string
}

func parseRecoveryArguments(raw map[string]any) (r recoveryArguments, err error) {
	action := strings.ToLower(strings.TrimSpace(mapString(raw, "action")))
	for _, key := range []string{"owner_session_id", "ownership_revision", "head", "fingerprint", "operation_id", "files"} {
		if _, ok := raw[key]; ok && action != "reclaim_worktree" && action != "copy_worktree" && action != "cancel_worktree_recovery" {
			return r, errors.New("recovery evidence is only accepted by recovery actions")
		}
	}
	if action != "reclaim_worktree" && action != "copy_worktree" && action != "cancel_worktree_recovery" {
		return r, nil
	}
	for key, dest := range map[string]*string{"owner_session_id": &r.Owner, "head": &r.HEAD, "fingerprint": &r.Fingerprint, "operation_id": &r.Operation} {
		if key == "head" && action == "cancel_worktree_recovery" {
			continue
		}
		value, ok := raw[key].(string)
		if !ok || value == "" || strings.TrimSpace(value) != value {
			return r, fmt.Errorf("recovery requires exact %s", key)
		}
		*dest = value
	}
	number, ok := raw["ownership_revision"].(json.Number)
	if !ok {
		return r, errors.New("recovery requires ownership_revision")
	}
	r.Revision, err = strconv.ParseUint(string(number), 10, 64)
	if err != nil || r.Revision == 0 {
		return r, errors.New("invalid ownership_revision")
	}
	if len(r.Operation) > 128 || len(r.Fingerprint) != 64 {
		return r, errors.New("invalid recovery operation or fingerprint")
	}
	if _, err = hex.DecodeString(r.Fingerprint); err != nil {
		return r, err
	}
	if action == "copy_worktree" {
		files, ok := raw["files"].([]any)
		if !ok || len(files) == 0 || len(files) > 2048 {
			return r, errors.New("copy_worktree requires explicit bounded files")
		}
		for _, value := range files {
			file, ok := value.(string)
			if !ok || file == "" {
				return r, errors.New("files must contain exact nonempty strings")
			}
			r.Files = append(r.Files, file)
		}
	} else if _, ok := raw["files"]; ok {
		return r, errors.New("reclaim_worktree does not import files")
	}
	return r, nil
}

// recoverSessionWorktree never changes source bytes. Durable reservation precedes
// snapshot/allocation; a failed publication retains its reservation and destination
// for explicit repair rather than reporting success or deleting recovered work.
func (s *Service) recoverSessionWorktree(sessionID string, principal identity.Principal, args manageWorkspaceArguments, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if !recoveryProducerMu.TryLock() {
		return "", errors.New("recovery producer is active; retry after it finishes")
	}
	defer recoveryProducerMu.Unlock()
	if apply == nil || s.worktrees == nil || s.sessionWorkspaceCanonicalize == nil {
		return "", errors.New("recovery requires canonical publisher and worktree service")
	}
	if args.WorkspaceID == "" || args.WorkspaceGeneration <= 0 || args.WorktreePath == "" || args.WorkspaceIDs != nil || args.PrimaryWorkspaceID != "" || args.WorkspacePathSet || args.WorkspaceNameSet || args.ThemeIDSet || args.ContentSet || args.ExpectedRevision != 0 || args.Intent != "" || args.PermissionScope != "" {
		return "", errors.New("recovery requires exact saved workspace identity and worktree_path only")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !ok || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID || mapString(session.Metadata, "lineage_kind") == "delegated_subagent" {
		return "", errors.New("recovery requires the owning primary session")
	}
	if err := s.sessions.EnsureWorkspaceTransitionIdle(sessionID); err != nil {
		return "", err
	}
	canonical, err := s.canonicalSessionWorkspace(principal, args.WorkspaceID, args.WorkspaceGeneration)
	if err != nil {
		return "", err
	}
	if session.WorkspacePath != canonical.SourceWorkspacePath || mapString(session.Metadata, "swarm_v3_source_workspace_id") != canonical.WorkspaceID || manageWorkspaceInt64(session.Metadata["swarm_v3_source_workspace_generation"]) != canonical.WorkspaceGeneration {
		return "", errors.New("recovery cannot change saved source identity")
	}
	if (args.Action == "reclaim_worktree" || args.Action == "cancel_worktree_recovery") && args.WorktreeName != "" {
		return "", errors.New("reclaim_worktree does not allocate a named destination")
	}
	if args.ExpectedWorktreePath != "" && args.ExpectedWorktreePath != session.WorktreeRootPath {
		return "", errors.New("stale current worktree")
	}
	if len(sessionWorktreeHistory(session.Metadata["swarm_v3_worktree_history"])) >= 63 {
		return "", errors.New("recovery history limit reached")
	}
	claims, err := s.sessions.Store().InspectRecoveryOwnership(principal.AccountScopeID, principal.UserID, canonical.SourceWorkspacePath, args.WorktreePath)
	if err != nil {
		return "", err
	}
	r := args.Recovery
	if len(claims) != 1 || claims[0].OwnerSessionID != r.Owner || claims[0].Revision != r.Revision {
		return "", pebblestore.ErrWorktreeRecoveryConflict
	}
	if args.Action == "cancel_worktree_recovery" {
		return s.cancelWorktreeRecovery(sessionID, principal, args, claims[0], apply)
	}
	source, err := worktreeruntime.InspectRecoveryWorktree(canonical.SourceWorkspacePath, args.WorktreePath)
	if err != nil {
		return "", err
	}
	if source.HEAD != r.HEAD || source.Fingerprint != r.Fingerprint || source.Path == canonical.SourceWorkspacePath {
		return "", errors.New("stale or nonisolated recovery source")
	}
	mutation := pebblestore.WorktreeRecoveryMutation{SourcePath: canonical.SourceWorkspacePath, Path: source.Path, OwnerSessionID: r.Owner, ExpectedRevision: r.Revision, OperationID: r.Operation, Evidence: r.Fingerprint}
	publish := func(action string, next *pebblestore.SessionSnapshot, admission *pebblestore.WorktreeAdmissionEvidence) (sessionruntime.SessionMutationResult, error) {
		mutation.Action = action
		payload, err := json.Marshal(map[string]any{"operation": mutation, "session": next})
		if err != nil {
			return sessionruntime.SessionMutationResult{}, err
		}
		key := manageWorkspaceMutationKey("manage-workspace-recovery-"+action, sessionID, payload)
		result, err := s.applyRecoveryPublication(session, apply, sessionruntime.SessionMutationInput{SessionID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: sessionruntime.SessionMutationUpdateSettings, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, EventType: "session.worktree.recovery." + action, EventPayload: payload, WorktreeRecovery: &mutation, WorktreeAdmission: admission, Session: next, NowUnixMs: time.Now().UnixMilli()})
		if err == nil && result.Conflict != nil {
			err = errors.New(result.Conflict.Message)
		}
		if err == nil && result.Error != nil {
			err = errors.New(result.Error.Message)
		}
		if err == nil {
			mutation.ExpectedRevision++
		}
		return result, err
	}
	reserve, finish := "reserve", "publish"
	if args.Action == "copy_worktree" {
		reserve, finish = "reserve_copy", "publish_copy"
	}
	if _, err := publish(reserve, nil, nil); err != nil {
		return "", err
	}
	allocation := worktreeruntime.Allocation{WorkspacePath: source.Path, BaseCommit: source.HEAD}
	retainedError := func(err error) (string, error) {
		return "", fmt.Errorf("recovery %s reserved; destination %q retained; publication not confirmed: %w", r.Operation, allocation.WorkspacePath, err)
	}
	releaseError := func(cause error) (string, error) {
		if _, err := publish("release", nil, nil); err != nil {
			return retainedError(errors.Join(cause, err))
		}
		return "", cause
	}
	var destination worktreeruntime.RecoveryIdentity
	if args.Action == "copy_worktree" {
		snapshot, err := worktreeruntime.SnapshotRecovery(canonical.SourceWorkspacePath, source, worktreeruntime.RecoverySelection{Files: r.Files})
		if err != nil {
			return releaseError(err)
		}
		branch, err := worktreeruntime.CanonicalizeRequestedWorktreeName(firstNonEmptyString(args.WorktreeName, "recovery-"+compactManageWorkspaceSessionID(sessionID)), "")
		if err != nil {
			return releaseError(err)
		}
		copier, ok := s.worktrees.(interface {
			CopyRecoveryJournaled(*worktreeruntime.RecoverySnapshot, string, string, func(worktreeruntime.Allocation) error) (worktreeruntime.RecoveryResult, error)
		})
		if !ok {
			return releaseError(errors.New("worktree recovery copy service unavailable"))
		}
		copied, err := copier.CopyRecoveryJournaled(snapshot, sessionID, branch, func(planned worktreeruntime.Allocation) error {
			mutation.DestinationPath, mutation.DestinationBranch, mutation.DestinationBase = planned.WorkspacePath, planned.BranchName, planned.BaseCommit
			_, err := publish("journal_copy", nil, nil)
			return err
		})
		allocation, destination = copied.Allocation, copied.Destination
		if err != nil {
			return retainedError(err)
		}
	} else {
		// Branch identity came from the bounded, no-follow Git inspection.
		if source.Branch == "" {
			return releaseError(errors.New("reclaim requires an attached branch"))
		}
		allocation.BranchName = source.Branch
		if session.WorktreeRootPath == source.Path {
			allocation.BaseBranch = session.WorktreeBaseBranch
			allocation.BaseCommit = firstNonEmptyString(mapString(session.Metadata, "swarm_v3_worktree_base_commit"), source.HEAD)
		}
		for _, item := range sessionWorktreeHistory(session.Metadata["swarm_v3_worktree_history"]) {
			if mapString(item, "path") == source.Path {
				allocation.BaseBranch = mapString(item, "base_branch")
				allocation.BaseCommit = firstNonEmptyString(mapString(item, "base_commit"), allocation.BaseCommit)
			}
		}
	}
	if err := worktreeruntime.ValidateOwnedIdentity(canonical.SourceWorkspacePath, allocation.WorkspacePath, allocation.BranchName, allocation.BaseCommit); err != nil {
		return retainedError(err)
	}
	next := session
	next.Metadata = cloneGenericMap(session.Metadata)
	next.WorktreeEnabled, next.WorktreeRootPath = true, allocation.WorkspacePath
	next.WorktreeBranch, next.WorktreeBaseBranch = allocation.BranchName, allocation.BaseBranch
	next.Metadata["swarm_v3_runtime_workspace_path"] = allocation.WorkspacePath
	next.Metadata["swarm_v3_mandatory_worktree"] = true
	next.Metadata["swarm_v3_worktree_owner_session_id"] = sessionID
	next.Metadata["swarm_v3_worktree_base_commit"], next.Metadata["base_commit"] = allocation.BaseCommit, allocation.BaseCommit
	if session.WorktreeEnabled {
		prior := worktreeruntime.Allocation{WorkspacePath: session.WorktreeRootPath, BranchName: session.WorktreeBranch, BaseBranch: session.WorktreeBaseBranch, BaseCommit: mapString(session.Metadata, "swarm_v3_worktree_base_commit")}
		next.Metadata["swarm_v3_worktree_history"] = appendSessionWorktreeHistory(next.Metadata["swarm_v3_worktree_history"], canonical, prior, sessionID)
	}
	next.Metadata["swarm_v3_worktree_history"] = appendSessionWorktreeHistory(next.Metadata["swarm_v3_worktree_history"], canonical, allocation, sessionID)
	grants := []pebblestore.WorkspaceGrant{}
	for _, grant := range pebblestore.NormalizeSessionWorkspaceGrants(session) {
		if grant.Kind != pebblestore.WorkspaceGrantWorktree {
			grants = append(grants, grant)
		}
	}
	available := true
	grants = append(grants, pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantWorktree, WorkspaceID: canonical.WorkspaceID, WorkspaceGeneration: canonical.WorkspaceGeneration, Path: allocation.WorkspacePath, Name: canonical.WorkspaceName, Available: &available})
	next.WorkspaceGrants = pebblestore.NormalizeSessionWorkspaceGrants(pebblestore.SessionSnapshot{WorkspaceGrants: grants})
	next.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(next.WorkspaceGrants)
	next.UpdatedAt = time.Now().UnixMilli()
	if err := worktreeruntime.ValidateRecoveryIdentity(canonical.SourceWorkspacePath, source); err != nil {
		return retainedError(err)
	}
	var admission *pebblestore.WorktreeAdmissionEvidence
	if args.Action == "copy_worktree" {
		if err := worktreeruntime.ValidateRecoveryIdentity(canonical.SourceWorkspacePath, destination); err != nil {
			return retainedError(err)
		}
		admission = sessionLaneAdmission(next, true)
	}
	fresh, err := s.canonicalSessionWorkspace(principal, args.WorkspaceID, args.WorkspaceGeneration)
	if err != nil {
		return retainedError(err)
	}
	if fresh.SourceWorkspacePath != canonical.SourceWorkspacePath || fresh.WorkspaceGeneration != canonical.WorkspaceGeneration {
		return retainedError(errors.New("saved workspace changed before publication"))
	}
	result, err := publish(finish, &next, admission)
	if err != nil {
		return retainedError(err)
	}
	return marshalManageWorkspace(map[string]any{"action": args.Action, "status": "ok", "runtime_worktree_path": next.WorktreeRootPath, "operation_id": r.Operation, "last_event_seq": result.LastSeq, "restart_turn": true})
}

// Cancellation is metadata-only: it neither publishes partial work nor deletes
// retained resources. The exact claimant/revision/operation must still match.
func (s *Service) cancelWorktreeRecovery(sessionID string, principal identity.Principal, args manageWorkspaceArguments, claim pebblestore.WorktreeOwnership, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if claim.ClaimantSessionID != sessionID || claim.OperationID != args.Recovery.Operation || claim.Evidence != args.Recovery.Fingerprint {
		return "", pebblestore.ErrWorktreeRecoveryConflict
	}
	mutation := pebblestore.WorktreeRecoveryMutation{Action: "release", Path: claim.Path, OwnerSessionID: claim.OwnerSessionID, ExpectedRevision: claim.Revision, OperationID: claim.OperationID, Evidence: claim.Evidence}
	payload, err := json.Marshal(mutation)
	if err != nil {
		return "", err
	}
	key := manageWorkspaceMutationKey("cancel-worktree-recovery", sessionID, payload)
	result, err := s.applyRecoveryPublication(pebblestore.SessionSnapshot{}, apply, sessionruntime.SessionMutationInput{SessionID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: sessionruntime.SessionMutationUpdateSettings, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, EventType: "session.worktree.recovery.release", EventPayload: payload, WorktreeRecovery: &mutation, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		return "", err
	}
	if result.Conflict != nil {
		return "", errors.New(result.Conflict.Message)
	}
	if result.Error != nil {
		return "", errors.New(result.Error.Message)
	}
	return marshalManageWorkspace(map[string]any{"action": args.Action, "status": "cancelled", "operation_id": claim.OperationID, "retained_destination": claim.DestinationPath, "resources_removed": false, "session_switched": false})
}

// applyRecoveryPublication refreshes the event CAS for each metadata step and
// retries only typed projection conflicts. Ownership revision/operation/evidence
// are never refreshed: a competing recovery must still fail at the store fence.
// Whole-session publication additionally refuses changed settings and carries
// forward concurrent message/lifecycle bookkeeping instead of overwriting it.
func (s *Service) applyRecoveryPublication(base pebblestore.SessionSnapshot, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
	var result sessionruntime.SessionMutationResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		projection, found, readErr := s.sessions.GetSessionProjection(input.SessionID)
		if readErr != nil {
			return result, readErr
		}
		if !found {
			return result, errors.New("recovery session projection missing")
		}
		input.ExpectedLastEventSeq = &projection.LastEventSeq
		if input.Session != nil {
			current, found, readErr := s.sessions.GetSession(input.SessionID)
			if readErr != nil {
				return result, readErr
			}
			if !found || !reflect.DeepEqual(recoverySessionSettings(base), recoverySessionSettings(current)) {
				return result, errors.New("recovery session settings changed before publication")
			}
			next := *input.Session
			next.MessageCount, next.LastMessageAt, next.Lifecycle = current.MessageCount, current.LastMessageAt, current.Lifecycle
			input.Session = &next
			payload, marshalErr := json.Marshal(map[string]any{"operation": input.WorktreeRecovery, "session": &next})
			if marshalErr != nil {
				return result, marshalErr
			}
			key := manageWorkspaceMutationKey("manage-workspace-recovery-"+input.WorktreeRecovery.Action, input.SessionID, payload)
			input.EventPayload = payload
			input.ClientRequestID, input.IdempotencyKey, input.PayloadHash, input.RequestHash = key, key, key, key
		}
		result, err = apply(input)
		var conflict *pebblestore.V3ProjectionConflictError
		if !errors.As(err, &conflict) {
			return result, err
		}
	}
	return result, err
}

func recoverySessionSettings(snapshot pebblestore.SessionSnapshot) pebblestore.SessionSnapshot {
	snapshot.UpdatedAt, snapshot.LastMessageAt, snapshot.MessageCount = 0, 0, 0
	snapshot.Lifecycle = nil
	return snapshot
}
