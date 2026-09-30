package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: budgets must be editable only by authenticated users, not delegated
// scoped tokens, and stale/foreign requests must leave policy unchanged. Owners
// handleWorkerBudget/SetWorkerBudget; a store-backed HTTP seam is the narrowest
// authorization and revision wire-contract test without live providers.
func TestWorkerBudgetUserAuthorizationRevision(t *testing.T) {
	server, _, db := newWorkspaceOverviewTopologyTestServer(t)
	account := testPrincipal().AccountScopeID
	if err := db.PutJSON(store.KeyWorker(account, "worker"), store.WorkerRecord{ID: "worker", AccountScopeID: account}); err != nil {
		t.Fatal(err)
	}
	invoke := func(body string, scoped bool, acct string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/v3/usage/worker-budget?worker_id=worker", strings.NewReader(body))
		if acct != "" {
			r = requestWithTestPrincipalForAccount(r, testPrincipal().UserID, acct)
		}
		if scoped {
			r = requestWithScopedToken(r, &store.ScopedTokenRecord{})
		}
		w := httptest.NewRecorder()
		server.handleWorkerBudget(w, r)
		return w
	}
	body := `{"expected_revision":0,"daily_cost_limit_usd":1,"daily_tokens_limit":100}`
	if w := invoke(body, false, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := invoke(body, true, account); w.Code != http.StatusForbidden {
		t.Fatalf("scoped: %d", w.Code)
	}
	if w := invoke(body, false, "other-account"); w.Code != http.StatusNotFound {
		t.Fatalf("foreign: %d", w.Code)
	}
	if w := invoke(body, false, account); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	if w := invoke(body, false, account); w.Code != http.StatusConflict {
		t.Fatalf("stale: %d", w.Code)
	}
	for _, invalid := range []string{`{"daily_cost_limit_usd":0}`, body + `{}`, `{"expected_revision":1,"account_scope_id":"other"}`, `{"expected_revision":1,"daily_tokens_limit":-1}`} {
		if w := invoke(invalid, false, account); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid: %d %s", w.Code, w.Body.String())
		}
	}
	policy, err := server.sessions.Store().GetWorkerBudget(account, "worker")
	if err != nil || policy.Revision != 1 || policy.DailyCostLimitUSD != 1 || policy.DailyTokensLimit != 100 {
		t.Fatalf("rejections mutated: %+v %v", policy, err)
	}
}
