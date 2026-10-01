package config

import "testing"

// Requirement: container access is explicit and cannot relax normal loopback or
// tool-permission defaults. Parse is the startup authority; isolated config
// parsing is the narrowest test and opens no daemon listener.
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
		{"--desktop-port=0", "--container-sdk-port=7783", "--bypass-permissions"},
		{"--desktop-port=0", "--container-sdk-port=7783", "--listen=0.0.0.0:7781"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted unsafe flags %v", args)
		}
	}
	after, err := Parse(nil)
	if err != nil || after.ContainerSDKPort != 0 || after.ListenAddr != defaults.ListenAddr {
		t.Fatal("opt-in leaked into persisted host defaults")
	}
}
