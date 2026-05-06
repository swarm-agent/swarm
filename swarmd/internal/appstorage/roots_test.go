package appstorage

import (
	"path/filepath"
	"testing"
)

func TestResolvePlatformRootsLinuxDefaults(t *testing.T) {
	roots, err := resolvePlatformRoots(rootResolveOptions{GOOS: "linux", Env: map[string]string{}})
	if err != nil {
		t.Fatalf("resolvePlatformRoots: %v", err)
	}
	want := PlatformRoots{
		DataDir:    "/var/lib/swarmd",
		CacheDir:   "/var/cache/swarmd",
		RuntimeDir: "/run/swarmd",
		LogDir:     "/var/log/swarmd",
		ConfigDir:  "/etc/swarmd",
	}
	if roots != want {
		t.Fatalf("roots = %#v, want %#v", roots, want)
	}
}

func TestResolvePlatformRootsDarwinDefaults(t *testing.T) {
	roots, err := resolvePlatformRoots(rootResolveOptions{GOOS: "darwin", Env: map[string]string{}})
	if err != nil {
		t.Fatalf("resolvePlatformRoots: %v", err)
	}
	want := PlatformRoots{
		DataDir:    "/Library/Application Support/Swarm/swarmd",
		CacheDir:   "/Library/Caches/Swarm/swarmd",
		RuntimeDir: "/var/run/swarmd",
		LogDir:     "/Library/Logs/Swarm/swarmd",
		ConfigDir:  "/Library/Application Support/Swarm/swarmd/config",
	}
	if roots != want {
		t.Fatalf("roots = %#v, want %#v", roots, want)
	}
	for _, got := range []string{roots.DataDir, roots.CacheDir, roots.RuntimeDir, roots.LogDir, roots.ConfigDir} {
		if filepath.IsLocal(got) || got == "" || got[0] != '/' {
			t.Fatalf("darwin root is not absolute: %q", got)
		}
		if got == "/Users/example/Library" || filepathHasPrefix(got, "/Users/example/Library") {
			t.Fatalf("darwin root used user Library: %q", got)
		}
	}
}

func TestResolvePlatformRootsLinuxSystemdOverrides(t *testing.T) {
	env := map[string]string{
		"STATE_DIRECTORY":         "/var/lib/swarm-test/state",
		"CACHE_DIRECTORY":         "/var/cache/swarm-test/cache",
		"RUNTIME_DIRECTORY":       "/run/swarm-test/run",
		"LOGS_DIRECTORY":          "/var/log/swarm-test/logs",
		"CONFIGURATION_DIRECTORY": "/etc/swarm-test/config",
	}
	roots, err := resolvePlatformRoots(rootResolveOptions{GOOS: "linux", Env: env})
	if err != nil {
		t.Fatalf("resolvePlatformRoots: %v", err)
	}
	want := PlatformRoots{
		DataDir:    "/var/lib/swarm-test/state",
		CacheDir:   "/var/cache/swarm-test/cache",
		RuntimeDir: "/run/swarm-test/run",
		LogDir:     "/var/log/swarm-test/logs",
		ConfigDir:  "/etc/swarm-test/config",
	}
	if roots != want {
		t.Fatalf("roots = %#v, want %#v", roots, want)
	}
}

func TestResolvePlatformRootsRejectsRelativeSystemdEnv(t *testing.T) {
	env := map[string]string{"STATE_DIRECTORY": "relative/state"}
	if _, err := resolvePlatformRoots(rootResolveOptions{GOOS: "linux", Env: env}); err == nil {
		t.Fatalf("resolvePlatformRoots accepted relative systemd env")
	}
}

func TestResolvePlatformRootsRejectsHomeAndXDGRuntimeRoots(t *testing.T) {
	env := map[string]string{
		"HOME":                    "/home/example",
		"XDG_DATA_HOME":           "/home/example/.local/share",
		"XDG_CACHE_HOME":          "/home/example/.cache",
		"XDG_STATE_HOME":          "/home/example/.local/state",
		"XDG_CONFIG_HOME":         "/home/example/.config",
		"STATE_DIRECTORY":         "/home/example/.local/share/swarmd",
		"CACHE_DIRECTORY":         "/home/example/.cache/swarmd",
		"RUNTIME_DIRECTORY":       "/home/example/.local/state/swarmd",
		"LOGS_DIRECTORY":          "/home/example/Documents/swarmd/logs",
		"CONFIGURATION_DIRECTORY": "/home/example/.config/swarmd",
	}
	if _, err := resolvePlatformRoots(rootResolveOptions{GOOS: "linux", Env: env}); err == nil {
		t.Fatalf("resolvePlatformRoots accepted HOME/XDG-derived roots")
	}
}

func TestResolvePlatformRootsRejectsUserLibraryAndDownloads(t *testing.T) {
	for name, root := range map[string]string{
		"library":   "/Users/example/Library/Application Support/Swarm/swarmd",
		"desktop":   "/Users/example/Desktop/swarmd",
		"documents": "/Users/example/Documents/swarmd",
		"downloads": "/Users/example/Downloads/swarmd",
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{
				"HOME":            "/Users/example",
				"STATE_DIRECTORY": root,
			}
			if _, err := resolvePlatformRoots(rootResolveOptions{GOOS: "linux", Env: env}); err == nil {
				t.Fatalf("resolvePlatformRoots accepted forbidden root %q", root)
			}
		})
	}
}

func TestResolvePlatformRootsRejectsUnsupportedOS(t *testing.T) {
	if _, err := resolvePlatformRoots(rootResolveOptions{GOOS: "plan9", Env: map[string]string{}}); err == nil {
		t.Fatalf("resolvePlatformRoots accepted unsupported OS")
	}
}

func TestResolvePlatformRootsRejectsPathEscapeAndExplicitWorkspaceRoots(t *testing.T) {
	if _, err := resolvePlatformRoots(rootResolveOptions{GOOS: "linux", Env: map[string]string{"STATE_DIRECTORY": "/var/lib/../tmp/swarmd"}}); err == nil {
		t.Fatalf("resolvePlatformRoots accepted parent path escape")
	}
	if _, err := resolvePlatformRoots(rootResolveOptions{
		GOOS:           "linux",
		Env:            map[string]string{"STATE_DIRECTORY": "/workspace/repo/.tmp/swarmd"},
		ForbiddenRoots: []string{"/workspace/repo"},
	}); err == nil {
		t.Fatalf("resolvePlatformRoots accepted explicit workspace root")
	}
}

func filepathHasPrefix(path, root string) bool {
	return pathWithinRoot(root, path)
}
