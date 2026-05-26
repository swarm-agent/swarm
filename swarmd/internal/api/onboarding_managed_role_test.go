package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestOnboardingReportsManagedSwarmRole(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{
		Node:    swarmruntime.LocalNodeState{SwarmID: "managed-swarm-1", Name: "Managed", Role: startupconfig.SwarmRoleManaged},
		Pairing: swarmruntime.PairingState{PairingState: startupconfig.PairingStatePaired, ParentSwarmID: "manager-swarm-1"},
	}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = false
		cfg.SwarmRole = ""
		cfg.PairingState = ""
		cfg.ParentSwarmID = ""
	})

	token, expiresAt, err := server.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	cookie := buildDesktopLocalSessionCookie(token, expiresAt, false)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/onboarding", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("onboarding status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var status onboardingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode onboarding response: %v", err)
	}
	if status.Config.SwarmRole != startupconfig.SwarmRoleManaged {
		t.Fatalf("swarm_role = %q, want %q", status.Config.SwarmRole, startupconfig.SwarmRoleManaged)
	}
}

func TestOnboardingIgnoresStaleManagedRoleConfigWhenDBCleared(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{
		Node:    swarmruntime.LocalNodeState{SwarmID: "local-swarm-1", Name: "Standalone", Role: bootstrapRoleMaster},
		Pairing: swarmruntime.PairingState{PairingState: startupconfig.PairingStateUnpaired},
	}}
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = true
		cfg.SwarmRole = startupconfig.SwarmRoleManaged
		cfg.PairingState = startupconfig.PairingStatePaired
		cfg.ParentSwarmID = "stale-manager-swarm"
	})

	token, expiresAt, err := server.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	cookie := buildDesktopLocalSessionCookie(token, expiresAt, false)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/onboarding", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("onboarding status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var status onboardingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode onboarding response: %v", err)
	}
	if status.Config.Child || status.Config.SwarmRole != bootstrapRoleMaster {
		t.Fatalf("onboarding used stale config managed state: child=%t role=%q", status.Config.Child, status.Config.SwarmRole)
	}
}
