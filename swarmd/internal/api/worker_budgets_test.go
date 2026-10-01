package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
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
	service := testPrincipal()
	service.Type = "service"
	serviceRequest := httptest.NewRequest(http.MethodPut, "/v3/usage/worker-budget?worker_id=worker", strings.NewReader(body))
	serviceRequest = serviceRequest.WithContext(context.WithValue(serviceRequest.Context(), productPrincipalRequestContextKey, service))
	serviceResponse := httptest.NewRecorder()
	server.handleWorkerBudget(serviceResponse, serviceRequest)
	if serviceResponse.Code != http.StatusUnauthorized && serviceResponse.Code != http.StatusForbidden {
		t.Fatalf("service principal accepted: %d", serviceResponse.Code)
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

// Purpose: user budget GET exposes canonical indexed status rather than policy
// alone. Owner handleWorkerBudget/GetWorkerBudgetStatus; store-backed HTTP is
// the narrowest wire layer that proves unset caps, coverage and UTC semantics.
func TestWorkerBudgetStatusHTTP(t *testing.T) {
	server, _, db := newWorkspaceOverviewTopologyTestServer(t)
	account := testPrincipal().AccountScopeID
	if err := db.PutJSON(store.KeyWorker(account, "worker"), store.WorkerRecord{ID: "worker", AccountScopeID: account}); err != nil {
		t.Fatal(err)
	}
	r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/usage/worker-budget?worker_id=worker", nil), testPrincipal().UserID, account)
	w := httptest.NewRecorder()
	server.handleWorkerBudget(w, r)
	var status store.WorkerBudgetStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || status.WorkerID != "worker" || status.Date != time.Now().UTC().Format("2006-01-02") || status.Usage.Coverage != "no_records" || status.RemainingTokens != nil || status.Blocked || status.Limitations == "" {
		t.Fatalf("status: %d %+v", w.Code, status)
	}
}

// Purpose: account caps must not classify sessionless Router calls as workers.
// Owners invokeConfiguredRouterOnce and CheckWorkerUnmeteredOperation; the
// existing provider seam proves dispatch and unchanged account policy.
func TestWorkerBudgetRouterAccountWithoutSession(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: "ordinary routing result"}}
	server, principal, _ := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Git workspace"}})
	budgetServer, _, _ := newWorkspaceOverviewTopologyTestServer(t)
	server.sessions = budgetServer.sessions
	principal.SessionID = ""
	if err := server.sessions.Store().PutUsageLimit(store.UsageLimitRecord{AccountScopeID: principal.AccountScopeID, Enabled: true, DailyCostLimitUSD: 1}); err != nil {
		t.Fatal(err)
	}
	if response, err := server.invokeConfiguredRouterOnce(context.Background(), principal, "instructions", "input", 1024); err != nil || response.Text != "ordinary routing result" {
		t.Fatalf("ordinary Router blocked: %+v %v", response, err)
	}
	if runner.createCalls != 1 || runner.streamingCalls != 0 {
		t.Fatal("ordinary Router was not dispatched once")
	}
	policy, _, err := server.sessions.Store().GetUsageLimit(principal.AccountScopeID)
	if err != nil || !policy.Enabled || policy.DailyCostLimitUSD != 1 {
		t.Fatalf("policy mutated: %+v %v", policy, err)
	}
}
