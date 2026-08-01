package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSwarmModeSettingsHTTPPutAndGet(t *testing.T) {
	server, favorites, principal := openSwarmModeSettingsHTTPTest(t)
	putSwarmModeSettingsHTTPFavorite(t, favorites, principal.AccountScopeID, "action")
	putSwarmModeSettingsHTTPFavorite(t, favorites, principal.AccountScopeID, "plan")

	put := swarmModeSettingsHTTP(t, server, principal, http.MethodPut,
		`{"action_favorite_id":"action","plan_enabled":true,"plan_favorite_id":"plan"}`,
		http.StatusOK)
	assertSwarmModeSettingsHTTPResponse(t, put, "action", true, "plan")

	got := swarmModeSettingsHTTP(t, server, principal, http.MethodGet, "", http.StatusOK)
	assertSwarmModeSettingsHTTPResponse(t, got, "action", true, "plan")

	actionOnly := swarmModeSettingsHTTP(t, server, principal, http.MethodPut,
		`{"action_favorite_id":"action","plan_enabled":false}`,
		http.StatusOK)
	assertSwarmModeSettingsHTTPResponse(t, actionOnly, "action", false, "")
}

func TestSwarmModeSettingsHTTPRejectsRemovedBundleFields(t *testing.T) {
	server, favorites, principal := openSwarmModeSettingsHTTPTest(t)
	putSwarmModeSettingsHTTPFavorite(t, favorites, principal.AccountScopeID, "action")

	removedFields := []string{"model_mode", "single", "auto", "plan", "provider", "model", "thinking", "service_tier", "context_mode"}
	for _, field := range removedFields {
		t.Run(field, func(t *testing.T) {
			body := `{"action_favorite_id":"action","` + field + `":"removed"}`
			out := swarmModeSettingsHTTP(t, server, principal, http.MethodPut, body, http.StatusBadRequest)
			if message, _ := out["error"].(string); !strings.Contains(message, "unknown field") {
				t.Fatalf("error = %q, want unknown field", message)
			}
		})
	}
}

func TestSwarmModeSettingsHTTPStatusMapping(t *testing.T) {
	t.Run("missing settings", func(t *testing.T) {
		server, _, principal := openSwarmModeSettingsHTTPTest(t)
		swarmModeSettingsHTTP(t, server, principal, http.MethodGet, "", http.StatusNotFound)
	})

	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "missing action favorite", body: `{"action_favorite_id":"missing"}`, want: http.StatusConflict},
		{name: "plan enabled without favorite", body: `{"action_favorite_id":"action","plan_enabled":true}`, want: http.StatusBadRequest},
		{name: "plan favorite while disabled", body: `{"action_favorite_id":"action","plan_favorite_id":"plan"}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, favorites, principal := openSwarmModeSettingsHTTPTest(t)
			putSwarmModeSettingsHTTPFavorite(t, favorites, principal.AccountScopeID, "action")
			swarmModeSettingsHTTP(t, server, principal, http.MethodPut, test.body, test.want)
		})
	}
}

func TestSwarmModeSettingsHTTPRejectsMissingPrincipalAndUnsupportedMethod(t *testing.T) {
	server, _, principal := openSwarmModeSettingsHTTPTest(t)
	swarmModeSettingsHTTP(t, server, identity.Principal{}, http.MethodGet, "", http.StatusUnauthorized)
	swarmModeSettingsHTTP(t, server, principal, http.MethodPost, `{}`, http.StatusMethodNotAllowed)
}

func openSwarmModeSettingsHTTPTest(t *testing.T) (*Server, *pebblestore.ModelProfileStore, identity.Principal) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-mode-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	favorites := pebblestore.NewModelProfileStore(store)
	server := &Server{}
	server.SetSwarmProfileService(modelprofile.NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store), favorites))
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-one", AccountScopeID: "account-one"}
	return server, favorites, principal
}

func putSwarmModeSettingsHTTPFavorite(t *testing.T, favorites *pebblestore.ModelProfileStore, accountScopeID, favoriteID string) {
	t.Helper()
	_, err := favorites.PutForAccount(pebblestore.ModelProfileRecord{
		ProfileID: favoriteID, AccountScopeID: accountScopeID, Name: favoriteID,
		Provider: "codex", Model: "gpt", Thinking: "high",
	})
	if err != nil {
		t.Fatalf("put favorite %q: %v", favoriteID, err)
	}
}

func swarmModeSettingsHTTP(t *testing.T, server *Server, principal identity.Principal, method, body string, want int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, SwarmModeSettingsPath, bytes.NewBufferString(body))
	if principal.Valid() {
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
	}
	res := httptest.NewRecorder()
	server.handleSwarmModeSettings(res, req)
	if res.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, SwarmModeSettingsPath, res.Code, want, res.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSwarmModeSettingsHTTPResponse(t *testing.T, out map[string]any, action string, planEnabled bool, plan string) {
	t.Helper()
	settings, ok := out["model_settings"].(map[string]any)
	if !ok {
		t.Fatalf("model_settings = %#v", out["model_settings"])
	}
	if settings["action_favorite_id"] != action || settings["plan_enabled"] != planEnabled {
		t.Fatalf("model_settings = %#v", settings)
	}
	gotPlan, _ := settings["plan_favorite_id"].(string)
	if gotPlan != plan {
		t.Fatalf("plan_favorite_id = %q, want %q", gotPlan, plan)
	}
}
