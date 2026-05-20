package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureLocalAuthRequiresBootstrappedIdentity(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	t.Setenv(localTransportSocketEnv, filepath.Join(t.TempDir(), "missing.sock"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/attach/token" {
			t.Fatalf("unexpected attach bootstrap request")
		}
		if r.URL.Path != "/v1/onboarding" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(OnboardingStatus{OK: true, NeedsOnboarding: true})
	}))
	defer server.Close()

	api := New(server.URL)
	if err := api.EnsureLocalAuth(context.Background()); !errors.Is(err, ErrLocalIdentityBootstrapRequired) {
		t.Fatalf("EnsureLocalAuth() error = %v, want %v", err, ErrLocalIdentityBootstrapRequired)
	}
	if got := api.Token(); got != "" {
		t.Fatalf("Token() = %q, want empty", got)
	}
}

func TestEnsureLocalAuthIssuesProductSessionForBootstrappedIdentity(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	t.Setenv(localTransportSocketEnv, filepath.Join(t.TempDir(), "missing.sock"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(OnboardingStatus{
				OK: true,
				Identity: OnboardingIdentity{
					Bootstrapped: true,
					UserID:       "user_123",
					Username:     "alice",
				},
			})
		case "/v1/auth/desktop/session":
			_ = json.NewEncoder(w).Encode(LocalProductSession{OK: true, Token: "jwt-token", UserID: "user_123", Username: "alice"})
		case "/v1/auth/attach/token":
			t.Fatalf("unexpected attach bootstrap request")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	api := New(server.URL)
	if err := api.EnsureLocalAuth(context.Background()); err != nil {
		t.Fatalf("EnsureLocalAuth() error = %v", err)
	}
	if got := api.Token(); got != "jwt-token" {
		t.Fatalf("Token() = %q, want jwt-token", got)
	}
}

func TestRequestTargetUsesLocalTransportWhenSocketExists(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "api.sock")
	if err := os.WriteFile(socketPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write fake socket sentinel: %v", err)
	}
	t.Setenv(localTransportSocketEnv, socketPath)

	api := New("http://127.0.0.1:7781")
	baseURL, httpClient, usedSocket := api.requestTarget()
	if baseURL != localTransportBaseURL {
		t.Fatalf("baseURL = %q, want %q", baseURL, localTransportBaseURL)
	}
	if usedSocket != socketPath {
		t.Fatalf("usedSocket = %q, want %q", usedSocket, socketPath)
	}
	if httpClient == nil {
		t.Fatal("requestTarget() returned nil http client")
	}
}

func TestResolveLocalTransportSocketPathRequiresLoopbackAndNoToken(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "api.sock")
	if err := os.WriteFile(socketPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write fake socket sentinel: %v", err)
	}
	t.Setenv(localTransportSocketEnv, socketPath)

	if got := resolveLocalTransportSocketPath("http://192.0.2.10:7781", ""); got != "" {
		t.Fatalf("non-loopback resolve = %q, want empty", got)
	}
	if got := resolveLocalTransportSocketPath("http://127.0.0.1:7781", "token-present"); got != "" {
		t.Fatalf("token-present resolve = %q, want empty", got)
	}
	if got := resolveLocalTransportSocketPath("http://127.0.0.1:7781", ""); got != socketPath {
		t.Fatalf("loopback resolve = %q, want %q", got, socketPath)
	}
}
