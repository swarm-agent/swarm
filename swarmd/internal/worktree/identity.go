package worktree

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// RepositoryIdentity resolves Git authority, not path ancestry or a catalog label.
// It deliberately requires a committed non-bare checkout at its exact root.
func RepositoryIdentity(path string) (string, error) {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	top, err := runGit(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top, err = filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil {
		return "", err
	}
	if filepath.Clean(top) != filepath.Clean(path) {
		return "", errors.New("workspace must identify the exact Git checkout root")
	}
	if _, err = runGit(path, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return "", err
	}
	common, err := runGit(path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common, err = resolveGitPath(path, common)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(common)
}

// ValidateOwnedIdentity never repairs provenance from a path or a display name.
// Dirty work is valid for execution; cleanliness is a separate transition gate.
func ValidateOwnedIdentity(source, lane, branch, base string) error {
	sourceID, err := RepositoryIdentity(source)
	if err != nil {
		return fmt.Errorf("source repository identity: %w", err)
	}
	laneID, err := RepositoryIdentity(lane)
	if err != nil {
		return fmt.Errorf("owned repository identity: %w", err)
	}
	sourcePath, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	lanePath, err := filepath.EvalSymlinks(lane)
	if err != nil {
		return err
	}
	if sourceID != laneID || filepath.Clean(sourcePath) == filepath.Clean(lanePath) {
		return errors.New("session repository identity mismatch: source and isolated lane must share Git authority without aliasing")
	}
	actual, err := currentBranch(lane)
	if err != nil {
		return err
	}
	if branch == "" || strings.TrimSpace(actual) != branch {
		return errors.New("session worktree branch identity is stale")
	}
	if strings.TrimSpace(base) == "" {
		return errors.New("session worktree base identity is missing")
	}
	if _, err := runGit(lane, "merge-base", "--is-ancestor", base, "HEAD"); err != nil {
		return errors.New("session worktree base is not an ancestor of HEAD")
	}
	return nil
}
