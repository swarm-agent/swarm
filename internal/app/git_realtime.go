package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const gitWatcherDebounce = 150 * time.Millisecond

func newRepoGitWatcher(path string) (*repoGitWatcher, error) {
	target := normalizePath(path)
	if target == "" {
		return nil, errors.New("path is required")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	gitDir, commonDir, err := resolveGitWatchPaths(target)
	if err != nil {
		_ = watcher.Close()
		return nil, err
	}
	watchPaths := []string{gitDir}
	if commonDir != "" && !pathsEqual(commonDir, gitDir) {
		watchPaths = append(watchPaths, commonDir)
	}
	for _, root := range watchPaths {
		for _, candidate := range candidateGitWatchPaths(root) {
			if err := addWatchIfExists(watcher, candidate); err != nil {
				_ = watcher.Close()
				return nil, err
			}
		}
	}
	return &repoGitWatcher{
		path:     target,
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
		debounce: make(chan struct{}, 1),
		watcher:  watcher,
	}, nil
}

func (w *repoGitWatcher) run(refresh func()) {
	if w == nil {
		return
	}
	defer close(w.stopped)
	defer func() {
		if w.watcher != nil {
			_ = w.watcher.Close()
		}
	}()
	if refresh == nil {
		refresh = func() {}
	}
	refresh()
	var (
		timer  *time.Timer
		timerC <-chan time.Time
	)
	for {
		select {
		case <-w.stop:
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			return
		case <-w.debounce:
			if timer == nil {
				timer = time.NewTimer(gitWatcherDebounce)
				timerC = timer.C
				continue
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(gitWatcherDebounce)
			timerC = timer.C
		case <-timerC:
			refresh()
			timerC = nil
		case _, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.signalRefresh()
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.signalRefresh()
		}
	}
}

func (w *repoGitWatcher) signalRefresh() {
	if w == nil {
		return
	}
	select {
	case w.debounce <- struct{}{}:
	default:
	}
}

func (w *repoGitWatcher) stopWatching() {
	if w == nil {
		return
	}
	select {
	case <-w.stopped:
		return
	default:
	}
	close(w.stop)
	<-w.stopped
}

func resolveGitWatchPaths(path string) (string, string, error) {
	gitDirRaw, err := runGitPathQuery(path, "rev-parse", "--git-dir")
	if err != nil {
		return "", "", err
	}
	gitDir, err := resolveReportedGitPath(path, gitDirRaw)
	if err != nil {
		return "", "", err
	}
	commonDirRaw, err := runGitPathQuery(path, "rev-parse", "--git-common-dir")
	if err != nil {
		return gitDir, "", nil
	}
	commonDir, err := resolveReportedGitPath(path, commonDirRaw)
	if err != nil {
		return gitDir, "", nil
	}
	return gitDir, commonDir, nil
}

func resolveReportedGitPath(basePath, reportedPath string) (string, error) {
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

func runGitPathQuery(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
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

func candidateGitWatchPaths(gitDir string) []string {
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

func addWatchIfExists(watcher *fsnotify.Watcher, path string) error {
	if watcher == nil {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return watcher.Add(path)
}
