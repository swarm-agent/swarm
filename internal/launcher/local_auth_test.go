package launcher

import (
	"net/http"
	"path/filepath"
	"testing"
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
