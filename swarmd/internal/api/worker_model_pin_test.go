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
