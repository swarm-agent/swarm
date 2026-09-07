package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type defaultsTransport func(*http.Request) (*http.Response, error)

func (f defaultsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Purpose: the authenticated restore endpoint must atomically restore all roles,
// preserve other accounts and unrelated keys, and reject stale/auth/refresh
// failures without assignment writes. Fake HTTP and temporary stores keep this
// at the narrow API-to-store boundary without provider or daemon access.
func TestRestoreAgentModelDefaultsAccountBoundary(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "defaults"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewAgentModelSettingsStore(db)
	original := testAgentModelSettingsRecord("account-one")
	original.Swarm.Action.ContextMode = "full"
	original.UpdatedAt = 123
	if _, err := store.PutForAccount(original); err != nil {
		t.Fatal(err)
	}
	other := testAgentModelSettingsRecord("account-two")
	if _, err := store.PutForAccount(other); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON("test-unrelated", map[string]string{"favorite": "keep"}); err != nil {
		t.Fatal(err)
	}
	catalog := model.NewCatalogService(pebblestore.NewModelCatalogStore(db))
	if err := catalog.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	meta, _, _ := catalog.Meta()
	version, _ := json.Marshal(map[string]any{"snapshot_id": meta.SnapshotID, "snapshot_version": meta.SnapshotVersion, "generated_at": meta.GeneratedAt})
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	fail := false
	http.DefaultTransport = defaultsTransport(func(r *http.Request) (*http.Response, error) {
		code, body := 200, string(version)
		if fail {
			code, body = 503, "unavailable"
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	server := &Server{model: model.NewService(pebblestore.NewModelStore(db), nil, catalog), agentModelSettingsStore: store,
		providers: registry.New(testProviderAdapter{status: provideriface.Status{ID: "codex", Ready: true}})}
	server.SetAgentModelSettingsService(agentmodelsettings.NewService(store))
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-one", AccountScopeID: "account-one"}
	call := func(p identity.Principal, expected int64, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"expected_updated_at": expected})
		req := httptest.NewRequest(http.MethodPost, AgentModelSettingsPath+"/restore-defaults", bytes.NewReader(body))
		if p.Valid() {
			req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
		}
		res := httptest.NewRecorder()
		server.handleRestoreAgentModelDefaults(res, req)
		if res.Code != want {
			t.Fatalf("status %d want %d: %s", res.Code, want, res.Body.String())
		}
	}
	call(identity.Principal{}, 123, 401)
	call(principal, 122, 409)
	fail = true
	call(principal, 123, 502)
	got, _, _ := store.GetForAccount("account-one")
	if got != original {
		t.Fatal("failed restore changed assignments")
	}
	// First onboarding must stop before persisting recommendations on refresh failure.
	server.agents = agent.NewService(pebblestore.NewAgentStore(db), nil)
	if _, err := server.hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount("account-new", "user-new", "codex"); err == nil {
		t.Fatal("onboarding ignored refresh failure")
	}
	if _, found, _ := store.GetForAccount("account-new"); found {
		t.Fatal("failed onboarding persisted settings")
	}
	if _, err := server.hydrateOnboardingProviderDefaultsAfterVerifiedCredentialActivationForAccount("account-one", "user-one", "codex"); err != nil {
		t.Fatal("existing settings must not need refresh", err)
	}
	fail = false
	server.providers = registry.New(testProviderAdapter{status: provideriface.Status{ID: "codex", Ready: false}})
	call(principal, 123, 409)
	unchanged, _, _ := store.GetForAccount("account-one")
	if unchanged != original {
		t.Fatal("unavailable provider changed assignments")
	}
	server.providers = registry.New(testProviderAdapter{status: provideriface.Status{ID: "codex", Ready: true}})
	call(principal, 123, 200)
	got, _, _ = store.GetForAccount("account-one")
	recs, ok, err := catalog.RecommendedRoleDefaults("codex", "auto")
	if err != nil || !ok {
		t.Fatal("missing recommendation")
	}
	if got.Swarm.Action.Model != recs["auto"].Model || got.Swarm.Action.ContextMode != "" || got.SystemAgents.Coder.Model == "coder" {
		t.Fatalf("not restored: %+v", got)
	}
	preserved, _, _ := store.GetForAccount("account-two")
	if preserved != other {
		t.Fatal("other account changed")
	}
	payload, found, err := db.GetBytes("test-unrelated")
	if err != nil || !found || !bytes.Contains(payload, []byte("keep")) {
		t.Fatal("unrelated state changed")
	}
	call(principal, 123, 409)
	final, _, _ := store.GetForAccount("account-one")
	if final != got {
		t.Fatal("stale restore changed state")
	}
}
