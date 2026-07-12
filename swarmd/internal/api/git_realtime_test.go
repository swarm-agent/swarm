package api

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/gitwatch"
)

type fakeGitRealtimeBackend struct {
	events chan gitwatch.Event
	mu     sync.Mutex
	closed bool
}

func newFakeGitRealtimeBackend() *fakeGitRealtimeBackend {
	return &fakeGitRealtimeBackend{events: make(chan gitwatch.Event, 8)}
}

func (b *fakeGitRealtimeBackend) Events() <-chan gitwatch.Event { return b.events }
func (b *fakeGitRealtimeBackend) Diagnostics() gitwatch.Diagnostics {
	return gitwatch.Diagnostics{BackendKind: "fake", WatchedDirs: 3}
}
func (b *fakeGitRealtimeBackend) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

func TestGitRealtimeManagerDeduplicatesNormalizedWorktreeRoots(t *testing.T) {
	repo := initGitRealtimeTestRepo(t)
	manager := testGitRealtimeManager()
	created := 0
	manager.backendFactory = func(gitstatus.WatchPaths) (gitwatch.Backend, error) {
		created++
		return newFakeGitRealtimeBackend(), nil
	}
	first, err := manager.ensure(repo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.ensure(filepath.Join(repo, "."))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAll()
	if first != second || created != 1 || len(manager.repos) != 1 {
		t.Fatalf("deduplication failed: first=%p second=%p created=%d repos=%d", first, second, created, len(manager.repos))
	}
}

func TestGitRealtimeManagerRebuildsAfterOverflow(t *testing.T) {
	repo := initGitRealtimeTestRepo(t)
	manager := testGitRealtimeManager()
	first := newFakeGitRealtimeBackend()
	second := newFakeGitRealtimeBackend()
	created := 0
	manager.backendFactory = func(gitstatus.WatchPaths) (gitwatch.Backend, error) {
		created++
		if created == 1 {
			return first, nil
		}
		return second, nil
	}
	watch, err := manager.ensure(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAll()
	first.events <- gitwatch.Event{RebuildRequired: true, Err: errors.New("injected overflow")}
	waitForGitRealtime(t, func() bool {
		_, _, diagnostics := watch.current()
		return created >= 2 && diagnostics.RecrawlCount >= 1 && diagnostics.Backend == "fake"
	})
}

func TestGitRealtimeManagerFallsBackAndRecovers(t *testing.T) {
	repo := initGitRealtimeTestRepo(t)
	manager := testGitRealtimeManager()
	attempts := 0
	manager.backendFactory = func(gitstatus.WatchPaths) (gitwatch.Backend, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("injected setup failure")
		}
		return newFakeGitRealtimeBackend(), nil
	}
	watch, err := manager.ensure(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAll()
	waitForGitRealtime(t, func() bool {
		_, _, diagnostics := watch.current()
		return attempts >= 2 && diagnostics.Backend == "fake" && diagnostics.FallbackReason == ""
	})
}

func TestGitRealtimeManagerDebouncesBurstEvents(t *testing.T) {
	repo := initGitRealtimeTestRepo(t)
	manager := testGitRealtimeManager()
	backend := newFakeGitRealtimeBackend()
	manager.backendFactory = func(gitstatus.WatchPaths) (gitwatch.Backend, error) {
		return backend, nil
	}
	watch, err := manager.ensure(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAll()
	_, _, before := watch.current()
	backend.events <- gitwatch.Event{Scope: gitwatch.ScopeWorktree}
	backend.events <- gitwatch.Event{Scope: gitwatch.ScopeWorktree}
	backend.events <- gitwatch.Event{Scope: gitwatch.ScopeWorktree}
	waitForGitRealtime(t, func() bool {
		_, _, after := watch.current()
		return after.RefreshCount > before.RefreshCount
	})
	time.Sleep(25 * time.Millisecond)
	_, _, after := watch.current()
	if after.RefreshCount != before.RefreshCount+1 {
		t.Fatalf("burst produced %d refreshes, want 1", after.RefreshCount-before.RefreshCount)
	}
}

func TestGitRealtimeWaitForChangeHoldsUnchangedToken(t *testing.T) {
	repo := initGitRealtimeTestRepo(t)
	manager := testGitRealtimeManager()
	manager.backendFactory = func(gitstatus.WatchPaths) (gitwatch.Backend, error) {
		return newFakeGitRealtimeBackend(), nil
	}
	watch, err := manager.ensure(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAll()
	_, token, _ := watch.current()
	const wait = 40 * time.Millisecond
	started := time.Now()
	watch.waitForChange(context.Background(), token, wait)
	if elapsed := time.Since(started); elapsed < wait {
		t.Fatalf("unchanged token returned after %s, want at least %s", elapsed, wait)
	}
}

func TestGitRealtimeWaitForChangeReturnsOnSnapshotChange(t *testing.T) {
	repo := initGitRealtimeTestRepo(t)
	manager := testGitRealtimeManager()
	manager.backendFactory = func(gitstatus.WatchPaths) (gitwatch.Backend, error) {
		return newFakeGitRealtimeBackend(), nil
	}
	watch, err := manager.ensure(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAll()
	_, token, _ := watch.current()
	done := make(chan struct{})
	go func() {
		watch.waitForChange(context.Background(), token, time.Second)
		close(done)
	}()
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	watch.signalRefresh(gitwatch.ScopeWorktree)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cache-hit long poll did not return after snapshot changed")
	}
}

func TestGitRealtimeManagerReconcilesWithoutTreeRecrawl(t *testing.T) {
	repo := initGitRealtimeTestRepo(t)
	manager := testGitRealtimeManager()
	manager.runtime.reconcileMin = 10 * time.Millisecond
	manager.runtime.reconcileMax = 0
	manager.backendFactory = func(gitstatus.WatchPaths) (gitwatch.Backend, error) {
		return newFakeGitRealtimeBackend(), nil
	}
	watch, err := manager.ensure(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAll()
	_, _, before := watch.current()
	waitForGitRealtime(t, func() bool {
		_, _, after := watch.current()
		return after.RefreshCount > before.RefreshCount && after.RecrawlCount == 0
	})
}

func TestGitRealtimeManagerEvictsIdleLease(t *testing.T) {
	repo := initGitRealtimeTestRepo(t)
	manager := testGitRealtimeManager()
	manager.runtime.idleLease = 20 * time.Millisecond
	manager.backendFactory = func(gitstatus.WatchPaths) (gitwatch.Backend, error) {
		return newFakeGitRealtimeBackend(), nil
	}
	if _, err := manager.ensure(repo); err != nil {
		t.Fatal(err)
	}
	waitForGitRealtime(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(manager.repos) == 0
	})
}

func TestGitRealtimeNativeIdleDoesNoRecurringRefreshBeforeReconcile(t *testing.T) {
	repo := initGitRealtimeTestRepo(t)
	manager := testGitRealtimeManager()
	manager.runtime.reconcileMin = time.Hour
	manager.runtime.reconcileMax = 0
	manager.runtime.idleLease = time.Hour
	manager.backendFactory = func(gitstatus.WatchPaths) (gitwatch.Backend, error) {
		return newFakeGitRealtimeBackend(), nil
	}
	watch, err := manager.ensure(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAll()
	_, _, before := watch.current()
	time.Sleep(30 * time.Millisecond)
	_, _, after := watch.current()
	if after.RefreshCount != before.RefreshCount || after.RecrawlCount != before.RecrawlCount {
		t.Fatalf("idle native watcher performed work: before=%+v after=%+v", before, after)
	}
}

func testGitRealtimeManager() *gitRealtimeManager {
	manager := newGitRealtimeManager(nil)
	manager.runtime = gitRealtimeRuntimeConfig{
		debounce: 10 * time.Millisecond, maxDelay: 20 * time.Millisecond,
		idleLease: time.Second, reconcileMin: time.Hour, reconcileMax: 0,
		fallbackMin: 10 * time.Millisecond, fallbackMax: 20 * time.Millisecond,
	}
	return manager
}

func initGitRealtimeTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitRealtimeTestCommand(t, repo, "init")
	runGitRealtimeTestCommand(t, repo, "config", "user.name", "Swarm Test")
	runGitRealtimeTestCommand(t, repo, "config", "user.email", "swarm-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitRealtimeTestCommand(t, repo, "add", "tracked.txt")
	runGitRealtimeTestCommand(t, repo, "commit", "-m", "initial")
	return repo
}

func runGitRealtimeTestCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func waitForGitRealtime(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for Git realtime condition")
}
