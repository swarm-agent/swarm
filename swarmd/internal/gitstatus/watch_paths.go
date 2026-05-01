package gitstatus

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

type WatchPaths struct {
	RepoRoot  string
	GitDir    string
	CommonDir string
}

func ResolveWatchPaths(ctx context.Context, path string) (WatchPaths, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		return WatchPaths{}, errors.New("workspace path is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repoRootRaw, err := gitOutputContext(ctx, target, "rev-parse", "--show-toplevel")
	if err != nil {
		return WatchPaths{}, err
	}
	repoRoot, err := resolveReportedPath(target, string(repoRootRaw))
	if err != nil {
		return WatchPaths{}, err
	}
	gitDirRaw, err := gitOutputContext(ctx, target, "rev-parse", "--git-dir")
	if err != nil {
		return WatchPaths{}, err
	}
	gitDir, err := resolveReportedPath(target, string(gitDirRaw))
	if err != nil {
		return WatchPaths{}, err
	}
	commonDirRaw, err := gitOutputContext(ctx, target, "rev-parse", "--git-common-dir")
	if err != nil {
		return WatchPaths{RepoRoot: repoRoot, GitDir: gitDir}, nil
	}
	commonDir, err := resolveReportedPath(target, string(commonDirRaw))
	if err != nil {
		return WatchPaths{RepoRoot: repoRoot, GitDir: gitDir}, nil
	}
	return WatchPaths{RepoRoot: repoRoot, GitDir: gitDir, CommonDir: commonDir}, nil
}

func resolveReportedPath(basePath, reportedPath string) (string, error) {
	basePath = strings.TrimSpace(basePath)
	reportedPath = strings.TrimSpace(reportedPath)
	if reportedPath == "" {
		return "", errors.New("git path is empty")
	}
	if !filepath.IsAbs(reportedPath) {
		if basePath == "" {
			return "", errors.New("base path is required for relative git path")
		}
		reportedPath = filepath.Join(basePath, reportedPath)
	}
	resolvedPath, err := filepath.Abs(reportedPath)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolvedPath), nil
}

func NormalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return trimmed
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

func WatchRootsForGitPaths(gitDir, commonDir string) []string {
	roots := []string{NormalizePath(gitDir)}
	common := NormalizePath(commonDir)
	if common != "" && common != roots[0] {
		roots = append(roots, common)
	}
	return roots
}

func CandidateGitWatchPaths(gitDir string) []string {
	candidates := []string{
		gitDir,
		filepath.Join(gitDir, "refs"),
		filepath.Join(gitDir, "refs", "heads"),
		filepath.Join(gitDir, "refs", "remotes"),
		filepath.Join(gitDir, "refs", "tags"),
		filepath.Join(gitDir, "logs"),
		filepath.Join(gitDir, "logs", "refs"),
		filepath.Join(gitDir, "logs", "refs", "heads"),
		filepath.Join(gitDir, "logs", "refs", "remotes"),
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		clean := filepath.Clean(strings.TrimSpace(candidate))
		if clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func RunGitPathQuery(ctx context.Context, path string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	commandArgs := make([]string, 0, len(args)+3)
	commandArgs = append(commandArgs, "--no-optional-locks", "-C", path)
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	raw, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
