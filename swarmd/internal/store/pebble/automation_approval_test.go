package pebblestore

import (
	"errors"
	"strings"
	"testing"
)

// Purpose: real-store approval CAS must preserve terminal revocation through
// restart and reject stale/cross-scope writes without changing durable head.
func TestAutomationApprovalCASRestart(t *testing.T) {
	path := t.TempDir()
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	m := automationFixture()
	if _, _, err := s.ApplyAutomationMutation(m); err != nil { t.Fatal(err) }
	g := AutomationApproval{Scope: m.Record.Scope, ID: "grant", AutomationID: "check", DefinitionRevision: 1, PolicySHA256: strings.Repeat("a", 64), SubjectID: "writer", WrittenAt: 100, ExpiresAt: 200}
	created, err := s.CreateAutomationApproval(g)
	if err != nil { t.Fatal(err) }
	if _, err := s.CreateAutomationApproval(g); !errors.Is(err, ErrAutomationConflict) { t.Fatalf("duplicate: %v", err) }
	for _, scope := range []AutomationScope{{AccountID: "other", WorkspaceID: g.Scope.WorkspaceID}, {AccountID: g.Scope.AccountID, WorkspaceID: "other"}} {
		if _, found, err := s.GetAutomationApproval(scope, g.ID); err != nil || found { t.Fatal("cross-scope read") }
		if _, err := s.RevokeAutomationApproval(scope, g.ID, g.SubjectID, 1, 101); err == nil { t.Fatal("cross-scope revoke") }
	}
	if _, err := s.RevokeAutomationApproval(g.Scope, g.ID, "other", 1, 101); err == nil { t.Fatal("wrong owner revoke") }
	if _, err := s.RevokeAutomationApproval(g.Scope, g.ID, g.SubjectID, 2, 101); err == nil { t.Fatal("stale revoke") }
	unchanged, _, _ := s.GetAutomationApproval(g.Scope, g.ID)
	if unchanged != created { t.Fatal("rejected write changed grant") }
	if _, err := s.RevokeAutomationApproval(g.Scope, g.ID, g.SubjectID, 1, 101); err != nil { t.Fatal(err) }
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(path)
	if err != nil { t.Fatal(err) }
	defer s.Close()
	got, found, err := s.GetAutomationApproval(g.Scope, g.ID)
	if err != nil || !found || got.Revision != 2 || got.RevokedAt != 101 { t.Fatalf("restart: %+v %v", got, err) }
	if _, err := s.CreateAutomationApproval(g); err == nil { t.Fatal("revoked grant resurrected") }
	g.ID, g.DefinitionRevision = "stale", 2
	if _, err := s.CreateAutomationApproval(g); err == nil { t.Fatal("stale definition approved") }
	if _, found, err := s.GetAutomationApproval(g.Scope, g.ID); err != nil || found { t.Fatal("failed create persisted") }
}
