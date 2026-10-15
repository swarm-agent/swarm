package sandbox

import (
	"context"

	"io"

	"strings"
	"sync"
	"testing"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	fail  map[string]error // keyed by first engine argument
	out   map[string]string
}

func (f *fakeRunner) record(args []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	key := strings.Join(args[:min(2, len(args))], " ")
	if err := f.fail[key]; err != nil {
		return "", err
	}
	return f.out[key], nil
}

func (f *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	out, err := f.record(args)
	if err == nil && len(args) > 3 && args[0] == "container" && args[1] == "inspect" && args[3] == "{{json .State}}" {
		out = `{"Running":true,"StartedAt":"first"}`
	}
	return []byte(out), err
}

func (f *fakeRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.Run(ctx, name, args...)
}

func (f *fakeRunner) RunWithIO(_ context.Context, _ io.Reader, _, _ io.Writer, _ string, args ...string) error {
	_, err := f.record(args)
	return err
}

func (f *fakeRunner) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func readyRunner() *fakeRunner {
	return &fakeRunner{fail: map[string]error{}, out: map[string]string{
		"image inspect": "sha256:img\n",
		"info --format": `{"runc":{}}`,
	}}
}

func newTestManager(t *testing.T, mode Mode, runner *fakeRunner, cfg Config) *Manager {
	t.Helper()
	cfg.Mode = mode
	// Hardening fixtures pin explicit limits; host sizing has its own tests.
	if cfg.Memory == "" {
		cfg.Memory = "4g"
	}
	if cfg.MaxWorkerRuns == 0 {
		cfg.MaxWorkerRuns = 2
	}
	if cfg.UID == 0 {
		cfg.UID, cfg.GID = 10001, 10001
	}
	m, err := NewManager(context.Background(), cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
