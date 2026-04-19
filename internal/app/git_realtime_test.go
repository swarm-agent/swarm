package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
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
