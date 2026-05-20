package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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
	server, _, identityStore, cleanup := newDesktopJWTTestServer(t, false)
	defer cleanup()

	rec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(rec, newSameOriginDesktopRequest(http.MethodGet, "/v1/auth/desktop/session"))
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
	server, storePath, _, cleanup := newDesktopJWTPersistentTestServer(t, true)
	defer cleanup()

	bootstrapRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(bootstrapRec, newSameOriginDesktopRequest(http.MethodGet, "/v1/auth/desktop/session"))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("desktop session status=%d want %d body=%s", bootstrapRec.Code, http.StatusOK, bootstrapRec.Body.String())
	}
	cookie := sessionCookieFromRecorder(t, bootstrapRec)
	if parts := strings.Split(cookie.Value, "."); len(parts) != 3 {
		t.Fatalf("desktop session cookie is not a compact jwt: %q", cookie.Value)
	}
	claims := decodeDesktopJWTClaims(t, cookie.Value)
	if claims["sub"] != "user_desktop_jwt_test" || claims["account_scope_id"] != "acct_desktop_jwt_test" || claims["team_id"] != nil {
		t.Fatalf("claims sub/account_scope_id/team_id = %v/%v/%v", claims["sub"], claims["account_scope_id"], claims["team_id"])
	}
	for _, required := range []string{"iss", "aud", "sid", "jti", "iat", "nbf", "exp"} {
		if _, ok := claims[required]; !ok {
			t.Fatalf("jwt claims missing %s: %+v", required, claims)
		}
	}
	var response struct {
		OK       bool   `json:"ok"`
		Token    string `json:"token"`
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(bootstrapRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if !response.OK || response.Token != cookie.Value || response.UserID != "user_desktop_jwt_test" || response.Username != "desktop-jwt-user" {
		t.Fatalf("bootstrap response = %+v", response)
	}

	cleanup()
	restartedStore, err := pebblestore.Open(storePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = restartedStore.Close() }()
	restarted := newDesktopJWTServerFromStore(t, restartedStore)
	vaultRec := httptest.NewRecorder()
	vaultReq := newSameOriginDesktopRequest(http.MethodGet, "/v1/vault")
	vaultReq.AddCookie(cookie)
	restarted.DesktopHandler().ServeHTTP(vaultRec, vaultReq)
	if vaultRec.Code != http.StatusOK {
		t.Fatalf("protected API with persisted jwt status=%d want %d body=%s", vaultRec.Code, http.StatusOK, vaultRec.Body.String())
	}
}

func TestLocalTransportSessionBootstrapReturnsTokenForTUI(t *testing.T) {
	server, _, _, cleanup := newDesktopJWTTestServer(t, true)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://swarm-local-transport/v1/auth/desktop/session", nil)
	req.RemoteAddr = "192.0.2.10:43210"
	server.LocalTransportHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local transport session status=%d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response struct {
		OK       bool   `json:"ok"`
		Token    string `json:"token"`
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode local transport session response: %v", err)
	}
	if !response.OK || strings.TrimSpace(response.Token) == "" || response.UserID != "user_desktop_jwt_test" || response.Username != "desktop-jwt-user" {
		t.Fatalf("local transport session response = %+v", response)
	}

	meRec := httptest.NewRecorder()
	meReq := httptest.NewRequest(http.MethodGet, "http://swarm-local-transport/me", nil)
	meReq.RemoteAddr = "192.0.2.10:43210"
	meReq.Header.Set("X-Swarm-Token", response.Token)
	server.LocalTransportHandler().ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("local transport /me with X-Swarm-Token status=%d want %d body=%s", meRec.Code, http.StatusOK, meRec.Body.String())
	}
	var me struct {
		Type                 string  `json:"type"`
		UserID               string  `json:"userID"`
		AccountScopeID       string  `json:"accountScopeID"`
		TeamID               *string `json:"teamID"`
		AccountScopeSource   string  `json:"accountScopeSource"`
		AccountScopeSourceUS string  `json:"account_scope_source"`
		SessionID            string  `json:"session_id"`
	}
	if err := json.Unmarshal(meRec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode local transport /me response: %v", err)
	}
	if me.Type != "user" || me.UserID != "user_desktop_jwt_test" || me.AccountScopeID != "acct_desktop_jwt_test" || me.TeamID != nil || strings.TrimSpace(me.SessionID) == "" {
		t.Fatalf("local transport /me principal = %+v", me)
	}
	if me.AccountScopeSource != string(identity.AccountScopeSourceSession) || me.AccountScopeSourceUS != string(identity.AccountScopeSourceSession) {
		t.Fatalf("local transport /me account scope source = %q/%q, want %q", me.AccountScopeSource, me.AccountScopeSourceUS, identity.AccountScopeSourceSession)
	}
}

func TestXSwarmTokenPrincipalTakesPrecedenceOverInvalidDesktopCookie(t *testing.T) {
	server, _, _, cleanup := newDesktopJWTTestServer(t, true)
	defer cleanup()

	bootstrapRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(bootstrapRec, newSameOriginDesktopRequest(http.MethodGet, "/v1/auth/desktop/session"))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("desktop session status=%d want %d body=%s", bootstrapRec.Code, http.StatusOK, bootstrapRec.Body.String())
	}
	validToken := sessionCookieFromRecorder(t, bootstrapRec).Value

	req := newSameOriginDesktopRequest(http.MethodGet, "/v1/me")
	req.AddCookie(buildDesktopLocalSessionCookie("not-a-valid-product-jwt", time.Now().Add(time.Hour), false))
	req.Header.Set("X-Swarm-Token", validToken)
	rec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me with X-Swarm-Token and invalid cookie status=%d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var me struct {
		Type           string  `json:"type"`
		UserID         string  `json:"userID"`
		AccountScopeID string  `json:"accountScopeID"`
		TeamID         *string `json:"teamID"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode /me response: %v", err)
	}
	if me.Type != "user" || me.UserID != "user_desktop_jwt_test" || me.AccountScopeID != "acct_desktop_jwt_test" || me.TeamID != nil {
		t.Fatalf("/me principal = %+v", me)
	}
}

func TestDesktopSessionRejectsOldRandomSingletonCookie(t *testing.T) {
	server, _, _, cleanup := newDesktopJWTTestServer(t, true)
	defer cleanup()

	req := newSameOriginDesktopRequest(http.MethodGet, "/v1/vault")
	req.AddCookie(buildDesktopLocalSessionCookie("not-a-jwt-random-cookie", time.Now().Add(identity.LocalProductSessionTTL), false))
	rec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old random cookie status=%d want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestDesktopSessionRejectsTeamOnlyJWTCookieForProtectedAPI(t *testing.T) {
	server, store, _, cleanup := newDesktopJWTTestServer(t, true)
	defer cleanup()

	teamOnlyJWT := signDesktopJWTForTest(t, store, map[string]any{
		"iss":              identity.LocalProductSessionIssuer,
		"aud":              identity.LocalProductSessionAudience,
		"sid":              "sid_team_only",
		"jti":              "jti_team_only",
		"account_scope_id": "acct_desktop_jwt_test",
		"iat":              time.Now().Unix(),
		"nbf":              time.Now().Add(-time.Minute).Unix(),
		"exp":              time.Now().Add(time.Hour).Unix(),
	})
	req := newSameOriginDesktopRequest(http.MethodGet, "/v1/vault")
	req.AddCookie(buildDesktopLocalSessionCookie(teamOnlyJWT, time.Now().Add(time.Hour), false))
	rec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("team-only jwt cookie status=%d want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestDesktopSessionRejectsStaleTeamMismatchJWTCookie(t *testing.T) {
	server, store, _, cleanup := newDesktopJWTTestServer(t, true)
	defer cleanup()

	staleTeamJWT := signDesktopJWTForTest(t, store, map[string]any{
		"iss":              identity.LocalProductSessionIssuer,
		"sub":              "user_desktop_jwt_test",
		"aud":              identity.LocalProductSessionAudience,
		"sid":              "sid_stale_team",
		"jti":              "jti_stale_team",
		"account_scope_id": "acct_desktop_jwt_test",
		"team_id":          "team_stale",
		"iat":              time.Now().Unix(),
		"nbf":              time.Now().Add(-time.Minute).Unix(),
		"exp":              time.Now().Add(time.Hour).Unix(),
	})
	req := newSameOriginDesktopRequest(http.MethodGet, "/v1/vault")
	req.AddCookie(buildDesktopLocalSessionCookie(staleTeamJWT, time.Now().Add(time.Hour), false))
	rec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale team jwt cookie status=%d want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func newDesktopJWTTestServer(t *testing.T, bootstrap bool) (*Server, *pebblestore.Store, *pebblestore.IdentityStore, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "desktop-jwt.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	identityStore := bootstrapDesktopJWTIdentity(t, store, bootstrap)
	server := newDesktopJWTServerFromStore(t, store)
	return server, store, identityStore, func() { _ = store.Close() }
}

func newDesktopJWTPersistentTestServer(t *testing.T, bootstrap bool) (*Server, string, *pebblestore.IdentityStore, func()) {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "desktop-jwt.pebble")
	store, err := pebblestore.Open(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	identityStore := bootstrapDesktopJWTIdentity(t, store, bootstrap)
	server := newDesktopJWTServerFromStore(t, store)
	closed := false
	cleanup := func() {
		if closed {
			return
		}
		closed = true
		_ = store.Close()
	}
	return server, storePath, identityStore, cleanup
}

func bootstrapDesktopJWTIdentity(t *testing.T, store *pebblestore.Store, bootstrap bool) *pebblestore.IdentityStore {
	t.Helper()
	identityStore := pebblestore.NewIdentityStore(store)
	if !bootstrap {
		return identityStore
	}
	_, err := identity.NewService(identityStore, identity.WithIDGenerator(func(prefix string) (string, error) {
		switch prefix {
		case "user":
			return "user_desktop_jwt_test", nil
		case "acct":
			return "acct_desktop_jwt_test", nil
		default:
			return prefix + "_desktop_jwt_test", nil
		}
	})).BootstrapFirstIdentity("desktop-jwt-user")
	if err != nil {
		t.Fatalf("bootstrap identity: %v", err)
	}
	return identityStore
}

func newDesktopJWTServerFromStore(t *testing.T, store *pebblestore.Store) *Server {
	t.Helper()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	securitySvc := security.NewService(pebblestore.NewClientAuthStore(store), events)
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		t.Fatalf("ensure attach auth: %v", err)
	}
	identityStore := pebblestore.NewIdentityStore(store)
	server := NewServer("test", auth.NewService(pebblestore.NewAuthStore(store), events), nil, nil, nil, nil, nil, nil, securitySvc, nil, nil, nil, events, stream.NewHub(events))
	server.SetIdentityService(identity.NewService(identityStore))
	server.SetIdentitySessionService(identity.NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(store)))
	return server
}

func signDesktopJWTForTest(t *testing.T, store *pebblestore.Store, claims map[string]any) string {
	t.Helper()
	key, _, err := pebblestore.NewIdentitySessionStore(store).EnsureLocalProductJWTSigningKey()
	if err != nil {
		t.Fatalf("load signing key: %v", err)
	}
	headerPayload, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsPayload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(headerPayload) + "." + base64.RawURLEncoding.EncodeToString(claimsPayload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func decodeDesktopJWTClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not jwt: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode jwt claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal jwt claims: %v", err)
	}
	return claims
}

func newSameOriginDesktopRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:5555"+path, nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return req
}
