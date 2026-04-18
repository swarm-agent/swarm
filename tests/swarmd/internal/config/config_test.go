package config_test

import (
	"os"
	"path/filepath"
	"testing"

	config "swarm/packages/swarmd/internal/config"
)

func TestParse_UsesXDGDataHomeByDefault(t *testing.T) {
	xdgData := filepath.Join(t.TempDir(), "xdg-data")
	t.Setenv("XDG_DATA_HOME", xdgData)
	configHome := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", configHome)

	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}

	wantDataDir := filepath.Join(xdgData, "swarmd")
	if cfg.DataDir != wantDataDir {
		t.Fatalf("cfg.DataDir = %q, want %q", cfg.DataDir, wantDataDir)
	}
	if cfg.DBPath != filepath.Join(wantDataDir, "swarmd.pebble") {
		t.Fatalf("cfg.DBPath = %q", cfg.DBPath)
	}
	if cfg.LockPath != filepath.Join(wantDataDir, "swarmd.lock") {
		t.Fatalf("cfg.LockPath = %q", cfg.LockPath)
	}
	if cfg.ConfigPath != filepath.Join(configHome, "swarm", "swarm.conf") {
		t.Fatalf("cfg.ConfigPath = %q", cfg.ConfigPath)
	}
	if cfg.StartupMode != config.StartupModeInteractive {
		t.Fatalf("cfg.StartupMode = %q", cfg.StartupMode)
	}
	if cfg.Mode != config.ModeSingle {
		t.Fatalf("cfg.Mode = %q, want %q", cfg.Mode, config.ModeSingle)
	}
	if cfg.ListenAddr != "127.0.0.1:7781" {
		t.Fatalf("cfg.ListenAddr = %q", cfg.ListenAddr)
	}

	data, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", cfg.ConfigPath, err)
	}
	wantConfig := `# Swarm startup mode.
# interactive = normal local use; Swarm runs when you launch it.
# box = always-on box mode; Swarm should be treated as an always-running service.
# box does NOT by itself survive reboot/login/logout. For true persistence,
# install/run Swarm under systemd or another OS service manager.
mode = interactive

# Network bind host for the Swarm backend.
# Keep this at 127.0.0.1 for local-only use.
# Use a non-loopback host only when you intentionally want remote access.
host = 127.0.0.1

# Backend API port.
port = 7781

# Desktop/web port. Set to 0 to disable the desktop listener.
desktop_port = 5555

# Bypass normal tool permission prompts.
# Plan mode still stays plan mode, and exit_plan_mode still requires approval.
bypass_permissions = false

# Keep sanitized tool/permission output in persisted history so refresh can show it.
# false keeps the current privacy-preserving placeholder behavior.
retain_tool_output_history = false

# Human-readable Swarm name shown in onboarding and discovery surfaces.
# Leave blank to set it later.
swarm_name = 

# Reachability intent for this Swarm: local, lan, or tailscale.
advertise_mode = local

# Optional manual advertised host or IP for this Swarm.
advertise_addr = 

# Desktop onboarding state.
# blank = auto-detect whether onboarding should appear
# pending = resume onboarding on next desktop launch
# done = skip onboarding and go straight to the launcher
onboarding_state = 
`
	if string(data) != wantConfig {
		t.Fatalf("startup config = %q, want %q", string(data), wantConfig)
	}
}

func TestParse_FallsBackToHomeLocalShare(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("HOME", home)

	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}

	wantDataDir := filepath.Join(home, ".local", "share", "swarmd")
	if cfg.DataDir != wantDataDir {
		t.Fatalf("cfg.DataDir = %q, want %q", cfg.DataDir, wantDataDir)
	}
}

func TestParse_UsesStartupConfigListenDefaults(t *testing.T) {
	xdgData := filepath.Join(t.TempDir(), "xdg-data")
	xdgConfig := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	configPath := filepath.Join(xdgConfig, "swarm", "swarm.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "mode = box\nhost = 0.0.0.0\nport = 8899\ndesktop_port = 5555\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.StartupMode != config.StartupModeBox {
		t.Fatalf("cfg.StartupMode = %q", cfg.StartupMode)
	}
	if cfg.Mode != config.ModeBox {
		t.Fatalf("cfg.Mode = %q, want %q", cfg.Mode, config.ModeBox)
	}
	if cfg.ListenAddr != "0.0.0.0:8899" {
		t.Fatalf("cfg.ListenAddr = %q", cfg.ListenAddr)
	}
}

func TestParse_CLIListenOverridesStartupConfig(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	xdgConfig := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	configPath := filepath.Join(xdgConfig, "swarm", "swarm.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "mode = interactive\nhost = 127.0.0.1\nport = 7781\ndesktop_port = 5555\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Parse([]string{"--listen", "0.0.0.0:9000"})
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Fatalf("cfg.ListenAddr = %q", cfg.ListenAddr)
	}
}

func TestParse_CLIModeOverridesStartupConfig(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	xdgConfig := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	configPath := filepath.Join(xdgConfig, "swarm", "swarm.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "mode = interactive\nhost = 127.0.0.1\nport = 7781\ndesktop_port = 5555\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Parse([]string{"--mode", config.ModeBox})
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.Mode != config.ModeBox {
		t.Fatalf("cfg.Mode = %q, want %q", cfg.Mode, config.ModeBox)
	}
}

func TestParse_InvalidStartupConfigFails(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	xdgConfig := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	configPath := filepath.Join(xdgConfig, "swarm", "swarm.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte("mode = bad\nhost = 127.0.0.1\nport = 7781\ndesktop_port = 5555\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := config.Parse(nil); err == nil {
		t.Fatal("config.Parse() error = nil, want failure")
	}
}

func TestParse_RequiresAllStartupConfigKeys(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	xdgConfig := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	configPath := filepath.Join(xdgConfig, "swarm", "swarm.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte("mode = box\nhost = 127.0.0.1\nport = 7781\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.DesktopPort != 5555 {
		t.Fatalf("cfg.DesktopPort = %d", cfg.DesktopPort)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", configPath, err)
	}
	want := `mode = box
host = 127.0.0.1
port = 7781

# Desktop/web port. Set to 0 to disable the desktop listener.
desktop_port = 5555

# Bypass normal tool permission prompts.
# Plan mode still stays plan mode, and exit_plan_mode still requires approval.
bypass_permissions = false

# Keep sanitized tool/permission output in persisted history so refresh can show it.
# false keeps the current privacy-preserving placeholder behavior.
retain_tool_output_history = false

# Human-readable Swarm name shown in onboarding and discovery surfaces.
# Leave blank to set it later.
swarm_name = 

# Reachability intent for this Swarm: local, lan, or tailscale.
advertise_mode = local

# Optional manual advertised host or IP for this Swarm.
advertise_addr = 

# Desktop onboarding state.
# blank = auto-detect whether onboarding should appear
# pending = resume onboarding on next desktop launch
# done = skip onboarding and go straight to the launcher
onboarding_state = 
`
	if string(data) != want {
		t.Fatalf("startup config = %q, want %q", string(data), want)
	}
}

func TestParse_FirstRunFlagsWriteStartupConfig(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	xdgConfig := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	cfg, err := config.Parse([]string{"--mode", config.ModeBox, "--listen", "0.0.0.0:8899", "--desktop-port", "6000"})
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.Mode != config.ModeBox {
		t.Fatalf("cfg.Mode = %q", cfg.Mode)
	}
	if cfg.ListenAddr != "0.0.0.0:8899" {
		t.Fatalf("cfg.ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.DesktopPort != 6000 {
		t.Fatalf("cfg.DesktopPort = %d", cfg.DesktopPort)
	}

	data, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", cfg.ConfigPath, err)
	}
	want := `# Swarm startup mode.
# interactive = normal local use; Swarm runs when you launch it.
# box = always-on box mode; Swarm should be treated as an always-running service.
# box does NOT by itself survive reboot/login/logout. For true persistence,
# install/run Swarm under systemd or another OS service manager.
mode = box

# Network bind host for the Swarm backend.
# Keep this at 127.0.0.1 for local-only use.
# Use a non-loopback host only when you intentionally want remote access.
host = 0.0.0.0

# Backend API port.
port = 8899

# Desktop/web port. Set to 0 to disable the desktop listener.
desktop_port = 6000

# Bypass normal tool permission prompts.
# Plan mode still stays plan mode, and exit_plan_mode still requires approval.
bypass_permissions = false

# Keep sanitized tool/permission output in persisted history so refresh can show it.
# false keeps the current privacy-preserving placeholder behavior.
retain_tool_output_history = false

# Human-readable Swarm name shown in onboarding and discovery surfaces.
# Leave blank to set it later.
swarm_name = 

# Reachability intent for this Swarm: local, lan, or tailscale.
advertise_mode = local

# Optional manual advertised host or IP for this Swarm.
advertise_addr = 

# Desktop onboarding state.
# blank = auto-detect whether onboarding should appear
# pending = resume onboarding on next desktop launch
# done = skip onboarding and go straight to the launcher
onboarding_state = 
`
	if string(data) != want {
		t.Fatalf("startup config = %q, want %q", string(data), want)
	}
}

func TestParse_LegacyWebAuthEnabledIsIgnored(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	xdgConfig := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	configPath := filepath.Join(xdgConfig, "swarm", "swarm.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "mode = interactive\nhost = 127.0.0.1\nport = 7781\ndesktop_port = 5555\nwebauth_enabled = maybe\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:7781" {
		t.Fatalf("cfg.ListenAddr = %q", cfg.ListenAddr)
	}
}

func TestParse_BypassPermissionsDefaultsFalse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.BypassPermissions {
		t.Fatal("BypassPermissions = true, want false by default")
	}
}

func TestParse_BypassPermissionsFromCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Parse([]string{"--bypass-permissions"})
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if !cfg.BypassPermissions {
		t.Fatal("BypassPermissions = false, want true from CLI")
	}
}

func TestParse_RetainToolOutputHistoryDefaultsFalse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if cfg.RetainToolOutputHistory {
		t.Fatal("RetainToolOutputHistory = true, want false by default")
	}
}

func TestParse_RetainToolOutputHistoryFromStartupConfig(t *testing.T) {
	xdgConfig := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	configPath := filepath.Join(xdgConfig, "swarm", "swarm.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "mode = interactive\nhost = 127.0.0.1\nport = 7781\ndesktop_port = 5555\nretain_tool_output_history = true\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	if !cfg.RetainToolOutputHistory {
		t.Fatal("RetainToolOutputHistory = false, want true from startup config")
	}
}
