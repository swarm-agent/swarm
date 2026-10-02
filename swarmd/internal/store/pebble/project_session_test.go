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
