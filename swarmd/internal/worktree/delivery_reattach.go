package worktree

import (
	"context"
	"errors"
	"fmt"
	"swarm/packages/swarmd/internal/gitstatus"
)

// ReattachDeliveryLane is mutation-only recovery for an authenticated delivery.
// Assessment reads deliberately never call it. It does not change the index,
// files or branch tip. A dirty lane or other mismatch returns no warning and
// no error, leaving the caller's original delivery assessment authoritative.
func ReattachDeliveryLane(ctx context.Context, path, branch, targetPath, targetBranch string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, deliveryBudgetFor())
	defer cancel()
	integrationLock, err := acquireIntegrationLock(path)
	if err != nil {
		return "", err
	}
	defer integrationLock.Release()
	d := &deliveryInspector{ctx: ctx, budget: deliveryBudget{remaining: 1 << 20}}
	if _, err := d.run(path, "check-ref-format", "refs/heads/"+branch); err != nil {
		return "", err
	}
	_, targetRepo, err := d.lane(targetPath, targetBranch)
	if err != nil {
		if errors.Is(err, errLaneMoved) {
			return "", nil
		}
		return "", err
	}
	root, err := d.run(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if gitstatus.NormalizePath(root) != gitstatus.NormalizePath(path) {
		return "", nil
	}
	common, err := d.run(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if gitstatus.NormalizePath(common) != gitstatus.NormalizePath(targetRepo) {
		return "", nil
	}
	name, err := d.run(path, "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil {
		return "", err
	}
	if name == "refs/heads/"+branch {
		return "", nil
	}
	if name != "HEAD" {
		return "", nil
	}
	head, err := d.run(path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	tip, err := d.run(path, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil || tip != head {
		return "", nil
	}
	status, err := d.run(path, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", nil
	}
	if output, err := d.run(path, "symbolic-ref", "-m", "Swarm delivery: reattach detached branch tip", "HEAD", "refs/heads/"+branch); err != nil {
		return "", fmt.Errorf("reattach delivery HEAD: %s: %w", output, err)
	}
	verified, _, err := d.lane(path, branch)
	if err != nil || verified != head {
		return "", fmt.Errorf("delivery lane moved during reattachment")
	}
	return "Reattached detached HEAD to the unchanged delivery branch tip", nil
}
