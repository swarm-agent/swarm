package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPermissionsBashProfileAPIRoundTripsAndDefaultsPerAccount(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permissions-bash-profile-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	permSvc := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, permSvc, nil, events, nil)

	call := func(method, account, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/v1/permissions/bash-profile", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req = requestWithTestPrincipalForAccount(req, "user-"+account, account)
		rec := httptest.NewRecorder()
		server.handlePermissions(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var response struct {
			BashProfile string `json:"bash_profile"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return response.BashProfile
	}

	if got := decode(call(http.MethodGet, "account-a", "")); got != "current_rules" {
		t.Fatalf("fresh account profile = %q", got)
	}
	if got := decode(call(http.MethodPut, "account-a", `{"bash_profile":"only_critical_prompts"}`)); got != "only_critical_prompts" {
		t.Fatalf("updated profile = %q", got)
	}
	if got := decode(call(http.MethodGet, "account-a", "")); got != "only_critical_prompts" {
		t.Fatalf("round-trip profile = %q", got)
	}
	if got := decode(call(http.MethodGet, "account-b", "")); got != "current_rules" {
		t.Fatalf("other account profile = %q", got)
	}
	if rec := call(http.MethodPut, "account-a", `{"bash_profile":"invalid"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid profile status = %d body=%s", rec.Code, rec.Body.String())
	}
}
