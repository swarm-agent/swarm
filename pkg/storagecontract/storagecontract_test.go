package storagecontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRootsLinuxDefaultsIgnoreHomeAndXDG(t *testing.T) {
	roots, err := ResolveRoots(Options{
		GOOS:    "linux",
		HomeDir: "/home/alice",
		Env: map[string]string{
			"HOME":            "/home/alice",
			"XDG_DATA_HOME":   "/home/alice/.local/share",
			"XDG_CACHE_HOME":  "/home/alice/.cache",
			"XDG_STATE_HOME":  "/home/alice/.local/state",
			"XDG_CONFIG_HOME": "/home/alice/.config",
		},
		WorkspaceRoots: []string{"/srv/workspace/swarm-go"},
	})
	if err != nil {
		t.Fatalf("ResolveRoots: %v", err)
	}
	want := Roots{
		DataDir:    "/var/lib/swarmd",
		CacheDir:   "/var/cache/swarmd",
		RuntimeDir: "/run/swarmd",
		ConfigDir:  "/etc/swarmd",
		LogsDir:    "/var/log/swarmd",
	}
	if roots != want {
		t.Fatalf("linux roots = %#v, want %#v", roots, want)
	}
}

func TestResolveRootsDarwinDefaultsUseSystemLibrary(t *testing.T) {
	roots, err := ResolveRoots(Options{
		GOOS:    "darwin",
		HomeDir: "/Users/alice",
		Env: map[string]string{
			"HOME": "/Users/alice",
		},
	})
	if err != nil {
		t.Fatalf("ResolveRoots: %v", err)
	}
	want := Roots{
		DataDir:    "/Library/Application Support/Swarm/swarmd",
		CacheDir:   "/Library/Caches/Swarm/swarmd",
		RuntimeDir: "/var/run/swarmd",
		ConfigDir:  "/Library/Application Support/Swarm/swarmd/config",
		LogsDir:    "/Library/Logs/Swarm/swarmd",
	}
	if roots != want {
		t.Fatalf("darwin roots = %#v, want %#v", roots, want)
	}
	for _, got := range []string{roots.DataDir, roots.CacheDir, roots.ConfigDir, roots.LogsDir} {
		if strings.HasPrefix(got, "/Users/alice/Library") {
			t.Fatalf("root %q unexpectedly uses user Library", got)
		}
	}
}

func TestResolveRootUsesSingleSystemdDirectoryEnvOnLinux(t *testing.T) {
	got, err := ResolveRoot(RootData, Options{
		GOOS:    "linux",
		HomeDir: "/home/alice",
		Env: map[string]string{
			"STATE_DIRECTORY": "/var/lib/swarmd-custom",
		},
	})
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	if got != "/var/lib/swarmd-custom" {
		t.Fatalf("data root = %q", got)
	}
}

func TestResolveRootRejectsMultiPathSystemdDirectoryEnv(t *testing.T) {
	_, err := ResolveRoot(RootData, Options{
		GOOS: "linux",
		Env: map[string]string{
			"STATE_DIRECTORY": "/var/lib/swarmd:/var/lib/other",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "STATE_DIRECTORY") {
		t.Fatalf("expected STATE_DIRECTORY error, got %v", err)
	}
}

func TestValidateRootRejectsForbiddenRoots(t *testing.T) {
	home := "/home/alice"
	opts := Options{
		HomeDir: home,
		Env: map[string]string{
			"HOME":            home,
			"XDG_DATA_HOME":   "/xdg/data",
			"XDG_CACHE_HOME":  "/xdg/cache",
			"XDG_STATE_HOME":  "/xdg/state",
			"XDG_CONFIG_HOME": "/xdg/config",
		},
		WorkspaceRoots: []string{"/workspace/swarm-go"},
	}
	cases := []string{
		"relative/path",
		"~/swarmd",
		"/",
		"/var/lib/../tmp/swarmd",
		filepath.Join(home, "swarmd"),
		filepath.Join(home, ".local", "share", "swarmd"),
		filepath.Join(home, ".cache", "swarmd"),
		filepath.Join(home, ".config", "swarm"),
		filepath.Join(home, "Library", "Application Support", "Swarm"),
		filepath.Join(home, "Desktop", "swarmd"),
		filepath.Join(home, "Documents", "swarmd"),
		filepath.Join(home, "Downloads", "swarmd"),
		"/xdg/data/swarmd",
		"/xdg/cache/swarmd",
		"/xdg/state/swarmd",
		"/xdg/config/swarmd",
		"/workspace/swarm-go/.swarmd",
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if err := ValidateRoot(tc, opts); err == nil {
				t.Fatalf("ValidateRoot(%q) succeeded, want rejection", tc)
			}
		})
	}
}

func TestOverridesUseSameForbiddenRootValidation(t *testing.T) {
	_, err := ResolveRoot(RootData, Options{
		GOOS:    "linux",
		HomeDir: "/home/alice",
		OverrideRoots: map[RootKind]string{
			RootData: "/home/alice/swarmd-test",
		},
	})
	if err == nil {
		t.Fatal("home override succeeded, want rejection")
	}

	got, err := ResolveRoot(RootData, Options{
		GOOS:    "linux",
		HomeDir: "/home/alice",
		OverrideRoots: map[RootKind]string{
			RootData: "/tmp/swarmd-test",
		},
	})
	if err != nil {
		t.Fatalf("safe override rejected: %v", err)
	}
	if got != "/tmp/swarmd-test" {
		t.Fatalf("safe override = %q", got)
	}
}

func TestValidateRootRejectsSymlinkEscapeIntoForbiddenRoot(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	safe := filepath.Join(tmp, "safe")
	if err := os.MkdirAll(filepath.Join(home, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(safe, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(safe, "link")
	if err := os.Symlink(filepath.Join(home, "target"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ValidateRoot(filepath.Join(link, "swarmd"), Options{HomeDir: home}); err == nil {
		t.Fatal("symlink escape into home succeeded, want rejection")
	}
}

func TestJoinRejectsEscapes(t *testing.T) {
	if got, err := Join("/var/lib/swarmd", "reports", "session-1"); err != nil || got != "/var/lib/swarmd/reports/session-1" {
		t.Fatalf("Join safe = %q, %v", got, err)
	}
	for _, part := range []string{"/etc/passwd", "../escape", "reports/../../escape"} {
		t.Run(part, func(t *testing.T) {
			if _, err := Join("/var/lib/swarmd", part); err == nil {
				t.Fatalf("Join accepted escaping part %q", part)
			}
		})
	}
}
