package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultLocalTransportSocketPathUsesDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "swarmd", "main")
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("SWARM_LANE", "main")
	got := defaultLocalTransportSocketPath()
	want := filepath.Join(dataDir, "local-transport", "api.sock")
	if got != want {
		t.Fatalf("defaultLocalTransportSocketPath() = %q, want %q", got, want)
	}
}

func TestRequestURLUsesLocalTransportHostWhenSocketEnvPresent(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "api.sock")
	if err := os.WriteFile(socketPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write fake socket sentinel: %v", err)
	}
	t.Setenv(localTransportSocketEnv, socketPath)

	got := requestURL("http://127.0.0.2:17881/v1/vault")
	want := "http://swarm-local-transport/v1/vault"
	if got != want {
		t.Fatalf("requestURL() = %q, want %q", got, want)
	}
}

func TestLocalTransportHTTPClientConfiguredFromEnv(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "api.sock")
	if err := os.WriteFile(socketPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write fake socket sentinel: %v", err)
	}
	t.Setenv(localTransportSocketEnv, socketPath)

	client := localTransportHTTPClient()
	if client == nil {
		t.Fatal("localTransportHTTPClient() = nil, want client")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("Transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("transport.Proxy should be nil for local transport")
	}
	if transport.DialContext == nil {
		t.Fatal("transport.DialContext should be configured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.DialContext(ctx, "tcp", "ignored"); err == nil {
		t.Fatal("DialContext error = nil, want canceled context error")
	}
}
