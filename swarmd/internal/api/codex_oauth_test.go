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
// MCP builder with in-memory OAuth state is the narrowest transport-level proof.
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
			if out["user_code"] != nil || out["credential"] != nil {
				t.Fatalf("leaked login: %+v", out)
			}
		})
	}
}
