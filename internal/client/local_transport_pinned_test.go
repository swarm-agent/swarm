package client

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Requirement: setup readiness must use the exact selected-user Unix transport,
// never HTTP fallback or an inherited socket. NewLocalTransport is the narrowest
// client boundary proving a successful request and socket-loss rejection.
func TestPinnedLocalTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/onboarding" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"ok":true,"identity":{"bootstrapped":false}}`))
	})}
	defer server.Close()
	go server.Serve(listener)
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", filepath.Join(t.TempDir(), "wrong.sock"))
	api := NewLocalTransport(path)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := api.GetOnboardingStatus(ctx)
	if err != nil || !status.OK {
		t.Fatalf("pinned request: %v", err)
	}
	server.Close()
	os.Remove(path)
	api = NewLocalTransport(path)
	if _, err := api.GetOnboardingStatus(ctx); err == nil {
		t.Fatal("vanished socket admitted")
	}
}
