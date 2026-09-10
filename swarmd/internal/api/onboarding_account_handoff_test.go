package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Requirement: OS/user-name selection must not authenticate clients. The HTTP
// onboarding registration/withAuth/updateOnboarding boundary issues a real
// identity session only for admitted bootstrap, preserves that account on retry,
// and rejects unauthenticated completion without changing saved setup state.
// Temporary identity storage plus the real handler is the narrowest proof of
// authentication and resume postconditions; no host accounts/providers are used.
func TestOnboardingAccountHandoffRequiresSessionAndResumes(t *testing.T) {
	server, store := newOnboardingIdentityTestServer(t, false)
	bootstrap := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrap, newJSONSameOriginDesktopRequest(t, map[string]any{"username": "developer", "swarm_name": "Test Device"}))
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d", bootstrap.Code)
	}
	cookie := sessionCookieFromRecorder(t, bootstrap)
	if server.identitySessions == nil {
		t.Fatal("identity session authority missing")
	}
	actor, err := server.identitySessions.Validate(cookie.Value)
	if err != nil || !isCompleteProductActor(actor) {
		t.Fatalf("bootstrap did not establish authenticated actor: %v", err)
	}
	before, err := store.IdentityCounts()
	if err != nil {
		t.Fatal(err)
	}
	cfgBefore, err := server.loadStartupConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfgBefore.DesktopOnboardingComplete || !cfgBefore.DesktopOnboardingCompleteSet {
		t.Fatal("bootstrap prematurely completed setup")
	}

	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, newJSONSameOriginDesktopRequest(t, map[string]any{"desktop_onboarding_complete": true}))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated completion status=%d", unauthorized.Code)
	}
	cfgAfter, err := server.loadStartupConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfgAfter.DesktopOnboardingComplete != cfgBefore.DesktopOnboardingComplete {
		t.Fatal("rejected completion changed setup")
	}

	for i := 0; i < 2; i++ {
		req := newJSONSameOriginDesktopRequest(t, map[string]any{"desktop_onboarding_complete": true})
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("authenticated retry %d status=%d", i, rec.Code)
		}
		var response onboardingResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.NeedsOnboarding || !response.Config.DesktopOnboardingComplete {
			t.Fatal("completion did not persist")
		}
	}
	after, err := store.IdentityCounts()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("retry duplicated identity")
	}
	if _, err := server.identitySessions.Validate(cookie.Value); err != nil {
		t.Fatal("retry invalidated established session")
	}
}
