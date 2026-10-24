package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"context"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: workflow history/restore must reuse set authorization, never let a
// write key resurrect host commands, and retain portable snapshots after delete.
// The MCP and HTTP handlers over a real store are the narrowest layer exercising
// controlMCPSaveAutomationChange, automationSteps and runStepOwnerError together.
func TestWorkflowHistoryRestoreAuthorization(t *testing.T) {
	server, store := newRunOwnerServer(t)
	account := testPrincipal().AccountScopeID
	call := func(level string) *controlMCPCall {
		return &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{ID: "key-" + level, Scopes: scopesForLevel(t, level)})}
	}
	full := call("full")
	args := map[string]any{"name": "history", "scope": map[string]any{"folder": t.TempDir()}, "steps": []any{map[string]any{"type": "run", "command": "deliver/push.sh"}}}
	if _, err := controlMCPSetAutomation(full, args); err != nil {
		t.Fatal(err)
	}
	args["enabled"] = false
	if _, err := controlMCPSetAutomation(full, args); err != nil {
		t.Fatal(err)
	}
	if _, err := controlMCPSetAutomation(full, args); err != nil {
		t.Fatal(err)
	}
	a, err := store.FindByName(account, "history")
	if err != nil || a.Revision != 2 {
		t.Fatalf("no-op %+v %v", a, err)
	}
	if _, err := controlMCPDeleteAutomation(full, map[string]any{"id": a.ID}); err != nil {
		t.Fatal(err)
	}
	lookup := map[string]any{"id": a.ID, "revision": 1}
	other := call("read")
	principal := testPrincipal()
	principal.AccountScopeID = "other-account"
	other.request = other.request.WithContext(context.WithValue(other.request.Context(), productPrincipalRequestContextKey, principal))
	if _, err := controlMCPWorkflowHistory(other, lookup); err != jobautomation.ErrNotFound {
		t.Fatalf("cross-account history: %v", err)
	}
	out, err := controlMCPWorkflowHistory(call("read"), lookup)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(out)
	if err != nil || !strings.Contains(string(data), `"swarm_workflow":1`) || strings.Contains(string(data), a.Scope.Folder) {
		t.Fatalf("portable: %s %v", data, err)
	}
	if _, err := controlMCPRestoreWorkflow(call("write"), lookup); err == nil {
		t.Fatal("write key restored run step")
	}
	r := requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", JobAutomationsPath+"/restore", strings.NewReader(`{"name":"history","revision":1}`))), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})
	w := httptest.NewRecorder()
	server.handleJobAutomations(w, r)
	if w.Code != 403 {
		t.Fatalf("HTTP refusal %d %s", w.Code, w.Body)
	}
	if _, err = store.Get(account, a.ID); err != jobautomation.ErrNotFound {
		t.Fatalf("refused restore mutated definition: %v", err)
	}
	h, err := store.History(account, a.ID, "")
	if err != nil || len(h) != 3 {
		t.Fatal("refused restore changed history", h, err)
	}
	if _, err = controlMCPRestoreWorkflow(full, lookup); err != nil {
		t.Fatal(err)
	}
	a, err = store.Get(account, a.ID)
	if err != nil || a.Revision != 4 || !a.Enabled {
		t.Fatalf("restore %+v %v", a, err)
	}
	h, err = store.History(account, a.ID, "")
	if err != nil || h[0].Kind != "restored" || h[0].Source != "restore" || h[0].Who != "key-full" {
		t.Fatal(h, err)
	}
	r = withTestPrincipal(httptest.NewRequest("GET", JobAutomationsPath+"/history?name=history", nil))
	w = httptest.NewRecorder()
	server.handleJobAutomations(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"changed_fields"`) || strings.Contains(w.Body.String(), `"definition"`) {
		t.Fatalf("HTTP history %d %s", w.Code, w.Body)
	}
	out, err = controlMCPListAutomations(full, nil)
	if err != nil {
		t.Fatal(err)
	}
	view := out.(map[string]any)["automations"].([]map[string]any)[0]
	if view["history_count"] != 4 || view["last_change"] == nil {
		t.Fatal(view)
	}
}
