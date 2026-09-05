package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/taskscope"
)

var taskSparseContextNames = []string{
	"AGENTS.md",
	"README",
	"README.*",
	"go.mod",
	"go.sum",
	"go.work",
	"go.work.sum",
	"package.json",
	"package-lock.json",
	"pnpm-lock.yaml",
	"yarn.lock",
	"bun.lockb",
	"tsconfig.json",
	"jsconfig.json",
	"Cargo.toml",
	"Cargo.lock",
	"pyproject.toml",
	"poetry.lock",
	"uv.lock",
	"requirements*.txt",
	"Makefile",
	"Justfile",
	"Taskfile.yml",
	"WORKSPACE",
	"WORKSPACE.bazel",
	"MODULE.bazel",
	"BUILD",
	"BUILD.bazel",
	".gitignore",
	".gitattributes",
	".editorconfig",
}

func prepareTaskWorktreeCheckout(worktreePath string, ownedScopes []string) error {
	scopes, wholeWorktree, err := canonicalTaskSparseScopes(ownedScopes)
	if err != nil {
		return err
	}
	if wholeWorktree {
		if _, err := runGitAllocation(worktreePath, nil, "checkout", "--force"); err != nil {
			return fmt.Errorf("materialize whole task worktree: %w", err)
		}
		return nil
	}
	patterns := taskSparseCheckoutPatterns(scopes)
	stdin := []byte(strings.Join(patterns, "\n") + "\n")
	if _, err := runGitAllocation(worktreePath, stdin, "sparse-checkout", "set", "--no-cone", "--stdin"); err != nil {
		return fmt.Errorf("configure sparse checkout: %w", err)
	}
	if _, err := runGitAllocation(worktreePath, nil, "checkout", "--force"); err != nil {
		return fmt.Errorf("materialize sparse task worktree: %w", err)
	}
	return nil
}

func canonicalTaskSparseScopes(rawScopes []string) ([]string, bool, error) {
	if len(rawScopes) == 0 {
		return nil, true, nil
	}
	seen := make(map[string]struct{}, len(rawScopes))
	scopes := make([]string, 0, len(rawScopes))
	wholeWorktree := false
	for _, raw := range rawScopes {
		clean, whole, err := taskscope.Canonical(raw)
		if err != nil {
			return nil, false, err
		}
		if whole {
			wholeWorktree = true
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		scopes = append(scopes, clean)
	}
	if wholeWorktree {
		return nil, true, nil
	}
	if len(scopes) == 0 {
		return nil, false, errors.New("at least one owned scope is required")
	}
	sort.Strings(scopes)
	return scopes, false, nil
}

func taskSparseCheckoutPatterns(scopes []string) []string {
	patterns := make([]string, 0, len(scopes)+len(taskSparseContextNames)*4+2)
	seen := map[string]struct{}{}
	appendPattern := func(pattern string) {
		if _, ok := seen[pattern]; ok {
			return
		}
		seen[pattern] = struct{}{}
		patterns = append(patterns, pattern)
	}
	appendContext := func(dir string) {
		prefix := "/"
		if dir != "" {
			prefix += strings.Trim(dir, "/") + "/"
		}
		for _, name := range taskSparseContextNames {
			appendPattern(prefix + name)
		}
		if dir == "" {
			appendPattern("/.agents/")
		}
	}
	appendContext("")
	for _, scope := range scopes {
		appendPattern("/" + scope)
		parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(scope)))
		if parent == "." {
			continue
		}
		parts := strings.Split(parent, "/")
		for i := 1; i <= len(parts); i++ {
			appendContext(strings.Join(parts[:i], "/"))
		}
	}
	return patterns
}

func runGitAllocation(path string, stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), worktreeAllocationTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		if output == "" {
			output = strings.TrimSpace(err.Error())
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), output)
	}
	return output, nil
}
