package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Requirement: missing or expired sign-ins are terminal, not pending. Threat:
// restart leaves clients waiting forever, or exposes another account's login.
// Owners: handleCodexOAuthStatus and controlMCPConnectChatGPT. Calling the real
// MCP builder with in-memory OAuth state is the narrowest transport-level proof;
// HTTP must retain its 404 contract while MCP translates it without credentials.
func TestCodexOAuthLostStatus(t *testing.T) {
	for _, kind := range []string{"missing", "foreign", "expired"} {
		t.Run(kind, func(t *testing.T) {
			s := &Server{}
			if kind != "missing" {
				p := testPrincipal()
				session := codexOAuthSession{UserID: p.UserID, AccountScopeID: p.AccountScopeID, Status: "waiting", ExpiresAt: time.Now().Add(time.Hour)}
				if kind == "foreign" {
					session.AccountScopeID = "other-account"
				}
				if kind == "expired" {
					session.ExpiresAt = time.Now().Add(-time.Hour)
				}
				s.codexOAuthSessions = map[string]*codexOAuthSession{"gone": &session}
			}
			httpResult := httptest.NewRecorder()
			s.handleCodexOAuthStatus(httpResult, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/auth/codex/oauth/status?session_id=gone", nil)))
			if httpResult.Code != http.StatusNotFound || !strings.Contains(httpResult.Body.String(), "oauth session not found") {
				t.Fatalf("HTTP contract changed: %d %s", httpResult.Code, httpResult.Body.String())
			}
			req := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil))
			c := &controlMCPCall{server: s, request: req}
			result, err := controlMCPConnectChatGPT(c, map[string]any{"action": "status", "login_id": "gone", "wait_seconds": 25})
			if err != nil {
				t.Fatal(err)
			}
			out := result.(map[string]any)
			if out["status"] != "lost" || !strings.Contains(out["message"].(string), "action start") {
				t.Fatalf("status = %+v", out)
			}
			if len(out) != 2 {
				t.Fatalf("leaked login: %+v", out)
			}
		})
	}
}
