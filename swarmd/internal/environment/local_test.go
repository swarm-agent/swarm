package environment

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/sandbox"
)

// engineDouble implements the sandbox manager's existing CommandRunner seam.
// It records actual manager lifecycle commands, without starting Docker.
type engineDouble struct{ calls []string }

func (d *engineDouble) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	text := strings.Join(args, " ")
	d.calls = append(d.calls, text)
	switch {
	case strings.HasPrefix(text, "info --format"):
		return []byte(`{"runc":{}}`), nil
	case strings.HasPrefix(text, "image inspect"):
		return []byte("sha256:fixture"), nil
	case strings.Contains(text, "{{json .State}}"):
		return []byte(`{"Running":true,"StartedAt":"first"}`), nil
	}
	return nil, nil
}
func (d *engineDouble) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return d.Run(ctx, name, args...)
}
func (d *engineDouble) RunWithIO(ctx context.Context, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
	_, err := d.Run(ctx, name, args...)
	return err
}

// Purpose: Local.Acquire must release the fresh manager container when initial
// synchronization fails. This engine-level test observes real manager creation
// and cleanup on an invalid commit, without live Docker or provider services.
func TestLocalEnvironmentAcquireFailureReleases(t *testing.T) {
	engine := &engineDouble{}
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeRequired, StateDir: t.TempDir(), Memory: "4g", MaxWorkerRuns: 2, UID: 10001, GID: 10001}, engine)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	env, err := (Local{Manager: m}).Acquire(context.Background(), Spec{Folder: root, Checkout: root, Commit: "invalid"})
	if err == nil || env != nil || !strings.Contains(err.Error(), "invalid environment commit") {
		t.Fatalf("%v %v", env, err)
	}
	created, removed, volume := false, false, false
	for _, cmd := range engine.calls {
		if strings.Contains(cmd, "-landing-") {
			created = created || strings.HasPrefix(cmd, "run -d")
			removed = removed || strings.HasPrefix(cmd, "rm -f")
			volume = volume || strings.HasPrefix(cmd, "volume rm")
		}
	}
	if !created || !removed || !volume {
		t.Fatalf("lifecycle create=%v remove=%v volume=%v", created, removed, volume)
	}
	_, err = (Local{}).Acquire(context.Background(), Spec{})
	if !errors.Is(err, sandbox.ErrInactive) {
		t.Fatal(err)
	}
}
