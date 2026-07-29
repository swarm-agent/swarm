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

func TestModelProfilesHTTPCRUDIsolationAndBulkDelete(t *testing.T) {
	server := newModelProfileTestServer(t)
	accountOne := modelProfileTestPrincipal("account-one")
	accountTwo := modelProfileTestPrincipal("account-two")

	single := `{"name":"Solo","model_mode":"single","single":{"provider":"codex","model":"single-model","thinking":"high","service_tier":"priority","context_mode":"full"}}`
	created := modelProfileRequestJSON(t, server, accountOne, http.MethodPost, modelProfilesPath, single, http.StatusCreated)
	profileID := modelProfileIDFromResponse(t, created)
	if !strings.HasPrefix(profileID, "mp_") {
		t.Fatalf("profile_id = %q, want opaque mp_ id", profileID)
	}
	createdProfile := created["model_profile"].(map[string]any)
	if createdProfile["is_default"] != true {
		t.Fatalf("first profile is_default = %#v", createdProfile["is_default"])
	}

	modelProfileRequestJSON(t, server, accountOne, http.MethodGet, modelProfilesPath+"/"+profileID, "", http.StatusOK)
	modelProfileRequestJSON(t, server, accountTwo, http.MethodGet, modelProfilesPath+"/"+profileID, "", http.StatusNotFound)

	split := `{"name":"Renamed","model_mode":"split","plan":{"provider":"codex","model":"plan-model","thinking":"high","service_tier":"priority","context_mode":"full"},"auto":{"provider":"codex","model":"auto-model","thinking":"medium","service_tier":"standard","context_mode":"compact"}}`
	updated := modelProfileRequestJSON(t, server, accountOne, http.MethodPut, modelProfilesPath+"/"+profileID, split, http.StatusOK)
	if got := modelProfileIDFromResponse(t, updated); got != profileID {
		t.Fatalf("updated profile_id = %q, want %q", got, profileID)
	}

	duplicate := strings.Replace(single, `"Solo"`, `"renamed"`, 1)
	modelProfileRequestJSON(t, server, accountOne, http.MethodPost, modelProfilesPath, duplicate, http.StatusConflict)
	second := modelProfileRequestJSON(t, server, accountOne, http.MethodPost, modelProfilesPath, strings.Replace(split, `"Renamed"`, `"Second"`, 1), http.StatusCreated)
	secondID := modelProfileIDFromResponse(t, second)
	setDefault := modelProfileRequestJSON(t, server, accountOne, http.MethodPost, modelProfilesPath+"/default", `{"profile_id":"`+secondID+`"}`, http.StatusOK)
	if setDefault["default_profile_id"] != secondID {
		t.Fatalf("default_profile_id = %#v, want %q", setDefault["default_profile_id"], secondID)
	}
	listed := modelProfileRequestJSON(t, server, accountOne, http.MethodGet, modelProfilesPath, "", http.StatusOK)
	if profiles, ok := listed["model_profiles"].([]any); !ok || len(profiles) != 2 {
		t.Fatalf("model_profiles = %#v, want two account-owned profiles", listed["model_profiles"])
	}
	if listed["default_profile_id"] != secondID {
		t.Fatalf("listed default_profile_id = %#v", listed["default_profile_id"])
	}
	modelProfileRequestJSON(t, server, accountOne, http.MethodPatch, modelProfilesPath, `{"profile_ids":["`+secondID+`","`+profileID+`"]}`, http.StatusOK)
	reordered := modelProfileRequestJSON(t, server, accountOne, http.MethodGet, modelProfilesPath, "", http.StatusOK)
	profiles := reordered["model_profiles"].([]any)
	if profiles[0].(map[string]any)["profile_id"] != secondID || profiles[1].(map[string]any)["profile_id"] != profileID {
		t.Fatalf("model_profiles order = %#v, want [%q %q]", profiles, secondID, profileID)
	}

	bulk := `{"profile_ids":["` + profileID + `","missing","` + profileID + `"]}`
	response := modelProfileRequestJSON(t, server, accountOne, http.MethodPost, modelProfilesPath+"/bulk-delete", bulk, http.StatusOK)
	if response["deleted_count"] != float64(1) {
		t.Fatalf("deleted_count = %#v", response["deleted_count"])
	}
	assertStringSlice(t, response["deleted_profile_ids"], []string{profileID})
	assertStringSlice(t, response["missing_profile_ids"], []string{"missing"})

	modelProfileRequestJSON(t, server, accountOne, http.MethodDelete, modelProfilesPath+"/"+secondID, "", http.StatusOK)
	modelProfileRequestJSON(t, server, accountOne, http.MethodGet, modelProfilesPath+"/"+secondID, "", http.StatusNotFound)
	modelProfileRequestJSON(t, server, accountTwo, http.MethodPost, modelProfilesPath, single, http.StatusCreated)
}

func TestModelProfilesHTTPErrors(t *testing.T) {
	server := newModelProfileTestServer(t)
	principal := modelProfileTestPrincipal("account-one")

	modelProfileRequestJSON(t, server, identity.Principal{}, http.MethodGet, modelProfilesPath, "", http.StatusUnauthorized)
	modelProfileRequestJSON(t, server, principal, http.MethodPost, modelProfilesPath, `{`, http.StatusBadRequest)
	modelProfileRequestJSON(t, server, principal, http.MethodPost, modelProfilesPath, `{}`, http.StatusBadRequest)
	modelProfileRequestJSON(t, server, principal, http.MethodPost, modelProfilesPath+"/bulk-delete", `{"profile_ids":[]}`, http.StatusBadRequest)
	modelProfileRequestJSON(t, server, principal, http.MethodPatch, modelProfilesPath, `{}`, http.StatusBadRequest)
	modelProfileRequestJSON(t, server, principal, http.MethodPost, modelProfilesPath+"/unknown/extra", `{}`, http.StatusBadRequest)
}

func newModelProfileTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "model-profiles.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := &Server{}
	server.SetModelProfileService(modelprofile.NewService(pebblestore.NewModelProfileStore(store)))
	return server
}

func modelProfileTestPrincipal(accountScopeID string) identity.Principal {
	return identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-one", AccountScopeID: accountScopeID}
}

func modelProfileRequestJSON(t *testing.T, server *Server, principal identity.Principal, method, path, body string, wantStatus int) map[string]any {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if principal.Valid() {
		request = request.WithContext(identity.ContextWithPrincipal(request.Context(), principal))
	}
	recorder := httptest.NewRecorder()
	server.apiMux().ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

func modelProfileIDFromResponse(t *testing.T, response map[string]any) string {
	t.Helper()
	profile, ok := response["model_profile"].(map[string]any)
	if !ok {
		t.Fatalf("model_profile = %#v", response["model_profile"])
	}
	profileID, _ := profile["profile_id"].(string)
	if profileID == "" {
		t.Fatalf("profile_id = %#v", profile["profile_id"])
	}
	return profileID
}

func assertStringSlice(t *testing.T, raw any, want []string) {
	t.Helper()
	values, ok := raw.([]any)
	if !ok || len(values) != len(want) {
		t.Fatalf("slice = %#v, want %q", raw, want)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("slice[%d] = %#v, want %q", i, values[i], want[i])
		}
	}
}
