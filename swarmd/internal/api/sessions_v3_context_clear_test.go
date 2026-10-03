package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the V3 context/clear HTTP boundary must require scope, ownership and
// a revision, preserve the session and return a durable receipt. The API/store
// layer proves denied/stale writes leave the epoch unchanged; executor helpers
// prove neither resume nor final-handoff injection crosses an explicit clear.
func TestSessionV3ContextClearAuthorizationAndProviderBoundary(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	created := projectConversationRequest(t, s, p, http.MethodPost, ProjectsPath+"/project/sessions", map[string]any{"client_request_id": "context-test"})
	var response struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil || response.SessionID == "" {
		t.Fatalf("create: %s %v", created.Body.String(), err)
	}
	id := response.SessionID
	projection, _, _ := sessions.GetSessionProjection(id)
	call := func(principal identity.Principal, scopes []string, body any) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+id+"/context/clear", bytes.NewReader(payload))
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, principal)
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Scopes: scopes})
		w := httptest.NewRecorder()
		s.handleSessionV3PrimaryByID(w, r.WithContext(ctx))
		return w
	}
	body := map[string]any{"client_request_id": "clear", "expected_last_event_seq": projection.LastEventSeq}
	before, _, _ := sessions.GetActiveExecutionEpoch(id)
	other := p
	other.UserID = "other"
	foreign := p
	foreign.AccountScopeID = "other-account"
	for _, item := range []struct {
		p      identity.Principal
		scopes []string
		body   any
	}{
		{p, []string{"sessions:read"}, body}, {other, []string{"sessions:write"}, body}, {foreign, []string{"sessions:write"}, body},
		{p, []string{"sessions:write"}, map[string]any{"client_request_id": "missing"}},
		{p, []string{"sessions:write"}, map[string]any{"client_request_id": "stale", "expected_last_event_seq": 0}},
	} {
		w := call(item.p, item.scopes, item.body)
		if w.Code < 400 {
			t.Fatalf("accepted invalid clear: %s", w.Body.String())
		}
		epoch, _, _ := sessions.GetActiveExecutionEpoch(id)
		if epoch.EpochID != before.EpochID {
			t.Fatal("rejected clear changed context")
		}
	}
	for i := 0; i < 2; i++ {
		w := call(p, []string{"sessions:write"}, body)
		if w.Code != http.StatusOK {
			t.Fatalf("clear: %d %s", w.Code, w.Body.String())
		}
		var result struct {
			SessionID string                              `json:"session_id"`
			Mutation  pebblestore.V3SessionMutationResult `json:"mutation"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.SessionID != id || result.Mutation.RealtimeOutbox == nil {
			t.Fatalf("receipt: %s %v", w.Body.String(), err)
		}
	}
	epoch, _, _ := sessions.GetActiveExecutionEpoch(id)
	if epoch.Ordinal != before.Ordinal+1 {
		t.Fatal("retry rotated context twice")
	}
	executor := &sessionV3Executor{server: s}
	messages := []pebblestore.MessageSnapshot{{ID: "fresh", Content: "fresh context"}}
	for _, load := range []func(string, pebblestore.ExecutionEpoch, []pebblestore.MessageSnapshot) ([]pebblestore.MessageSnapshot, error){executor.sessionV3ProviderFinalHandoffContextMessages, executor.sessionV3ProviderResumeContextMessages} {
		got, err := load(id, epoch, messages)
		if err != nil || len(got) != 1 || got[0].ID != "fresh" {
			t.Fatalf("crossed clear boundary: %+v %v", got, err)
		}
	}
	items, _ := sessions.Store().ListProjectConversations(p.AccountScopeID, p.UserID, "project", 10)
	if len(items) != 1 || items[0].ID != id {
		t.Fatal("clear created a replacement session")
	}
}
