package run

import (
	"context"
	"errors"
	"io"
	"testing"

	"swarm/packages/swarmd/internal/sandbox"
)

type failingSandboxRunner struct{}

func (failingSandboxRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("no container engine in this test")
}
func (failingSandboxRunner) RunCombined(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("no container engine in this test")
}
func (failingSandboxRunner) RunWithIO(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error {
	return errors.New("no container engine in this test")
}

// withUnavailableSandbox makes the default sandbox one the owner requires
// but that cannot start (no container engine): reviewers must refuse, unlike
// a sandbox the owner turned off.
func withUnavailableSandbox(t *testing.T) {
	t.Helper()
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeRequired, StateDir: t.TempDir(), Memory: "4g", UID: 10001, GID: 10001}, failingSandboxRunner{})
	if err != nil {
		t.Fatal(err)
	}
	previous := sandbox.Default()
	sandbox.SetDefault(m)
	t.Cleanup(func() { sandbox.SetDefault(previous) })
	if m.Active() || sandbox.OwnerTurnedOff() {
		t.Fatalf("fixture: active %v, off %v", m.Active(), sandbox.OwnerTurnedOff())
	}
}
