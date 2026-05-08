package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestSwarmManagedHostRemoveDetachesManagedHostConfig(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{
		Node:    swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed B", Role: startupconfig.SwarmRoleManaged},
		Pairing: swarmruntime.PairingState{PairingState: startupconfig.PairingStatePaired, ParentSwarmID: "manager-swarm-1"},
	}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = true
		cfg.SwarmRole = startupconfig.SwarmRoleManaged
		cfg.ParentSwarmID = "manager-swarm-1"
		cfg.PairingState = startupconfig.PairingStatePaired
	})

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{
		"propagate": false,
		"reason":    "test detach",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmManagedHostRemoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || !response.LocalRemoved || response.Pairing.PairingState != startupconfig.PairingStateUnpaired {
		t.Fatalf("response = %+v", response)
	}
	cfg, err := server.loadStartupConfig()
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if !cfg.SwarmMode || cfg.Child || cfg.SwarmRole != "" || cfg.ParentSwarmID != "" || cfg.PairingState != startupconfig.PairingStateUnpaired {
		t.Fatalf("config not detached: mode=%t child=%t role=%q parent=%q pairing=%q", cfg.SwarmMode, cfg.Child, cfg.SwarmRole, cfg.ParentSwarmID, cfg.PairingState)
	}
}

func TestSwarmManagedHostRemoveRejectsManagerWithoutManagedID(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", Role: "master"}}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = false
		cfg.SwarmRole = ""
		cfg.PairingState = ""
	})

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "managed swarm id is required") {
		t.Fatalf("remove status/body = %d/%s", rec.Code, rec.Body.String())
	}
}

func TestSwarmManagedHostRemoveFromManagerCanPropagate(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{
		state: swarmruntime.LocalState{
			Node:         swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", Role: "master"},
			TrustedPeers: []swarmruntime.TrustedPeer{{SwarmID: "managed-swarm-1", Relationship: swarmruntime.RelationshipManaged}},
		},
		outgoingPeerAuthTokens: map[string]string{"managed-swarm-1": "manager-to-managed-token"},
	}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = false
		cfg.SwarmRole = ""
	})

	var remoteCalled bool
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/managed-host/remove" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "manager-swarm-1" || r.Header.Get(peerAuthTokenHeader) == "" {
			t.Fatalf("missing peer auth headers")
		}
		remoteCalled = true
		writeJSON(w, http.StatusOK, swarmManagedHostRemoveResponse{OK: true, Role: startupconfig.SwarmRoleManaged, LocalRemoved: true})
	}))
	defer remoteServer.Close()

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/managed-host/remove", map[string]any{
		"managed_swarm_id": "managed-swarm-1",
		"endpoint":         remoteServer.URL,
		"propagate":        true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !remoteCalled {
		t.Fatalf("manager removal did not propagate to managed host")
	}
	var response swarmManagedHostRemoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.LocalRemoved || !response.RemoteRemoved || !response.Cleanup.RemovedTrustedPeer {
		t.Fatalf("response = %+v", response)
	}
}
