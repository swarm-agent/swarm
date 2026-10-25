package api

import (
	"context"
	"encoding/json"
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
// prove these postconditions without a provider or a live daemon. The shared
// jobAutomationView and ManageWorkflows results must also distinguish replacement
// proposals from new workflows, without leaking replacement metadata after acceptance.
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
		out, err := s.ManageWorkflows(context.Background(), p, "session", args)
		if err != nil {
			t.Fatal(err)
		}
		list := storedAutomations(t, store)
		for _, a := range list {
			if a.Proposed {
				check := func(view map[string]any) {
					t.Helper()
					if a.BaseID == "" {
						if _, exists := view["replaces"]; exists {
							t.Fatal("new workflow claims replacement", view)
						}
						return
					}
					base, err := store.Get(p.AccountScopeID, a.BaseID)
					if err != nil {
						t.Fatal(err)
					}
					replaces, ok := view["replaces"].(map[string]any)
					if !ok || replaces["id"] != base.ID || replaces["revision"] != base.Revision || replaces["name"] != base.Name {
						t.Fatal("incorrect replacement", view, base)
					}
				}
				check(jobAutomationView(a))
				response := out.(map[string]any)
				check(response["automation"].(map[string]any))
				message := "Accepting adds a new workflow."
				if a.BaseID != "" {
					message = fmt.Sprintf("Accepting replaces %s (revision %d); it keeps that workflow's id.", a.Name, a.BaseRevision)
				}
				if !strings.Contains(response["next"].(string), message) {
					t.Fatal("missing acceptance effect", response)
				}
				got, err := s.ManageWorkflows(context.Background(), p, "session", map[string]any{"action": "get", "id": a.ID})
				if err != nil {
					t.Fatal(err)
				}
				check(got.(map[string]any)["automation"].(map[string]any))
				listed, err := s.ManageWorkflows(context.Background(), p, "session", map[string]any{"action": "list"})
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, view := range listed.(map[string]any)["automations"].([]map[string]any) {
					if view["id"] == a.ID {
						check(view)
						found = true
					}
				}
				if !found {
					t.Fatal("proposal absent from list")
				}
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
		if w.Code == http.StatusOK {
			var response struct {
				Automation map[string]any `json:"automation"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			wantID := a.ID
			if a.BaseID != "" {
				wantID = a.BaseID
			}
			if response.Automation["id"] != wantID || response.Automation["state"] != "active" {
				t.Fatal("acceptance changed workflow identity", response)
			}
			if _, exists := response.Automation["replaces"]; exists {
				t.Fatal("accepted workflow still claims replacement", response)
			}
		}
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
	if a.BaseID != active.ID || a.BaseRevision != active.Revision || a.ID == active.ID {
		t.Fatal("replacement proposal lost its active base", a, active)
	}
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
