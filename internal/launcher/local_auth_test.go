package launcher

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestLocalTransportSocketPathUsesProfileDataDir(t *testing.T) {
	profile := Profile{DataDir: filepath.Join(t.TempDir(), "swarmd", "main")}
	got := LocalTransportSocketPath(profile)
	want := filepath.Join(profile.DataDir, "local-transport", "api.sock")
	if got != want {
		t.Fatalf("LocalTransportSocketPath() = %q, want %q", got, want)
	}
}

func TestHTTPRequestUsesLocalTransportURLRewriteAndNoTokenHeader(t *testing.T) {
	profile := Profile{DataDir: filepath.Join(t.TempDir(), "swarmd", "main")}
	body, status, err := httpRequest(t.Context(), profile, http.MethodGet, "http://127.0.0.1:7781/v1/vault", map[string]string{"Accept": "application/json"}, nil)
	if err == nil {
		t.Fatal("httpRequest() error = nil, want dial error for missing socket")
	}
	if status != 0 {
		t.Fatalf("status = %d, want 0 on dial failure", status)
	}
	if len(body) != 0 {
		t.Fatalf("body length = %d, want 0 on dial failure", len(body))
	}
}

func TestRequestReleaseUpdatePlanBootstrapsPrincipalOverLocalTransport(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "swarm-update-auth-")
	if err != nil {
		t.Fatalf("create short data dir: %v", err)
	}
	defer os.RemoveAll(dataDir)
	socketPath := LocalTransportSocketPath(Profile{DataDir: dataDir})
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()

	const token = "test-product-session"
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/desktop/session":
			_ = json.NewEncoder(w).Encode(client.LocalProductSession{OK: true, Token: token})
		case "/v1/update/apply":
			if got := r.Header.Get("X-Swarm-Token"); got != token {
				http.Error(w, "trusted principal is required", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(plan)
		default:
			http.NotFound(w, r)
		}
	})}
	defer server.Shutdown(context.Background())
	go func() { _ = server.Serve(listener) }()

	got, err := RequestReleaseUpdatePlan(t.Context(), Profile{DataDir: dataDir, URL: "http://127.0.0.1:7781"})
	if err != nil {
		t.Fatalf("RequestReleaseUpdatePlan() error = %v", err)
	}
	if got.TargetVersion != plan.TargetVersion {
		t.Fatalf("TargetVersion = %q, want %q", got.TargetVersion, plan.TargetVersion)
	}
}
