package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/provider"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
)

// Requirement: prepareLandWorktree must import a newly advanced folder base
// into an independent clone before rebasing. Real Git at this narrow executor
// boundary proves source HEAD remains untouched until explicit FF-only landing;
// a failed base fetch must leave both heads intact.
func TestPrepareLandIndependentClone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Fixture")
	t.Setenv("GIT_AUTHOR_EMAIL", "fixture@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Fixture")
	t.Setenv("GIT_COMMITTER_EMAIL", "fixture@example.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root := t.TempDir()
	source, clone := filepath.Join(root, "source"), filepath.Join(root, "clone")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(path string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = path
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(source, "init", "-b", "dev")
	git(source, "commit", "--allow-empty", "-m", "base")
	git(source, "clone", "--no-local", source, clone)
	git(clone, "checkout", "-b", "agent/job")
	if err := os.WriteFile(filepath.Join(clone, "job.txt"), []byte("job"), 0600); err != nil {
		t.Fatal(err)
	}
	git(clone, "add", "job.txt")
	git(clone, "commit", "-m", "job")
	jobHead := git(clone, "rev-parse", "HEAD")
	git(source, "commit", "--allow-empty", "-m", "advance")
	base := git(source, "rev-parse", "HEAD")
	e := &publish.Entry{Request: publish.Request{Branch: "agent/job", BaseBranch: "dev"}}
	execute := func(ctx context.Context, r sandbox.ExecRequest) (*provider.ExecResult, error) {
		cmd := exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
		cmd.Dir = r.WorkingDir
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				return nil, err
			}
		}
		return &provider.ExecResult{Stdout: string(out), ExitCode: code}, nil
	}
	update := func(context.Context, publish.Rule, string) (publish.Base, error) {
		return publish.Base{Commit: base, Branch: "dev"}, nil
	}
	if err := prepareLandWorktree(ctx, publish.Rule{Folder: filepath.Join(root, "missing")}, e, clone, nil, 1, execute, update); err == nil {
		t.Fatal("missing base repository accepted")
	}
	if git(clone, "rev-parse", "HEAD") != jobHead || git(source, "rev-parse", "HEAD") != base {
		t.Fatal("failed fetch changed heads")
	}
	if err := prepareLandWorktree(ctx, publish.Rule{Folder: source}, e, clone, nil, 1, execute, update); err != nil {
		t.Fatal(err)
	}
	if git(source, "rev-parse", "HEAD") != base {
		t.Fatal("preparation changed source")
	}
	git(source, "fetch", "--no-tags", clone, e.Request.Head)
	git(source, "merge", "--ff-only", e.Request.Head)
	if git(source, "rev-parse", "HEAD") != e.Request.Head {
		t.Fatal("landing did not fast-forward")
	}
	if data, err := os.ReadFile(filepath.Join(source, "job.txt")); err != nil || string(data) != "job" {
		t.Fatal("lost job content", err)
	}
}
