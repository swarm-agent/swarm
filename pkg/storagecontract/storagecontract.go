package storagecontract

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const appDirName = "swarmd"

type RootKind string

const (
	RootData    RootKind = "data"
	RootCache   RootKind = "cache"
	RootRuntime RootKind = "runtime"
	RootConfig  RootKind = "config"
	RootLogs    RootKind = "logs"
)

type Roots struct {
	DataDir    string
	CacheDir   string
	RuntimeDir string
	ConfigDir  string
	LogsDir    string
}

type Options struct {
	// GOOS selects the platform contract. Empty means runtime.GOOS.
	GOOS string
	// Env supplies environment values for deterministic tests. Nil means os.Getenv.
	Env map[string]string
	// HomeDir supplies the user home to reject. Empty falls back to HOME when available.
	HomeDir string
	// WorkspaceRoots are repository/workspace roots that must never contain Swarm-owned defaults.
	WorkspaceRoots []string
	// ForbiddenRoots are additional roots rejected by validation.
	ForbiddenRoots []string
	// OverrideRoots are explicit test/dev roots. They are validated with the same forbidden-root rules.
	OverrideRoots map[RootKind]string
}

func ResolveRoots(opts Options) (Roots, error) {
	dataDir, err := ResolveRoot(RootData, opts)
	if err != nil {
		return Roots{}, err
	}
	cacheDir, err := ResolveRoot(RootCache, opts)
	if err != nil {
		return Roots{}, err
	}
	runtimeDir, err := ResolveRoot(RootRuntime, opts)
	if err != nil {
		return Roots{}, err
	}
	configDir, err := ResolveRoot(RootConfig, opts)
	if err != nil {
		return Roots{}, err
	}
	logsDir, err := ResolveRoot(RootLogs, opts)
	if err != nil {
		return Roots{}, err
	}
	return Roots{DataDir: dataDir, CacheDir: cacheDir, RuntimeDir: runtimeDir, ConfigDir: configDir, LogsDir: logsDir}, nil
}

func ResolveRoot(kind RootKind, opts Options) (string, error) {
	if err := validateKind(kind); err != nil {
		return "", err
	}
	goos := opts.GOOS
	if strings.TrimSpace(goos) == "" {
		goos = runtime.GOOS
	}

	root := strings.TrimSpace(opts.OverrideRoots[kind])
	if root == "" && goos == "linux" {
		if envName := systemdEnvName(kind); envName != "" {
			value := strings.TrimSpace(getenv(opts, envName))
			if value != "" {
				parsed, err := parseSystemdDirectoryEnv(envName, value)
				if err != nil {
					return "", err
				}
				root = parsed
			}
		}
	}
	if root == "" {
		var err error
		root, err = defaultRoot(goos, kind)
		if err != nil {
			return "", err
		}
	}
	if err := ValidateRoot(root, opts); err != nil {
		return "", fmt.Errorf("invalid %s root %q: %w", kind, root, err)
	}
	return filepath.Clean(root), nil
}

func (r Roots) Root(kind RootKind) (string, error) {
	switch kind {
	case RootData:
		return r.DataDir, nil
	case RootCache:
		return r.CacheDir, nil
	case RootRuntime:
		return r.RuntimeDir, nil
	case RootConfig:
		return r.ConfigDir, nil
	case RootLogs:
		return r.LogsDir, nil
	default:
		return "", fmt.Errorf("unknown storage root kind %q", kind)
	}
}

func ValidateRoot(path string, opts Options) error {
	raw := strings.TrimSpace(path)
	if raw == "" {
		return errors.New("path is required")
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		return errors.New("home-relative paths are forbidden")
	}
	if !filepath.IsAbs(raw) {
		return errors.New("relative paths are forbidden")
	}
	if containsParentElement(raw) {
		return errors.New("parent directory traversal is forbidden")
	}

	cleaned := filepath.Clean(raw)
	if isFilesystemRoot(cleaned) {
		return errors.New("filesystem root is forbidden")
	}

	forbidden, err := forbiddenRoots(opts)
	if err != nil {
		return err
	}
	if matched, root := pathWithinAnyRoot(cleaned, forbidden); matched {
		return fmt.Errorf("path is under forbidden root %q", root)
	}

	resolved, err := resolveExistingPrefix(cleaned)
	if err != nil {
		return fmt.Errorf("resolve existing path prefix: %w", err)
	}
	if resolved != cleaned {
		if matched, root := pathWithinAnyRoot(resolved, forbidden); matched {
			return fmt.Errorf("symlink-resolved path is under forbidden root %q", root)
		}
	}
	return nil
}

func Join(root string, parts ...string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return "", errors.New("root path is required")
	}
	joined := root
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if filepath.IsAbs(part) {
			return "", fmt.Errorf("path part %q must be relative", part)
		}
		if containsParentElement(part) {
			return "", fmt.Errorf("path part %q must not contain parent traversal", part)
		}
		joined = filepath.Join(joined, filepath.Clean(part))
	}
	if !pathWithinRoot(root, joined) {
		return "", fmt.Errorf("joined path %q escapes root %q", joined, root)
	}
	return joined, nil
}

func validateKind(kind RootKind) error {
	switch kind {
	case RootData, RootCache, RootRuntime, RootConfig, RootLogs:
		return nil
	default:
		return fmt.Errorf("unknown storage root kind %q", kind)
	}
}

func defaultRoot(goos string, kind RootKind) (string, error) {
	switch goos {
	case "linux":
		switch kind {
		case RootData:
			return "/var/lib/" + appDirName, nil
		case RootCache:
			return "/var/cache/" + appDirName, nil
		case RootRuntime:
			return "/run/" + appDirName, nil
		case RootConfig:
			return "/etc/" + appDirName, nil
		case RootLogs:
			return "/var/log/" + appDirName, nil
		}
	case "darwin":
		switch kind {
		case RootData:
			return "/Library/Application Support/Swarm/" + appDirName, nil
		case RootCache:
			return "/Library/Caches/Swarm/" + appDirName, nil
		case RootRuntime:
			return "/var/run/" + appDirName, nil
		case RootConfig:
			return "/Library/Application Support/Swarm/" + appDirName + "/config", nil
		case RootLogs:
			return "/Library/Logs/Swarm/" + appDirName, nil
		}
	default:
		return "", fmt.Errorf("unsupported storage platform %q", goos)
	}
	return "", fmt.Errorf("unknown storage root kind %q", kind)
}

func systemdEnvName(kind RootKind) string {
	switch kind {
	case RootData:
		return "STATE_DIRECTORY"
	case RootCache:
		return "CACHE_DIRECTORY"
	case RootRuntime:
		return "RUNTIME_DIRECTORY"
	case RootConfig:
		return "CONFIGURATION_DIRECTORY"
	case RootLogs:
		return "LOGS_DIRECTORY"
	default:
		return ""
	}
}

func parseSystemdDirectoryEnv(name, value string) (string, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 1 {
		return "", fmt.Errorf("%s must contain exactly one directory", name)
	}
	part := strings.TrimSpace(parts[0])
	if part == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return part, nil
}

func getenv(opts Options, name string) string {
	if opts.Env != nil {
		return opts.Env[name]
	}
	return os.Getenv(name)
}

func forbiddenRoots(opts Options) ([]string, error) {
	roots := make([]string, 0, 12+len(opts.WorkspaceRoots)+len(opts.ForbiddenRoots))
	add := func(path string) error {
		path = strings.TrimSpace(path)
		if path == "" || path == "~" || strings.HasPrefix(path, "~/") {
			return nil
		}
		if !filepath.IsAbs(path) {
			abs, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolve forbidden root %q: %w", path, err)
			}
			path = abs
		}
		path = filepath.Clean(path)
		if isFilesystemRoot(path) {
			return nil
		}
		roots = append(roots, path)
		return nil
	}

	home := strings.TrimSpace(opts.HomeDir)
	if home == "" {
		home = strings.TrimSpace(getenv(opts, "HOME"))
	}
	if home != "" {
		if err := add(home); err != nil {
			return nil, err
		}
		for _, rel := range []string{
			filepath.Join(".local", "share"),
			filepath.Join(".local", "state"),
			".cache",
			".config",
			"Library",
			"Desktop",
			"Documents",
			"Downloads",
		} {
			if err := add(filepath.Join(home, rel)); err != nil {
				return nil, err
			}
		}
	}

	for _, envName := range []string{"XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		if err := add(getenv(opts, envName)); err != nil {
			return nil, err
		}
	}
	for _, root := range opts.WorkspaceRoots {
		if err := add(root); err != nil {
			return nil, err
		}
	}
	for _, root := range opts.ForbiddenRoots {
		if err := add(root); err != nil {
			return nil, err
		}
	}
	return dedupeRoots(roots), nil
}

func dedupeRoots(roots []string) []string {
	seen := make(map[string]struct{}, len(roots))
	out := roots[:0]
	for _, root := range roots {
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

func pathWithinAnyRoot(path string, roots []string) (bool, string) {
	path = filepath.Clean(path)
	for _, root := range roots {
		if pathWithinRoot(root, path) {
			return true, root
		}
	}
	return false, ""
}

func pathWithinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func containsParentElement(path string) bool {
	for _, elem := range strings.Split(path, string(filepath.Separator)) {
		if elem == ".." {
			return true
		}
	}
	return false
}

func isFilesystemRoot(path string) bool {
	cleaned := filepath.Clean(path)
	return filepath.Dir(cleaned) == cleaned
}

func resolveExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	suffix := make([]string, 0)
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path), nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}
