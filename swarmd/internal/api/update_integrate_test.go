package api

// Requirement: integration rebuilds must use the configured dev checkout, not a
// sibling lane or release updater. validateExpectedDevRoot is the pre-run guard;
// temporary config tests prove rejection without launching any helper.
import (
	"context"
	"os"
	"path/filepath"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	"testing"
)

func TestIntegrationRebuildCheckoutGuard(t *testing.T) {
	root := makeUpdateDevRoot(t)
	path := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(path)
	cfg.DevMode = true
	cfg.DevRoot = root
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatal(err)
	}
	s := &Server{startupConfigPath: path}
	before, _ := os.ReadFile(path)
	for _, input := range []string{"", "relative", t.TempDir(), filepath.Join(root, "missing")} {
		if err := s.validateExpectedDevRoot(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	if err := s.validateExpectedDevRoot(root); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("guard mutated config")
	}
	cfg.DevMode = false
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.validateExpectedDevRoot(root); err == nil {
		t.Fatal("accepted release mode")
	}
}

// Guarded starts must not reuse a pre-integration job or start release updates.
func TestIntegrationRebuildDoesNotReuseRunningJob(t *testing.T) {
	root := makeUpdateDevRoot(t)
	path := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(path)
	cfg.DevMode = true
	cfg.DevRoot = root
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatal(err)
	}
	s := &Server{startupConfigPath: path, dataDir: t.TempDir()}
	original := desktopUpdateJob{ID: "existing", Kind: updateKindDev, Status: updateJobStatusRunning}
	runner := &updateJobRunner{current: original}
	ctx := context.WithValue(context.Background(), expectedDevRootKey{}, root)
	if _, err := runner.Start(ctx, s); err == nil {
		t.Fatal("reused running job")
	}
	if runner.current.ID != original.ID || runner.current.Status != original.Status {
		t.Fatal("changed running job")
	}
	cfg.DevMode = false
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(ctx, s); err == nil {
		t.Fatal("started release update")
	}
	if runner.current.ID != original.ID || runner.current.Status != original.Status {
		t.Fatal("changed job on mode rejection")
	}
}
