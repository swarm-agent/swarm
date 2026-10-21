package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Requirement: managed commits return only to the unchanged allocated child,
// with exact ancestry/content and owned scope. Threat: remote data overwrites
// local changes, widens scope, imports symlinks or lies about commit ancestry.
// ExportManagedCheckout/ImportManagedCheckout own this boundary; real Git in
// temporary repositories is the narrowest layer proving refs and file contents.
func TestManagedCheckoutHandoff(t *testing.T) {
	for _, tc := range []string{"pass", "dirty", "moved", "branch", "scope", "symlink", "unrelated", "corrupt", "hidden_scope", "ignored_collision"} {
		t.Run(tc, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			host := t.TempDir()
			remote := t.TempDir()
			git := func(root string, args ...string) string {
				t.Helper()
				out, err := runGit(root, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			write := func(root, name, content string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, root := range []string{host, remote} {
				git(root, "init", "-b", "child")
				git(root, "config", "user.name", "Fixture")
				git(root, "config", "user.email", "fixture@example.invalid")
			}
			write(host, "fix.txt", "broken\n")
			git(host, "add", "fix.txt")
			git(host, "commit", "-m", "base")
			base := git(host, "rev-parse", "HEAD")
			pack, err := ExportManagedCheckout(ctx, host, "child", base)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = managedGit(ctx, remote, pack, "unpack-objects"); err != nil {
				t.Fatal(err)
			}
			write(remote, ".git/shallow", base+"\n")
			git(remote, "checkout", "-B", "child", base)
			if got := git(remote, "rev-parse", "HEAD"); got != base {
				t.Fatal("base identity changed")
			}
			write(remote, "fix.txt", "fixed\n")
			git(remote, "add", "fix.txt")
			git(remote, "commit", "-m", "repair")
			if tc == "symlink" {
				if err := os.Symlink("/outside", filepath.Join(remote, "escape")); err != nil {
					t.Fatal(err)
				}
				git(remote, "add", "escape")
				git(remote, "commit", "-m", "bad link")
			}
			if tc == "unrelated" {
				git(remote, "checkout", "--orphan", "unrelated")
				git(remote, "commit", "-m", "unrelated")
			}
			if tc == "ignored_collision" {
				write(remote, "ignored.txt", "remote")
				git(remote, "add", "ignored.txt")
				git(remote, "commit", "-m", "new tracked file")
			}
			if tc == "hidden_scope" {
				write(remote, "hidden.txt", "unowned")
				git(remote, "add", "hidden.txt")
				git(remote, "commit", "-m", "hidden")
				git(remote, "rm", "hidden.txt")
				git(remote, "commit", "-m", "hide change")
			}
			head := git(remote, "rev-parse", "HEAD")
			revision := base + ".." + head
			if tc == "unrelated" {
				revision = head
			}
			objects, err := managedGit(ctx, remote, nil, "rev-list", "--objects", "--no-object-names", revision)
			if err != nil {
				t.Fatal(err)
			}
			result, err := managedGit(ctx, remote, objects, "pack-objects", "--stdout")
			if err != nil {
				t.Fatal(err)
			}
			scopes := []string{"fix.txt"}
			switch tc {
			case "dirty":
				write(host, "fix.txt", "user change\n")
			case "moved":
				write(host, "extra", "local\n")
				git(host, "add", "extra")
				git(host, "commit", "-m", "local")
			case "branch":
				git(host, "checkout", "-b", "other")
			case "scope":
				scopes = []string{"elsewhere"}
			case "ignored_collision":
				scopes = []string{"."}
				write(host, ".git/info/exclude", "ignored.txt\n")
				write(host, "ignored.txt", "local ignored content")
			case "corrupt":
				result = []byte("not a pack")
			}
			before := git(host, "rev-parse", "HEAD")
			content, _ := os.ReadFile(filepath.Join(host, "fix.txt"))
			err = ImportManagedCheckout(ctx, host, "child", base, head, result, scopes, func() error { return ctx.Err() })
			if tc == "pass" {
				if err != nil {
					t.Fatal(err)
				}
				if got := git(host, "rev-parse", "HEAD"); got != head {
					t.Fatal("commit identity changed")
				}
				got, _ := os.ReadFile(filepath.Join(host, "fix.txt"))
				if string(got) != "fixed\n" {
					t.Fatal("wrong files")
				}
				if err = ImportManagedCheckout(ctx, host, "child", base, head, result, scopes, func() error { return ctx.Err() }); err != nil {
					t.Fatal("retry", err)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe import accepted")
				}
				if git(host, "rev-parse", "HEAD") != before {
					t.Fatal("rejected import moved HEAD")
				}
				got, _ := os.ReadFile(filepath.Join(host, "fix.txt"))
				if tc == "ignored_collision" {
					ignored, _ := os.ReadFile(filepath.Join(host, "ignored.txt"))
					if string(ignored) != "local ignored content" {
						t.Fatal("ignored file overwritten")
					}
				}
				if string(got) != string(content) {
					t.Fatal("rejected import overwrote files")
				}
			}
		})
	}
}

// Requirement: preparation copies the exact committed tree without credentials
// or linked paths. ExportManagedCheckout is tested with real Git so rejection
// does not rely on a mocked path filter or only on response text.
func TestManagedCheckoutExportRejectsUnsafeSource(t *testing.T) {
	for _, name := range []string{".env", "secret.key", "symlink"} {
		t.Run(strings.ReplaceAll(name, ".", "_"), func(t *testing.T) {
			root := t.TempDir()
			for _, args := range [][]string{{"init", "-b", "child"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.invalid"}} {
				if _, err := runGit(root, args...); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if name == "symlink" {
				err = os.Symlink("/outside", filepath.Join(root, name))
			} else {
				err = os.WriteFile(filepath.Join(root, name), []byte("test fixture only"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = runGit(root, "add", name); err != nil {
				t.Fatal(err)
			}
			if _, err = runGit(root, "commit", "-m", "base"); err != nil {
				t.Fatal(err)
			}
			head, _ := runGit(root, "rev-parse", "HEAD")
			if pack, err := ExportManagedCheckout(context.Background(), root, "child", head); err == nil || len(pack) != 0 {
				t.Fatal("unsafe source exported")
			}
		})
	}
}
