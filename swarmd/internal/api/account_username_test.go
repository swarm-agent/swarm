package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountUsernameRenamePreservesUserID(t *testing.T) {
	server, identityStore := newOnboardingIdentityTestServer(t, true)
	req := newAuthenticatedJSONSameOriginDesktopRequest(t, server, map[string]any{"username": " Renamed User "})
	req.Method = http.MethodPut
	req.URL.Path = "/v1/account/username"
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode rename response: %v", err)
	}
	if response.UserID != "user_onboarding_test" || response.Username != "renamed user" {
		t.Fatalf("rename response=%+v", response)
	}
	user, ok, err := identityStore.GetUser("user_onboarding_test")
	if err != nil || !ok {
		t.Fatalf("get renamed user ok=%v err=%v", ok, err)
	}
	if user.ID != "user_onboarding_test" || user.Username != "renamed user" || user.DisplayName != "renamed user" {
		t.Fatalf("renamed user=%+v", user)
	}
	if _, ok, err := identityStore.GetUserByUsername("alice"); err != nil || ok {
		t.Fatalf("old username index ok=%v err=%v", ok, err)
	}
}
