package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type defaultsOfflineTransport struct{}

func (defaultsOfflineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("catalog offline")
}

// Requirement: PATCH repairs missing account assignments using complete catalog
// recommendations even when refresh fails; GET stays read-only with unset roles.
// Missing readiness must return 409 rather than a server fault.
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
					if method == http.MethodPatch && rec.Code != http.StatusConflict {
						t.Fatalf("unready PATCH: want 409, got %d: %s", rec.Code, rec.Body.String())
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

type defaultsStatusFailureAdapter struct{ onboardingProviderTestAdapter }

func (defaultsStatusFailureAdapter) Status(context.Context) (provideriface.Status, error) {
	return provideriface.Status{}, errors.New("status lookup failed")
}

// Requirement: missing-record restore preserves its 401/503/502/409 contract.
// Threat: repair masks authentication, configuration, and upstream failures as
// conflicts, or writes settings despite failed prerequisites. Owners:
// setUpRecommendedAgentModels and ensureRecommendedAgentModels. Calling the
// restore setup boundary with real stores is the narrowest layer that also
// exercises the missing-identity branch normally guarded by HTTP middleware.
func TestAgentModelRestoreMissingRecordErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		message string
	}{
		{"identity", http.StatusUnauthorized, identity.ErrProductIdentityRequired.Error()},
		{"configuration", http.StatusServiceUnavailable, "model defaults are not configured"},
		{"status", http.StatusBadGateway, "status lookup failed"},
		{"readiness", http.StatusConflict, "connect a model provider first: none is ready for this account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
			s.agentModelSettingsStore = pebblestore.NewAgentModelSettingsStore(sessions.Store().Underlying())
			p := testPrincipal()
			p.AccountScopeID = "restore-error-account"
			accountCtx := identity.ContextWithPrincipal(context.Background(), p)
			ctx := accountCtx
			s.providers = registry.New(onboardingProviderTestAdapter{id: "codex", ready: false})
			switch tc.name {
			case "identity":
				ctx = context.Background()
			case "configuration":
				s.providers = nil
			case "status":
				s.providers = registry.New(defaultsStatusFailureAdapter{onboardingProviderTestAdapter{id: "codex"}})
			}
			rec := httptest.NewRecorder()
			s.setUpRecommendedAgentModels(rec, ctx)
			want := httptest.NewRecorder()
			writeError(want, tc.status, errors.New(tc.message))
			if rec.Code != tc.status || rec.Body.String() != want.Body.String() {
				t.Fatalf("want %d %s, got %d %s", tc.status, want.Body.String(), rec.Code, rec.Body.String())
			}
			if _, err := s.agentModelSettings.Get(accountCtx); !errors.Is(err, agentmodelsettings.ErrNotFound) {
				t.Fatalf("failed restore changed missing record: %v", err)
			}
		})
	}
}
