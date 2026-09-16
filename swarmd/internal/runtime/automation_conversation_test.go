package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: daemon conversation composition must resolve omitted automation plans
// through the real session service, not a fake with a different GetActivePlan
// signature. This hermetic composition test exercises canonical V3 plan writes,
// exact pinning and unapproved/missing-plan rejection without automation writes.
func TestAutomationConversationComposition(t *testing.T) {
	for _, state := range []string{"approved", "pending", "missing", "seconds-expiry"} {
		t.Run(state, func(t *testing.T) {
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			events, err := store.NewEventLog(db)
			if err != nil {
				t.Fatal(err)
			}
			sessions := session.NewService(store.NewSessionStore(db), events)
			_, err = sessions.ApplySessionMutation(session.SessionMutationInput{SessionID: "chat", UserID: "owner", AccountScopeID: "account", Kind: session.SessionMutationCreateSession, Session: &store.SessionSnapshot{ID: "chat", UserID: "owner", AccountScopeID: "account", WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: "workspace"}}}, IdempotencyKey: "create", PayloadHash: "create"})
			if err != nil {
				t.Fatal(err)
			}
			if state != "missing" {
				doc := &store.SessionPlanDocument{Title: "Archive check", Info: store.SessionPlanInfo{Goal: "Inspect integrated work"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "check", Title: "Check", Status: "pending", Order: 1, Tasks: []string{"Inspect integration"}, AcceptanceCriteria: []string{"Only integrated work qualifies"}}}}
				approval := state
				if state == "seconds-expiry" {
					approval = "approved"
				}
				prepared, err := sessions.PreparePlanSaveWithMetadata("chat", "plan", doc.Title, "", approval, approval, true, session.PlanSaveMetadata{Document: doc})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := sessions.CommitPreparedPlanSave(prepared, sessions.ApplySessionMutation); err != nil {
					t.Fatal(err)
				}
			}
			access := automationAccess{workspaces: &automationCatalogFake{}, members: &automationMembersFake{active: true}, sessions: sessions}
			now := time.Unix(1789238436, 0)
			policy, err := automation.NewPolicyApproval(db, sessions.Store(), access, automation.RuntimeApprovalIdentity(), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			domain, err := composeConversationAutomation(db, sessions, policy, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := automation.BindRuntimeIdentity(context.Background(), identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}, "agent", "chat")
			if err != nil {
				t.Fatal(err)
			}
			p, _ := automation.RuntimePrincipal(ctx)
			scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
			draft := store.AutomationDefinition{Name: "Hourly archive check", Schedule: store.AutomationSchedulePolicy{Kind: "interval", IntervalSeconds: 3600, MissedPolicy: "coalesce", OverlapPolicy: "serialize"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required", ExpiresAt: now.Add(30 * 24 * time.Hour).UnixMilli()}}
			if state == "seconds-expiry" {
				draft.Authorization.ExpiresAt = 1791830436
			}
			got, err := domain.PrepareConversationDefinition(ctx, p, scope, nil, draft)
			if state == "seconds-expiry" && (err == nil || !strings.Contains(err.Error(), "milliseconds, not seconds")) {
				t.Fatalf("expiry rejection did not explain the units: %v", err)
			}
			if state == "approved" {
				if err != nil {
					t.Fatalf("omitted plans failed with production composition: %v", err)
				}
				active, found, err := sessions.GetActivePlan("chat")
				if err != nil || !found {
					t.Fatal("active plan missing", err)
				}
				if got.SessionID != "chat" || len(got.Plans) != 1 || got.Plans[0].Plan.PlanID != active.ID || got.Plans[0].Plan.Revision != uint64(active.Version) || got.Plans[0].Plan.DocumentSHA256 == "" || got.Enabled || got.Authorization.Mode != "approval_required" || got.Authorization.ApprovalReference != "" || got.Schedule != draft.Schedule {
					t.Fatalf("incorrect prepared definition: %+v", got)
				}
			} else if err == nil {
				t.Fatal("missing or unapproved instructions accepted")
			}
			rows, _, err := db.SearchAutomationRecords(store.AutomationSearch{Scope: scope, Kind: "definition", Limit: 10})
			if err != nil || len(rows) != 0 {
				t.Fatal("preparation wrote automation state", err)
			}
		})
	}
}
