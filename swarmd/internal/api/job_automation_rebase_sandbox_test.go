package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/sandbox"
)

// Requirement: the daemon's rebase works when daemon Git on the folder runs in
// the project's Bubblewrap sandbox (the headless default), which mounts only
// the folder, its Git directory and the system runtime and refuses a
// GIT_INDEX_FILE outside them: a replay that conflicts and its resumption
// with resolved files both complete through JobAutomationRebaseGit routed by
// an installed sandbox manager. Threat: every rebase with commits to replay
// failing (rebase_error) under the sandbox because it reads scratch files in
// daemon storage. Owners: JobAutomationRebaseGit, sandbox.TimedGitOutputInput,
// jobautomation.GitRebaser. Layer: actual routed Git; it skips where
// Bubblewrap isolation is unavailable (the jobautomation tests hold every
// rebase to the same reach rules without it).
func TestDaemonRebaseRunsThroughSandboxedGit(t *testing.T) {
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeRequired, Backend: sandbox.BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Active() {
		t.Skipf("actual bubblewrap isolation unavailable: %s", m.Status().Reason)
	}
	t.Cleanup(m.Close)
	folder := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = folder
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + folder, "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("f.txt", "a\nb\nc\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "agent/run-1")
	write("f.txt", "a\nJOB\nc\n")
	git("commit", "-q", "-am", "job")
	head := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	write("f.txt", "a\nDEV\nc\n")
	git("commit", "-q", "-am", "dev")
	tip := git("rev-parse", "HEAD")
	m.SetLayout(sandbox.Layout{Projects: func() ([]string, error) { return []string{folder}, nil }})
	prev := sandbox.Default()
	sandbox.SetDefault(m)
	t.Cleanup(func() { sandbox.SetDefault(prev) })

	g := jobautomation.GitRebaser{Git: JobAutomationRebaseGit}
	res, err := g.Replay(context.Background(), folder, head, tip)
	if err != nil || res.Status != jobautomation.RebaseConflicted || res.Conflict == nil {
		t.Fatalf("sandboxed replay: %+v %v", res, err)
	}
	resolved := t.TempDir()
	if err := os.WriteFile(filepath.Join(resolved, "f.txt"), []byte("a\nDEV JOB\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done, err := g.Resume(context.Background(), folder, *res.State, *res.Conflict, resolved)
	if err != nil || done.Status != jobautomation.RebaseRebased {
		t.Fatalf("sandboxed resume: %+v %v", done, err)
	}
	if got := git("show", done.To+":f.txt"); got != "a\nDEV JOB\nc" {
		t.Fatalf("f.txt = %q", got)
	}
	git("merge-base", "--is-ancestor", tip, done.To)
}
