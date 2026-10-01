package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: HTTP user budget policy rejects above-account requests without a
// revision change and returns nullable effective ceilings for dependent UI.
// handleWorkerBudget/SetWorkerBudget own the contract; store-backed HTTP is
// the narrowest layer proving wire errors and rejection postconditions.
func TestWorkerBudgetHTTPAccountCeiling(t *testing.T) {
	server, _, db := newWorkspaceOverviewTopologyTestServer(t)
	account := testPrincipal().AccountScopeID
	if err := db.PutJSON(store.KeyWorker(account, "worker"), store.WorkerRecord{ID: "worker", AccountScopeID: account}); err != nil {
		t.Fatal(err)
	}
	if err := server.sessions.Store().PutUsageLimit(store.UsageLimitRecord{AccountScopeID: account, Enabled: true, DailyCostLimitUSD: 1, DailyTokensLimit: 10}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"expected_revision":0,"daily_cost_limit_usd":2}`, `{"expected_revision":0,"daily_tokens_limit":11}`} {
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPut, "/v3/usage/worker-budget?worker_id=worker", strings.NewReader(body)), testPrincipal().UserID, account)
		w := httptest.NewRecorder()
		server.handleWorkerBudget(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("above ceiling: %d %s", w.Code, w.Body.String())
		}
	}
	p, err := server.sessions.Store().GetWorkerBudget(account, "worker")
	if err != nil || p.Revision != 0 {
		t.Fatalf("rejection changed policy: %+v %v", p, err)
	}
	r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/usage/worker-budget?worker_id=worker", nil), testPrincipal().UserID, account)
	w := httptest.NewRecorder()
	server.handleWorkerBudget(w, r)
	var status store.WorkerBudgetStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || status.Hold != nil || status.ResetAt <= 0 || status.EffectiveCostLimitUSD == nil || *status.EffectiveCostLimitUSD != 1 || status.EffectiveTokensLimit == nil || *status.EffectiveTokensLimit != 10 {
		t.Fatalf("effective status: %d %+v", w.Code, status)
	}
}
