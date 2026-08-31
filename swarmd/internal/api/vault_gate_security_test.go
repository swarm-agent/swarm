package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVaultGateRejectsLockedAccountWithoutCallingProtectedHandler(t *testing.T) {
	server := newLocalAuthTestServer(t)
	principal := accountTestPrincipal()
	const password = "vault-gate-test-password"

	if _, err := server.auth.EnableVaultForAccount(principal.AccountScopeID, password); err != nil {
		t.Fatalf("enable account vault: %v", err)
	}
	if _, err := server.auth.LockVaultForAccount(principal.AccountScopeID); err != nil {
		t.Fatalf("lock account vault: %v", err)
	}

	called := false
	protected := server.withVaultGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/protected", nil)
	req = requestWithTestPrincipalForAccount(req, principal.UserID, principal.AccountScopeID)
	rec := httptest.NewRecorder()

	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusLocked {
		t.Fatalf("locked vault status = %d, want %d, body=%s", rec.Code, http.StatusLocked, rec.Body.String())
	}
	if called {
		t.Fatal("locked vault request reached protected handler")
	}
	if body := rec.Body.String(); body == "" {
		t.Fatal("locked vault response omitted the fail-closed error")
	}
}

func TestVaultGateAllowsAuthenticatedVaultManagementWhileLocked(t *testing.T) {
	server := newLocalAuthTestServer(t)
	principal := accountTestPrincipal()
	const password = "vault-gate-exemption-test-password"

	if _, err := server.auth.EnableVaultForAccount(principal.AccountScopeID, password); err != nil {
		t.Fatalf("enable account vault: %v", err)
	}
	if _, err := server.auth.LockVaultForAccount(principal.AccountScopeID); err != nil {
		t.Fatalf("lock account vault: %v", err)
	}

	called := false
	exempt := server.withVaultGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/vault", nil)
	req = requestWithTestPrincipalForAccount(req, principal.UserID, principal.AccountScopeID)
	rec := httptest.NewRecorder()

	exempt.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("locked vault-management exemption status=%d called=%v, want authenticated downstream handoff", rec.Code, called)
	}
}
