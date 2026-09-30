package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"encoding/json"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: indexed accounting must not expose projections without an authenticated
// principal. Boundary: handleUsageScope. Direct HTTP handler invocation proves
// fail-closed rejection without constructing an unrelated executor or provider.
func TestUsageScopeRequiresPrincipal(t *testing.T) {
	server := &Server{}
	response := httptest.NewRecorder()
	server.handleUsageScope(response, httptest.NewRequest(http.MethodGet, "/v3/usage/scope?kind=worker&id=worker", nil))
	if response.Code != http.StatusUnauthorized { t.Fatalf("status = %d", response.Code) }
}

// Purpose: authenticated hydration returns indexed measured components, while
// foreign entities and malformed windows are rejected. Owners: handleUsageScope
// and account-keyed GetProjectTask/GetUsageScopeDay. A temporary store-backed
// handler is the narrowest layer proving the HTTP contract without providers.
func TestUsageScopeHydrationAndForeignEntity(t *testing.T) {
	server, _, db := newWorkspaceOverviewTopologyTestServer(t)
	s := pebblestore.NewSessionStore(db)
	account := testPrincipal().AccountScopeID
	task := pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: account, SessionID: "scope-api"}
	if err := db.PutJSON(pebblestore.KeyProjectTask(account, "project", "task"), task); err != nil { t.Fatal(err) }
	if err := db.PutJSON(pebblestore.KeySession("scope-api"), pebblestore.SessionSnapshot{ID: "scope-api", AccountScopeID: account, Metadata: map[string]any{"project_id": "project", "task_id": "task"}}); err != nil { t.Fatal(err) }
	if err := s.PutTurnUsage(pebblestore.SessionTurnUsageSnapshot{SessionID: "scope-api", AccountScopeID: account, RunID: "run", Provider: "fixture", Model: "fixture", BilledUsagePresent: true, BilledTokens: 100, BilledInputTokens: 100, PriceStatus: "free", CreatedAt: 3000}); err != nil { t.Fatal(err) }
	request := func(account, suffix string) *httptest.ResponseRecorder {
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/usage/scope?kind=task&project_id=project&id=task"+suffix, nil), testPrincipal().UserID, account)
		w := httptest.NewRecorder()
		server.handleUsageScope(w, r)
		return w
	}
	w := request(account, "&date=1970-01-01")
	if w.Code != http.StatusOK { t.Fatalf("hydrate: %d %s", w.Code, w.Body.String()) }
	var response struct { Usage pebblestore.UsageScopeTotal `json:"usage"`; Recorded bool `json:"recorded"` }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil { t.Fatal(err) }
	if !response.Recorded || response.Usage.TotalTokens != 100 || response.Usage.InputTokens != 100 || response.Usage.HistoryComplete { t.Fatalf("hydrate projection: %+v", response) }
	if w := request("foreign-account", ""); w.Code != http.StatusNotFound { t.Fatalf("foreign entity: %d", w.Code) }
	if w := request(account, "&date=1970-02-30"); w.Code != http.StatusBadRequest { t.Fatalf("invalid day: %d", w.Code) }
}

// Purpose: child/scoped credentials cannot hydrate account-wide accounting.
// Owner: handleUsageScope; direct handler rejection proves no store is accessed.
func TestUsageScopeRejectsScopedToken(t *testing.T) {
	r := requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/usage/scope?kind=worker&id=worker", nil))
	r = requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{})
	w := httptest.NewRecorder()
	(&Server{}).handleUsageScope(w, r)
	if w.Code != http.StatusForbidden { t.Fatalf("scoped token: %d", w.Code) }
}
