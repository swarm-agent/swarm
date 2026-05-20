package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/notification"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/workspace"
)

func TestProtectedCreateAPIsRequireBootstrappedProductIdentity(t *testing.T) {
	server, store, identityStore := newProtectedIdentityGuardTestServer(t, false)

	cases := []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{name: "workspace", method: http.MethodPost, path: "/v1/workspace/add", body: map[string]any{"path": filepath.Join(t.TempDir(), "workspace")}},
		{name: "agent", method: http.MethodPut, path: "/v2/agents/slice15", body: map[string]any{"mode": "subagent", "description": "Slice 1.5 guard test"}},
		{name: "credential", method: http.MethodPost, path: "/v1/auth/credentials", body: map[string]any{"provider": "codex", "type": "api", "api_key": "test-key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			server.DesktopHandler().ServeHTTP(rec, newProtectedJSONRequest(t, tc.method, tc.path, tc.body, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, identity.ErrProductIdentityRequired.Error()) && !strings.Contains(body, "invalid or missing attach token") {
				t.Fatalf("body %q missing identity/auth error", body)
			}
		})
	}
	counts, err := identityStore.IdentityCounts()
	if err != nil {
		t.Fatalf("identity counts: %v", err)
	}
	if counts != (pebblestore.IdentityCounts{}) {
		t.Fatalf("guarded creates bootstrapped identity counts=%+v", counts)
	}
	if profiles, err := pebblestore.NewAgentStore(store).ListProfiles(2000); err != nil {
		t.Fatalf("list profiles: %v", err)
	} else if len(profiles) != 0 {
		t.Fatalf("pre-bootstrap agent create persisted profiles=%+v", profiles)
	}
}

func TestProtectedCreateAPIsRejectNonProductAuthAndIncompleteActors(t *testing.T) {
	server, store, _ := newProtectedIdentityGuardTestServer(t, true)

	t.Run("random old cookie is rejected", func(t *testing.T) {
		req := newProtectedJSONRequest(t, http.MethodPut, "/v2/agents/random-cookie", map[string]any{"mode": "subagent"}, nil)
		req.AddCookie(buildDesktopLocalSessionCookie("old-random-token", time.Now().Add(time.Hour), false))
		rec := httptest.NewRecorder()
		server.DesktopHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("random cookie status=%d want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})

	t.Run("team-only jwt is rejected", func(t *testing.T) {
		teamOnlyJWT := signDesktopJWTForTest(t, store, map[string]any{
			"iss":              identity.LocalProductSessionIssuer,
			"aud":              identity.LocalProductSessionAudience,
			"sid":              "sid_team_only_slice15",
			"jti":              "jti_team_only_slice15",
			"account_scope_id": "acct_guard_test",
			"team_id":          "team_guard_test",
			"iat":              time.Now().Unix(),
			"nbf":              time.Now().Add(-time.Minute).Unix(),
			"exp":              time.Now().Add(time.Hour).Unix(),
		})
		req := newProtectedJSONRequest(t, http.MethodPost, "/v1/workspace/add", map[string]any{"path": filepath.Join(t.TempDir(), "team-only")}, nil)
		req.AddCookie(buildDesktopLocalSessionCookie(teamOnlyJWT, time.Now().Add(time.Hour), false))
		rec := httptest.NewRecorder()
		server.DesktopHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("team-only status=%d want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})

	t.Run("valid jwt without account association is rejected", func(t *testing.T) {
		accountUserKey := pebblestore.KeyAccountUser("acct_guard_test", "user_guard_test")
		accountUserByUserKey := pebblestore.KeyAccountUserByUser("user_guard_test", "acct_guard_test")
		accountUserPayload, ok, err := store.GetBytes(accountUserKey)
		if err != nil || !ok {
			t.Fatalf("read account user ok=%v err=%v", ok, err)
		}
		accountUserByUserPayload, ok, err := store.GetBytes(accountUserByUserKey)
		if err != nil || !ok {
			t.Fatalf("read account user by-user ok=%v err=%v", ok, err)
		}
		if err := store.Delete(accountUserKey); err != nil {
			t.Fatalf("delete account user: %v", err)
		}
		if err := store.Delete(accountUserByUserKey); err != nil {
			t.Fatalf("delete account user by-user: %v", err)
		}
		t.Cleanup(func() {
			if err := store.PutBytes(accountUserKey, accountUserPayload); err != nil {
				t.Fatalf("restore account user: %v", err)
			}
			if err := store.PutBytes(accountUserByUserKey, accountUserByUserPayload); err != nil {
				t.Fatalf("restore account user by-user: %v", err)
			}
		})

		jwt := signDesktopJWTForTest(t, store, validProtectedGuardClaims())
		req := newProtectedJSONRequest(t, http.MethodPost, "/v1/auth/credentials", map[string]any{"provider": "codex", "type": "api", "api_key": "test-key"}, nil)
		req.AddCookie(buildDesktopLocalSessionCookie(jwt, time.Now().Add(time.Hour), false))
		rec := httptest.NewRecorder()
		server.DesktopHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("missing account association status=%d want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})
}

func TestProtectedCreateAPIsSucceedAfterBootstrapWithValidProductJWT(t *testing.T) {
	server, _, _ := newProtectedIdentityGuardTestServer(t, false)

	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, newProtectedJSONRequest(t, http.MethodPost, "/v1/onboarding", map[string]any{"username": "guard-user", "swarm_name": "Guard Device", "local_owner_confirmation": requiredLocalOwnerConfirmation}, nil))
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	cookie := sessionCookieFromRecorder(t, bootstrapRec)

	workspacePath := filepath.Join(t.TempDir(), "created-workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	workspaceRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(workspaceRec, newProtectedJSONRequest(t, http.MethodPost, "/v1/workspace/add", map[string]any{"path": workspacePath}, cookie))
	if workspaceRec.Code != http.StatusOK {
		t.Fatalf("workspace create status=%d body=%s", workspaceRec.Code, workspaceRec.Body.String())
	}
	var workspaceResp struct {
		OK        bool `json:"ok"`
		Workspace struct {
			WorkspacePath string `json:"workspace_path"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(workspaceRec.Body.Bytes(), &workspaceResp); err != nil {
		t.Fatalf("decode workspace response: %v", err)
	}
	if !workspaceResp.OK || workspaceResp.Workspace.WorkspacePath != workspacePath {
		t.Fatalf("workspace response=%+v want path %q", workspaceResp, workspacePath)
	}

	agentRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(agentRec, newProtectedJSONRequest(t, http.MethodPut, "/v2/agents/slice15-created", map[string]any{"mode": "subagent", "description": "created after jwt"}, cookie))
	if agentRec.Code != http.StatusOK {
		t.Fatalf("agent create status=%d body=%s", agentRec.Code, agentRec.Body.String())
	}

	credentialRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(credentialRec, newProtectedJSONRequest(t, http.MethodPost, "/v1/auth/credentials", map[string]any{"provider": "codex", "type": "api", "api_key": "test-key"}, cookie))
	if credentialRec.Code != http.StatusOK {
		t.Fatalf("credential create status=%d body=%s", credentialRec.Code, credentialRec.Body.String())
	}
}

func newProtectedIdentityGuardTestServer(t *testing.T, bootstrap bool) (*Server, *pebblestore.Store, *pebblestore.IdentityStore) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "protected-identity-guard.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	authSvc := auth.NewService(pebblestore.NewAuthStore(store), eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	workspaceSvc := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	securitySvc := security.NewService(pebblestore.NewClientAuthStore(store), eventLog)
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		t.Fatalf("ensure attach auth: %v", err)
	}
	hub := stream.NewHub(eventLog)
	notificationSvc := notification.NewService(pebblestore.NewNotificationStore(store), eventLog, hub.Publish)
	identityStore := pebblestore.NewIdentityStore(store)
	identitySvc := identity.NewService(identityStore, identity.WithIDGenerator(protectedGuardIdentityTestIDGenerator))
	if bootstrap {
		if _, err := identitySvc.BootstrapFirstIdentity("guard-user"); err != nil {
			t.Fatalf("bootstrap identity: %v", err)
		}
	}
	server := NewServer("test", authSvc, agentSvc, nil, nil, nil, workspaceSvc, nil, securitySvc, nil, nil, notificationSvc, eventLog, hub)
	server.SetIdentityService(identitySvc)
	server.SetIdentitySessionService(identity.NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(store)))
	server.SetUISettingsService(uisettings.NewService(pebblestore.NewUISettingsStore(store)))
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmName = "Guard Device"
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	return server, store, identityStore
}

func protectedGuardIdentityTestIDGenerator(prefix string) (string, error) {
	switch prefix {
	case "user":
		return "user_guard_test", nil
	case "acct":
		return "acct_guard_test", nil
	default:
		return prefix + "_guard_test", nil
	}
}

func validProtectedGuardClaims() map[string]any {
	return map[string]any{
		"iss":              identity.LocalProductSessionIssuer,
		"sub":              "user_guard_test",
		"aud":              identity.LocalProductSessionAudience,
		"sid":              "sid_guard_test",
		"jti":              "jti_guard_test",
		"account_scope_id": "acct_guard_test",
		"iat":              time.Now().Unix(),
		"nbf":              time.Now().Add(-time.Minute).Unix(),
		"exp":              time.Now().Add(time.Hour).Unix(),
	}
}

func newProtectedJSONRequest(t *testing.T, method, path string, payload map[string]any, cookie *http.Cookie) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := newSameOriginDesktopRequest(method, path)
	req.Body = ioNopCloserBytes(body)
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

func ioNopCloserBytes(body []byte) *nopReadCloser {
	return &nopReadCloser{Reader: bytes.NewReader(body)}
}

type nopReadCloser struct {
	*bytes.Reader
}

func (n *nopReadCloser) Close() error { return nil }
