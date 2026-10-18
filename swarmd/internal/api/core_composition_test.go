package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func newCoreCompositionTestServer(t *testing.T, store *pebblestore.Store) *Server {
	t.Helper()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	securitySvc := security.NewService(pebblestore.NewClientAuthStore(store), events)
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		t.Fatal(err)
	}
	server := NewCoreServer(auth.NewService(pebblestore.NewAuthStore(store), events), nil, nil, nil, nil, nil, nil, securitySvc, nil, nil, nil, events, stream.NewHub(events))
	identityStore := pebblestore.NewIdentityStore(store)
	server.SetIdentityService(identity.NewService(identityStore))
	server.SetIdentitySessionService(identity.NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(store)))
	return server
}

func TestCoreCompositionExcludesBrowserRoutesAndAssets(t *testing.T) {
	// Requirement: Headless exposes backend authority without Desktop routes or
	// assets. Threat: a shared mux, environment asset path, or exported handler
	// accidentally restores UI bootstrap. Owners: NewCoreServer, apiMux and
	// DesktopHandler. Real handlers and a temporary Pebble store are the narrowest
	// layer proving route absence plus absence of identity/cookie side effects.
	desktop, store, identities, cleanup := newDesktopJWTTestServer(t, false)
	defer cleanup()
	core := newCoreCompositionTestServer(t, store)
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("stage2a-desktop-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARM_WEB_DIST_DIR", assets)
	for _, path := range []string{"/ws", "/v1/auth/desktop/session", TailscaleOnboardingApprovalPath, "/v1/feedback", "/v1/update/status", "/v1/update/apply", "/v1/update/run"} {
		request := newSameOriginDesktopRequest(http.MethodGet, path)
		if _, pattern := core.apiMux().Handler(request); pattern != "" {
			t.Fatalf("core registered browser route %s as %s", path, pattern)
		}
		if _, pattern := desktop.apiMux().Handler(request); pattern != path {
			t.Fatalf("Desktop lost route %s", path)
		}
		for name, handler := range map[string]http.Handler{"API": core.Handler(), "loopback": core.LoopbackAPIHandler(), "local": core.LocalTransportHandler()} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, request.Clone(request.Context()))
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s returned %d", name, path, rec.Code)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatalf("%s %s issued cookies", name, path)
			}
		}
	}
	for _, path := range []string{"/v3/sessions", "/v3/sync/bootstrap", "/v3/realtime/stream", "/mcp", "/v1/onboarding"} {
		if _, pattern := core.apiMux().Handler(httptest.NewRequest(http.MethodGet, path, nil)); pattern == "" {
			t.Fatalf("core lost backend route %s", path)
		}
	}
	for _, path := range []string{"/", "/index.html", "/v1/auth/desktop/session"} {
		rec := httptest.NewRecorder()
		core.DesktopHandler().ServeHTTP(rec, newSameOriginDesktopRequest(http.MethodGet, path))
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "stage2a-desktop-marker") || len(rec.Result().Cookies()) != 0 {
			t.Fatalf("core exposed Desktop surface at %s", path)
		}
	}
	counts, err := identities.IdentityCounts()
	if err != nil || counts != (pebblestore.IdentityCounts{}) {
		t.Fatalf("rejected browser requests changed identity state: counts=%+v error=%v", counts, err)
	}
}

func TestCoreCompositionRequiresHeaderIdentityAndPreservesDesktop(t *testing.T) {
	// Requirement: the same canonical identity authenticates both compositions,
	// while only Desktop accepts browser cookies. Threat: Headless cookie auth or
	// broken Desktop sessions. Owners: withAuth, productSessionToken and
	// ConfigureDesktop. Real JWT issuance/validation against a shared temporary
	// store proves this at the HTTP boundary without starting a daemon/provider.
	desktop, store, _, cleanup := newDesktopJWTTestServer(t, true)
	defer cleanup()
	core := newCoreCompositionTestServer(t, store)
	bootstrap := httptest.NewRecorder()
	desktop.DesktopHandler().ServeHTTP(bootstrap, newSameOriginDesktopRequest(http.MethodGet, "/v1/auth/desktop/session"))
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("Desktop bootstrap status %d", bootstrap.Code)
	}
	cookie := sessionCookieFromRecorder(t, bootstrap)
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("stage2a-desktop-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARM_WEB_DIST_DIR", assets)
	assetResponse := httptest.NewRecorder()
	assetRequest := newSameOriginDesktopRequest(http.MethodGet, "/")
	assetRequest.AddCookie(cookie)
	desktop.DesktopHandler().ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK || !strings.Contains(assetResponse.Body.String(), "stage2a-desktop-marker") {
		t.Fatalf("Desktop composition lost asset serving: status %d", assetResponse.Code)
	}
	for _, tc := range []struct {
		name    string
		handler http.Handler
		header  bool
		want    int
	}{
		{"core-cookie-rejected", core.Handler(), false, http.StatusUnauthorized},
		{"core-header-accepted", core.Handler(), true, http.StatusOK},
		{"desktop-cookie-preserved", desktop.DesktopHandler(), false, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newSameOriginDesktopRequest(http.MethodGet, "/v1/me")
			req.AddCookie(cookie)
			if tc.header {
				req.Header.Set("Authorization", "Bearer "+cookie.Value)
			}
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want %d", rec.Code, tc.want)
			}
			if rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), "acct_desktop_jwt_test") {
				t.Fatal("canonical account identity missing")
			}
			if tc.header && len(rec.Result().Cookies()) != 0 {
				t.Fatal("core header authentication set browser cookie")
			}
		})
	}
}
