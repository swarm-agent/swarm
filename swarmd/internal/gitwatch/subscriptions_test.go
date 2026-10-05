package gitwatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
)

// Requirement: native source/common-dir/target changes must reach subscribers
// without activity or polling. Subscriptions + NewFSNotify is the narrowest real
// producer layer; real linked Git checkouts expose missing common-dir watches.
// The HTTP/board fixture additionally counts all actual Git commands while idle.
func TestSubscriptionsNativeLinkedWorktrees(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "-b", "dev")
	git(root, "config", "user.name", "Fixture")
	git(root, "config", "user.email", "fixture@example.invalid")
	git(root, "commit", "--allow-empty", "-m", "base")
	base := git(root, "rev-parse", "HEAD")
	source := filepath.Join(t.TempDir(), "source")
	git(root, "worktree", "add", "-b", "agent/task", source)
	paths, err := gitstatus.ResolveWatchPaths(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSubscriptions()
	defer s.Close()
	config := Config{source, paths.GitDir, paths.CommonDir}
	changes, release, err := s.Acquire(config, "agent/task")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	target, releaseTarget, err := s.Acquire(config, "dev")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseTarget()
	wait := func(ch <-chan Notice, kind string) {
		t.Helper()
		select {
		case n := <-ch:
			if n.Kind != kind {
				t.Fatalf("got %s want %s", n.Kind, kind)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing native notification")
		}
	}
	wait(changes, "ready")
	wait(target, "ready")
	s.mu.Lock()
	count := len(s.roots)
	s.mu.Unlock()
	if count != 1 {
		t.Fatalf("duplicate native root: %d", count)
	}
	if err := os.WriteFile(filepath.Join(source, "change"), []byte("external\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", "change")
	git(source, "commit", "-m", "external source")
	wait(changes, "changed")
	wait(target, "changed") // Both subscriptions also observe this checkout's index.
	head := git(source, "rev-parse", "HEAD")
	git(root, "update-ref", "refs/heads/dev", head)
	wait(target, "changed")
	git(root, "update-ref", "refs/heads/dev", base)
	wait(target, "changed")
	git(root, "pack-refs", "--all", "--prune")
	wait(target, "changed")
	git(root, "update-ref", "refs/heads/dev", head)
	wait(target, "changed")
	// Parent and unrelated branch activity must not invalidate this worktree.
	git(root, "update-ref", "refs/heads/unrelated", head)
	select {
	case notice := <-target:
		t.Fatalf("unrelated notice: %+v", notice)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	releaseTarget()
	s.mu.Lock()
	count = len(s.roots)
	s.mu.Unlock()
	if count != 0 {
		t.Fatal("subscriptions not released")
	}
}

// Requirement: watcher loss fences observations and only successful reinstallation
// emits ready. A backend seam injects overflow (not fake Git evidence); counts
// prove idle time does not recreate watchers, and teardown closes ownership.
func TestSubscriptionsLossRecoveryAndIdle(t *testing.T) {
	s := NewSubscriptions()
	defer s.Close()
	var installs atomic.Int32
	first := &subscriptionTestBackend{events: make(chan Event, 64)}
	second := &subscriptionTestBackend{events: make(chan Event, 64)}
	s.factory = func(Config) (Backend, error) {
		if installs.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}
	ch, release, err := s.Acquire(Config{WorktreeRoot: t.TempDir()}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	wait := func(kind string) {
		t.Helper()
		select {
		case n := <-ch:
			if n.Kind != kind {
				t.Fatalf("got %s want %s", n.Kind, kind)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("missing notice")
		}
	}
	wait("ready")
	first.events <- Event{RebuildRequired: true}
	wait("lost")
	wait("ready")
	select {
	case n := <-ch:
		t.Fatalf("idle notification %+v", n)
	case <-time.After(250 * time.Millisecond):
	}
	if installs.Load() != 2 {
		t.Fatal("idle backend churn")
	}
	release()
	if !first.closed.Load() || !second.closed.Load() {
		t.Fatal("backend ownership leaked")
	}
}

type subscriptionTestBackend struct {
	events chan Event
	closed atomic.Bool
}

func (b *subscriptionTestBackend) Events() <-chan Event     { return b.events }
func (b *subscriptionTestBackend) Diagnostics() Diagnostics { return Diagnostics{} }
func (b *subscriptionTestBackend) Close() error             { b.closed.Store(true); return nil }
