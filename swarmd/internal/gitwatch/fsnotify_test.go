package gitwatch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

type fakeNative struct {
	mu       sync.Mutex
	added    []string
	failPath string
	events   chan fsnotify.Event
	errors   chan error
	closed   bool
}

func newFakeNative() *fakeNative {
	return &fakeNative{events: make(chan fsnotify.Event, 16), errors: make(chan error, 4)}
}

func (f *fakeNative) Add(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if filepath.Clean(path) == filepath.Clean(f.failPath) {
		return errors.New("injected watch failure")
	}
	f.added = append(f.added, filepath.Clean(path))
	return nil
}

func (f *fakeNative) Remove(string) error               { return nil }
func (f *fakeNative) WatchList() []string               { return nil }
func (f *fakeNative) EventsChan() <-chan fsnotify.Event { return f.events }
func (f *fakeNative) ErrorsChan() <-chan error          { return f.errors }
func (f *fakeNative) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func TestFSNotifyObservesWorktreeAndLinkedWorktreeMetadata(t *testing.T) {
	root := t.TempDir()
	mainWorktree := filepath.Join(root, "main")
	linkedWorktree := filepath.Join(root, "linked")
	mustMkdir(t, mainWorktree)
	runGitWatchCommand(t, mainWorktree, "init")
	runGitWatchCommand(t, mainWorktree, "config", "user.name", "Swarm Test")
	runGitWatchCommand(t, mainWorktree, "config", "user.email", "swarm-test@example.invalid")
	if err := os.WriteFile(filepath.Join(mainWorktree, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitWatchCommand(t, mainWorktree, "add", "tracked.txt")
	runGitWatchCommand(t, mainWorktree, "commit", "-m", "initial")
	runGitWatchCommand(t, mainWorktree, "worktree", "add", "-b", "topic", linkedWorktree)

	gitDir := strings.TrimSpace(runGitWatchCommand(t, linkedWorktree, "rev-parse", "--absolute-git-dir"))
	commonDir := strings.TrimSpace(runGitWatchCommand(t, linkedWorktree, "rev-parse", "--git-common-dir"))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(linkedWorktree, commonDir)
	}
	backend, err := NewFSNotify(Config{WorktreeRoot: linkedWorktree, GitDir: gitDir, CommonDir: commonDir})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	if err := os.WriteFile(filepath.Join(linkedWorktree, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeEvent := receiveEvent(t, backend.Events())
	if worktreeEvent.RebuildRequired || worktreeEvent.Scope != ScopeWorktree {
		t.Fatalf("unexpected worktree event: %+v", worktreeEvent)
	}

	runGitWatchCommand(t, linkedWorktree, "checkout", "-b", "topic-next")
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-backend.Events():
			if event.RebuildRequired {
				t.Fatalf("unexpected rebuild while observing linked metadata: %+v", event)
			}
			if event.Scope == ScopeMetadata {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for linked-worktree metadata event")
		}
	}
}

func TestSetupCrawlsWorktreeAndLinkedMetadataSeparately(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	gitDir := filepath.Join(root, "common", "worktrees", "topic")
	commonDir := filepath.Join(root, "common")
	mustMkdir(t, filepath.Join(worktree, "src", "nested"))
	mustMkdir(t, filepath.Join(commonDir, "refs", "heads"))
	mustMkdir(t, filepath.Join(commonDir, "logs", "refs"))
	mustMkdir(t, gitDir)
	fake := newFakeNative()
	backend, err := newWithNative(Config{WorktreeRoot: worktree, GitDir: gitDir, CommonDir: commonDir}, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	assertAdded(t, fake, filepath.Join(worktree, "src", "nested"))
	assertAdded(t, fake, gitDir)
	assertAdded(t, fake, filepath.Dir(gitDir))
	assertAdded(t, fake, filepath.Join(commonDir, "refs", "heads"))
	if backend.Diagnostics().WatchedDirs == 0 {
		t.Fatal("expected watched-directory diagnostics")
	}
}

func TestNewDirectoryIsWatchedBeforeInvalidation(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	gitDir := filepath.Join(root, "git")
	mustMkdir(t, worktree)
	mustMkdir(t, gitDir)
	fake := newFakeNative()
	backend, err := newWithNative(Config{WorktreeRoot: worktree, GitDir: gitDir, CommonDir: gitDir}, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	created := filepath.Join(worktree, "new", "nested")
	mustMkdir(t, created)
	fake.events <- fsnotify.Event{Name: filepath.Join(worktree, "new"), Op: fsnotify.Create}
	event := receiveEvent(t, backend.Events())
	if event.RebuildRequired || event.Scope != ScopeWorktree {
		t.Fatalf("unexpected event: %+v", event)
	}
	assertAdded(t, fake, created)
}

func TestAtomicMetadataReplacementInvalidates(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	gitDir := filepath.Join(root, "git")
	mustMkdir(t, worktree)
	mustMkdir(t, gitDir)
	fake := newFakeNative()
	backend, err := newWithNative(Config{WorktreeRoot: worktree, GitDir: gitDir, CommonDir: gitDir}, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	index := filepath.Join(gitDir, "index")
	fake.events <- fsnotify.Event{Name: index, Op: fsnotify.Rename}
	event := receiveEvent(t, backend.Events())
	if event.RebuildRequired || event.Scope != ScopeMetadata || event.Path != index {
		t.Fatalf("unexpected metadata invalidation: %+v", event)
	}
}

func TestOverflowAndChannelLossRequireRebuild(t *testing.T) {
	for _, test := range []struct {
		name string
		act  func(*fakeNative)
	}{
		{name: "overflow", act: func(fake *fakeNative) { fake.errors <- fsnotify.ErrEventOverflow }},
		{name: "event channel closure", act: func(fake *fakeNative) { close(fake.events) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			worktree := filepath.Join(root, "worktree")
			gitDir := filepath.Join(root, "git")
			mustMkdir(t, worktree)
			mustMkdir(t, gitDir)
			fake := newFakeNative()
			backend, err := newWithNative(Config{WorktreeRoot: worktree, GitDir: gitDir, CommonDir: gitDir}, fake)
			if err != nil {
				t.Fatal(err)
			}
			test.act(fake)
			event := receiveEvent(t, backend.Events())
			if !event.RebuildRequired || event.Err == nil {
				t.Fatalf("expected explicit rebuild requirement, got %+v", event)
			}
			diagnostics := backend.Diagnostics()
			if diagnostics.RebuildRequired != 1 {
				t.Fatalf("rebuild count = %d, want 1", diagnostics.RebuildRequired)
			}
			if test.name == "overflow" && diagnostics.Overflows != 1 {
				t.Fatalf("overflow count = %d, want 1", diagnostics.Overflows)
			}
			_ = backend.Close()
		})
	}
}

func TestWatchAddFailureFailsSetupVisibly(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	gitDir := filepath.Join(root, "git")
	mustMkdir(t, worktree)
	mustMkdir(t, gitDir)
	fake := newFakeNative()
	fake.failPath = worktree
	if _, err := newWithNative(Config{WorktreeRoot: worktree, GitDir: gitDir}, fake); err == nil {
		t.Fatal("expected setup failure")
	}
}

func runGitWatchCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertAdded(t *testing.T, fake *fakeNative, path string) {
	t.Helper()
	path = filepath.Clean(path)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, added := range fake.added {
		if added == path {
			return
		}
	}
	t.Fatalf("watch was not added for %q; added=%v", path, fake.added)
}

func receiveEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event channel closed")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watcher event")
		return Event{}
	}
}
