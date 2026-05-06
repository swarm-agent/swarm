package appstorage

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PlatformRoots is the canonical OS-aware storage contract for the swarmd daemon.
// These roots are for daemon-owned runtime, state, cache, log, and config data;
// they intentionally do not default under HOME, XDG home directories, or ~/Library.
type PlatformRoots struct {
	DataDir    string
	CacheDir   string
	RuntimeDir string
	LogDir     string
	ConfigDir  string
}

type rootResolveOptions struct {
	GOOS           string
	Env            map[string]string
	CWD            string
	ForbiddenRoots []string
}

// DefaultRoots resolves the platform storage roots for the current process.
func DefaultRoots() (PlatformRoots, error) {
	return resolvePlatformRoots(rootResolveOptions{
		GOOS: runtime.GOOS,
		Env:  envMap(os.Environ()),
	})
}

func resolvePlatformRoots(opts rootResolveOptions) (PlatformRoots, error) {
	goos := strings.TrimSpace(opts.GOOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	env := opts.Env
	if env == nil {
		env = envMap(os.Environ())
	}

	var roots PlatformRoots
	switch goos {
	case "linux":
		roots = PlatformRoots{
			DataDir:    "/var/lib/swarmd",
			CacheDir:   "/var/cache/swarmd",
			RuntimeDir: "/run/swarmd",
			LogDir:     "/var/log/swarmd",
			ConfigDir:  "/etc/swarmd",
		}
		applyEnvRoot(env, "STATE_DIRECTORY", &roots.DataDir)
		applyEnvRoot(env, "CACHE_DIRECTORY", &roots.CacheDir)
		applyEnvRoot(env, "RUNTIME_DIRECTORY", &roots.RuntimeDir)
		applyEnvRoot(env, "LOGS_DIRECTORY", &roots.LogDir)
		applyEnvRoot(env, "CONFIGURATION_DIRECTORY", &roots.ConfigDir)
	case "darwin":
		roots = PlatformRoots{
			DataDir:    "/Library/Application Support/Swarm/swarmd",
			CacheDir:   "/Library/Caches/Swarm/swarmd",
			RuntimeDir: "/var/run/swarmd",
			LogDir:     "/Library/Logs/Swarm/swarmd",
			ConfigDir:  "/Library/Application Support/Swarm/swarmd/config",
		}
	default:
		return PlatformRoots{}, fmt.Errorf("unsupported platform %q for swarmd storage roots", goos)
	}

	roots, err := validatePlatformRoots(roots, forbiddenRoots(env, opts.ForbiddenRoots))
	if err != nil {
		return PlatformRoots{}, err
	}
	return roots, nil
}

// Validate verifies that all roots are absolute daemon-safe paths.
func (r PlatformRoots) Validate() error {
	_, err := validatePlatformRoots(r, forbiddenRoots(envMap(os.Environ()), nil))
	return err
}

func applyEnvRoot(env map[string]string, key string, dst *string) {
	if value := strings.TrimSpace(env[key]); value != "" {
		*dst = value
	}
}

func validatePlatformRoots(roots PlatformRoots, forbidden []string) (PlatformRoots, error) {
	checks := []struct {
		name string
		path string
	}{
		{name: "data", path: roots.DataDir},
		{name: "cache", path: roots.CacheDir},
		{name: "runtime", path: roots.RuntimeDir},
		{name: "log", path: roots.LogDir},
		{name: "config", path: roots.ConfigDir},
	}
	for _, check := range checks {
		path, err := validateRootPath(check.name, check.path, forbidden)
		if err != nil {
			return PlatformRoots{}, err
		}
		switch check.name {
		case "data":
			roots.DataDir = path
		case "cache":
			roots.CacheDir = path
		case "runtime":
			roots.RuntimeDir = path
		case "log":
			roots.LogDir = path
		case "config":
			roots.ConfigDir = path
		}
	}
	return roots, nil
}

func validateRootPath(name, path string, forbidden []string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return "", fmt.Errorf("%s root directory is required", name)
	}
	if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("%s root %q must not use home-relative paths", name, path)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s root %q must be absolute", name, path)
	}
	if hasParentPathRef(path) {
		return "", fmt.Errorf("%s root %q must not contain parent path escapes", name, path)
	}
	path = filepath.Clean(path)
	if path == string(filepath.Separator) {
		return "", fmt.Errorf("%s root must not be filesystem root", name)
	}
	if isUserConveniencePath(path) && !strings.HasPrefix(path, "/Library/") {
		return "", fmt.Errorf("%s root %q must not be under forbidden user convenience directories", name, path)
	}
	for _, forbiddenRoot := range forbidden {
		forbiddenRoot = filepath.Clean(strings.TrimSpace(forbiddenRoot))
		if forbiddenRoot == "" || forbiddenRoot == "." || forbiddenRoot == string(filepath.Separator) {
			continue
		}
		if pathWithinRoot(forbiddenRoot, path) {
			return "", fmt.Errorf("%s root %q must not be under forbidden user/workspace root %q", name, path, forbiddenRoot)
		}
		if pathWithinRoot(path, forbiddenRoot) {
			return "", fmt.Errorf("%s root %q must not be a parent of forbidden user/workspace root %q", name, path, forbiddenRoot)
		}
	}
	return path, nil
}

func forbiddenRoots(env map[string]string, extra []string) []string {
	keys := []string{"HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"}
	roots := make([]string, 0, len(keys)+len(extra)+8)
	for _, key := range keys {
		if value := strings.TrimSpace(env[key]); value != "" {
			roots = append(roots, value)
		}
	}
	if home := strings.TrimSpace(env["HOME"]); home != "" {
		roots = append(roots,
			filepath.Join(home, ".local", "share"),
			filepath.Join(home, ".cache"),
			filepath.Join(home, ".local", "state"),
			filepath.Join(home, ".config"),
			filepath.Join(home, "Library"),
			filepath.Join(home, "Desktop"),
			filepath.Join(home, "Documents"),
			filepath.Join(home, "Downloads"),
		)
	}
	roots = append(roots, extra...)
	return roots
}

func hasParentPathRef(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}

func isUserConveniencePath(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for _, part := range parts {
		switch part {
		case "Library", "Desktop", "Documents", "Downloads":
			return true
		}
	}
	return false
}

func envMap(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return env
}
