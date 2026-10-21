package pebblestore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Purpose: a reviewer worker session that reads the reviewed job's own
// worktree in place is admitted without claiming it: the worktree stays owned
// by the job's session. A reviewer naming another user's session, another
// branch, a session that is not the worktree's owner, or a reviewer that may
// commit fixes is refused.
// Threat: a reviewer session taking over the coder's worktree (a second
// ownership claim that later blocks or redirects the coder), or a session
// borrowing a worktree it has no relation to.
// Owners: SessionStore.ApplyV3SessionMutation (worktree recovery admission)
// and validateBorrowedReviewWorktreeBinding over a real Pebble store.
func TestReviewerBorrowsTheReviewedWorktreeWithoutClaimingIt(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewSessionStore(db)
	const account, user = "account", "user"
	source, lane := t.TempDir(), t.TempDir()
	ownerMeta := map[string]any{"swarm_v3_worktree_owner_session_id": "coder-session", "swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": lane}
	owner := SessionSnapshot{ID: "coder-session", AccountScopeID: account, UserID: user, WorkspacePath: lane, WorktreeEnabled: true, WorktreeRootPath: lane, WorktreeBranch: "agent/task-coder", Metadata: ownerMeta}
	if err := store.CreateSessionForAccount(owner, user, account); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyV3SessionMutation(V3SessionMutationInput{Kind: V3SessionMutationUpdateMetadata, SessionID: owner.ID, UserID: user, AccountScopeID: account, Session: &owner, ClientRequestID: "owner", PayloadHash: "owner",
		WorktreeAdmission: &WorktreeAdmissionEvidence{Kind: "allocated", Path: lane, SourcePath: source, OwnerSessionID: owner.ID, Branch: owner.WorktreeBranch, AllocatedRuntimeRoot: true}}); err != nil {
		t.Fatal(err)
	}
	reviewer := func(id string, edit func(*SessionSnapshot)) SessionSnapshot {
		s := SessionSnapshot{ID: id, AccountScopeID: account, UserID: user, WorkspacePath: source, WorktreeEnabled: true, WorktreeRootPath: lane, WorktreeBranch: owner.WorktreeBranch,
			Metadata: map[string]any{"worker_role": WorkerRoleReviewer, "worker_review_lane_owner": owner.ID, "worker_execution_run_id": "run-" + id, SessionPurposeMetadataKey: SessionPurposeAutomationExecution, SessionPurposeWorkspaceMetadataKey: "ws-1",
				"swarm_v3_worktree_owner_session_id": owner.ID, "swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": lane}}
		s.WorkspaceGrants = []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: "ws-1", WorkspaceGeneration: 1, Path: source}}
		if edit != nil {
			edit(&s)
		}
		return s
	}
	create := func(s SessionSnapshot, as string) error {
		_, err := store.ApplyV3SessionMutation(V3SessionMutationInput{Kind: V3SessionMutationCreateSession, SessionID: s.ID, UserID: as, AccountScopeID: account, Session: &s, ClientRequestID: "create-" + s.ID, PayloadHash: "create-" + s.ID})
		return err
	}

	if err := create(reviewer("review-ok", nil), user); err != nil {
		t.Fatalf("borrowing reviewer refused: %v", err)
	}
	var claim WorktreeOwnership
	if found, err := store.store.GetJSON(worktreeOwnershipKey(lane), &claim); err != nil || !found || claim.OwnerSessionID != owner.ID || claim.ClaimantSessionID != "" {
		t.Fatalf("worktree claim after borrowing = %+v %v %v, want still the coder's", claim, found, err)
	}

	for name, s := range map[string]SessionSnapshot{
		"other branch": reviewer("review-branch", func(s *SessionSnapshot) { s.WorktreeBranch = "agent/other" }),
		"not the owner": reviewer("review-owner", func(s *SessionSnapshot) {
			s.Metadata["worker_review_lane_owner"] = "review-ok"
			s.Metadata["swarm_v3_worktree_owner_session_id"] = "review-ok"
		}),
		"owner mismatch":   reviewer("review-meta", func(s *SessionSnapshot) { s.Metadata["swarm_v3_worktree_owner_session_id"] = "review-meta" }),
		"with fixes":       reviewer("review-fixes", func(s *SessionSnapshot) { s.Metadata["worker_review_fixes"] = "security" }),
		"not a worker run": reviewer("review-plain", func(s *SessionSnapshot) { delete(s.Metadata, SessionPurposeMetadataKey) }),
	} {
		if err := create(s, user); !errors.Is(err, ErrWorktreeRecoveryConflict) {
			t.Fatalf("%s: err = %v, want a worktree conflict", name, err)
		}
	}
	if err := create(reviewer("review-stranger", nil), "someone-else"); err == nil {
		t.Fatal("another user's reviewer borrowed the worktree")
	}
}
