package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// wireWorkflowRoutes gives legacy project fixtures a real workflow HTTP boundary.
// Project reads remain fixture-provided; workflow persistence always runs through
// handleJobAutomations, including nested import/export project authorization.
func wireWorkflowRoutes(c *controlMCPCall) {
	projects := c.next
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, JobAutomationsPath) {
			c.server.handleJobAutomations(w, r)
			return
		}
		if projects != nil {
			projects.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	c.server.controlMCPRoutesOnce.Do(func() {})
	c.server.controlMCPRoutes = routes
	c.next = routes
}

// Purpose: all workflow MCP entrypoints must dispatch to their HTTP authority,
// not touch storage themselves. A recording route with no server/store is the
// narrowest proof that each tool delegates and preserves route refusals.
func TestWorkflowToolsDispatch(t *testing.T) {
	tools := controlMCPAutomationTools()
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			calls := 0
			c := &controlMCPCall{request: withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.HasPrefix(r.URL.Path, JobAutomationsPath) {
					t.Errorf("route %s", r.URL.Path)
				}
				writeError(w, http.StatusForbidden, errRunStepNeedsOwner)
			})}
			_, err := tool.call(c, map[string]any{"id": "workflow", "revision": 1, "stopped": true})
			if calls != 1 || err == nil || err.Error() != errRunStepNeedsOwner.Error() {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

// Purpose: SaveWorkflow is the single validation/persistence boundary. HTTP and
// MCP must store identical definitions, and owner/reviewer refusals must neither
// mutate the definition nor append history. Real handler/store calls are the
// narrowest layer proving transport parity and both negative postconditions.
func TestWorkflowSaveParity(t *testing.T) {
	s, store := newRunOwnerServer(t)
	folder := t.TempDir()
	request := requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})
	c := &controlMCPCall{server: s, request: request, next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"projects": []any{map[string]any{"id": "project", "name": "Project", "workspaces": []any{map[string]any{"path": folder}}}}})
	})}
	wireWorkflowRoutes(c)
	args := map[string]any{"name": "parity", "project": "Project", "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "review"}}}
	route := func() *httptest.ResponseRecorder {
		data, _ := json.Marshal(args)
		r := httptest.NewRequest("POST", JobAutomationsPath, bytes.NewReader(data)).WithContext(request.Context())
		w := httptest.NewRecorder()
		s.handleJobAutomations(w, r)
		return w
	}
	if w := route(); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	before := storedAutomations(t, store)[0]
	if _, err := controlMCPSetAutomation(c, args); err != nil {
		t.Fatal(err)
	}
	after := storedAutomations(t, store)[0]
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("different stored automation: %+v %+v", before, after)
	}
	for _, step := range []map[string]any{
		{"type": "run", "command": "deliver/push.sh"},
		{"type": "agent", "worker": "coder", "brief": "fix", "fixes": "security"},
	} {
		args["steps"] = []any{step}
		w := route()
		var response map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		_, err := controlMCPSetAutomation(c, args)
		if w.Code < 400 || err == nil || err.Error() != response["error"] {
			t.Fatalf("refusal mismatch: %d %s %v", w.Code, w.Body, err)
		}
		if got := storedAutomations(t, store)[0]; !reflect.DeepEqual(before, got) {
			t.Fatal("refusal mutated workflow")
		}
		history, err := store.History(testPrincipal().AccountScopeID, before.ID, "")
		if err != nil || len(history) != 1 {
			t.Fatalf("refusal mutated history: %v %v", history, err)
		}
	}
}

// Purpose: StopWorkflows owns the machine switch. Real MCP dispatch plus store
// reads prove both transitions and that a missing boolean never changes state.
func TestWorkflowStopParity(t *testing.T) {
	s, store := newRunOwnerServer(t)
	c := &controlMCPCall{server: s, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})}
	wireWorkflowRoutes(c)
	for _, stopped := range []bool{true, false} {
		out, err := controlMCPStopAutomations(c, map[string]any{"stopped": stopped})
		if err != nil || out.(map[string]any)["stopped"] != stopped || out.(map[string]any)["next"] == "" {
			t.Fatalf("%v %v", out, err)
		}
		state, err := store.StopState()
		if err != nil || state.Stopped != stopped {
			t.Fatalf("%+v %v", state, err)
		}
		if _, err := controlMCPStopAutomations(c, nil); err == nil {
			t.Fatal("missing boolean accepted")
		}
		after, err := store.StopState()
		if err != nil || !reflect.DeepEqual(state, after) {
			t.Fatal("refusal changed switch")
		}
	}
}
