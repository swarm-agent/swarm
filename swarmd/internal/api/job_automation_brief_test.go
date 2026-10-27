package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: controlMCPValidateThen must reject invalid definition placeholders
// before GiveJob or ScheduleJob writes anything. The MCP handler boundary with
// a recording downstream transport proves refusal and absence of job/schedule
// side effects, while the real store proves no workflow was saved.
func TestThenBriefPlaceholdersRefusedBeforeStart(t *testing.T) {
	server, store := newRunOwnerServer(t)
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})}
	folder := t.TempDir()
	var writes []string
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes = append(writes, r.Method+" "+r.URL.Path)
		}
		if r.URL.Path == "/v3/workers" {
			writeJSON(w, 200, map[string]any{"workers": []any{map[string]any{"id": "wkr-1", "name": "coder", "role": "coder"}}})
			return
		}
		if r.URL.Path == "/v3/projects" {
			writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "proj-1", "name": "Site", "workspaces": []any{map[string]any{"path": folder, "workspace_id": "ws-1", "role": "primary_code"}}}}})
			return
		}
		writeJSON(w, 200, map[string]any{})
	})
	for _, brief := range []string{"{{item.x}}", "{{unknown.x}}"} {
		then := map[string]any{"steps": []any{map[string]any{"type": "agent", "worker": "maker", "brief": brief}}}
		if _, err := controlMCPGiveJob(c, map[string]any{"worker": "wkr-1", "brief": "make", "then": then}); err == nil || !strings.Contains(err.Error(), brief) {
			t.Fatalf("give %s: %v", brief, err)
		}
		if _, err := controlMCPScheduleJob(c, map[string]any{"worker": "wkr-1", "brief": "make", "schedule": map[string]any{"every": "1d"}, "then": then}); err == nil || !strings.Contains(err.Error(), brief) {
			t.Fatalf("schedule %s: %v", brief, err)
		}
	}
	if len(writes) != 0 {
		t.Fatalf("refused then started something: %v", writes)
	}
	if list := storedAutomations(t, store); len(list) != 0 {
		t.Fatalf("refused then saved workflow: %+v", list)
	}
	if err := controlMCPValidateThen(c, map[string]any{"steps": []any{map[string]any{"type": "agent", "worker": "maker", "brief": "{{item.x}} {{item}}", "for_each": map[string]any{"file": "plan.json"}}}}); err != nil {
		t.Fatalf("valid then: %v", err)
	}
}
