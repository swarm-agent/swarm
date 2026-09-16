package automation

import (
	"context"
	"encoding/json"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: ReviewContext must resolve an exact owned parent definition, never a
// stale/foreign locator or forged executable digest. Domain fakes isolate this
// admission boundary and assert that even successful review makes no writes.
func TestAutomationReviewContext(t *testing.T) {
	for _, scenario := range []string{"valid", "stale", "foreign", "parent", "digest", "identity"} {
		t.Run(scenario, func(t *testing.T) {
			s, repo, _, plans, p, scope, d := fixture(t)
			d.SessionID = "session"
			data, _ := json.Marshal(plans.plan.Document)
			d.Plans[0].Plan.DocumentSHA256 = executionDocumentDigest(data)
			revision, parent := uint64(3), "session"
			switch scenario {
			case "stale":
				revision = 2
			case "foreign":
				plans.plan.AccountScopeID = "foreign"
			case "parent":
				parent = "other"
			case "digest":
				d.Plans[0].Plan.DocumentSHA256 = "forged"
			case "identity":
				p.AccountID = "foreign"
			}
			repo.rows["definitionauto"] = store.AutomationRecord{Scope: scope, AutomationID: "auto", ID: "auto", Revision: 3, Definition: &d}
			got, err := s.ReviewContext(context.Background(), p, scope, "auto", revision, parent)
			if scenario == "valid" {
				if err != nil || got.Revision != 3 || got.Definition.SessionID != parent {
					t.Fatalf("review: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("forged review admitted")
			}
			if repo.writes != 0 {
				t.Fatal("review mutated definition")
			}
		})
	}
}

type editPlans struct {
	*fakePlans
	sessions map[string]store.SessionSnapshot
}

func (p *editPlans) GetSession(id string) (store.SessionSnapshot, bool, error) {
	s, found := p.sessions[id]
	return s, found, nil
}

// Purpose: EditParentDefinition is a proposal capability, not execution or write
// authority. Exercise canonical parent/child binding, replacement instruction
// pins and omission preservation; rejection and success must leave live work
// and the enabled definition untouched. Fakes are the narrowest service layer.
func TestAutomationEditProposal(t *testing.T) {
	for _, scenario := range []string{"replace", "omit", "digest", "foreign", "unapproved", "stale", "forged-context"} {
		t.Run(scenario, func(t *testing.T) {
			s, repo, _, plans, _, scope, d := fixture(t)
			d.SessionID, d.Enabled = "session", true
			d.Authorization.ExpiresAt = 200000
			data, _ := json.Marshal(plans.plan.Document)
			d.Plans[0].Plan.DocumentSHA256 = executionDocumentDigest(data)
			repo.rows["definitionauto"] = store.AutomationRecord{Scope: scope, AutomationID: "auto", ID: "auto", Revision: 3, Definition: &d}
			metadata := map[string]any{"parent_session_id": "session", "lineage_kind": "system_sidechat", "system_sidechat": true, "system_sidechat_kind": "plan", "plan_context_source": "automation_definition", "automation_review_id": "auto", "automation_review_revision": "3", "automation_review_workspace_id": "workspace"}
			s.plans = &editPlans{fakePlans: plans, sessions: map[string]store.SessionSnapshot{
				"child":   {ID: "child", UserID: "human", AccountScopeID: "account", Metadata: metadata},
				"session": {ID: "session", UserID: "human", AccountScopeID: "account", WorkspaceGrants: []store.WorkspaceGrant{{WorkspaceID: "workspace", Kind: store.WorkspaceGrantPrimary}}},
			}}
			next := d
			next.Plans = append([]store.AutomationPlanBinding(nil), d.Plans...)
			if scenario == "omit" {
				next.Plans = nil
			} else {
				plans.plan.Version = 2
				next.Plans[0].Plan.Revision = 2
			}
			switch scenario {
			case "digest":
				next.Plans[0].Plan.DocumentSHA256 = "forged"
			case "foreign":
				next.Plans[0].Plan.SessionID = "foreign"
			case "unapproved":
				plans.plan.ApprovalState = "pending"
			case "stale":
				metadata["automation_review_revision"] = "2"
			case "forged-context":
				metadata["automation_review_id"] = "other"
			}
			ctx := context.WithValue(context.Background(), runtimeIdentityKey{}, runtimeIdentity{principal: Principal{AccountID: "account", SubjectID: "child", Role: "agent"}})
			got, _, err := s.EditParentDefinition(ctx, scope, "auto", "edit", 3, next)
			if scenario == "replace" || scenario == "omit" {
				if err != nil {
					t.Fatal(err)
				}
				want := uint64(2)
				if scenario == "omit" {
					want = 1
				}
				if got.Revision != 3 || got.Definition.Enabled || got.Definition.Authorization.Mode != "approval_required" || got.Definition.Plans[0].Plan.Revision != want {
					t.Fatalf("invalid proposal: %+v", got)
				}
			} else if err == nil {
				t.Fatal("invalid proposal accepted")
			}
			if repo.writes != 0 || !repo.rows["definitionauto"].Definition.Enabled || d.Plans[0].Plan.Revision != 1 {
				t.Fatal("proposal changed live definition")
			}
		})
	}
}
