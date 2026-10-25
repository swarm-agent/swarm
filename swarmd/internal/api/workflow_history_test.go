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
// handleJobAutomations and SaveWorkflow together through MCP dispatch.
func TestWorkflowHistoryRestoreAuthorization(t *testing.T) {
	server, store := newRunOwnerServer(t)
	account := testPrincipal().AccountScopeID
	call := func(level string) *controlMCPCall {
		c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{ID: "key-" + level, Scopes: scopesForLevel(t, level)})}
		wireWorkflowRoutes(c)
		return c
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
	if _, err := controlMCPWorkflowHistory(other, lookup); err == nil || !strings.Contains(err.Error(), "workflow history was not found") {
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
	view := out.(map[string]any)["automations"].([]any)[0].(map[string]any)
	if view["history_count"] != float64(4) || view["last_change"] == nil {
		t.Fatal(view)
	}
}

// Purpose: history/restore client mistakes must remain actionable through MCP
// serialization and HTTP, without changing saved definitions or history. Real
// handler/store tests are the narrowest boundary proving error mapping and the
// absence of writes, including restore's shared fixes validation.
func TestWorkflowHistoryClientErrors(t *testing.T) {
	server, store := newRunOwnerServer(t)
	account := testPrincipal().AccountScopeID
	a, _, err := store.Put(account, jobautomation.Automation{Name: "deleted", Scope: jobautomation.Scope{Folder: t.TempDir()}, Enabled: true, Steps: []jobautomation.Step{{Type: jobautomation.StepRun, Command: "deliver/push.sh"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Delete(account, a.ID); err != nil {
		t.Fatal(err)
	}
	// Store accepts the structurally valid snapshot; restore must re-check the
	// API-owned worker authorization even when its worker service is unavailable.
	_, _, err = store.Put(account, jobautomation.Automation{Name: "fixes", Scope: jobautomation.Scope{Folder: t.TempDir()}, Enabled: true, Steps: []jobautomation.Step{{Type: jobautomation.StepAgent, Worker: "missing-worker", Brief: "Review", Fixes: "security"}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		defs, err := store.List(account)
		if err != nil {
			t.Fatal(err)
		}
		deleted, err := store.History(account, "", "deleted")
		if err != nil {
			t.Fatal(err)
		}
		fixes, err := store.History(account, "", "fixes")
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal([]any{defs, deleted, fixes})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	before := snapshot()
	for _, tc := range []struct {
		action, args, query, want string
		status                    int
	}{
		{"history", `{"name":"deleted","revision":"abc"}`, "name=deleted&revision=abc", "revision must be a positive integer", 400},
		{"restore", `{"name":"deleted"}`, "", "revision is required", 400},
		{"restore", `{"name":"deleted","revision":0}`, "", "revision must be a positive integer", 400},
		{"history", `{"name":"unknown"}`, "name=unknown", "workflow history was not found", 404},
		{"restore", `{"name":"unknown","revision":1}`, "", "workflow history was not found", 404},
		{"history", `{"name":"deleted","revision":99}`, "name=deleted&revision=99", "revision 99 was not found", 404},
		{"restore", `{"name":"deleted","revision":99}`, "", "revision 99 was not found", 404},
		{"restore", `{"name":"deleted","revision":2}`, "", "deleted marker", 400},
		{"restore", `{"name":"fixes","revision":1}`, "", "fixes option", 400},
	} {
		t.Run(tc.action+tc.args, func(t *testing.T) {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
				t.Fatal(err)
			}
			r := requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "full")})
			c := &controlMCPCall{server: server, request: r}
			wireWorkflowRoutes(c)
			var out any
			var err error
			if tc.action == "history" {
				out, err = controlMCPWorkflowHistory(c, args)
			} else {
				out, err = controlMCPRestoreWorkflow(c, args)
			}
			result := controlMCPToolResult(out, err)
			text := result["content"].([]map[string]any)[0]["text"].(string)
			if result["isError"] != true || !strings.Contains(text, tc.want) {
				t.Fatalf("MCP: %#v", result)
			}
			method := "POST"
			if tc.action == "history" {
				method = "GET"
			}
			r = withTestPrincipal(httptest.NewRequest(method, JobAutomationsPath+"/"+tc.action+"?"+tc.query, strings.NewReader(tc.args)))
			w := httptest.NewRecorder()
			server.handleJobAutomations(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("HTTP: %d %s", w.Code, w.Body)
			}
			if after := snapshot(); after != before {
				t.Fatal("rejected request changed definitions or history")
			}
		})
	}
}
