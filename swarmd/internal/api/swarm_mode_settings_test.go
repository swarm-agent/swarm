package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSwarmModeSettingsHTTPPutAndGetDirectSelections(t *testing.T) {
	server, principal := openSwarmModeSettingsHTTPTest(t)
	body := `{"action":{"provider":"codex","model":"action","thinking":"high","service_tier":"fast","context_mode":"full"},"plan":{"provider":"openai","model":"plan","thinking":"xhigh","service_tier":"priority","context_mode":"compact"}}`
	put := swarmModeSettingsHTTP(t, server, principal, http.MethodPut, body, http.StatusOK)
	assertSwarmModeSettingsHTTPResponse(t, put, "action", "plan")
	got := swarmModeSettingsHTTP(t, server, principal, http.MethodGet, "", http.StatusOK)
	assertSwarmModeSettingsHTTPResponse(t, got, "action", "plan")
}

func TestSwarmModeSettingsHTTPRejectsProfileFieldsAndMissingSelections(t *testing.T) {
	server, principal := openSwarmModeSettingsHTTPTest(t)
	for _, body := range []string{
		`{"action_favorite_id":"favorite","action":{"provider":"codex","model":"action","thinking":"high"},"plan":{"provider":"codex","model":"plan","thinking":"high"}}`,
		`{"action":{"provider":"codex","model":"action","thinking":"high"}}`,
	} {
		swarmModeSettingsHTTP(t, server, principal, http.MethodPut, body, http.StatusBadRequest)
	}
}

func TestSwarmModeSettingsHTTPMissingPrincipalAndSettings(t *testing.T) {
	server, principal := openSwarmModeSettingsHTTPTest(t)
	swarmModeSettingsHTTP(t, server, principal, http.MethodGet, "", http.StatusNotFound)
	swarmModeSettingsHTTP(t, server, identity.Principal{}, http.MethodGet, "", http.StatusUnauthorized)
	swarmModeSettingsHTTP(t, server, principal, http.MethodPost, `{}`, http.StatusMethodNotAllowed)
}

func openSwarmModeSettingsHTTPTest(t *testing.T) (*Server, identity.Principal) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-mode-settings.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	server := &Server{}
	server.SetSwarmModelSettingsService(modelprofile.NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store)))
	return server, identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-one", AccountScopeID: "account-one"}
}

func swarmModeSettingsHTTP(t *testing.T, server *Server, principal identity.Principal, method, body string, want int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, SwarmModeSettingsPath, bytes.NewBufferString(body))
	if principal.Valid() { req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal)) }
	res := httptest.NewRecorder()
	server.handleSwarmModeSettings(res, req)
	if res.Code != want { t.Fatalf("%s = %d, want %d: %s", method, res.Code, want, res.Body.String()) }
	var out map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil { t.Fatal(err) }
	return out
}

func assertSwarmModeSettingsHTTPResponse(t *testing.T, out map[string]any, actionModel, planModel string) {
	t.Helper()
	settings := out["model_settings"].(map[string]any)
	action := settings["action"].(map[string]any)
	plan := settings["plan"].(map[string]any)
	if action["model"] != actionModel || plan["model"] != planModel { t.Fatalf("model_settings = %#v", settings) }
	if _, ok := settings["action_favorite_id"]; ok { t.Fatalf("profile field leaked: %#v", settings) }
}
