package publish

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Queue.Process must refresh and rebase before Land receipts, and
// conflicting source must return to its worker without pushing. Temp Git repos
// and an owner-controlled fake command prove the process/queue boundary, not
// production sandbox confinement, provider follow-ups or GitHub branch rules.
func TestQueueGitCommand(t *testing.T) {
	allowOwner = os.Getuid()
	t.Cleanup(func() { allowOwner = -1 })
	for _, tc := range []struct {
		name     string
		moves    int
		conflict bool
		want     string
	}{
		{"clean", 0, false, "landed"}, {"moved", 1, false, "landed"}, {"moving", 3, false, "land_failed"}, {"conflict", 0, true, "sent_back"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ctx := context.Background()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-b", "main")
			git("config", "user.name", "Test")
			git("config", "user.email", "test@example.invalid")
			writeFile(t, filepath.Join(dir, "file"), "base\n", 0600)
			git("add", "file")
			git("commit", "-m", "base")
			git("checkout", "-b", "job")
			writeFile(t, filepath.Join(dir, "file"), "job\n", 0600)
			git("commit", "-am", "job")
			head := git("rev-parse", "HEAD")
			git("checkout", "main")
			if tc.conflict {
				writeFile(t, filepath.Join(dir, "file"), "main\n", 0600)
				git("commit", "-am", "main")
			}
			base := git("rev-parse", "HEAD")
			git("checkout", "job")
			commandDir := t.TempDir()
			commandPath := filepath.Join(commandDir, "command")
			counter := filepath.Join(commandDir, "counter")
			writeFile(t, commandPath, "#!/bin/sh\ncase \"$SWARM_PUBLISH_ACTION\" in\nupdate) echo '"+base+" main';;\nland) n=0; test ! -f '"+counter+"' || n=$(cat '"+counter+"'); n=$((n+1)); echo $n > '"+counter+"'; if test $n -le "+fmt.Sprint(tc.moves)+"; then echo MOVED; else echo \"LANDED $SWARM_PUBLISH_HEAD\"; fi;;\nesac\n", 0755)
			rule := Rule{Folder: dir, Command: commandPath, Mode: "land"}
			q := NewQueue(queueMemory{})
			_, err := q.Enqueue(Entry{Account: "a", Request: Request{Folder: dir, JobID: "job", Branch: "job", Head: head, BaseBranch: "main"}})
			if err != nil {
				t.Fatal(err)
			}
			backs := 0
			err = q.Process(ctx, "a", dir, QueueHooks{Prepare: func(ctx context.Context, e *Entry) error {
				b, err := Update(ctx, rule, "main")
				if err != nil {
					return err
				}
				cmd := exec.CommandContext(ctx, "git", "rebase", b.Commit)
				cmd.Dir = dir
				if _, err := cmd.CombinedOutput(); err != nil {
					files := git("diff", "--name-only", "--diff-filter=U")
					git("rebase", "--abort")
					return &SendBackError{Reason: "Rebase conflict: " + files}
				}
				e.Request.Head = git("rev-parse", "HEAD")
				e.Request.BaseCommit = b.Commit
				e.Request.BaseBranch = b.Branch
				return nil
			}, Land: func(ctx context.Context, e Entry) (LandResult, error) { return Land(ctx, rule, e.Request) }, SendBack: func(context.Context, Entry) error { backs++; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			entry, _, err := q.Get("a", "job")
			if err != nil || entry.State != tc.want {
				t.Fatalf("%+v %v", entry, err)
			}
			if tc.conflict {
				if backs != 1 || !strings.Contains(entry.Reason, "file") {
					t.Fatalf("%+v backs=%d", entry, backs)
				}
				if git("rev-parse", "HEAD") != head {
					t.Fatal("conflict changed source head")
				}
			} else if entry.State == "landed" && entry.Commit != git("rev-parse", "HEAD") {
				t.Fatal("wrong landed head")
			}
		})
	}
}
