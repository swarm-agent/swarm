package session

import (
	"fmt"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: proposal validation is the narrow layer enforcing complete executable
// instructions and rejecting prior execution state before store publication.
func TestAutomationV2ProposalRejectsRuntime(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{Title: "Review", Info: pebblestore.SessionPlanInfo{Goal: "Work"}, AutomationV2: &pebblestore.AutomationV2Settings{SchemaVersion: 2, Schedule: pebblestore.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "skip", Overlap: "serialize", ActivateOnAccept: true, Expiration: pebblestore.AutomationV2Expiration{Kind: "indefinite"}}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Work", Objective: "Implement", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Works"}}}}
	if err := validateAutomationV2Proposal(doc); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*pebblestore.SessionPlanDocument){
		func(d *pebblestore.SessionPlanDocument) {
			d.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: "idle"}
		},
		func(d *pebblestore.SessionPlanDocument) { d.Checkpoints[0].RunID = "forged" },
		func(d *pebblestore.SessionPlanDocument) { d.Checkpoints[0].Status = "in_progress" },
		func(d *pebblestore.SessionPlanDocument) { d.Checkpoints[0].AcceptanceCriteria = nil },
		func(d *pebblestore.SessionPlanDocument) { d.Checkpoints = nil },
		func(d *pebblestore.SessionPlanDocument) { d.ID = "forged" },
		func(d *pebblestore.SessionPlanDocument) { d.RevisionID = "forged" },
	} {
		copy := clonePlanDocument(doc)
		mutate(copy)
		if validateAutomationV2Proposal(copy) == nil {
			t.Fatal("invalid proposal accepted")
		}
	}
}

// Purpose: Service.AcceptAutomationV2 must validate a fresh executable document,
// but an exact durable receipt is historical evidence, not a new finite grant.
// A real store with an expired receipt proves replay does not reauthorize it.
func TestAutomationV2ExpiredReceiptReplay(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ss := pebblestore.NewSessionStore(db)
	ids := pebblestore.NewIdentityStore(db)
	if _, err := ids.PutUser(pebblestore.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountScope(pebblestore.AccountScopeRecord{ID: "account", Type: pebblestore.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountUser(pebblestore.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	workspace, err := pebblestore.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	available := true
	if err := ss.CreateSession(pebblestore.SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "owner", WorkspacePath: workspace.Path, WorkspaceGrants: []pebblestore.WorkspaceGrant{{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: workspace.WorkspaceID, Path: workspace.Path, Available: &available}}}); err != nil {
		t.Fatal(err)
	}
	doc := pebblestore.SessionPlanDocument{Title: "Review", Info: pebblestore.SessionPlanInfo{Goal: "Work"}, AutomationV2: &pebblestore.AutomationV2Settings{SchemaVersion: 2, Schedule: pebblestore.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "skip", Overlap: "serialize", ActivateOnAccept: true, Expiration: pebblestore.AutomationV2Expiration{Kind: "at", ExpiresAt: 2000}}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Work", Objective: "Implement", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Works"}}}}
	digest, err := pebblestore.AutomationV2DocumentDigest(doc)
	if err != nil {
		t.Fatal(err)
	}
	p := pebblestore.AutomationV2Proposal{AutomationV2Review: pebblestore.AutomationV2Review{ProposalID: "proposal", Revision: 1, Digest: digest}, AccountID: "account", UserID: "owner", WorkspaceID: workspace.WorkspaceID, SessionID: "conversation", Document: doc, CreatedAt: 1000}
	key := func(kind string) string {
		return fmt.Sprintf("automation/v2/%s/%x/%x", kind, "account", "conversation")
	}
	if err := db.PutJSON(key("proposal"), p); err != nil {
		t.Fatal(err)
	}
	svc := NewService(ss, nil)
	if _, err := svc.AcceptAutomationV2("account", "owner", workspace.WorkspaceID, "conversation", p.AutomationV2Review); err == nil {
		t.Fatal("fresh expired grant accepted")
	}
	want := pebblestore.AutomationV2Record{AutomationV2Proposal: p, AutomationID: "accepted", AcceptedBy: "owner", AcceptedAt: 1500, Authorization: doc.AutomationV2.Expiration, Enabled: true}
	if err := db.PutJSON(key("accepted"), want); err != nil {
		t.Fatal(err)
	}
	got, err := svc.AcceptAutomationV2("account", "owner", workspace.WorkspaceID, "conversation", p.AutomationV2Review)
	if err != nil || !reflect.DeepEqual(want, got) {
		t.Fatal("historical receipt changed", err)
	}
	if _, ok, err := ss.GetV3SessionActiveRunIntent("conversation"); err != nil || ok {
		t.Fatal("replay started run", err)
	}
}
