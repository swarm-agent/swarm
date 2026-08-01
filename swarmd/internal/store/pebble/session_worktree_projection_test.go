package pebblestore

import "testing"

func TestV3RealtimeOutboxMembershipProjectsDurableWorktreeFacts(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	session := SessionSnapshot{
		ID:                 "worktree-session",
		UserID:             "user-1",
		AccountScopeID:     "account-1",
		WorkspacePath:      "/managed/final-worktree",
		WorkspaceName:      "source-workspace",
		WorktreeEnabled:    true,
		WorktreeRootPath:   "/managed/final-worktree",
		WorktreeBaseBranch: "dev",
		WorktreeBranch:     "agent/final-worktree-1",
		Metadata: map[string]any{
			"routed_worktree_name":            "final-worktree-1",
			"swarm_v3_source_workspace_path": "/source/workspace",
		},
	}
	input := V3SessionMutationInput{
		SessionID:      session.ID,
		UserID:         session.UserID,
		AccountScopeID: session.AccountScopeID,
		IdempotencyKey: "create-worktree-session",
		RequestHash:    "create-worktree-session-hash",
		Kind:           V3SessionMutationCreateSession,
		Session:        &session,
		NowUnixMs:      100,
	}

	result, err := sessions.ApplyV3SessionMutation(input)
	if err != nil {
		t.Fatalf("apply worktree session create: %v", err)
	}
	if result.RealtimeOutbox == nil {
		t.Fatal("create result has no durable realtime outbox")
	}
	membership := result.RealtimeOutbox.Membership
	assertV3WorktreeMembership(t, membership, session)

	persisted, ok, err := sessions.GetV3RealtimeOutbox(result.RealtimeOutbox.EndpointSeq)
	if err != nil || !ok {
		t.Fatalf("load durable realtime outbox: ok=%t err=%v", ok, err)
	}
	assertV3WorktreeMembership(t, persisted.Membership, session)

	replayed, err := sessions.ApplyV3SessionMutation(input)
	if err != nil {
		t.Fatalf("replay worktree session create: %v", err)
	}
	if !replayed.Replayed || replayed.RealtimeOutbox != nil {
		t.Fatalf("idempotent replay result = %+v", replayed)
	}
	replayedOutbox, ok, err := sessions.GetV3RealtimeOutbox(result.RealtimeOutbox.EndpointSeq)
	if err != nil || !ok {
		t.Fatalf("reload durable outbox after replay: ok=%t err=%v", ok, err)
	}
	assertV3WorktreeMembership(t, replayedOutbox.Membership, session)
}

func assertV3WorktreeMembership(t *testing.T, membership *V3RealtimeOutboxMembership, session SessionSnapshot) {
	t.Helper()
	if membership == nil {
		t.Fatal("membership is nil")
	}
	if !membership.WorktreeEnabled || membership.RequestedWorktreeName != "final-worktree-1" || membership.WorktreeRootPath != session.WorktreeRootPath || membership.WorktreeBaseBranch != "dev" || membership.WorktreeBranch != "agent/final-worktree-1" {
		t.Fatalf("durable worktree membership = %+v", membership)
	}
	if got := membership.Metadata["routed_worktree_name"]; got != "final-worktree-1" {
		t.Fatalf("membership final requested name metadata = %v", got)
	}
}
