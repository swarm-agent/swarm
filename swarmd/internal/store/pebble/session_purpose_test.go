package pebblestore

import (
	"errors"
	"testing"
)

// Purpose: management classification must survive reopening Pebble and be scoped
// by account and its original workspace before pagination. The canonical mutation
// guard must reject purpose changes without changing durable state or granting execution.
func TestAutomationManagementPurposeDurabilityAndIsolation(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	original := SessionSnapshot{ID: "management", UserID: "owner", AccountScopeID: "account", WorkspacePath: t.TempDir(), UpdatedAt: 100, Metadata: map[string]any{SessionPurposeMetadataKey: SessionPurposeAutomationManagement, SessionPurposeWorkspaceMetadataKey: "workspace"}, WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: "workspace"}}}
	// Seed a pre-existing canonical snapshot; HTTP test owns actual create admission.
	if err := s.CreateSession(original); err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.Metadata = map[string]any{SessionPurposeMetadataKey: SessionPurposeAutomationManagement, SessionPurposeWorkspaceMetadataKey: "other"}
	in := V3SessionMutationInput{SessionID: original.ID, Session: &changed, Kind: V3SessionMutationUpdateMetadata}
	if err := s.guardAutomationSessionMutation(&in); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("purpose changed: %v", err)
	}
	membership := newV3RealtimeOutboxMembershipFromSession(original, 100)
	if membership.Metadata[SessionPurposeMetadataKey] != SessionPurposeAutomationManagement {
		t.Fatal("realtime lost purpose")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	got, found, err := s.GetSession(original.ID)
	if err != nil || !found || SessionAutomationManagementWorkspace(got) != "workspace" || got.Automation != nil {
		t.Fatalf("restart changed identity: %+v %v", got, err)
	}
	for _, tc := range []struct {
		account, workspace string
		count              int
	}{{"account", "workspace", 1}, {"foreign", "workspace", 0}, {"account", "other", 0}} {
		result, err := s.BuildV3SessionWorkset(V3SessionWorksetOptions{AccountScopeID: tc.account, UserID: "owner", AutomationManagementWorkspaceID: tc.workspace, RecentLimit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.SessionOrder) != tc.count {
			t.Fatalf("scope %+v leaked rows: %+v", tc, result.SessionOrder)
		}
	}
}
