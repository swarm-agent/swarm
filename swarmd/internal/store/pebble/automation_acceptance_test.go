package pebblestore

import (
	"context"
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

// Purpose: the full conversion transaction must publish a grant and binding once,
// preserve conversation history, approve without activating its proposal, and
// reject stale proposal bytes with no partial grant. This real-store test covers
// the transaction rather than only the guard or permission batch helper.
func TestAutomationAcceptanceTransactionAndReplay(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			db, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			f := automationFixture()
			f.Record.Definition.SessionID = "conversation"
			f.Record.Definition.Authorization.ExpiresAt = 999999
			def, _, err := db.ApplyAutomationMutation(f)
			if err != nil {
				t.Fatal(err)
			}
			sessions := NewSessionStore(db)
			original := SessionSnapshot{ID: "conversation", UserID: "writer", AccountScopeID: f.Record.Scope.AccountID, WorkspacePath: t.TempDir(), WorktreeRootPath: t.TempDir(), WorktreeBranch: "agent/conversion-fixture", WorktreeEnabled: true, Title: "Preserved", MessageCount: 7}
			if err := sessions.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: original.ID, UserID: original.UserID, AccountScopeID: original.AccountScopeID, Kind: V3SessionMutationCreateSession, Session: &original, IdempotencyKey: "create", PayloadHash: "create", WorktreeAdmission: &WorktreeAdmissionEvidence{Kind: "allocated", Path: original.WorktreeRootPath, SourcePath: original.WorkspacePath, OwnerSessionID: original.ID, Branch: original.WorktreeBranch}}); err != nil {
				t.Fatal(err)
			}
			doc := &SessionPlanDocument{ID: "proposal", Title: "Review", Automation: &SessionPlanAutomationIntent{Scope: def.Scope, AutomationID: def.ID, DefinitionRevision: def.Revision, Definition: *def.Definition}}
			plan := SessionPlanSnapshot{ID: "proposal", SessionID: original.ID, UserID: original.UserID, AccountScopeID: original.AccountScopeID, Version: 1, Status: "pending", ApprovalState: "pending", Document: doc}
			if _, err := sessions.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: original.ID, UserID: original.UserID, AccountScopeID: original.AccountScopeID, Kind: V3SessionMutationSavePlan, IdempotencyKey: "proposal", PayloadHash: "proposal", PlanSave: &V3PlanSaveMutation{Plan: plan, Activate: true}}); err != nil {
				t.Fatal(err)
			}
			policy := *def.Definition
			policy.Enabled = false
			policy.Authorization.Mode = "approval_required"
			policy.Authorization.ApprovalReference = ""
			raw, _ := json.Marshal(policy)
			savedPlan, found, err := sessions.GetPlan(original.ID, plan.ID)
			if err != nil || !found {
				t.Fatal("missing proposal", err)
			}
			docRaw, _ := json.Marshal(savedPlan.Document)
			g := AutomationApproval{Scope: def.Scope, ID: "grant", AutomationID: def.ID, DefinitionRevision: def.Revision, SubjectID: original.UserID, PolicySHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), WrittenAt: 100001, ExpiresAt: 999999}
			ref := &AutomationPlanReference{SessionID: original.ID, PlanID: plan.ID, Revision: 1, DocumentSHA256: fmt.Sprintf("%x", sha256.Sum256(docRaw))}
			if stale {
				ref.DocumentSHA256 = "stale"
			}
			in := V3SessionMutationInput{SessionID: original.ID, UserID: original.UserID, AccountScopeID: original.AccountScopeID, Kind: V3SessionMutationUpdateMetadata, EventType: "session.automation.accepted", AutomationDefinitionRevision: def.Revision, AutomationProposal: ref, AutomationBinding: &SessionAutomationBinding{AutomationID: def.ID, WorkspaceID: def.Scope.WorkspaceID, Policy: def.Definition.Authorization}}
			got, err := db.CreateAutomationApprovalWithSession(g, in)
			current, _, readErr := sessions.GetSession(original.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if stale {
				if err == nil || current.Automation != nil {
					t.Fatal("stale conversion published binding", err)
				}
				if _, found, err := db.GetAutomationApproval(g.Scope, g.ID); err != nil || found {
					t.Fatal("partial grant", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v saved intent=%#v definition=%#v saved plan=%#v", err, savedPlan.Document.Automation, def.Definition, savedPlan)
			}
			if current.Automation == nil || current.Title != original.Title || current.MessageCount != original.MessageCount {
				t.Fatal("conversation lost", current)
			}
			approved, _, err := sessions.GetPlan(original.ID, plan.ID)
			if err != nil || approved.ApprovalState != "approved" || approved.Active {
				t.Fatal("proposal not approved inactive", approved, err)
			}
			g.ID = "retry-nonce"
			replay, err := db.CreateAutomationApprovalWithSession(g, in)
			if err != nil || replay.ID != got.ID {
				t.Fatal("retry minted grant", replay, err)
			}
			if _, found, err := db.GetAutomationApproval(g.Scope, g.ID); err != nil || found {
				t.Fatal("retry nonce persisted", err)
			}
			after, _, err := sessions.GetPlan(original.ID, plan.ID)
			if err != nil || after.Version != approved.Version {
				t.Fatal("retry revised proposal", err)
			}
		})
	}
}
