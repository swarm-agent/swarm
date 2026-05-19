package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestDesktopSessionBootstrapFailsBeforeIdentityAndDoesNotCreateIdentity(t *testing.T) {
	server, identityStore, cleanup := newDesktopJWTTestServer(t, false)
	defer cleanup()

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newSameOriginDesktopRequest(http.MethodGet, "/v1/auth/desktop/session"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("desktop session before identity status=%d want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	counts, err := identityStore.IdentityCounts()
	if err != nil {
		t.Fatalf("identity counts: %v", err)
	}
	if counts != (pebblestore.IdentityCounts{}) {
		t.Fatalf("identity counts after failed session bootstrap = %+v, want zero", counts)
	}
}

func TestDesktopSessionBootstrapIssuesJWTAndProtectedAPIAcceptsAfterRestart(t *testing.T) {
	server, _, cleanup := newDesktopJWTTestServer(t, true)
	defer cleanup()

	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, newSameOriginDesktopRequest(http.MethodGet, "/v1/auth/desktop/session"))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("desktop session status=%d want %d body=%s", bootstrapRec.Code, http.StatusOK, bootstrapRec.Body.String())
	}
	cookie := sessionCookieFromRecorder(t, bootstrapRec)
	if parts := strings.Split(cookie.Value, "."); len(parts) != 3 {
		t.Fatalf("desktop session cookie is not a compact jwt: %q", cookie.Value)
	}
	var response struct {
		OK     bool   `json:"ok"`
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(bootstrapRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if !response.OK || response.UserID != "user_desktop_jwt_test" {
		t.Fatalf("bootstrap response = %+v", response)
	}

	restarted := server
	vaultRec := httptest.NewRecorder()
	vaultReq := newSameOriginDesktopRequest(http.MethodGet, "/v1/vault")
	vaultReq.AddCookie(cookie)
	restarted.Handler().ServeHTTP(vaultRec, vaultReq)
	if vaultRec.Code != http.StatusOK {
		t.Fatalf("protected API with persisted jwt status=%d want %d body=%s", vaultRec.Code, http.StatusOK, vaultRec.Body.String())
	}
}

func TestDesktopSessionRejectsOldRandomSingletonCookie(t *testing.T) {
	server, _, cleanup := newDesktopJWTTestServer(t, true)
	defer cleanup()

	req := newSameOriginDesktopRequest(http.MethodGet, "/v1/vault")
	req.AddCookie(buildDesktopLocalSessionCookie("not-a-jwt-random-cookie", time.Now().Add(identity.LocalProductSessionTTL), false))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old random cookie status=%d want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func newDesktopJWTTestServer(t *testing.T, bootstrap bool) (*Server, *pebblestore.IdentityStore, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "desktop-jwt.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("event log: %v", err)
	}
	identityStore := pebblestore.NewIdentityStore(store)
	if bootstrap {
		_, err := identity.NewService(identityStore, identity.WithIDGenerator(func(prefix string) (string, error) {
			switch prefix {
			case "user":
				return "user_desktop_jwt_test", nil
			case "team":
				return "team_desktop_jwt_test", nil
			default:
				return prefix + "_desktop_jwt_test", nil
			}
		})).BootstrapFirstIdentity("desktop-jwt-user")
		if err != nil {
			_ = store.Close()
			t.Fatalf("bootstrap identity: %v", err)
		}
	}
	securitySvc := security.NewService(pebblestore.NewClientAuthStore(store), events)
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		_ = store.Close()
		t.Fatalf("ensure attach auth: %v", err)
	}
	server := NewServer("test", auth.NewService(pebblestore.NewAuthStore(store), events), nil, nil, nil, nil, nil, nil, securitySvc, nil, nil, nil, events, stream.NewHub(events))
	server.SetIdentitySessionService(identity.NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(store)))
	return server, identityStore, func() { _ = store.Close() }
}

func newSameOriginDesktopRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:5555"+path, nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return req
}
