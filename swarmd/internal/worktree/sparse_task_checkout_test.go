package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTaskCheckoutMaterializesAllSourceWithoutWritingGitConfig(t *testing.T) {
	// Purpose:
	// - Requirement: task checkout includes dependencies outside mutation scope,
	//   while shared repository configuration remains untouched.
	// - Threat/regression: sparse-checkout set writes extensions.worktreeConfig
	//   and fails against sandbox-protected .git/config before Coder can start.
	// - Authority: prepareTaskWorktreeCheckout via routed runGitAllocation.
	// - Narrowest layer: real Git in temporary repositories, with a config lock
	//   rejecting writes; inherited sparsity and invalid-scope failures included.
	for _, sparse := range []bool{false, true} {
		name := "plain"
		if sparse {
			name = "inherited-sparse"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			wt := filepath.Join(root, "child")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			git := func(path string, args ...string) string {
				t.Helper()
				out, err := runGitAllocation(path, nil, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			git(repo, "init", "-b", "main")
			git(repo, "config", "user.name", "Test")
			git(repo, "config", "user.email", "test@example.invalid")
			for _, f := range []string{"owned.mjs", "dependency.mjs"} {
				if err := os.WriteFile(filepath.Join(repo, f), []byte("export const value = 1;\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git(repo, "add", "owned.mjs", "dependency.mjs")
			git(repo, "commit", "-m", "fixture")
			if sparse {
				git(repo, "sparse-checkout", "set", "--no-cone", "/owned.mjs")
			}
			git(repo, "worktree", "add", "--no-checkout", "-b", "child", wt)
			config := filepath.Join(repo, ".git", "config")
			before, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config+".lock", []byte("protected config\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := prepareTaskWorktreeCheckout(wt, []string{"../escape"}); err == nil {
				t.Fatal("invalid ownership scope accepted")
			}
			if _, err := os.Stat(filepath.Join(wt, "owned.mjs")); !os.IsNotExist(err) {
				t.Fatal("invalid allocation materialized files")
			}
			if err := prepareTaskWorktreeCheckout(wt, []string{"owned.mjs"}); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"owned.mjs", "dependency.mjs"} {
				data, err := os.ReadFile(filepath.Join(wt, f))
				if err != nil || string(data) != "export const value = 1;\n" {
					t.Fatalf("committed source %s missing: %v", f, err)
				}
			}
			after, err := os.ReadFile(config)
			if err != nil || string(before) != string(after) {
				t.Fatal("shared config changed")
			}
			if status := git(wt, "status", "--porcelain"); status != "" {
				t.Fatalf("new task checkout dirty: %s", status)
			}
			if head := git(wt, "rev-parse", "HEAD"); head != git(repo, "rev-parse", "HEAD") {
				t.Fatal("checkout changed revision")
			}
		})
	}
}
