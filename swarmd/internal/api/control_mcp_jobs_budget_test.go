package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: MCP budget writes must reach SetWorkerBudget, the policy read by
// admission, without usage-management authority. Real temporary store/handlers
// are the narrowest boundary proving set/reset, receipt-backed listing, and
// invalid/unauthorized writes leave policy unchanged. Listing must disclose spend
// only with sessions:read, including nested budget data.
func TestControlMCPWorkerBudget(t *testing.T) {
	c, _ := newMCPPresetTestCall(t)
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"automations:read", "automations:write"}})
	result, err := controlMCPDeployWorker(c, map[string]any{"preset": "coder", "budget": map[string]any{"daily_usd": 10, "daily_tokens": 100}})
	if err != nil {
		t.Fatal(err)
	}
	id := result.(map[string]any)["worker"].(map[string]any)["id"].(string)
	account := testPrincipal().AccountScopeID
	st := c.server.sessions.Store()
	policy, err := st.GetWorkerBudget(account, id)
	if err != nil || policy.DailyCostLimitUSD != 10 || policy.DailyTokensLimit != 100 {
		t.Fatalf("policy: %+v %v", policy, err)
	}
	for _, raw := range []any{map[string]any{"daily_usd": -1}, map[string]any{"daily_usd": 10, "daily_tokens": 1.5}, map[string]any{"daily_tokens": 5}, map[string]any{"daily_usd": 10, "extra": true}, "bad"} {
		if _, err := controlMCPDeployWorker(c, map[string]any{"worker": id, "budget": raw}); err == nil {
			t.Fatalf("accepted invalid budget %#v", raw)
		}
		got, _ := st.GetWorkerBudget(account, id)
		if got != policy {
			t.Fatal("invalid request changed policy")
		}
	}
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"automations:read", "automations:trigger"}})
	if _, err := controlMCPDeployWorker(c, map[string]any{"worker": id, "budget": nil}); err == nil {
		t.Fatal("trigger cleared budget")
	}
	got, _ := st.GetWorkerBudget(account, id)
	if got != policy {
		t.Fatal("denial changed policy")
	}
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"automations:read", "automations:write"}})
	if _, err := controlMCPDeployWorker(c, map[string]any{"worker": id, "budget": nil}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetWorkerBudget(account, id)
	if got.DailyCostLimitUSD != 0 || got.DailyTokensLimit != 0 || got.Revision != policy.Revision+1 {
		t.Fatalf("reset: %+v", got)
	}
	if _, err := controlMCPDeployWorker(c, map[string]any{"worker": id, "budget": map[string]any{"daily_usd": 10}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(store.SessionSnapshot{ID: "budget-session", UserID: testPrincipal().UserID, AccountScopeID: account, Metadata: map[string]any{"worker_id": id, "worker_run_id": "budget-run"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WorkerStore().RecordWorkerRun(account, store.WorkerRunRecord{ID: "budget-run", WorkerID: id, UserID: testPrincipal().UserID, SessionID: "budget-session", Status: "succeeded", RequestSource: "direct", CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTurnUsage(store.SessionTurnUsageSnapshot{SessionID: "budget-session", RunID: "budget-run", AccountScopeID: account, UserID: testPrincipal().UserID, Provider: "fixture", Model: "fixture", TotalTokens: 200, EstimatedCostUSD: 11, CostProvenance: "provider", PriceStatus: "known", CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"automations:read"}})
	restricted, err := controlMCPListWorkerJobs(c, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	restrictedRows := restricted.(map[string]any)["workers"].([]map[string]any)
	if len(restrictedRows) != 1 {
		t.Fatalf("rows: %v", restrictedRows)
	}
	restrictedBudget := restrictedRows[0]["budget"].(map[string]any)
	if restrictedBudget["daily_usd"] != float64(10) || restrictedBudget["blocked"] != true {
		t.Fatalf("missing policy/status: %v", restrictedBudget)
	}
	encoded, err := json.Marshal(restrictedRows[0])
	if err != nil {
		t.Fatal(err)
	}
	var row any
	if err := json.Unmarshal(encoded, &row); err != nil {
		t.Fatal(err)
	}
	var checkNoSpend func(any)
	checkNoSpend = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if key != "daily_tokens" && key != "daily_usd" && (strings.Contains(strings.ToLower(key), "usage") || strings.Contains(strings.ToLower(key), "token") || strings.Contains(strings.ToLower(key), "cost")) {
					t.Fatalf("spend field %q in restricted row: %s", key, encoded)
				}
				checkNoSpend(child)
			}
		case []any:
			for _, child := range v {
				checkNoSpend(child)
			}
		}
	}
	checkNoSpend(row)
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"automations:read", "sessions:read"}})
	listed, err := controlMCPListWorkerJobs(c, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	rows := listed.(map[string]any)["workers"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("rows: %v", rows)
	}
	budget := rows[0]["budget"].(map[string]any)
	if budget["daily_usd"] != float64(10) || budget["blocked"] != true || budget["usage"].(store.UsageScopeTotal).TotalTokens != 200 {
		t.Fatalf("budget: %#v", budget)
	}
	if err := st.CheckWorkerBudgetAdmission(account, id); err == nil {
		t.Fatal("enforcement allowed exhausted budget")
	}
}

// Purpose: handleWorkerBudget must reject worker-bound automation writers before
// any policy access. Direct handler requests with a real store prove HTTP 403 on
// reads and writes and unchanged policy, including a nonexistent target.
func TestWorkerBudgetRejectsWorkerBoundToken(t *testing.T) {
	c, _ := newMCPPresetTestCall(t)
	c.request = requestWithScopedToken(c.request, &store.ScopedTokenRecord{Scopes: []string{"automations:read", "automations:write"}})
	result, err := controlMCPDeployWorker(c, map[string]any{"preset": "coder", "budget": map[string]any{"daily_usd": 10}})
	if err != nil {
		t.Fatal(err)
	}
	id := result.(map[string]any)["worker"].(map[string]any)["id"].(string)
	st := c.server.sessions.Store()
	policy, err := st.GetWorkerBudget(testPrincipal().AccountScopeID, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{id, "missing-worker"} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			body := fmt.Sprintf(`{"expected_revision":%d,"daily_cost_limit_usd":0,"daily_tokens_limit":0}`, policy.Revision)
			req := httptest.NewRequest(method, "/v3/usage/worker-budget?worker_id="+target, strings.NewReader(body)).WithContext(c.request.Context())
			req = requestWithScopedToken(req, &store.ScopedTokenRecord{WorkerID: id, Scopes: []string{"automations:write"}})
			response := httptest.NewRecorder()
			c.server.handleWorkerBudget(response, req)
			if response.Code != http.StatusForbidden {
				t.Fatalf("%s %s: status %d", method, target, response.Code)
			}
			got, err := st.GetWorkerBudget(testPrincipal().AccountScopeID, id)
			if err != nil || got != policy {
				t.Fatalf("denied request mutated policy: %+v %v", got, err)
			}
		}
	}
}
