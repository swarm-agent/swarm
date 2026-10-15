package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: resolveWorker must initialize reviewer on first use, while unknown
// names must not create presets. This MCP dispatch test is the narrowest layer
// proving the preset allowlist reaches ensure-presets and propagates failures.
func TestControlMCPReviewerFirstUse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fail      bool
		writes    int
		wantError bool
	}{
		{"reviewer", false, 1, false}, {"reviewer", true, 1, true}, {"unknown", false, 0, true},
	} {
		t.Run(tc.name+map[bool]string{true: " failure", false: " success"}[tc.fail], func(t *testing.T) {
			writes := 0
			c := &controlMCPCall{request: httptest.NewRequest(http.MethodPost, "/mcp", nil), next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v3/workers":
					json.NewEncoder(w).Encode(map[string]any{"workers": []any{}})
				case r.Method == http.MethodPost && r.URL.Path == "/v3/workers:ensure-presets":
					writes++
					if tc.fail {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"workers": []any{map[string]any{"id": "review-preset", "role": "reviewer", "metadata": map[string]any{"preset": "reviewer"}}}})
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			})}
			worker, err := c.resolveWorker(tc.name)
			if (err != nil) != tc.wantError || writes != tc.writes {
				t.Fatalf("worker=%v err=%v writes=%d", worker, err, writes)
			}
			if !tc.wantError && worker["id"] != "review-preset" {
				t.Fatal(worker)
			}
			if tc.wantError && worker != nil {
				t.Fatalf("failed resolution returned worker: %v", worker)
			}
		})
	}
}

// Purpose: controlMCPWorkerModel must display the reviewer model selected by
// run.ResolveWorkerRoleModelProfile, including an empty reviewer's fallback.
// The real isolated execution/model fixture plus summary function is the
// narrowest cross-boundary proof; a pinned model must still override the role.
func TestControlMCPReviewerModelMatchesResolution(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "unassigned", true: "explicit"}[explicit], func(t *testing.T) {
			s, db, _ := setupWorkerAPITestServer(t)
			setupWorkerAPIExecution(t, s, db)
			settings := store.NewAgentModelSettingsStore(db)
			record, _, err := settings.GetForAccount("acct-test")
			if err != nil {
				t.Fatal(err)
			}
			if explicit {
				record.SystemAgents.Reviewer = record.SystemAgents.Coder
			} else {
				record.SystemAgents.Reviewer = store.AgentModelAssignment{}
			}
			if _, err := settings.PutForAccount(record); err != nil {
				t.Fatal(err)
			}
			execution, err := s.workerExecutionService()
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := execution.ResolveRoleModelProfile("acct-test", store.WorkerRoleReviewer, nil)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			request = request.WithContext(context.WithValue(request.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", AccountScopeID: "acct-test", UserID: "user-test"}))
			c := &controlMCPCall{server: s, request: request}
			roles, err := c.roleModels()
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Action.Model == "" {
				t.Fatal("execution resolved an empty model")
			}
			worker := map[string]any{"role": "reviewer"}
			got := controlMCPWorkerModel(worker, roles)
			want := controlMCPRoleModel(map[string]any{"provider": resolved.Action.Provider, "model": resolved.Action.Model, "thinking": resolved.Action.Thinking, "service_tier": resolved.Action.ServiceTier})
			want["source"] = "role default"
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("summary=%v resolution=%v", got, want)
			}
			worker["model_profile"] = map[string]any{"action": map[string]any{"provider": "pinned", "model": "selected"}}
			got = controlMCPWorkerModel(worker, roles)
			if got["source"] != "pinned" || got["model"] != "selected" {
				t.Fatal(got)
			}
		})
	}
}
