package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: real terminal input must recover a launch directory without Git
// history through the canonical repository and onboarding APIs. PTY output alone
// is not completion evidence: the persisted config and Git tree are checked.
// This opt-in integration test uses the real handler/store and TUI binary, no
// provider credentials, and a disposable loopback server, not an installed daemon.
func TestOnboardingTerminalRepositoryRecovery(t *testing.T) {
	binary := os.Getenv("SWARM_TEST_TUI_BINARY")
	if binary == "" {
		t.Skip("set SWARM_TEST_TUI_BINARY to a freshly built swarmtui")
	}
	driver, err := filepath.Abs("../../../tests/onboarding_terminal.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"empty", "existing", "unborn", "home", "root", "new-folder", "committed", "permission", "cancel-recover", "cancel", "identity-exit", "provider-exit", "missing-git", "pending-exit"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
			server, _, _, _ := newOnboardingPrimaryTopologyTestServer(t)
			db, err := pebblestore.Open(filepath.Join(root, "models"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			server.providers = registry.New()
			server.model = model.NewService(pebblestore.NewModelStore(db), nil, model.NewCatalogService(pebblestore.NewModelCatalogStore(db)))
			setLocalAuthTestStartupConfig(t, server, func(c *startupconfig.FileConfig) { c.DesktopOnboardingComplete = false })
			socket := filepath.Join(root, "api.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "pending-exit" && r.URL.Path == "/v1/workspace/repository/setup" {
					io.Copy(io.Discard, io.LimitReader(r.Body, 65536))
					if err := os.WriteFile(filepath.Join(root, "pending-request"), []byte("pending"), 0600); err != nil {
						panic(err)
					}
					select {
					case <-r.Context().Done():
					case <-time.After(20 * time.Second):
					}
					return
				}
				server.LocalTransportHandler().ServeHTTP(w, r)
			})
			httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second}
			go httpServer.Serve(listener)
			defer httpServer.Close()
			tcpServer := httptest.NewServer(handler)
			defer tcpServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", driver, binary, scenario)
			cmd.Env = append(os.Environ(), "SWARMD_URL="+tcpServer.URL, "SWARMD_TOKEN=", "SWARMD_LOCAL_TRANSPORT_SOCKET="+socket, "DATA_DIR="+filepath.Join(root, "data"))
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("terminal scenario failed: %v\n%s", err, out)
			}
			cfg, err := server.loadStartupConfig()
			if err != nil {
				t.Fatal(err)
			}
			want := scenario != "cancel" && scenario != "identity-exit" && scenario != "provider-exit" && scenario != "pending-exit"
			if cfg.DesktopOnboardingComplete != want {
				t.Fatalf("persisted completion=%v want %v", cfg.DesktopOnboardingComplete, want)
			}
			if want {
				folder := "launch"
				switch scenario {
				case "home", "root", "new-folder":
					folder = "new-project"
				case "existing", "cancel-recover":
					folder = "safe-project"
				case "permission":
					folder = "recovered"
				}
				principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user_onboarding_test", AccountScopeID: "acct_onboarding_test", AccountScopeSource: identity.AccountScopeSourceServerState}
				entries, err := server.workspace.ListKnownForPrincipal(principal, 10)
				if err != nil || len(entries) != 1 || entries[0].Path != filepath.Join(root, folder) {
					t.Fatalf("persisted workspace=%+v err=%v want selected folder %q", entries, err, folder)
				}
			}
		})
	}
}
