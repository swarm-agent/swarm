package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type managedImportCase struct {
	root, branch, base, head, content string
	pack                              []byte
}

func managedImportCases(t *testing.T, ctx context.Context, count int) (string, []managedImportCase) {
	t.Helper()
	git := func(root string, args ...string) string {
		t.Helper()
		out, err := managedGit(ctx, root, nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(root, path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repo := t.TempDir()
	git(repo, "init", "-b", "source")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "fixture@example.invalid")
	write(repo, "fix.txt", "broken\n")
	git(repo, "add", "fix.txt")
	git(repo, "commit", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	baseline, err := ExportManagedCheckout(ctx, repo, "source", base)
	if err != nil {
		t.Fatal(err)
	}
	cases := make([]managedImportCase, 0, count)
	for i := 0; i < count; i++ {
		branch := fmt.Sprintf("child-%d", i)
		root, remote := filepath.Join(t.TempDir(), "checkout"), t.TempDir()
		git(repo, "worktree", "add", "-b", branch, root, base)
		git(remote, "init", "-b", branch)
		git(remote, "config", "user.name", "Fixture")
		git(remote, "config", "user.email", "fixture@example.invalid")
		if _, err := managedGit(ctx, remote, baseline, "unpack-objects"); err != nil {
			t.Fatal(err)
		}
		write(remote, ".git/shallow", base+"\n")
		git(remote, "checkout", "-B", branch, base)
		content := fmt.Sprintf("fixed by child %d\n", i)
		write(remote, "fix.txt", content)
		git(remote, "add", "fix.txt")
		git(remote, "commit", "-m", "repair "+branch)
		head := git(remote, "rev-parse", "HEAD")
		objects, err := managedGit(ctx, remote, nil, "rev-list", "--objects", "--no-object-names", base+".."+head)
		if err != nil {
			t.Fatal(err)
		}
		pack, err := managedGit(ctx, remote, objects, "pack-objects", "--stdout")
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, managedImportCase{root, branch, base, head, content, pack})
	}
	return repo, cases
}

// Requirement: independent managed children sharing a Git common directory must
// import concurrently without losing a result to sibling lock contention.
// ImportManagedCheckout/acquireManagedImportLock retain the canonical repository
// lock and destination checks. Ten real temporary linked worktrees prove exact
// refs/content and unchanged source; this is hermetic Git testing, not AI telemetry.
func TestManagedCheckoutConcurrentImports(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo, cases := managedImportCases(t, ctx, 10)
	start, done := make(chan struct{}), make(chan error, len(cases))
	for _, c := range cases {
		go func(c managedImportCase) {
			<-start
			done <- ImportManagedCheckout(ctx, c.root, c.branch, c.base, c.head, c.pack, []string{"fix.txt"}, func() error { return ctx.Err() })
		}(c)
	}
	close(start)
	for range cases {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for _, c := range cases {
		if err := checkManagedDestination(ctx, c.root, c.branch, c.head); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(c.root, "fix.txt"))
		if err != nil || string(content) != c.content {
			t.Fatalf("wrong child result: %q %v", content, err)
		}
	}
	if err := checkManagedDestination(ctx, repo, "source", cases[0].base); err != nil {
		t.Fatal("source moved", err)
	}
	content, _ := os.ReadFile(filepath.Join(repo, "fix.txt"))
	if string(content) != "broken\n" {
		t.Fatal("source overwritten")
	}
}

// Requirement: waiting never steals an owner's lock or makes cancelled or
// revoked imports advance refs/files. The real lock, ImportManagedCheckout and
// its required authority fence are the narrowest boundaries for these failures.
func TestManagedCheckoutWaitingAndFenceFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	repo, cases := managedImportCases(t, ctx, 1)
	c := cases[0]
	owner, err := acquireIntegrationLock(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	lockPath := filepath.Join(repo, ".git", "swarm-integration.lock")
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	waiting, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	calls := 0
	err = ImportManagedCheckout(waiting, c.root, c.branch, c.base, c.head, c.pack, []string{"fix.txt"}, func() error { calls++; return nil })
	stop()
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
		t.Fatalf("cancelled waiter: %v, fence calls %d", err, calls)
	}
	after, err := os.ReadFile(lockPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("waiter changed owner's lock", err)
	}
	if err := checkManagedDestination(ctx, c.root, c.branch, c.base); err != nil {
		t.Fatal("cancelled waiter changed destination", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	revoked := errors.New("fixture receipt revoked")
	for _, revokeAt := range []int{1, 2} {
		calls = 0
		err = ImportManagedCheckout(ctx, c.root, c.branch, c.base, c.head, c.pack, []string{"fix.txt"}, func() error {
			calls++
			if calls == revokeAt {
				return revoked
			}
			return nil
		})
		if !errors.Is(err, revoked) || calls != revokeAt {
			t.Fatalf("fence %d: %v calls %d", revokeAt, err, calls)
		}
		if err := checkManagedDestination(ctx, c.root, c.branch, c.base); err != nil {
			t.Fatal("revoked import changed refs", err)
		}
		content, _ := os.ReadFile(filepath.Join(c.root, "fix.txt"))
		if string(content) != "broken\n" {
			t.Fatal("revoked import overwrote files")
		}
	}
	if err := ImportManagedCheckout(ctx, c.root, c.branch, c.base, c.head, c.pack, []string{"fix.txt"}, nil); err == nil {
		t.Fatal("missing authority fence accepted")
	}
	if err := ImportManagedCheckout(ctx, c.root, c.branch, c.base, c.head, c.pack, []string{"fix.txt"}, func() error { return ctx.Err() }); err != nil {
		t.Fatal("queue leaked after rejection", err)
	}
	if err := checkManagedDestination(ctx, c.root, c.branch, c.head); err != nil {
		t.Fatal(err)
	}
}
