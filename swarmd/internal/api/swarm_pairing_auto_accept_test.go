package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestSwarmEnrollWithPeerAuthTokenDoesNotAutoApprove(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", PublicKey: "manager-public-key", Fingerprint: "manager-fingerprint"}}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = false
		cfg.SwarmName = "Manager A"
	})

	rec := postRemotePairingJSONWithDesktopSession(t, server, "/v1/swarm/enroll", map[string]any{
		"invite_token":     "manager-to-managed-token",
		"primary_swarm_id": "manager-swarm-1",
		"child_swarm_id":   "managed-swarm-1",
		"child_name":       "Managed B",
		"child_role":       startupconfig.SwarmRoleManaged,
		"child_public_key": "managed-public-key",
		"transport_mode":   startupconfig.NetworkModeTailscale,
		"peer_auth_token":  "managed-to-manager-token",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response struct {
		Enrollment swarmruntime.Enrollment    `json:"enrollment"`
		Trusted    []swarmruntime.TrustedPeer `json:"trusted_peers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode enroll response: %v", err)
	}
	if response.Enrollment.Status != swarmruntime.EnrollmentStatusPending {
		t.Fatalf("enrollment status = %q, want pending", response.Enrollment.Status)
	}
	if len(response.Trusted) != 0 || strings.Contains(rec.Body.String(), "trusted_peers") {
		t.Fatalf("generic enroll returned trusted peers / approval material: %s", rec.Body.String())
	}
}

func TestSwarmRemotePairingStartRejectsAlreadyManagedHost(t *testing.T) {
	managed := newLocalAuthTestServer(t)
	managed.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{
		Node:    swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed B", PublicKey: "managed-public-key", Fingerprint: "managed-fingerprint", Role: startupconfig.SwarmRoleManaged},
		Pairing: swarmruntime.PairingState{PairingState: startupconfig.PairingStatePaired, ParentSwarmID: "old-manager-swarm"},
	}}
	setLocalAuthTestStartupConfig(t, managed, func(cfg *startupconfig.FileConfig) {
		cfg.Child = true
		cfg.SwarmRole = startupconfig.SwarmRoleManaged
		cfg.ParentSwarmID = "old-manager-swarm"
		cfg.PairingState = startupconfig.PairingStatePaired
		cfg.SwarmName = "Managed B"
		cfg.TailscaleURL = "https://managed-b.example.ts.net"
	})

	rec := postRemotePairingJSONWithDesktopSession(t, managed, "/v1/swarm/remote-pairing/start", map[string]any{
		"endpoint": "http://127.0.0.1:65535",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("already managed start status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already linked") {
		t.Fatalf("already managed error missing linked guidance: %s", rec.Body.String())
	}
}
