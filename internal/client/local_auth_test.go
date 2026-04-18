package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureLocalAuthIsNoOpWithoutAttachBootstrap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/attach/token" {
			t.Fatalf("unexpected attach bootstrap request")
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	api := New(server.URL)
	if err := api.EnsureLocalAuth(context.Background()); err != nil {
		t.Fatalf("EnsureLocalAuth() error = %v", err)
	}
	if got := api.Token(); got != "" {
		t.Fatalf("Token() = %q, want empty", got)
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
