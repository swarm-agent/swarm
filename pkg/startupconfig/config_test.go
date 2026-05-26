package startupconfig

import "testing"

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
