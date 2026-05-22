package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestDesktopHandlerServesShellBeforeProductIdentityBootstrap(t *testing.T) {
	server, _, _ := newProtectedIdentityGuardTestServer(t, false)
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<html>onboarding shell</html>"), 0o644); err != nil {
		t.Fatalf("write desktop index: %v", err)
	}
	t.Setenv("SWARM_WEB_DIST_DIR", distDir)

	req := newSameOriginDesktopRequest(http.MethodGet, "/")
	rec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("desktop shell before identity status=%d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("content-type=%q want text/html", got)
	}
	if !strings.Contains(rec.Body.String(), "onboarding shell") {
		t.Fatalf("desktop shell body=%q", rec.Body.String())
	}
}

func TestSwarmStateGetIsDesktopOnboardingAuthExempt(t *testing.T) {
	server, _, _ := newProtectedIdentityGuardTestServer(t, false)
	server.swarm = fakeLocalAuthSwarmService{}
	setProtectedIdentityGuardStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmName = "Unbootstrapped Desktop"
	})

	req := newSameOriginDesktopRequest(http.MethodGet, "/v1/swarm/state")
	rec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("desktop swarm state before identity status=%d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"state"`) {
		t.Fatalf("swarm state response missing state: %s", rec.Body.String())
	}
}

func setProtectedIdentityGuardStartupConfig(t *testing.T, server *Server, mutate func(*startupconfig.FileConfig)) {
	t.Helper()
	cfg, err := server.loadStartupConfig()
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	mutate(&cfg)
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
}
