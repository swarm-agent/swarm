package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: the native lifecycle and HTTP acceptance boundary must keep proposals
// inert, reject AI/agent acceptance and non-owner scripts without changing state,
// and preserve active revisions until explicit acceptance. Real routes and Pebble
// prove these postconditions without a provider or a live daemon.
func TestWorkflowNativeProposals(t *testing.T) {
	s, store := newRunOwnerServer(t)
	folder := t.TempDir()
	p := testPrincipal()
	c := &controlMCPCall{server: s, request: withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "project", "name": "Project", "workspaces": []any{map[string]any{"path": folder}}}}})
	})}
	wireWorkflowRoutes(c)
	args := map[string]any{"action": "propose", "name": "review", "project": "Project", "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "review"}}}
	propose := func() jobautomation.Automation {
		t.Helper()
		if _, err := s.ManageWorkflows(context.Background(), p, "session", args); err != nil {
			t.Fatal(err)
		}
		list := storedAutomations(t, store)
		for _, a := range list {
			if a.Proposed {
				return a
			}
		}
		t.Fatal("missing proposal")
		return jobautomation.Automation{}
	}
	a := propose()
	if controlMCPRouteAllowed(http.MethodPost, JobAutomationsPath+"/"+a.ID+"/accept") {
		t.Fatal("MCP may accept proposals")
	}
	first := a
	a = propose()
	if a.ID != first.ID || a.Revision <= first.Revision || len(storedAutomations(t, store)) != 1 {
		t.Fatal("proposal not revised")
	}
	result := &jobautomation.Result{Folder: folder, Status: jobautomation.StatusSucceeded}
	if a.Matches(result) {
		t.Fatal("proposal runnable")
	}
	accept := func(token *pebblestore.ScopedTokenRecord, revision uint64) int {
		t.Helper()
		r := withTestPrincipal(httptest.NewRequest("POST", JobAutomationsPath+"/"+a.ID+"/accept", strings.NewReader(fmt.Sprintf(`{"expected_revision":%d}`, revision))))
		if token != nil {
			r = requestWithScopedToken(r, token)
		}
		w := httptest.NewRecorder()
		s.handleJobAutomations(w, r)
		return w.Code
	}
	if got := accept(&pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "full")}, a.Revision); got != 403 {
		t.Fatal("AI accepted", got)
	}
	if _, err := s.ManageWorkflows(context.Background(), p, "session", map[string]any{"action": "accept", "id": a.ID}); err == nil {
		t.Fatal("agent accepted")
	}
	if got := accept(nil, first.Revision); got != 409 {
		t.Fatal("stale accepted", got)
	}
	unchanged, _ := store.Get(p.AccountScopeID, a.ID)
	if !unchanged.Proposed || unchanged.Revision != a.Revision {
		t.Fatal("rejection mutated proposal")
	}
	if got := accept(nil, a.Revision); got != 200 {
		t.Fatal("person refused", got)
	}
	active, _ := store.Get(p.AccountScopeID, a.ID)
	if !active.Matches(result) {
		t.Fatal("accepted workflow inert")
	}
	args["steps"] = []any{map[string]any{"type": "run", "command": "deliver/check.sh"}}
	a = propose()
	still, _ := store.Get(p.AccountScopeID, active.ID)
	if still.Steps[0].Type != "agent" || !still.Matches(result) {
		t.Fatal("proposal changed active")
	}
	if got := accept(&pebblestore.ScopedTokenRecord{Scopes: []string{"automations:write"}}, a.Revision); got != 403 {
		t.Fatal("non-owner script accepted", got)
	}
	if got := accept(nil, a.Revision); got != 200 {
		t.Fatal("owner script refused", got)
	}
	updated, _ := store.Get(p.AccountScopeID, active.ID)
	if updated.Steps[0].Type != "run" {
		t.Fatal("not replaced")
	}
	a = propose()
	r := withTestPrincipal(httptest.NewRequest("DELETE", JobAutomationsPath+"/"+a.ID, nil))
	w := httptest.NewRecorder()
	s.handleJobAutomations(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if _, err := store.Get(p.AccountScopeID, a.ID); err != jobautomation.ErrNotFound {
		t.Fatal("discard retained proposal", err)
	}
	// MCP keeps its existing immediate activation contract.
	args["name"] = "mcp-active"
	delete(args, "action")
	args["steps"] = []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "review"}}
	if _, err := controlMCPSetAutomation(c, args); err != nil {
		t.Fatal(err)
	}
	mcp, err := store.FindByName(p.AccountScopeID, "mcp-active")
	if err != nil || mcp.Proposed || !mcp.Matches(result) {
		t.Fatal("MCP did not activate", err)
	}
}
