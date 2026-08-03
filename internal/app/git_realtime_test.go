package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
)

func TestGitStatusForPathUsesNoOptionalLocks(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "hello\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "init")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "changed\n")

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "git-args.log")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up git: %v", err)
	}
	wrapper := "#!/bin/sh\nprintf '%s\n' \"$*\" >> \"" + logPath + "\"\nexec \"" + realGit + "\" \"$@\"\n"
	wrapperPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	status, ok := gitStatusForPath(repo)
	if !ok {
		t.Fatal("gitStatusForPath returned !ok")
	}
	if status.DirtyCount == 0 {
		t.Fatalf("dirty count = %d, want > 0", status.DirtyCount)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read git args log: %v", err)
	}
	want := "--no-optional-locks -C " + repo + " status --porcelain=v2 --branch"
	if !containsLine(string(logged), want) {
		t.Fatalf("git invocation missing no-optional-locks:\n%s", string(logged))
	}
}

func TestApplyGitStatusRefreshUpdatesV3ChatWithoutDirectoryEntry(t *testing.T) {
	repo := initGitRepo(t)
	runtime := v3chat.NewRuntime(nil, v3chat.NewStore(), nil)
	page := v3chat.NewPage(runtime, v3chat.PageStyles{})
	page.Runtime().Store().Dispatch(v3chat.HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "session-1", GitBranch: "main", GitHasGit: true},
	}})
	a := &App{
		activePath: repo,
		v3Chat:     page,
		homeModel:  model.HomeModel{CWD: repo},
	}

	changed := a.applyGitStatusRefresh(gitStatusRefreshResult{
		path: repo,
		ok:   true,
		status: gitRepoStatus{
			Branch:     "feature/live",
			DirtyCount: 2,
			HasGit:     true,
		},
	})
	if !changed {
		t.Fatal("applyGitStatusRefresh returned false for active V3 chat")
	}
	state := page.Runtime().Store().Snapshot()
	if state.Session.GitBranch != "feature/live" || !state.Session.GitHasGit || state.Session.GitDirtyCount != 2 {
		t.Fatalf("V3 Git header state = %#v", state.Session)
	}
}

func TestApplyGitStatusRefreshUpdatesHomeAndChat(t *testing.T) {
	repo := initGitRepo(t)
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:          "session-1",
			AuthConfigured:     true,
			SessionMode:        "auto",
			SwarmName:          "swarm",
			InitialPrompt:      "test",
			CommandSuggestions: nil,
			Meta:               ui.ChatSessionMeta{Branch: "main"},
		}),
		homeModel: model.HomeModel{
			CWD: repo,
			Directories: []model.DirectoryItem{{
				Name:         filepath.Base(repo),
				Path:         repo,
				ResolvedPath: repo,
				Branch:       "main",
				HasGit:       true,
			}},
		},
		activePath: repo,
	}

	changed := a.applyGitStatusRefresh(gitStatusRefreshResult{
		path: repo,
		ok:   true,
		status: gitRepoStatus{
			Branch:         "feature/live",
			DirtyCount:     3,
			StagedCount:    1,
			ModifiedCount:  1,
			UntrackedCount: 1,
			HasGit:         true,
		},
	})
	if !changed {
		t.Fatal("applyGitStatusRefresh returned false")
	}
	if got := a.homeModel.Directories[0].Branch; got != "feature/live" {
		t.Fatalf("home branch = %q, want feature/live", got)
	}
	if got := a.homeModel.Directories[0].DirtyCount; got != 3 {
		t.Fatalf("home dirty count = %d, want 3", got)
	}
	homeText := renderPageText(t, a.home)
	if !strings.Contains(homeText, "feature/live") {
		t.Fatalf("home render missing updated branch:\n%s", homeText)
	}
	if got := a.chat.ClipboardText(); !strings.Contains(got, "branch: feature/live") {
		t.Fatalf("chat clipboard missing updated branch:\n%s", got)
	}
}

func TestStartGitRealtimeWatcherBuildsOffCallerAndInstallsOnReady(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "hello\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "init")

	a := &App{
		activePath:      repo,
		gitStatusCh:     make(chan gitStatusRefreshResult, 8),
		gitWatcherReady: make(chan gitWatcherStartResult, 1),
	}
	a.startGitRealtimeWatcher(repo)
	if a.gitWatcher != nil {
		t.Fatal("watcher was installed synchronously")
	}

	select {
	case result := <-a.gitWatcherReady:
		a.gitWatcherReady <- result
	case <-time.After(3 * time.Second):
		t.Fatal("watcher construction did not finish")
	}
	if a.gitWatcher != nil {
		t.Fatal("watcher was installed before the ready result was consumed")
	}
	_ = a.consumeGitStatusRefreshResults()
	if a.gitWatcher == nil {
		t.Fatal("watcher was not installed after consuming the ready result")
	}
	defer a.stopGitRealtimeWatcher()
}

func TestRepoGitWatcherEmitsOnHeadChange(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "hello\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "init")

	watcher, err := newRepoGitWatcher(repo)
	if err != nil {
		t.Fatalf("newRepoGitWatcher: %v", err)
	}
	defer watcher.stopWatching()

	triggered := make(chan struct{}, 8)
	go watcher.run(func() {
		select {
		case triggered <- struct{}{}:
		default:
		}
	})

	awaitWatcherRefresh(t, triggered, "initial refresh")

	runGit(t, repo, "checkout", "-b", "feature/watch")

	awaitWatcherRefresh(t, triggered, "HEAD change")
}

func TestRepoGitWatcherEmitsOnWorkingTreeChange(t *testing.T) {
	repo := initGitRepo(t)
	tracked := filepath.Join(repo, "tracked.txt")
	writeFile(t, tracked, "hello\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "init")

	watcher, err := newRepoGitWatcher(repo)
	if err != nil {
		t.Fatalf("newRepoGitWatcher: %v", err)
	}
	defer watcher.stopWatching()

	triggered := make(chan struct{}, 8)
	go watcher.run(func() {
		select {
		case triggered <- struct{}{}:
		default:
		}
	})

	awaitWatcherRefresh(t, triggered, "initial refresh")
	writeFile(t, tracked, "changed\n")
	awaitWatcherRefresh(t, triggered, "working tree change")
}

func TestRepoGitWatcherRefreshesOpenV3HeaderAfterWorkingTreeChange(t *testing.T) {
	repo := initGitRepo(t)
	tracked := filepath.Join(repo, "tracked.txt")
	writeFile(t, tracked, "hello\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "init")

	runtime := v3chat.NewRuntime(nil, v3chat.NewStore(), nil)
	page := v3chat.NewPage(runtime, v3chat.PageStyles{})
	page.Runtime().Store().Dispatch(v3chat.HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "session-1", GitBranch: "main", GitHasGit: true},
	}})
	a := &App{activePath: repo, v3Chat: page, homeModel: model.HomeModel{CWD: repo}}

	watcher, err := newRepoGitWatcher(repo)
	if err != nil {
		t.Fatalf("newRepoGitWatcher: %v", err)
	}
	defer watcher.stopWatching()

	refreshed := make(chan int, 8)
	go watcher.run(func() {
		status, ok := gitStatusForPath(repo)
		a.applyGitStatusRefresh(gitStatusRefreshResult{path: repo, status: status, ok: ok})
		select {
		case refreshed <- page.Runtime().Store().Snapshot().Session.GitDirtyCount:
		default:
		}
	})

	awaitGitDirtyCount(t, refreshed, 0)
	writeFile(t, tracked, "changed\n")
	awaitGitDirtyCount(t, refreshed, 1)
	state := page.Runtime().Store().Snapshot()
	if state.Session.GitBranch != "main" || !state.Session.GitHasGit || state.Session.GitDirtyCount != 1 {
		t.Fatalf("open V3 header remained stale: %#v", state.Session)
	}
}

func TestRepoGitWatcherDoesNotRecursivelyWatchHomeRepo(t *testing.T) {
	repo := initGitRepo(t)
	nested := filepath.Join(repo, "project", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	writeFile(t, filepath.Join(nested, "file.txt"), "hello\n")

	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", repo)
	if oldHome != "" {
		t.Setenv("USERPROFILE", repo)
	}

	watcher, err := newRepoGitWatcher(repo)
	if err != nil {
		t.Fatalf("newRepoGitWatcher: %v", err)
	}
	defer watcher.stopWatching()

	if _, ok := watcher.watched[repo]; !ok {
		t.Fatalf("home repo root was not watched: %#v", watcher.watched)
	}
	if _, ok := watcher.watched[nested]; ok {
		t.Fatalf("home repo nested directory was watched: %#v", watcher.watched)
	}
	if len(watcher.watched) != 1 {
		t.Fatalf("watched %d paths, want only home root: %#v", len(watcher.watched), watcher.watched)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func containsLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func awaitWatcherRefresh(t *testing.T, triggered <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-triggered:
	case <-time.After(3 * time.Second):
		t.Fatalf("watcher did not refresh after %s", label)
	}
}

func awaitGitDirtyCount(t *testing.T, refreshed <-chan int, want int) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-refreshed:
			if got == want {
				return
			}
		case <-timer.C:
			t.Fatalf("Git header dirty count did not refresh to %d", want)
		}
	}
}
