package tool

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/gitstatus"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Recovery uses the account catalog for Git authority. Session identity is an
// optional evidence destination, never an admission requirement or completion signal.
func (r *Runtime) manageSessionsRecovery(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(stringValue(args["action"]))
	if action != "commit" && action != "git_status" {
		return "", errors.New("recovery supports commit or git_status only")
	}
	if args["commits"] != nil || args["manifest"] != nil {
		return "", errors.New("recovery cannot be combined with a session commit batch or manifest")
	}
	repo, err := r.recoveryRepository(scope, stringValue(args["workspace_path"]))
	if err != nil {
		return "", err
	}
	result := map[string]any{"action": action, "repository": repo, "recovery": true}
	if action == "commit" {
		out, err := recoveryCommit(ctx, repo, args, scope.Principal.AccountScopeID+"/"+scope.Principal.UserID)
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			return out, err
		}
	}
	return r.recoveryEvidence(ctx, scope, args, repo, result)
}

func (r *Runtime) recoveryEvidence(ctx context.Context, scope WorkspaceScope, args map[string]any, repo string, result map[string]any) (string, error) {
	snapshot, err := gitstatus.SnapshotForPath(ctx, repo, gitstatus.Options{})
	if err != nil {
		result["inspection_error"] = err.Error()
		result["reconciliation"] = "pending"
		return marshalManageSessions(result) // never turn durable Git success into a failed commit
	}
	result["git_status"] = snapshot
	result["session_state_unchanged"] = true
	result["reconciliation"] = "not_requested"
	if id := strings.TrimSpace(stringValue(args["session_id"])); id != "" {
		if err := r.reconcileRecoveryEvidence(scope, id, repo, result); err != nil {
			result["reconciliation"] = "pending"
			result["reconciliation_error"] = err.Error()
		} else {
			result["reconciliation"] = "recorded"
		}
	}
	return marshalManageSessions(result)
}

func (r *Runtime) reconcileRecoveryEvidence(scope WorkspaceScope, id, repo string, evidence map[string]any) error {
	if r.sessions == nil {
		return errors.New("session bookkeeping unavailable; retry the exact recovery request")
	}
	owner, ok, err := r.sessions.GetSession(id)
	if err != nil || !ok || owner.AccountScopeID != scope.Principal.AccountScopeID || owner.UserID != scope.Principal.UserID {
		return errors.New("session evidence destination unavailable or unauthorized")
	}
	// Read the event fence first, then the snapshot. A concurrent mutation makes
	// this best-effort bookkeeping CAS fail without undoing or repeating Git.
	events, err := r.sessions.ListSessionEventsBefore(id, 0, 1)
	if err != nil || len(events) != 1 {
		return errors.New("session evidence fence unavailable")
	}
	seq := events[0].Seq
	session, ok, err := r.sessions.GetSession(id)
	if err != nil || !ok || session.AccountScopeID != scope.Principal.AccountScopeID || session.UserID != scope.Principal.UserID {
		return errors.New("session evidence destination unavailable or unauthorized")
	}
	path := session.WorkspacePath
	if session.WorktreeEnabled {
		path = session.WorktreeRootPath
	}
	canonical, err := canonicalExistingPath(path)
	if err != nil || canonical != repo {
		return errors.New("session does not name the inspected recovery repository")
	}
	// Do not change task/checkpoint lifecycle or claim the workspace is clean.
	// Only store bounded, freshly observed Git evidence via the canonical mutation.
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("git-recovery-evidence:%x", sha256.Sum256(body))
	session.Metadata = cloneRecoveryRow(session.Metadata)
	session.Metadata["git_recovery_evidence"] = evidence
	mutation, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: id, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		Kind: pebblestore.V3SessionMutationUpdateMetadata, Session: &session,
		ExpectedLastEventSeq: &seq, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
	})
	if err != nil {
		return err
	}
	if mutation.Conflict != nil || mutation.Error != nil {
		return errors.New("session evidence update conflicted; Git result is retained")
	}
	if mutation.RealtimeOutbox != nil && r.publishSessionOutbox != nil {
		if err := r.publishSessionOutbox(*mutation.RealtimeOutbox); err != nil {
			return fmt.Errorf("evidence recorded; realtime delivery pending: %w", err)
		}
	}
	return nil
}
