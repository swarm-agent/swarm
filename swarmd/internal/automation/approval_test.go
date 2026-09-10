package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: PolicyApproval is the narrow authenticated policy boundary. Real
// durable storage plus fake trusted identity/ownership prevent tool impersonation,
// stale document/target grants, expiry and revocation from authorizing execution.
func TestPolicyApprovalRejectsUntrustedAndStaleGrants(t *testing.T) {
	ctx := context.Background()
	_, _, ownership, plans, user, scope, d := fixture(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := int64(100000)
	actual := user
	explicit := true
	a, err := NewPolicyApproval(db, plans, ownership, ApprovalIdentity{
		Current: func(context.Context) (Principal, error) { return actual, nil },
		ExplicitUser: func(context.Context) (Principal, error) {
			if !explicit {
				return Principal{}, ErrDenied
			}
			return actual, nil
		},
	}, func() time.Time { return time.UnixMilli(now) })
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(plans.plan.Document)
	d.Plans[0].Plan.DocumentSHA256 = executionDocumentDigest(data)
	d.Schedule.MissedPolicy, d.Schedule.OverlapPolicy = "skip", "independent"
	d.Authorization.ExpiresAt = 200000
	r, _, err := db.ApplyAutomationMutation(store.AutomationMutation{Actor: "user", SubjectID: user.SubjectID, WrittenAt: now, MutationID: "create", Record: store.AutomationRecord{Scope: scope, AutomationID: "check", ID: "check", Kind: "definition", Definition: &d}})
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := ApprovalPolicyDigest(d)
	input := ApprovalRequest{Scope: scope, AutomationID: "check", DefinitionRevision: 1, PolicySHA256: digest}
	for _, scenario := range []string{"agent", "forged-user", "wrong-account", "stale-digest", "stale-revision"} {
		t.Run(scenario, func(t *testing.T) {
			actual, explicit = user, true
			bad := input
			switch scenario {
			case "agent":
				actual.Role = "agent"
			case "forged-user":
				explicit = false
			case "wrong-account":
				bad.Scope.AccountID = "other"
			case "stale-digest":
				bad.PolicySHA256 = "stale"
			case "stale-revision":
				bad.DefinitionRevision++
			}
			if _, err := a.ApproveUser(ctx, bad); err == nil {
				t.Fatal("approval accepted")
			}
			if scenario == "forged-user" {
				if _, err := a.Request(ctx, user, r); err == nil {
					t.Fatal("request bypassed explicit origin")
				}
			}
			current, _, err := db.GetAutomationRecord(scope, "check", "definition", "check", 0)
			if err != nil || current.Revision != 1 {
				t.Fatal("rejected request mutated definition")
			}
		})
	}
	actual, explicit = user, true
	g, err := a.ApproveUser(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	d.Enabled, d.Authorization.Mode, d.Authorization.ApprovalReference = true, "approved_policy", g.ID
	r, _, err = db.ApplyAutomationMutation(store.AutomationMutation{Actor: "user", SubjectID: user.SubjectID, WrittenAt: now, MutationID: "enable", ExpectedRevision: 1, Record: store.AutomationRecord{Scope: scope, AutomationID: "check", ID: "check", Kind: "definition", Definition: &d}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(ctx, user, r); err != nil {
		t.Fatal(err)
	}
	changed := d
	changed.Authorization.TargetIDs = []string{"different"}
	if err := a.Execution(ctx, user, scope, changed, "run"); err == nil {
		t.Fatal("changed target accepted")
	}
	plans.plan.Document.Title = "rewritten"
	if err := a.Verify(ctx, user, r); err == nil {
		t.Fatal("stale plan accepted")
	}
	plans.plan.Document.Title = ""
	now = g.ExpiresAt
	if err := a.Verify(ctx, user, r); err == nil {
		t.Fatal("expired grant accepted")
	}
	now = 100001
	actual.Role = "agent"
	explicit = false
	if _, err := a.RevokeUser(ctx, scope, g.ID, 1); err == nil {
		t.Fatal("agent revoked grant")
	}
	persisted, _, _ := db.GetAutomationApproval(scope, g.ID)
	if persisted.Revision != 1 || persisted.RevokedAt != 0 {
		t.Fatal("denied revoke mutated grant")
	}
	actual, explicit = user, true
	if _, err := a.RevokeUser(ctx, scope, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(ctx, user, r); err == nil {
		t.Fatal("revoked grant accepted")
	}
}
