package sandbox

import (
	"context"
	"errors"
	"testing"
)

// Purpose: ResourceDefaults and scopeContainerName must bound aggregate worker
// memory and isolate OOM domains without weakening reviewer cache isolation.
// Pure unit tests are the narrowest layer for host-independent sizing decisions.
func TestWorkerResources(t *testing.T) {
	for _, tc := range []struct {
		cpu     int
		ram     uint64
		workers int
		bytes   uint64
	}{
		{8, 31 * gib, 4, 31 * gib * 3 / 16}, {32, 8 * gib, 2, 3 * gib}, {1, 2 * gib, 1, 3 * gib / 2},
	} {
		n, b := ResourceDefaults(tc.cpu, tc.ram)
		if n != tc.workers || b != tc.bytes {
			t.Fatalf("%+v: got %d %d", tc, n, b)
		}
	}
	a := Scope{Root: "/project", JobCheckout: "/worktrees/a"}
	b := Scope{Root: a.Root, JobCheckout: "/worktrees/b"}
	if scopeContainerName(a) == scopeContainerName(b) {
		t.Fatal("jobs share OOM domain")
	}
	if cacheContainerName(scopeContainerName(a), a.Root) != ContainerName(a.Root) {
		t.Fatal("jobs lost shared cache")
	}
	a.ReviewCheckout = "/review"
	if cacheContainerName(scopeContainerName(a), a.Root) == ContainerName(a.Root) {
		t.Fatal("reviewer shares writable cache")
	}
}

// Purpose: Manager.interruption must distinguish container death from ordinary
// Git/command failure, preserve the checkout and invalidate only the dead box.
// Fake engine state proves classification without needing Docker.
func TestContainerInterruption(t *testing.T) {
	for _, state := range []string{"true false false", "false false false", "false true true"} {
		runner := readyRunner()
		runner.out["container inspect"] = state
		m := newTestManager(t, ModeRequired, runner, Config{})
		err := m.interruption(context.Background(), Scope{Root: t.TempDir()}, "OCI runtime exec failed: container in state stopped")
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("%s: %v", state, err)
		}
	}
	runner := readyRunner()
	runner.out["container inspect"] = "false true false"
	m := newTestManager(t, ModeRequired, runner, Config{})
	if err := m.interruption(context.Background(), Scope{Root: t.TempDir()}, "fatal: not a git repository"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"container is not running", "cannot execute in container in state stopped", "container is restarting"} {
		if !ContainerStopped(s) {
			t.Fatal(s)
		}
	}
	if ReadOnlyGit([]string{"commit"}) || ReadOnlyGit([]string{"worktree", "add"}) || !ReadOnlyGit([]string{"rev-parse", "HEAD"}) {
		t.Fatal("unsafe retry policy")
	}
}

// Purpose: gitOutputWithRecovery retries exactly once for observational checks,
// never replays mutations, and exposes retryable interruption after exhaustion.
// Injected engine death and command callbacks isolate the retry boundary.
func TestGitContainerRetryOnce(t *testing.T) {
	for _, command := range []string{"rev-parse", "commit"} {
		runner := readyRunner()
		runner.out["container inspect"] = "true false false"
		m := newTestManager(t, ModeRequired, runner, Config{})
		root := t.TempDir()
		m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{root}, nil }})
		calls := 0
		out, err := gitOutputWithRecovery(context.Background(), m, root, []string{command}, func() ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("container in state stopped"), errors.New("exec failed")
			}
			return []byte("HEAD"), nil
		})
		if command == "rev-parse" {
			if calls != 2 || err != nil || string(out) != "HEAD" {
				t.Fatalf("read retry: %d %s %v", calls, out, err)
			}
		} else {
			if calls != 1 || !errors.Is(err, ErrInterrupted) {
				t.Fatalf("mutation replayed: %d %v", calls, err)
			}
		}
	}
}
