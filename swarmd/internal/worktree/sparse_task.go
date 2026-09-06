package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

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
