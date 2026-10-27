package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/security"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: authenticated non-socket callers with automations:write can pin
// a connected catalog model. Threat: internal lookups re-enter attach auth, or
// bypassing that wrapper accidentally bypasses the outer write-scope boundary.
// Owners: withAuth, handleWorkerModelPin, workerModel, SetWorkerModelProfile.
// Real HTTP handlers and temporary Pebble state are the narrowest layer proving
// both authenticated dispatch and durable mutation, without provider execution.
func TestWorkerModelPinScopedToken(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	s := c.server
	db := s.sessions.Store().Underlying()
	s.SetIdentitySessionService(identity.NewSessionService(store.NewIdentityStore(db), store.NewIdentitySessionStore(db)))
	s.security = security.NewService(store.NewClientAuthStore(s.sessions.Store().Underlying()), nil)
	s.providers = registry.New(onboardingProviderTestAdapter{id: "codex", ready: true})
	s.providers.RegisterRunner(controlMCPConnectTestRunner{id: "codex"})
	if err := store.NewModelCatalogStore(db).SetRecord(store.ModelCatalogRecord{Provider: "codex", Model: "gpt-5.5", ThinkingOptions: []string{"high"}, DefaultThinking: "high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controlMCPDeployWorker(c, map[string]any{"preset": "coder"}); err != nil {
		t.Fatal(err)
	}
	p := testPrincipal()
	snapshot := func() store.WorkerRecord {
		t.Helper()
		result, err := ws.ListWorkers(p.AccountScopeID, store.ListWorkersQuery{Limit: 10})
		if err != nil || len(result.Workers) != 1 {
			t.Fatalf("worker snapshot: %+v, %v", result, err)
		}
		return result.Workers[0]
	}
	before := snapshot()
	body := fmt.Sprintf(`{"expected_revision":%d,"model":{"provider":"codex","model":"gpt-5.5","thinking":"high"}}`, before.Revision)
	handler := s.Handler()
	for _, tc := range []struct {
		name   string
		scopes []string
		status int
	}{
		{"unauthenticated", nil, http.StatusUnauthorized},
		{"read-only", []string{"automations:read"}, http.StatusForbidden},
		{"write", []string{"automations:write"}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/v3/workers/"+before.ID+"/model", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.scopes != nil {
				token, _, err := s.security.CreateInternalToken(tc.name, tc.scopes, p.AccountScopeID, p.UserID)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("want %d, got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
			after := snapshot()
			if tc.status != http.StatusOK {
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rejected request changed worker")
				}
				return
			}
			var response struct {
				Worker store.WorkerRecord `json:"worker"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			want := store.ModelProfileSelection{Provider: "codex", Model: "gpt-5.5", Thinking: "high"}
			if !reflect.DeepEqual(response.Worker, after) || after.Revision != before.Revision+1 || after.ModelProfile == nil || after.ModelProfile.Source != store.SessionModelProfileSourceTemporary || after.ModelProfile.Action != want || after.ModelProfile.Plan == nil || *after.ModelProfile.Plan != want {
				t.Fatalf("model pin not returned and persisted: %+v", after)
			}
		})
	}
}

// Purpose: MCP and native callers share ValidateWorkerModelPin rejection text,
// and a rejected deployment must not mutate its worker. This handler/store test
// proves the MCP boundary plus explicit null reset without live provider calls.
func TestWorkerModelPinMCPValidationAndClear(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	s := c.server
	s.providers = registry.New(onboardingProviderTestAdapter{id: "codex", ready: true})
	s.providers.RegisterRunner(controlMCPConnectTestRunner{id: "codex"})
	if err := store.NewModelCatalogStore(s.sessions.Store().Underlying()).SetRecord(store.ModelCatalogRecord{Provider: "codex", Model: "fixture-model", ThinkingOptions: []string{"high"}, DefaultThinking: "high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controlMCPDeployWorker(c, map[string]any{"preset": "coder", "model": map[string]any{"provider": "codex", "model": "fixture-model", "thinking": "high"}}); err != nil {
		t.Fatal(err)
	}
	result, err := ws.ListWorkers(testPrincipal().AccountScopeID, store.ListWorkersQuery{Limit: 10})
	if err != nil || len(result.Workers) != 1 {
		t.Fatalf("workers: %+v %v", result, err)
	}
	before := result.Workers[0]
	invalid := map[string]any{"provider": "codex", "model": "missing", "thinking": "high"}
	_, mcpErr := c.workerModel(invalid)
	_, nativeErr := runruntime.ValidateWorkerModelPin(c.request.Context(), s.providers, s.model, invalid)
	if mcpErr == nil || nativeErr == nil || mcpErr.Error() != nativeErr.Error() {
		t.Fatalf("different errors: %v / %v", mcpErr, nativeErr)
	}
	encoded, _ := json.Marshal(before)
	var view map[string]any
	if err := json.Unmarshal(encoded, &view); err != nil {
		t.Fatal(err)
	}
	if _, err := c.deployWorkerModel(view, map[string]any{"model": invalid}); err == nil {
		t.Fatal("invalid model accepted")
	}
	after, _, err := ws.GetWorker(before.AccountScopeID, before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("invalid pin changed worker")
	}
	if _, err := c.deployWorkerModel(view, map[string]any{"model": nil}); err != nil {
		t.Fatal(err)
	}
	after, _, err = ws.GetWorker(before.AccountScopeID, before.ID)
	if err != nil || after.ModelProfile == nil || !after.ModelProfile.UseAccountDefault || after.Revision != before.Revision+1 {
		t.Fatalf("null did not clear: %+v %v", after, err)
	}
}

// Purpose: custom MCP creation must use the strict create route followed by the
// existing model-pin boundary, returning an active worker with a durable pin.
// controlMCPDeployWorker, handleWorkerModelPin and ValidateWorkerModelPin own
// this contract. Real handler dispatch with a temporary store is the narrowest
// layer detecting obsolete create fields and invalid-model partial creation.
func TestWorkerModelPinMCPCustomCreate(t *testing.T) {
	c, ws := newMCPPresetTestCall(t)
	s := c.server
	s.providers = registry.New(onboardingProviderTestAdapter{id: "codex", ready: true})
	s.providers.RegisterRunner(controlMCPConnectTestRunner{id: "codex"})
	if err := store.NewModelCatalogStore(s.sessions.Store().Underlying()).SetRecord(store.ModelCatalogRecord{Provider: "codex", Model: "fixture-model", ThinkingOptions: []string{"high"}, DefaultThinking: "high"}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": "Custom coder", "role": "coder", "description": "Custom worker", "instructions": "Inspect requested changes", "model": map[string]any{"provider": "codex", "model": "missing", "thinking": "high"}}
	_, wantErr := runruntime.ValidateWorkerModelPin(c.request.Context(), s.providers, s.model, args["model"])
	if _, err := controlMCPDeployWorker(c, args); err == nil || wantErr == nil || err.Error() != wantErr.Error() {
		t.Fatalf("invalid create error: %v; want %v", err, wantErr)
	}
	before, err := ws.ListWorkers(testPrincipal().AccountScopeID, store.ListWorkersQuery{Limit: 10})
	if err != nil || len(before.Workers) != 0 {
		t.Fatalf("invalid model created a worker: %+v %v", before, err)
	}
	args["model"] = map[string]any{"provider": "codex", "model": "fixture-model", "thinking": "high"}
	result, err := controlMCPDeployWorker(c, args)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ws.ListWorkers(testPrincipal().AccountScopeID, store.ListWorkersQuery{Limit: 10})
	if err != nil || len(after.Workers) != 1 {
		t.Fatalf("created workers: %+v %v", after, err)
	}
	worker := after.Workers[0]
	want := store.ModelProfileSelection{Provider: "codex", Model: "fixture-model", Thinking: "high"}
	if worker.LifecycleState != store.WorkerLifecycleStateActive || worker.ModelProfile == nil || worker.ModelProfile.Action != want || worker.ModelProfile.Plan == nil || *worker.ModelProfile.Plan != want {
		t.Fatalf("active pinned worker not persisted: %+v", worker)
	}
	view := controlMCPMap(controlMCPMap(result)["worker"])
	model := controlMCPMap(view["model"])
	if view["id"] != worker.ID || model["provider"] != want.Provider || model["model"] != want.Model || model["thinking"] != want.Thinking {
		t.Fatalf("pinned worker not returned: %+v", result)
	}

	// Inject a pin-route failure after prevalidation to prove that partial
	// creation is reported rather than disguised as a successful deployment.
	next := c.next
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/model") {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "injected pin failure"})
			return
		}
		next.ServeHTTP(w, r)
	})
	args["name"] = "Unpinned coder"
	if _, err := controlMCPDeployWorker(c, args); err == nil || !strings.Contains(err.Error(), "created and activated without the requested model pin") || !strings.Contains(err.Error(), "injected pin failure") {
		t.Fatalf("partial creation not reported: %v", err)
	}
	partial, err := ws.ListWorkers(testPrincipal().AccountScopeID, store.ListWorkersQuery{Limit: 10})
	if err != nil || len(partial.Workers) != 2 {
		t.Fatalf("partial workers: %+v %v", partial, err)
	}
	for _, w := range partial.Workers {
		if w.Name == "Unpinned coder" && (w.LifecycleState != store.WorkerLifecycleStateActive || (w.ModelProfile != nil && w.ModelProfile.Source == store.SessionModelProfileSourceTemporary)) {
			t.Fatalf("unexpected partial worker state: %+v", w)
		}
	}
}
