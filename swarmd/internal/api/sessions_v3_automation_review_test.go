package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/automation"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type sidechatReviewFixture struct {
	store.AutomationRecord
	plan store.SessionPlanSnapshot
}

func (f *sidechatReviewFixture) GetAutomationRecord(store.AutomationScope, string, string, string, uint64) (store.AutomationRecord, bool, error) {
	return f.AutomationRecord, true, nil
}
func (f *sidechatReviewFixture) ApplyAutomationMutation(store.AutomationMutation) (store.AutomationRecord, bool, error) {
	panic("review cannot mutate automations")
}
func (f *sidechatReviewFixture) SearchAutomationRecords(store.AutomationSearch) ([]store.AutomationRecord, string, error) {
	panic("review must resolve exact identity")
}
func (f *sidechatReviewFixture) GetPlanRevision(string, string, int) (store.SessionPlanSnapshot, bool, error) {
	return f.plan, true, nil
}
func (f *sidechatReviewFixture) Workspace(_ context.Context, p automation.Principal, scope store.AutomationScope, _ string) error {
	if scope != f.Scope || p.AccountID != f.Scope.AccountID { return automation.ErrDenied }
	return nil
}
func (f *sidechatReviewFixture) PlanSession(_ context.Context, _ automation.Principal, _ store.AutomationScope, id string) error {
	if id != f.Definition.SessionID { return automation.ErrDenied }
	return nil
}
func (f *sidechatReviewFixture) Execution(context.Context, automation.Principal, store.AutomationScope, store.AutomationDefinition, string) error {
	panic("review cannot execute")
}
func (f *sidechatReviewFixture) OccurrenceSession(context.Context, automation.Principal, store.AutomationScope, string) error {
	return automation.ErrDenied
}

// Purpose: authenticated accepted-automation review creates the canonical Plan
// sidechat without permission history. Exact identity/revision and immutable pins
// are checked before V3 session creation. Real session storage proves rejected
// contexts leave no sidechat; automation fakes panic on any mutation/dispatch.
func TestSessionsV3AcceptedAutomationReview(t *testing.T) {
	for _, scenario := range []string{"valid", "stale", "identity", "foreign", "digest"} {
		t.Run(scenario, func(t *testing.T) {
			server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			parent := createSessionsV3PrimaryTestSession(t, server, "automation-review", "Plan")
			parent.ModelProfile = &store.SessionModelProfileSnapshot{Action: store.ModelProfileSelection{Provider: "test", Model: "action"}, Plan: &store.ModelProfileSelection{Provider: "test", Model: "plan"}}
			if err := sessions.Store().UpdateSession(parent); err != nil { t.Fatal(err) }
			doc := &store.SessionPlanDocument{}
			data, _ := json.Marshal(doc)
			f := &sidechatReviewFixture{AutomationRecord: store.AutomationRecord{Scope: store.AutomationScope{AccountID: parent.AccountScopeID, WorkspaceID: "workspace"}, AutomationID: "auto", ID: "auto", Revision: 3, Definition: &store.AutomationDefinition{SessionID: parent.ID, Enabled: true, Plans: []store.AutomationPlanBinding{{ID: "primary", Plan: store.AutomationPlanReference{SessionID: parent.ID, PlanID: "instructions", Revision: 1, DocumentSHA256: fmt.Sprintf("%x", sha256.Sum256(data))}}}}}, plan: store.SessionPlanSnapshot{ID: "instructions", SessionID: parent.ID, AccountScopeID: parent.AccountScopeID, Version: 1, ApprovalState: "approved", Document: doc}}
			domain, err := automation.New(f, f, f, time.Now)
			if err != nil { t.Fatal(err) }
			server.ConfigureAutomations(domain, nil, nil, nil)
			id, revision := "auto", 3
			switch scenario {
			case "stale": revision = 2
			case "identity": id = "other"
			case "foreign": f.plan.AccountScopeID = "foreign"
			case "digest": f.Definition.Plans[0].Plan.DocumentSHA256 = "forged"
			}
			body := fmt.Sprintf(`{"automation_id":%q,"automation_revision":%d,"workspace_id":"workspace"}`, id, revision)
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			w := httptest.NewRecorder()
			server.handleSessionV3SystemSidechat(w, r, testPrincipal(), parent.ID, "plan")
			sideID, _ := sessionsV3SystemSidechatID(parent.ID, "plan")
			side, found, err := sessions.GetSession(sideID)
			if err != nil { t.Fatal(err) }
			if scenario == "valid" {
				if w.Code != http.StatusOK || !found || side.Metadata["automation_review_id"] != "auto" || side.Metadata["automation_review_revision"] != "3" || side.Metadata["parent_session_id"] != parent.ID { t.Fatalf("review: %d %s %+v", w.Code, w.Body.String(), side) }
			} else if w.Code == http.StatusOK || found { t.Fatalf("invalid context mutated session: %d %+v", w.Code, side) }
		})
	}
}
