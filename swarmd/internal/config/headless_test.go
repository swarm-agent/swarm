package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestParseHeadlessCannotEnableDesktopOrRewriteExistingConfig(t *testing.T) {
	// Requirement: Headless cannot compose Desktop, including with an existing
	// UI config. Threat: stored ports or CLI flags silently enable UI or rewrite
	// the user's configuration. Owner: ParseHeadless/parse. Parsing a temporary
	// config is the narrowest layer proving effective values and disk invariance.
	dir := writeTestStartupConfig(t)
	// Isolate the simulated user's home so a caller-provided TMPDIR beneath
	// their real home does not invalidate the temporary system-config fixture.
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("CONFIGURATION_DIRECTORY", dir)
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	workspace := t.TempDir()
	// Give repository discovery a bounded fixture root instead of an ambient
	// ancestor repository that may also contain the caller's TMPDIR.
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	path := filepath.Join(dir, "swarm.conf")
	cfg, err := startupconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DesktopPort = 7780
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	core, err := ParseHeadless([]string{"--cwd", workspace})
	if err != nil {
		t.Fatal(err)
	}
	if core.DesktopPort != 0 {
		t.Fatal("stored Desktop port enabled in Headless")
	}
	desktop, err := Parse([]string{"--cwd", workspace})
	if err != nil || desktop.DesktopPort != 7780 {
		t.Fatalf("Desktop configuration lost: port=%d error=%v", desktop.DesktopPort, err)
	}
	for _, args := range [][]string{{"--desktop-port", "7780"}, {"--desktop-port", "0"}, {"runner"}, {"--listen", "0.0.0.0:7781"}} {
		if _, err := ParseHeadless(args); err == nil {
			t.Fatalf("accepted unsupported arguments %v", args)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("existing config changed; read error=%v", err)
	}
}

func TestParseHeadlessRequiresIsolationByDefault(t *testing.T) {
	// Requirement: headless never selects host execution implicitly. Threat:
	// absent/empty flags or auto fall back to the host. Owners: ParseHeadless and
	// HeadlessSandboxMode; isolated config parsing proves values and disk invariance.
	dir := writeTestStartupConfig(t)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("CONFIGURATION_DIRECTORY", dir)
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	path := filepath.Join(dir, "swarm.conf")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "required"}, {[]string{"--sandbox="}, "required"},
		{[]string{"--sandbox=required"}, "required"}, {[]string{"--sandbox=off"}, "off"},
	} {
		cfg, err := ParseHeadless(append([]string{"--cwd", workspace}, tc.args...))
		if err != nil || cfg.SandboxMode != tc.want || cfg.SandboxBackend != "bubblewrap" {
			t.Fatalf("%v: mode=%q err=%v", tc.args, cfg.SandboxMode, err)
		}
	}
	for _, value := range []string{"auto", "AUTO", "unknown"} {
		if _, err := ParseHeadless([]string{"--sandbox=" + value}); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	ui, err := Parse([]string{"--cwd", workspace})
	if err != nil || ui.SandboxMode != "auto" || ui.SandboxBackend != "docker" {
		t.Fatalf("Desktop default changed: %q %v", ui.SandboxMode, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing config changed")
	}
}
