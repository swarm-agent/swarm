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

func TestSwarmProfilesHTTPCRUD(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-profiles.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{}
	server.SetSwarmProfileService(modelprofile.NewSwarmService(pebblestore.NewSwarmProfileStore(store)))
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-one", AccountScopeID: "account-one"}
	body := `{"name":"Crew","members":[{"agent_id":"swarm","model_mode":"single","single":{"provider":"codex","model":"gpt","thinking":"high","service_tier":"default","context_mode":"full"}},{"agent_id":"system-finder","model_mode":"split","plan":{"provider":"codex","model":"gpt","thinking":"high","service_tier":"default","context_mode":"full"},"auto":{"provider":"codex","model":"gpt","thinking":"medium","service_tier":"default","context_mode":"full"}}]}`
	created := swarmProfileHTTP(t, server, principal, http.MethodPost, swarmProfilesPath, body, http.StatusCreated)
	profile := created["swarm_profile"].(map[string]any)
	profileID := profile["profile_id"].(string)
	if len(profile["members"].([]any)) != 2 {
		t.Fatalf("profile = %#v", profile)
	}
	swarmProfileHTTP(t, server, principal, http.MethodGet, swarmProfilesPath+"/"+profileID, "", http.StatusOK)
	swarmProfileHTTP(t, server, principal, http.MethodPut, swarmProfilesPath+"/"+profileID, bytes.NewBufferString(body).String(), http.StatusOK)
	swarmProfileHTTP(t, server, principal, http.MethodGet, swarmProfilesPath, "", http.StatusOK)
	swarmProfileHTTP(t, server, principal, http.MethodDelete, swarmProfilesPath+"/"+profileID, "", http.StatusOK)
}

func swarmProfileHTTP(t *testing.T, server *Server, principal identity.Principal, method, path, body string, want int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if principal.Valid() {
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
	}
	res := httptest.NewRecorder()
	if path == swarmProfilesPath {
		server.handleSwarmProfiles(res, req)
	} else {
		server.handleSwarmProfileByID(res, req)
	}
	if res.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, res.Code, want, res.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
