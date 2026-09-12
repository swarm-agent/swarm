package pebblestore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

// Purpose: CreateAutomationApprovalWithSession must not publish a grant when
// session ownership/binding validation fails. Real temporary-store postconditions
// exercise the canonical transaction, rather than a mocked approval callback.
func TestAutomationAcceptanceRejectsWithoutPartialGrant(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fixture := automationFixture()
	fixture.Record.Definition.SessionID = "conversation"
	fixture.Record.Definition.Authorization.ExpiresAt = 999999
	if _, _, err := db.ApplyAutomationMutation(fixture); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(db)
	if err := sessions.CreateSession(SessionSnapshot{ID: "conversation", UserID: "different-owner", AccountScopeID: fixture.Record.Scope.AccountID, WorktreeEnabled: true}); err != nil {
		t.Fatal(err)
	}
	policy := *fixture.Record.Definition
	policy.Enabled = false
	policy.Authorization.Mode = "approval_required"
	policy.Authorization.ApprovalReference = ""
	raw, _ := json.Marshal(policy)
	g := AutomationApproval{Scope: fixture.Record.Scope, ID: "grant", AutomationID: fixture.Record.AutomationID, DefinitionRevision: 1, SubjectID: "writer", PolicySHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), WrittenAt: 100001, ExpiresAt: 999999}
	in := V3SessionMutationInput{SessionID: "conversation", UserID: g.SubjectID, AccountScopeID: g.Scope.AccountID, Kind: V3SessionMutationUpdateMetadata, EventType: "session.automation.accepted", AutomationDefinitionRevision: 1, AutomationBinding: &SessionAutomationBinding{AutomationID: g.AutomationID, WorkspaceID: g.Scope.WorkspaceID, Policy: fixture.Record.Definition.Authorization}}
	for i := 0; i < 2; i++ {
		if _, err := db.CreateAutomationApprovalWithSession(g, in); err == nil {
			t.Fatal("foreign owner accepted")
		}
		if _, found, err := db.GetAutomationApproval(g.Scope, g.ID); err != nil || found {
			t.Fatalf("partial grant: found=%v err=%v", found, err)
		}
		current, _, err := sessions.GetSession("conversation")
		if err != nil || current.Automation != nil {
			t.Fatalf("partial binding: %+v %v", current.Automation, err)
		}
	}
}
