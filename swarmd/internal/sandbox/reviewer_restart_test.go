package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Purpose: Docker-like missing-container errors must be retryable only after
// absence verification; fresh managers at every durable phase must retain deny
// identity and finish without resources. This engine-state test proves restart
// ordering, not kernel mount enforcement.
type cleanupEngine struct {
	*fakeRunner
	container, volume bool
	failVolume        bool
	inspectFailure    bool
}

func (r *cleanupEngine) Run(ctx context.Context, engine string, args ...string) ([]byte, error) {
	switch strings.Join(args[:min(2, len(args))], " ") {
	case "rm -f":
		if !r.container {
			return []byte("No such container"), errors.New("missing")
		}
		r.container = false
		return nil, nil
	case "container inspect":
		if r.inspectFailure {
			return []byte("daemon unavailable"), errors.New("engine unavailable")
		}
		if !r.container {
			return []byte("Error: No such container"), errors.New("missing")
		}
		return []byte("{}"), nil
	case "volume ls":
		if r.volume {
			return []byte("home\n"), nil
		}
		return nil, nil
	case "volume rm":
		if r.failVolume {
			return nil, errors.New("volume busy")
		}
		r.volume = false
		return nil, nil
	}
	return r.fakeRunner.Run(ctx, engine, args...)
}
func TestReviewerCleanupRestartPhases(t *testing.T) {
	for _, phase := range []string{"active", "closing", "container_removed", "checkout_removed", "volume_removed"} {
		t.Run(phase, func(t *testing.T) {
			root, checkout := t.TempDir(), t.TempDir()
			name := scopeContainerName(Scope{Root: root, ReviewCheckout: checkout})
			state := ReviewCleanupState{"reviewer", checkout, checkout, root, name, name + "-home", phase}
			r := &cleanupEngine{fakeRunner: readyRunner(), container: phase == "active" || phase == "closing", volume: phase != "volume_removed"}
			m := newTestManager(t, ModeRequired, r.fakeRunner, Config{})
			m.runner = r
			removed := phase == "checkout_removed" || phase == "volume_removed"
			validate := func() error {
				if phase != "active" {
					t.Fatal("revalidated closing checkout")
				}
				return nil
			}
			remove := func() error {
				if !m.reviewClosing[checkout] {
					t.Fatal("not restrictive")
				}
				removed = true
				return nil
			}
			if err := m.CleanupReviewDurable(context.Background(), &state, func(s ReviewCleanupState) error { return nil }, validate, remove); err != nil {
				t.Fatal(err)
			}
			if state.Phase != "volume_removed" || !removed || r.container || r.volume || m.reviews[checkout] != "" {
				t.Fatal(state, r.container, r.volume, removed)
			}
			if _, err := m.acquire(context.Background(), Scope{Root: root, ReviewCheckout: checkout}, "git"); err == nil {
				t.Fatal("terminal lane acquired")
			}
		})
	}
}
func TestReviewerCleanupPartialRetries(t *testing.T) {
	for _, failure := range []string{"checkout", "volume"} {
		t.Run(failure, func(t *testing.T) {
			root, checkout := t.TempDir(), t.TempDir()
			r := &cleanupEngine{fakeRunner: readyRunner(), container: true, volume: true, failVolume: failure == "volume"}
			m := newTestManager(t, ModeRequired, r.fakeRunner, Config{})
			m.runner = r
			m.reviews = map[string]string{checkout: root}
			fail := true
			remove := func() error {
				if fail && failure == "checkout" {
					return errors.New("remove failed")
				}
				return nil
			}
			if err := m.CleanupReviewValidated(context.Background(), checkout, func() error { return nil }, remove); err == nil {
				t.Fatal("failure missing")
			}
			if r.container || m.reviews[checkout] != root {
				t.Fatal("not restrictive after partial success")
			}
			fail = false
			r.failVolume = false
			if err := m.CleanupReviewValidated(context.Background(), checkout, func() error { t.Fatal("revalidated"); return nil }, remove); err != nil {
				t.Fatal(err)
			}
			if r.container || r.volume || m.reviews[checkout] != "" {
				t.Fatal("resources retained")
			}
		})
	}
	r := &cleanupEngine{fakeRunner: readyRunner(), inspectFailure: true}
	m := newTestManager(t, ModeRequired, r.fakeRunner, Config{})
	m.runner = r
	if err := m.EndReview(context.Background(), t.TempDir()); err == nil {
		t.Fatal("engine error swallowed")
	}
}
