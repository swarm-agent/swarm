package config

import (
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

// Requirement: container access is explicit and cannot relax normal loopback
// defaults. Tool permissions stay the owner's choice: a startup config with
// bypass on (set through the owner-only setting) still starts the headless
// listener, so the choice survives restarts; --lock-permission-policy forces
// permissions on. Parse is the startup authority; isolated config parsing is
// the narrowest test and opens no daemon listener.
func TestContainerSDKConfig(t *testing.T) {
	t.Setenv("CONFIGURATION_DIRECTORY", writeTestStartupConfig(t))
	t.Setenv("SWARM_CHILD_STARTUP_CONFIG", "")
	defaults, err := Parse(nil)
	if err != nil || defaults.ContainerSDKPort != 0 || defaults.ListenAddr != "127.0.0.1:7781" {
		t.Fatalf("unsafe defaults: %+v %v", defaults, err)
	}
	cfg, err := Parse([]string{"--desktop-port=0", "--container-sdk-port=7783"})
	if err != nil || cfg.ContainerSDKPort != 7783 || cfg.ListenAddr != defaults.ListenAddr {
		t.Fatalf("explicit SDK: %+v %v", cfg, err)
	}
	for _, args := range [][]string{
		{"--container-sdk-port=7783"},
		{"--desktop-port=0", "--container-sdk-port=-1"},
		{"--desktop-port=0", "--container-sdk-port=65536"},
		{"--desktop-port=0", "--container-sdk-port=7781"},
		{"--desktop-port=0", "--container-sdk-port=7791"},
		{"--desktop-port=0", "--container-sdk-port=7783", "--listen=0.0.0.0:7781"},
		// The Serve identity header is trusted only on a loopback listener.
		{"--tailnet-identity"},
		{"--desktop-port=0", "--container-sdk-port=7783", "--tailnet-identity"},
		{"--desktop-port=0", "--container-sdk-port=7783", "--container-sdk-host=0.0.0.0", "--tailnet-identity"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted unsafe flags %v", args)
		}
	}
	tailnet, err := Parse([]string{"--desktop-port=0", "--container-sdk-port=7783", "--container-sdk-host=127.0.0.1", "--tailnet-identity"})
	if err != nil || !tailnet.TailnetIdentity {
		t.Fatalf("tailnet identity refused on a loopback listener: %+v %v", tailnet, err)
	}
	owner, err := Parse([]string{"--desktop-port=0", "--container-sdk-port=7783", "--bypass-permissions"})
	if err != nil || !owner.BypassPermissions {
		t.Fatalf("owner bypass refused with the headless listener: %+v %v", owner, err)
	}
	locked, err := Parse([]string{"--desktop-port=0", "--container-sdk-port=7783", "--bypass-permissions", "--lock-permission-policy"})
	if err != nil || locked.BypassPermissions {
		t.Fatalf("lock did not keep permissions on: %+v %v", locked, err)
	}
	after, err := Parse(nil)
	if err != nil || after.ContainerSDKPort != 0 || after.ListenAddr != defaults.ListenAddr {
		t.Fatal("opt-in leaked into persisted host defaults")
	}
}

// Requirement: --lock-permission-policy is a property of how the process was
// started. It is off by default, wins over a startup config that enables
// bypass (a config file an agent could edit), is never persisted, and a later
// --lock-permission-policy=false (the owner's explicit docker run override)
// turns it off. Threat: a locked headless daemon starting with bypass on, or
// the lock leaking into or out of persisted host defaults. Parse is the
// startup authority; isolated config parsing is the narrowest test.
func TestParseLockPermissionPolicy(t *testing.T) {
	configDir := writeTestStartupConfig(t)
	t.Setenv("CONFIGURATION_DIRECTORY", configDir)
	t.Setenv("SWARM_CHILD_STARTUP_CONFIG", "")
	defaults, err := Parse(nil)
	if err != nil || defaults.LockPermissionPolicy {
		t.Fatalf("lock on by default: %+v %v", defaults, err)
	}
	path := filepath.Join(configDir, "swarm.conf")
	edited := startupconfig.Default(path)
	edited.BypassPermissions = true
	if err := startupconfig.Write(edited); err != nil {
		t.Fatal(err)
	}
	locked, err := Parse([]string{"--lock-permission-policy"})
	if err != nil || !locked.LockPermissionPolicy || locked.BypassPermissions {
		t.Fatalf("lock did not force bypass off: %+v %v", locked, err)
	}
	if explicit, err := Parse([]string{"--lock-permission-policy", "--bypass-permissions"}); err != nil || explicit.BypassPermissions {
		t.Fatalf("bypass flag beat the lock: %+v %v", explicit, err)
	}
	overridden, err := Parse([]string{"--lock-permission-policy", "--lock-permission-policy=false"})
	if err != nil || overridden.LockPermissionPolicy || !overridden.BypassPermissions {
		t.Fatalf("owner override not honoured: %+v %v", overridden, err)
	}
	persisted, err := startupconfig.Load(path)
	if err != nil || !persisted.BypassPermissions {
		t.Fatalf("parsing rewrote the startup config: %+v %v", persisted, err)
	}
}
