package pebblestore

import (
	"path/filepath"
	"testing"
)

// Purpose: ApplyV3SessionMutation must preserve project provenance and reject
// filesystem grants atomically; PermissionStore must reject stale decisions at
// the durable write boundary. A reopened temporary store proves pending records
// are not dependent on a live connection or in-memory permission waiter.
func TestProjectConversationDurableAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.pebble")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s := NewSessionStore(db)
	if err := s.PutProject("account", &ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	session := SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "user", Metadata: map[string]any{
		"swarm_v3_project_id": "project", "project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator",
	}}
	mutate := func(key, kind string, next SessionSnapshot) error {
		_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: session.ID, UserID: "user", AccountScopeID: "account", ClientRequestID: key, PayloadHash: key, Kind: kind, Session: &next})
		return err
	}
	if err := mutate("create", V3SessionMutationCreateSession, session); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"workspace", "agent", "project"} {
		next := session
		next.Metadata = cloneSessionMetadataMap(session.Metadata)
		switch field {
		case "workspace":
			next.WorkspacePath = t.TempDir()
		case "agent":
			next.Metadata["agent_name"] = "swarm"
		case "project":
			delete(next.Metadata, "swarm_v3_project_id")
		}
		if err := mutate(field, V3SessionMutationUpdateMetadata, next); err == nil {
			t.Fatalf("accepted %s authority mutation", field)
		}
		got, found, err := s.GetSession(session.ID)
		if err != nil || !found || got.WorkspacePath != "" || ProjectConversationID(got) != "project" || got.Metadata["agent_name"] != "system-orchestrator" {
			t.Fatalf("partial mutation: %+v %v", got, err)
		}
		if _, found, err := s.GetV3SessionOperationIdempotencyRecord("account", session.ID, V3SessionMutationUpdateMetadata, field); err != nil || found {
			t.Fatalf("rejected mutation retained receipt: %v %v", found, err)
		}
	}
	permissions := NewPermissionStore(db)
	pending := PermissionRecord{ID: "permission", SessionID: session.ID, RunID: "old-run", CallID: "call", Status: PermissionStatusPending, ToolName: "ask_user", CreatedAt: 1}
	if err := permissions.PutPermission(pending, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	permissions = NewPermissionStore(db)
	for _, status := range []string{PermissionStatusApproved, PermissionStatusDenied} {
		next := pending
		next.Status = status
		if err := permissions.PutPermissionWithSummary(next, &pending, PermissionSummary{}); err == nil {
			t.Fatalf("accepted stale %s", status)
		}
		got, found, err := permissions.GetPermission(session.ID, pending.ID)
		if err != nil || !found || got.Status != PermissionStatusPending {
			t.Fatalf("stale reply changed pending authority: %+v %v", got, err)
		}
	}
	cancelled := pending
	cancelled.Status = PermissionStatusCancelled
	if err := permissions.PutPermission(cancelled, &pending); err != nil {
		t.Fatalf("run cleanup cancellation failed: %v", err)
	}
}

// Purpose: project archiving must reject active work at the locked store boundary,
// atomically retain all batch members on failure, and discover/restore only the
// caller's project history after reopen. Temporary Pebble is the narrowest layer
// proving durable postconditions, not just an HTTP status or UI safeguard.
func TestProjectConversationArchiveSafetyAndDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.pebble")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s := NewSessionStore(db)
	for _, item := range []struct{ id, account, user, project string }{{"idle", "a", "u", "p"}, {"active", "a", "u", "p"}, {"other-project", "a", "u", "q"}, {"other-user", "a", "v", "p"}, {"other-account", "b", "u", "p"}} {
		snapshot := SessionSnapshot{ID: item.id, AccountScopeID: item.account, UserID: item.user, CreatedAt: 1, UpdatedAt: 1, Metadata: map[string]any{"project_id": item.project, "agent_name": "system-orchestrator"}}
		if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: item.id, AccountScopeID: item.account, UserID: item.user, Kind: V3SessionMutationCreateSession, Session: &snapshot, ClientRequestID: "create", PayloadHash: "create"}); err != nil {
			t.Fatal(err)
		}
	}
	run := V3SessionRunIntent{SessionID: "active", AccountScopeID: "a", UserID: "u", RunID: "run", Status: V3RunIntentPendingExecutor, CreatedAt: 2, UpdatedAt: 2}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "active", AccountScopeID: "a", UserID: "u", Kind: V3SessionMutationAppendMessage, RunIntent: &run, ClientRequestID: "run", PayloadHash: "run", NowUnixMs: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveSessions([]string{"idle", "active"}); err == nil {
		t.Fatal("active archive accepted")
	}
	for _, id := range []string{"idle", "active"} {
		if _, ok, err := s.GetSession(id); err != nil || !ok {
			t.Fatalf("session removed %s: %v", id, err)
		}
		if _, ok, err := s.GetV3SessionTombstone(id); err != nil || ok {
			t.Fatalf("partial tombstone %s: %v", id, err)
		}
	}
	if got, ok, err := s.GetV3SessionActiveRunIntent("active"); err != nil || !ok || got.RunID != "run" {
		t.Fatalf("run changed: %+v %v", got, err)
	}
	if err := s.ArchiveSessions([]string{"idle", "other-project", "other-user", "other-account"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = NewSessionStore(db)
	items, err := s.ListArchivedProjectConversations("a", "u", "p", 1)
	if err != nil || len(items) != 1 || items[0].SessionID != "idle" {
		t.Fatalf("archive scope: %+v %v", items, err)
	}
	version := items[0].UpdatedAt
	if err := s.ReactivateArchivedSessions([]string{"idle"}, map[string]int64{"idle": version - 1}); err == nil {
		t.Fatal("stale restore accepted")
	}
	if _, ok, _ := s.GetSession("idle"); ok {
		t.Fatal("stale restore mutated session")
	}
	if err := s.ReactivateArchivedSessions([]string{"idle"}, map[string]int64{"idle": version}); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.GetSession("idle"); err != nil || !ok || got.Metadata["project_id"] != "p" {
		t.Fatalf("restore lost provenance: %+v %v", got, err)
	}
	items, err = s.ListArchivedProjectConversations("a", "u", "p", 200)
	if err != nil || len(items) != 0 {
		t.Fatalf("restored still listed: %+v %v", items, err)
	}
}
