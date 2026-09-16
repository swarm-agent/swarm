package worktree

import (
	"errors"
	"fmt"
	"io"
	"os"
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
	if err := validateOwnedIdentity(source, lane, branch, base, false); err != nil {
		return err
	}
	if _, err := runGit(lane, "merge-base", "--is-ancestor", base, "HEAD"); err != nil {
		return errors.New("session worktree base is not an ancestor of HEAD")
	}
	return nil
}

// ValidateOwnedExecutionIdentity checks the owned checkout, not the shape of its
// mutable history. A reset or rebase must not strand conversation in that lane.
// The recorded base remains immutable provenance; transitions and integration
// continue to use ValidateOwnedIdentity and their own history/cleanliness gates.
func ValidateOwnedExecutionIdentity(source, lane, branch, base string) error {
	return validateOwnedIdentity(source, lane, branch, base, true)
}

func validateOwnedIdentity(source, lane, branch, base string, allowRebase bool) error {
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
		if !allowRebase || actual != "" || branch == "" {
			return errors.New("session worktree branch identity is stale")
		}
		if err := validateRebaseBranch(lane, branch); err != nil {
			return fmt.Errorf("session worktree branch identity is stale: %w", err)
		}
	}
	if strings.TrimSpace(base) == "" {
		return errors.New("session worktree base identity is missing")
	}
	if _, err := runGit(lane, "rev-parse", "--verify", "--end-of-options", base+"^{commit}"); err != nil {
		return fmt.Errorf("session worktree base identity is invalid: %w", err)
	}
	return nil
}

// validateRebaseBranch recognizes Git's detached rebase state for execution only.
// Resolve the administrative directory through Git: a linked checkout's .git is
// a file, and another worktree's rebase state is never evidence for this lane.
// This is a read-only consistency check, not permission to repair Git metadata.
func validateRebaseBranch(lane, branch string) error {
	ref := "refs/heads/" + branch
	if _, err := runGit(lane, "check-ref-format", ref); err != nil {
		return errors.New("invalid recorded branch")
	}
	gitDir, err := runGit(lane, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	gitDir, err = filepath.EvalSymlinks(gitDir)
	if err != nil {
		return err
	}
	var state string
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		reported, err := runGit(lane, "rev-parse", "--git-path", name)
		if err != nil {
			return err
		}
		path, err := resolveGitPath(lane, reported)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !info.IsDir() || resolved != filepath.Join(gitDir, name) {
			return errors.New("invalid worktree rebase metadata path")
		}
		if state != "" {
			return errors.New("ambiguous worktree rebase metadata")
		}
		state = path
	}
	if state == "" {
		return errors.New("detached HEAD has no worktree rebase metadata")
	}
	headName, err := readRebaseIdentity(state, "head-name")
	if err != nil || headName != ref {
		return errors.New("rebase branch does not match recorded branch")
	}
	original, err := readRebaseIdentity(state, "orig-head")
	if err != nil {
		return err
	}
	onto, err := readRebaseIdentity(state, "onto")
	if err != nil {
		return err
	}
	// Require exact commit IDs, not revision expressions supplied in state files.
	for _, oid := range []string{original, onto} {
		resolved, err := runGit(lane, "rev-parse", "--verify", "--end-of-options", oid+"^{commit}")
		if err != nil || resolved != oid {
			return errors.New("invalid rebase commit identity")
		}
	}
	tip, err := runGit(lane, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil || tip != original {
		return errors.New("rebase original HEAD does not match recorded branch tip")
	}
	if _, err := runGit(lane, "merge-base", "--is-ancestor", onto, "HEAD"); err != nil {
		return errors.New("detached HEAD is outside the rebase destination history")
	}
	return nil
}

func readRebaseIdentity(state, name string) (string, error) {
	path := filepath.Join(state, name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("invalid rebase identity file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("rebase identity file changed during validation")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(string(data), "\n")
	if len(data) > 4096 || value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("invalid rebase identity value")
	}
	return value, nil
}
