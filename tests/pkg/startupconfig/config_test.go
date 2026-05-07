package startupconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestWriteAndLoad_PersistsSwarmMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(path)
	cfg.SwarmName = "my-device"
	cfg.SwarmMode = true
	cfg.DevMode = true
	cfg.DevRoot = filepath.Clean(filepath.Join(t.TempDir(), "repo"))
	cfg.Child = true
	cfg.NetworkMode = startupconfig.NetworkModeTailscale
	cfg.TailscaleURL = "https://my-device.example.ts.net"

	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("startup config mode = %#o, want 0o600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	text := string(data)
	if !strings.Contains(text, "swarm_mode = true") {
		t.Fatalf("startup config missing swarm_mode=true: %q", text)
	}
	if !strings.Contains(text, "dev_mode = true") {
		t.Fatalf("startup config missing dev_mode=true: %q", text)
	}
	if !strings.Contains(text, "dev_root = "+cfg.DevRoot) {
		t.Fatalf("startup config missing dev_root=%q: %q", cfg.DevRoot, text)
	}

	loaded, err := startupconfig.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.SwarmMode {
		t.Fatal("loaded.SwarmMode = false, want true")
	}
	if !loaded.DevMode {
		t.Fatal("loaded.DevMode = false, want true")
	}
	if loaded.DevRoot != cfg.DevRoot {
		t.Fatalf("loaded.DevRoot = %q, want %q", loaded.DevRoot, cfg.DevRoot)
	}
	if !loaded.Child {
		t.Fatal("loaded.Child = false, want true")
	}
	if loaded.TailscaleURL != cfg.TailscaleURL {
		t.Fatalf("loaded.TailscaleURL = %q, want %q", loaded.TailscaleURL, cfg.TailscaleURL)
	}
}

func TestResolvePath_DefaultsToSystemConfigRoot(t *testing.T) {
	t.Setenv("HOME", "/home/startupconfig-test-user")
	t.Setenv("XDG_CONFIG_HOME", "/home/startupconfig-test-user/.config")
	t.Setenv("XDG_DATA_HOME", "/home/startupconfig-test-user/.local/share")
	t.Setenv("XDG_CACHE_HOME", "/home/startupconfig-test-user/.cache")
	t.Setenv("XDG_STATE_HOME", "/home/startupconfig-test-user/.local/state")
	t.Setenv("CONFIGURATION_DIRECTORY", "")

	path, err := startupconfig.ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if path != "/etc/swarmd/swarm.conf" {
		t.Fatalf("ResolvePath() = %q, want /etc/swarmd/swarm.conf", path)
	}
	forbiddenPrefixes := []string{
		"/home/startupconfig-test-user",
		"/home/startupconfig-test-user/.config",
		"/home/startupconfig-test-user/.local/share",
		"/home/startupconfig-test-user/.cache",
		"/home/startupconfig-test-user/.local/state",
	}
	for _, prefix := range forbiddenPrefixes {
		if strings.HasPrefix(path, prefix) {
			t.Fatalf("ResolvePath() = %q under forbidden prefix %q", path, prefix)
		}
	}
}

func TestResolvePath_UsesValidatedSystemdConfigurationDirectory(t *testing.T) {
	t.Setenv("HOME", "/home/startupconfig-test-user")
	t.Setenv("XDG_CONFIG_HOME", "/home/startupconfig-test-user/.config")
	t.Setenv("CONFIGURATION_DIRECTORY", "/etc/swarmd-test")

	path, err := startupconfig.ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	if path != "/etc/swarmd-test/swarm.conf" {
		t.Fatalf("ResolvePath() = %q, want /etc/swarmd-test/swarm.conf", path)
	}
}

func TestResolvePath_RejectsForbiddenConfigurationDirectory(t *testing.T) {
	t.Setenv("HOME", "/home/startupconfig-test-user")
	t.Setenv("CONFIGURATION_DIRECTORY", "/home/startupconfig-test-user/.config/swarm")

	_, err := startupconfig.ResolvePath()
	if err == nil {
		t.Fatal("ResolvePath() succeeded with forbidden CONFIGURATION_DIRECTORY")
	}
	if !strings.Contains(err.Error(), "forbidden root") {
		t.Fatalf("ResolvePath() error = %v, want forbidden root", err)
	}
}

func TestRemoteDeployBootstrapSecretPath_DefaultResolvedPathNotUnderHomeXDGOrWorkspace(t *testing.T) {
	t.Setenv("HOME", "/home/startupconfig-test-user")
	t.Setenv("XDG_CONFIG_HOME", "/home/startupconfig-test-user/.config")
	t.Setenv("CONFIGURATION_DIRECTORY", "")

	configPath, err := startupconfig.ResolvePath()
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}
	secretPath := startupconfig.RemoteDeployBootstrapSecretPath(configPath)
	if secretPath != "/etc/swarmd/remote-deploy-bootstrap.secret" {
		t.Fatalf("RemoteDeployBootstrapSecretPath() = %q, want /etc/swarmd/remote-deploy-bootstrap.secret", secretPath)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	for _, forbidden := range []string{"/home/startupconfig-test-user", "/home/startupconfig-test-user/.config", cwd} {
		if strings.HasPrefix(secretPath, forbidden) {
			t.Fatalf("remote deploy bootstrap secret %q under forbidden prefix %q", secretPath, forbidden)
		}
	}
}

func TestWriteAndLoad_RemoteDeploySecretsUseSeparateSecretFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(path)
	cfg.Child = true
	cfg.RemoteDeploy.Enabled = true
	cfg.RemoteDeploy.SessionID = "remote-1"
	cfg.RemoteDeploy.SessionToken = "session-secret"
	cfg.RemoteDeploy.InviteToken = "invite-secret"
	cfg.RemoteDeploy.HostAPIBaseURL = "https://host.example"
	cfg.RemoteDeploy.HostDesktopURL = "https://host.example"

	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	text := string(data)
	if strings.Contains(text, "remote_deploy_session_token") || strings.Contains(text, "remote_deploy_invite_token") {
		t.Fatalf("startup config should not include remote deploy bootstrap secrets: %q", text)
	}

	secretPath := startupconfig.RemoteDeployBootstrapSecretPath(path)
	secretInfo, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", secretPath, err)
	}
	if got := secretInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("remote deploy bootstrap secret mode = %#o, want 0o600", got)
	}
	secretData, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", secretPath, err)
	}
	secretText := string(secretData)
	if !strings.Contains(secretText, "remote_deploy_session_token = session-secret") {
		t.Fatalf("remote deploy bootstrap secret missing session token: %q", secretText)
	}
	if !strings.Contains(secretText, "remote_deploy_invite_token = invite-secret") {
		t.Fatalf("remote deploy bootstrap secret missing invite token: %q", secretText)
	}

	loaded, err := startupconfig.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.RemoteDeploy.SessionToken != cfg.RemoteDeploy.SessionToken {
		t.Fatalf("loaded remote deploy session token = %q, want %q", loaded.RemoteDeploy.SessionToken, cfg.RemoteDeploy.SessionToken)
	}
	if loaded.RemoteDeploy.InviteToken != cfg.RemoteDeploy.InviteToken {
		t.Fatalf("loaded remote deploy invite token = %q, want %q", loaded.RemoteDeploy.InviteToken, cfg.RemoteDeploy.InviteToken)
	}
}

func TestDefault_SwarmModeDisabled(t *testing.T) {
	cfg := startupconfig.Default(filepath.Join(t.TempDir(), "swarm.conf"))
	if cfg.SwarmMode {
		t.Fatal("Default().SwarmMode = true, want false")
	}
	if cfg.DevMode {
		t.Fatal("Default().DevMode = true, want false")
	}
	if cfg.DevRoot != "" {
		t.Fatalf("Default().DevRoot = %q, want empty", cfg.DevRoot)
	}
}
