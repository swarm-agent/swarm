package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type defaultsOfflineTransport struct{}

func (defaultsOfflineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("catalog offline")
}

// Requirement: PATCH repairs missing account assignments using complete catalog
// recommendations even when refresh fails; GET stays read-only with unset roles.
// Threat: a read mutates settings, or a connected account remains
// unusable, or repair overwrites another account or partially installs defaults.
// Owners: handleAgentModelSettings, ensureRecommendedAgentModels, and onboarding
// hydration. Real temporary stores and the existing provider fake are the narrowest
// layer proving persisted repair; an offline transport forbids external requests.
func TestAgentModelMissingRecordRecovery(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = defaultsOfflineTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		for _, ready := range []bool{false, true} {
			t.Run(method+"/"+map[bool]string{true: "ready", false: "unready"}[ready], func(t *testing.T) {
				s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
				s.agentModelSettingsStore = pebblestore.NewAgentModelSettingsStore(sessions.Store().Underlying())
				s.providers = registry.New(onboardingProviderTestAdapter{id: "codex", ready: ready})
				principal := testPrincipal()
				principal.AccountScopeID = "recovery-account"
				body := ""
				if method == http.MethodPatch {
					body = `{"system_agents":{"coder":{"provider":"codex","model":"requested-model","thinking":"high"}}}`
				}
				req := httptest.NewRequest(method, AgentModelSettingsPath, strings.NewReader(body))
				req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
				before, err := s.agentModelSettings.Get(identity.ContextWithPrincipal(req.Context(), testPrincipal()))
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				s.handleAgentModelSettings(rec, req)
				got, err := s.agentModelSettings.Get(req.Context())
				if method == http.MethodGet {
					if rec.Code != http.StatusOK || !errors.Is(err, agentmodelsettings.ErrNotFound) {
						t.Fatalf("GET persisted settings or failed: %d %s, %v", rec.Code, rec.Body.String(), err)
					}
					want := httptest.NewRecorder()
					writeAgentModelSettingsResponse(want, agentmodelsettings.Settings{AccountScopeID: principal.AccountScopeID})
					var actual, expected any
					if err := json.Unmarshal(rec.Body.Bytes(), &actual); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(want.Body.Bytes(), &expected); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(actual, expected) {
						t.Fatalf("GET must return unset roles and revision zero: %s", rec.Body.String())
					}
				} else if ready {
					if rec.Code != http.StatusOK || err != nil {
						t.Fatalf("repair: %d %s, %v", rec.Code, rec.Body.String(), err)
					}
					if got.Swarm.Action.Model == "" || got.Swarm.Plan.Model == "" || got.SystemAgents.Router.Model == "" || got.SystemAgents.Compact.Model == "" || got.SystemAgents.Finder.Model == "" || got.SystemAgents.Designer.Model == "" {
						t.Fatalf("incomplete record: %+v", got)
					}
					if method == http.MethodPatch && got.SystemAgents.Coder.Model != "requested-model" {
						t.Fatalf("patch lost: %+v", got)
					}
				} else {
					if err == nil {
						t.Fatalf("unready provider created record: %+v", got)
					}
					if method == http.MethodPatch && rec.Code < 400 {
						t.Fatal("unready PATCH succeeded")
					}
				}
				after, err := s.agentModelSettings.Get(identity.ContextWithPrincipal(req.Context(), testPrincipal()))
				if err != nil || after != before {
					t.Fatalf("other account changed: %+v %v", after, err)
				}
			})
		}
	}
}
