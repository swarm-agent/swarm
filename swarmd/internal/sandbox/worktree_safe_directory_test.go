package sandbox

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: a task worktree of a folder the person owns has its Git directory
// in their repository, so daemon Git there (task changes, the merge
// assessment) needs that exact worktree as safe.directory: Git before 2.46
// refuses it as "dubious ownership" otherwise, and the folder and bucket
// entries do not cover it.
func TestWorktreeOfAPersonsFolderIsNamedSafe(t *testing.T) {
	m := newTestManager(t, ModeOff, readyRunner(), Config{StateDir: t.TempDir()})
	project, trees := t.TempDir(), t.TempDir()
	lane := filepath.Join(trees, "demo", "agent-lane")
	m.SetLayout(Layout{
		Projects:      func() ([]string, error) { return []string{project}, nil },
		WorktreesRoot: trees,
		Bucket:        func(string) (string, error) { return "demo", nil },
	})
	person := m.cfg.UID + 1
	m.owner = func(path string) (int, bool) {
		if path == project {
			return person, true
		}
		return m.cfg.UID, true
	}
	for target, want := range map[string]string{
		lane:                                 lane,
		filepath.Join(lane, "swarmd", "api"): lane,
		filepath.Join(trees, "demo"):         "",
		filepath.Join(trees, "other", "x"):   "",
		project:                              "",
	} {
		if got := m.worktreeSafeDirectory(target); got != want {
			t.Errorf("worktreeSafeDirectory(%q) = %q, want %q", target, got, want)
		}
	}

	cmd := exec.CommandContext(context.Background(), "git", "-C", lane, "rev-parse", "HEAD")
	if err := m.RouteGit(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	env := strings.Join(cmd.Env, "\n")
	if !strings.Contains(env, "=safe.directory") || !strings.Contains(env, "GIT_CONFIG_VALUE_") || !strings.Contains(env, "="+lane+"\n") {
		t.Fatalf("Git in the worktree lacks it as safe.directory:\n%s", env)
	}

	// The daemon's own folder: its worktrees need nothing.
	m.owner = func(string) (int, bool) { return m.cfg.UID, true }
	if got := m.worktreeSafeDirectory(lane); got != "" {
		t.Fatalf("own folder's worktree named safe: %q", got)
	}
}
