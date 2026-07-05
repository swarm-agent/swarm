package startupconfig

import (
	"strings"
	"testing"
)

func TestV3DiagnosticsConfigParsesAndFormats(t *testing.T) {
	cfg, _, err := parseEntries("v3_diagnostics = true\n", Default(t.TempDir()+"/swarm.conf"))
	if err != nil {
		t.Fatalf("parseEntries: %v", err)
	}
	if !cfg.V3Diagnostics {
		t.Fatalf("V3Diagnostics = false, want true")
	}
	if !containsLine(Format(cfg), "v3_diagnostics = true") {
		t.Fatalf("formatted config missing v3_diagnostics = true")
	}
}

func TestProviderAPIDiagnosticsLegacyAliasParsesAsV3Diagnostics(t *testing.T) {
	cfg, seen, err := parseEntries("provider_api_diagnostics = true\n", Default(t.TempDir()+"/swarm.conf"))
	if err != nil {
		t.Fatalf("parseEntries: %v", err)
	}
	if !cfg.V3Diagnostics {
		t.Fatalf("V3Diagnostics = false, want true")
	}
	if _, ok := seen["v3_diagnostics"]; !ok {
		t.Fatalf("seen missing v3_diagnostics")
	}
}

func TestV3DiagnosticsOverridesProviderAPIDiagnosticsLegacyAlias(t *testing.T) {
	cfg, _, err := parseEntries("provider_api_diagnostics = true\nv3_diagnostics = false\n", Default(t.TempDir()+"/swarm.conf"))
	if err != nil {
		t.Fatalf("parseEntries: %v", err)
	}
	if cfg.V3Diagnostics {
		t.Fatalf("V3Diagnostics = true, want canonical v3_diagnostics value")
	}
}

func TestScrubManagedLinkStateClearsManagedFields(t *testing.T) {
	cfg := Default(t.TempDir() + "/swarm.conf")
	cfg.Child = true
	cfg.SwarmRole = SwarmRoleManaged
	cfg.ParentSwarmID = "manager-swarm"
	cfg.PairingState = PairingStatePaired
	cfg.ManagedHostSync = ManagedHostSyncConfig{
		Mode:              "managed",
		Modules:           []string{"credentials", "agents"},
		OwnerSwarmID:      "manager-swarm",
		HostAPIBaseURL:    "https://manager.example.test",
		SyncCredentialURL: "https://manager.example.test/v1/deploy/container/sync/credentials",
		SyncAgentURL:      "https://manager.example.test/v1/deploy/container/sync/agents",
	}

	cfg = ScrubManagedLinkState(cfg)

	if cfg.Child || cfg.SwarmRole != "" || cfg.ParentSwarmID != "" || cfg.PairingState != "" {
		t.Fatalf("managed state not scrubbed: child=%t role=%q parent=%q pairing=%q", cfg.Child, cfg.SwarmRole, cfg.ParentSwarmID, cfg.PairingState)
	}
	if cfg.ManagedHostSync.Mode != "" || len(cfg.ManagedHostSync.Modules) != 0 || cfg.ManagedHostSync.OwnerSwarmID != "" || cfg.ManagedHostSync.HostAPIBaseURL != "" || cfg.ManagedHostSync.SyncCredentialURL != "" || cfg.ManagedHostSync.SyncAgentURL != "" {
		t.Fatalf("managed sync not scrubbed: %+v", cfg.ManagedHostSync)
	}
}

func containsLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if line == want {
			return true
		}
	}
	return false
}
