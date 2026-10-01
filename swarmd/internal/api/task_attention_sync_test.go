package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

// Purpose: sessionsV3SyncSnapshot must recover the authorized ancestor chain of
// pending children outside the recent window. Principal boundaries stop forged
// cross-account ancestry and cycles terminate. The HTTP snapshot is the narrowest
// layer proving the actual bootstrap payload, without providers or transcripts.
func TestTaskAttentionBootstrapRecoversAuthorizedAncestors(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "attention.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	permissions := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permissions.SetSessionResolver(sessions)
	server := NewServer(nil, nil, nil, nil, sessions, nil, nil, nil, nil, permissions, nil, eventLog, stream.NewHub(eventLog))
	principal := testPrincipal()
	create := func(title, parent, account string) string {
		t.Helper()
		session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			UserID: principal.UserID, AccountScopeID: account, Title: title,
			WorkspacePath: t.TempDir(), WorkspaceName: "workspace",
			Preference: &pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
		})
		if err != nil {
			t.Fatal(err)
		}
		session.UpdatedAt = 1
		session.Metadata = map[string]any{"parent_session_id": parent}
		if err := sessions.Store().UpdateSession(session); err != nil {
			t.Fatal(err)
		}
		return session.ID
	}
	foreign := create("foreign", "", "foreign-account")
	root := create("root", foreign, principal.AccountScopeID)
	child := create("child", root, principal.AccountScopeID)
	grandchild := create("grandchild", child, principal.AccountScopeID)
	for _, id := range []string{root, grandchild} {
		if _, err := permissions.CreatePending(permission.CreateInput{SessionID: id, RunID: "run", CallID: id, ToolName: "ask_user", ToolArguments: "{}", Mode: sessionruntime.ModeAuto}); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte(`{"surface":"desktop","selector":{"kind":"recent","global":true,"recent":{"limit":1},"attention":{"pending_permissions":true}},"history":{"mode":"none"},"resources":{"permission_summaries":true}}`)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sync/bootstrap", bytes.NewReader(body))))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Sessions  map[string]pebblestore.SessionSnapshot `json:"sessions_by_id"`
		Summaries map[string]sessionsV3PermissionSummary `json:"permission_summaries_by_session"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root, child, grandchild} {
		if _, exists := payload.Sessions[id]; !exists {
			t.Fatalf("missing authorized ancestor %s", id)
		}
	}
	if _, exists := payload.Sessions[foreign]; exists {
		t.Fatal("foreign ancestor leaked through pending attention")
	}
	if payload.Summaries[root].PendingApprovalCount != 1 || payload.Summaries[grandchild].PendingApprovalCount != 1 {
		t.Fatalf("unresolved requests missing: %+v", payload.Summaries)
	}
}
