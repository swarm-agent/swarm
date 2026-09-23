package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: accepted Automation optimization uses a durable separate sidechat
// bound to exact current ownership/revision, never appends to its main chat.
// Threat: stale or foreign binding reuses a child with different instructions.
// Authority: handleSessionV3SystemSidechat and canonical proposal/acceptance.
// The real API/service/store fixture is the narrowest connected binding proof.
func TestAutomationV2OptimizationSidechat(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	parent := createSessionsV3PrimaryTestSession(t, server, "optimization", "Automation")
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	identities := store.NewIdentityStore(db)
	if _, err := identities.PutUser(store.UserRecord{ID: parent.UserID, Username: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identities.PutAccountScope(store.AccountScopeRecord{ID: parent.AccountScopeID, Type: store.AccountScopeTypePersonal, CreatedByUserID: parent.UserID}); err != nil {
		t.Fatal(err)
	}
	if _, err := identities.PutAccountUser(store.AccountUserRecord{ID: "membership", AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	entry, err := store.NewWorkspaceStore(db).AddForAccount(parent.AccountScopeID, parent.WorkspacePath, "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	available := true
	parent.WorkspaceGrants = []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: entry.WorkspaceID, Path: entry.Path, Available: &available}}
	sessions = session.NewService(store.NewSessionStore(db), nil)
	server.sessions = sessions
	server.perm = permission.NewService(store.NewPermissionStore(db), nil, nil)
	if err := sessions.Store().CreateSession(parent); err != nil {
		t.Fatal(err)
	}
	parent.ModelProfile = &store.SessionModelProfileSnapshot{Action: store.ModelProfileSelection{Provider: "test", Model: "action"}, Plan: &store.ModelProfileSelection{Provider: "test", Model: "plan"}}
	if err := sessions.Store().UpdateSession(parent); err != nil {
		t.Fatal(err)
	}
	workspace := ""
	for _, g := range parent.WorkspaceGrants {
		if g.Kind == store.WorkspaceGrantPrimary {
			workspace = g.WorkspaceID
		}
	}
	doc := &store.SessionPlanDocument{Title: "Harmless check", Info: store.SessionPlanInfo{Goal: "Inspect fixture"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "check", Title: "Check", Objective: "Read fixture", Status: "pending", Order: 1, AcceptanceCriteria: []string{"No writes"}}}, AutomationV2: &store.AutomationV2Settings{SchemaVersion: 2, Schedule: store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 3600}, Expiration: store.AutomationV2Expiration{Kind: "indefinite"}, Missed: "skip", Overlap: "serialize", ActivateOnAccept: true}}
	p, err := sessions.ProposeAutomationV2(parent.AccountScopeID, parent.UserID, workspace, parent.ID, doc, store.AutomationV2Review{})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := sessions.AcceptAutomationV2(parent.AccountScopeID, parent.UserID, workspace, parent.ID, p.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}
	// This test exercises the legacy bound-worker optimization path. New workers
	// remain independent and require no sidechat to edit the authoring chat.
	legacy := accepted
	legacy.Independent = false
	if err := db.PutJSON("automation/v2/accepted/"+fmt.Sprintf("%x/%x", parent.AccountScopeID, parent.ID), legacy); err != nil {
		t.Fatal(err)
	}
	parent.AutomationV2 = &store.SessionAutomationV2Binding{AutomationID: accepted.AutomationID, WorkspaceID: workspace, Digest: accepted.Digest}
	if err := sessions.Store().UpdateSession(parent); err != nil {
		t.Fatal(err)
	}
	open := func(revision uint64) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"automation_v2": true, "automation_id": accepted.AutomationID, "automation_revision": revision, "workspace_id": workspace})
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+parent.ID+"/sidechats/plan", strings.NewReader(string(b)))
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		return rec
	}
	if rec := open(accepted.Revision + 1); rec.Code != http.StatusConflict {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body.String())
	}
	childID, _ := sessionsV3SystemSidechatID(parent.ID, "plan")
	if _, found, _ := sessions.GetSession(childID); found {
		t.Fatal("stale binding created a child")
	}
	if rec := open(accepted.Revision); rec.Code != http.StatusOK {
		t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
	}
	child, found, err := sessions.GetSession(childID)
	if err != nil || !found || child.ID == parent.ID || child.Metadata["automation_v2_parent_id"] != parent.ID {
		t.Fatal("missing isolated child", err)
	}
	appendSessionsV3PrimaryTestUserMessage(t, server, childID, "optimization-message", "Move the schedule later")
	messages, err := sessions.ListSessionMessages(parent.ID, 0, 10)
	if err != nil || len(messages) != 0 {
		t.Fatal("optimization contaminated main", err)
	}
	after, found, err := sessions.GetAutomationV2Record(parent.AccountScopeID, parent.UserID, workspace, parent.ID)
	if err != nil || !found || after.Digest != accepted.Digest {
		t.Fatal("conversation changed accepted instructions", err)
	}
}
