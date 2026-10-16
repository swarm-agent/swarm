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
	"swarm/packages/swarmd/internal/sandbox"
	"time"

	"swarm/packages/swarmd/internal/taskscope"
)

func prepareTaskWorktreeCheckout(worktreePath string, ownedScopes []string) error {
	if _, _, err := canonicalTaskSparseScopes(ownedScopes); err != nil {
		return err
	}
	// Ownership constrains mutations, not the committed source a job may read.
	// Language dependencies and build inputs cannot be inferred from an owned
	// file list. Override inherited sparse configuration in this new worktree
	// only; tool mutation scopes and committed-diff handoff checks still apply.
	if _, err := runGitAllocation(worktreePath, []byte("/*\n"), "sparse-checkout", "set", "--no-cone", "--stdin"); err != nil {
		return fmt.Errorf("configure complete task checkout: %w", err)
	}
	if _, err := runGitAllocation(worktreePath, nil, "checkout", "--force"); err != nil {
		return fmt.Errorf("materialize committed task source: %w", err)
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

func runGitAllocation(path string, stdin []byte, args ...string) (string, error) {
	return runGitAllocationTimeout(path, stdin, worktreeAllocationTimeout, args...)
}

func runGitAllocationTimeout(path string, stdin []byte, timeout time.Duration, args ...string) (string, error) {
	return runGitAllocationGuarded(path, stdin, timeout, "", args...)
}

func runGitAllocationGuarded(path string, stdin []byte, timeout time.Duration, guard string, args ...string) (string, error) {
	return runGitAllocationGuardedContext(context.Background(), path, stdin, timeout, guard, args...)
}

func runGitAllocationGuardedContext(ctx context.Context, path string, stdin []byte, timeout time.Duration, guard string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	cmd.WaitDelay = time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	sandbox.Prepare(ctx, cmd)
	defer sandbox.ReleaseGit(cmd)
	waitForAllocationGit(cmd)
	if guard != "" {
		if err := recordReviewWriter(guard, cmd); err != nil {
			return "", fmt.Errorf("%w: %v", ErrAllocationWriterUnconfirmed, err)
		}
	}
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		if allocationGitRouted(cmd) && (errors.Is(err, exec.ErrWaitDelay) || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() < 0) {
			return "", fmt.Errorf("%w: %v", ErrAllocationWriterUnconfirmed, err)
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s (allocation timeout %s): %w: %s", strings.Join(args, " "), timeout, ctx.Err(), output)
		}
		if output == "" {
			output = strings.TrimSpace(err.Error())
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), output)
	}
	return output, nil
}

var ErrAllocationWriterUnconfirmed = errors.New("allocation writer termination unconfirmed; cleanup deferred")

func allocationGitRouted(cmd *exec.Cmd) bool {
	return filepath.Base(cmd.Path) != "git" && len(cmd.Args) > 1 && cmd.Args[1] == "exec"
}

func waitForAllocationGit(cmd *exec.Cmd) {
	if allocationGitRouted(cmd) {
		// The container timeout owns termination. Killing the engine client at
		// our deadline would return before its Git process has stopped writing.
		cmd.Cancel = func() error { return nil }
		cmd.WaitDelay = 5 * time.Second
	}
}
