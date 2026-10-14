package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: worker/job model choices must match list_models' connected catalog,
// thinking and fast options without adding read/settings grants. Threat: invalid
// selections reach mutation routes. This MCP composition test is the narrowest
// layer checking refusal before any write and naming the discovery tool.
func TestControlMCPWorkerModelValidation(t *testing.T) {
	writes := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
			t.Errorf("unexpected write %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/providers" {
			json.NewEncoder(w).Encode(map[string]any{"providers": []any{map[string]any{"id": "fixture", "runnable": true}, map[string]any{"id": "offline", "runnable": false}}})
			return
		}
		if r.URL.Path == "/v1/model/catalog" {
			var record any
			if r.URL.Query().Get("model") == "offered" {
				record = map[string]any{"model": "offered", "thinking_options": []any{"low", "high"}, "default_thinking": "low", "service_tier_mappings": []any{map[string]any{"swarm_setting": "fast", "tier": "priority"}}}
			}
			if r.URL.Query().Get("model") == "standard" {
				record = map[string]any{"model": "standard", "thinking_options": []any{"low"}, "default_thinking": "low"}
			}
			json.NewEncoder(w).Encode(map[string]any{"lookup": map[string]any{"record": record}})
			return
		}
		t.Errorf("unexpected read %s", r.URL.Path)
	})
	c := &controlMCPCall{request: httptest.NewRequest(http.MethodPost, "/mcp", nil), next: next}
	for _, args := range []map[string]any{
		{"provider": "offline", "model": "offered", "thinking": "low"},
		{"provider": "fixture", "model": "missing", "thinking": "low"},
		{"provider": "fixture", "model": "offered", "thinking": "invalid"},
		{"provider": "fixture", "model": "standard", "thinking": "low", "fast": true},
	} {
		if _, err := controlMCPDeployWorker(c, map[string]any{"name": "Reviewer", "model": args}); err == nil || !strings.Contains(err.Error(), "swarm_list_models") {
			t.Fatalf("invalid selection: %v", err)
		}
	}
	if writes != 0 {
		t.Fatal("invalid model wrote state")
	}
	profile, err := c.workerModel(map[string]any{"provider": "fixture", "model": "offered", "thinking": "high", "fast": true})
	if err != nil || profile.Action.ServiceTier != "priority" || profile.Action.Thinking != "high" {
		t.Fatalf("valid choice: %+v %v", profile, err)
	}
	view := controlMCPRunModel(map[string]any{"model_profile": map[string]any{"action": map[string]any{"provider": "fixture", "model": "offered", "service_tier": "priority"}}, "model_source": "job override"})
	if view["source"] != "job override" || view["fast"] != true {
		t.Fatalf("receipt view: %+v", view)
	}
}

// Purpose: the model-only HTTP boundary preserves automations:write, bypasses
// staged review only for model policy, and refuses stale/foreign edits without
// mutation. The real API/store fixture is the narrowest proof of scope and CAS.
func TestWorkerModelPinAPI(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	setupWorkerAPIExecution(t, s, db)
	ws := s.sessions.Store().WorkerStore()
	worker, err := ws.CreateWorker("acct-test", "user-test", pebblestore.CreateWorkerRequest{Name: "Reviewer", Instructions: "Review"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"expected_revision":%d,"model":null}`, worker.Revision)
	response := executeWorkerAPI(h, http.MethodPut, "/"+worker.ID+"/model", body, workerAPICallOptions{scopes: []string{"automations:read"}})
	if response.Code != http.StatusForbidden {
		t.Fatalf("read scope wrote pin: %d %s", response.Code, response.Body.String())
	}
	response = executeWorkerAPI(h, http.MethodPut, "/"+worker.ID+"/model", body, workerAPICallOptions{scopes: []string{"automations:write"}})
	if response.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", response.Code, response.Body.String())
	}
	current, _, _ := ws.GetWorker("acct-test", worker.ID)
	if current.PendingReview != nil || !current.ModelProfile.UseAccountDefault || current.Revision != worker.Revision+1 {
		t.Fatalf("clear staged or lost: %+v", current)
	}
	response = executeWorkerAPI(h, http.MethodPut, "/"+worker.ID+"/model", body, workerAPICallOptions{scopes: []string{"automations:write"}})
	if response.Code != http.StatusConflict {
		t.Fatalf("stale write: %d %s", response.Code, response.Body.String())
	}
	after, _, _ := ws.GetWorker("acct-test", worker.ID)
	if after.Revision != current.Revision {
		t.Fatal("stale rejection mutated worker")
	}
}
