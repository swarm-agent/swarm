package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: boxSandbox and boxFolderEnvironment must distinguish off/host
// execution from folder Docker leases in the API shape retained by MCP.
// Real workspace/environment stores exercise account lookup and lease wiring;
// only engine discovery is faked. Absent services and missing definitions must
// not invent folder environments.
func TestBoxSandboxOffWithFolderLeases(t *testing.T) {
	path := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeOff, Backend: sandbox.BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path)
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{workspace: workspace.NewService(pebblestore.NewWorkspaceStore(db)), sandboxEnvironments: pebblestore.NewSandboxEnvironmentStore(db)}
	principal := accountTestPrincipal()
	root := t.TempDir()
	outboxPinGit(t, root, "init", "-q")
	outboxPinGit(t, root, "commit", "-q", "--allow-empty", "-m", "base")
	if _, err := s.workspace.AddForPrincipal(principal, root, "folder", "", false); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []*Server{{}, {workspace: s.workspace}, {sandboxEnvironments: s.sandboxEnvironments}, s} {
		if got := absent.boxSandbox(principal, m.Status()); len(got["folder_environments"].([]map[string]any)) != 0 || got["error"] != nil {
			t.Fatalf("absent environment: %#v", got)
		}
	}
	if _, _, err := s.sandboxEnvironments.Define(principal.AccountScopeID, root, pebblestore.SandboxEnvironmentDefinition{BaseImage: "fixture:latest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sandboxEnvironments.SetMaxLeases(principal.AccountScopeID, root, 3); err != nil {
		t.Fatal(err)
	}
	leases, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeRequired, UID: 997, GID: 988, Memory: "4g", MaxWorkerRuns: 2, StateDir: t.TempDir()}, boxLeaseRunner{})
	if err != nil {
		t.Fatal(err)
	}
	previous := sandbox.Leases()
	sandbox.SetLeases(leases)
	defer sandbox.SetLeases(previous)
	out := s.boxSandbox(principal, m.Status())
	folders := out["folder_environments"].([]map[string]any)
	if len(folders) != 1 || folders[0]["workspace_path"] != root {
		t.Fatalf("folder lookup: %#v", out)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	leaseUID := os.Getuid()
	if leaseUID == 0 {
		leaseUID = 997
	}
	for _, want := range []string{`"mode":"off"`, `"runtime":"host"`, `"engine":"docker"`, `"runtime":"runsc"`, fmt.Sprintf(`"owner_user":"%d:988"`, leaseUID), `"lease_user":"997:988"`, `"max_leases":3`, `"isolation":"gVisor"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
	if strings.Contains(string(data), "bubblewrap") {
		t.Fatalf("false runtime: %s", data)
	}
}

type boxLeaseRunner struct{}

func (boxLeaseRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	switch strings.Join(args[:min(2, len(args))], " ") {
	case "info --format":
		return []byte(`{"runc":{},"runsc":{}}`), nil
	case "image inspect":
		return []byte("sha256:fixture\n"), nil
	default:
		return nil, nil
	}
}
func (r boxLeaseRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func (r boxLeaseRunner) RunWithIO(ctx context.Context, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
	_, err := r.Run(ctx, name, args...)
	return err
}
