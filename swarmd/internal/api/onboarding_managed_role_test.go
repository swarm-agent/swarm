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

func TestOnboardingOmitsManagedHostRoleMetadata(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{
		Node:    swarmruntime.LocalNodeState{SwarmID: "local-swarm-1", Name: "Local", Role: startupconfig.SwarmRoleManaged},
		Pairing: swarmruntime.PairingState{PairingState: startupconfig.PairingStatePaired, ParentSwarmID: "retired-manager-swarm"},
	}}

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
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode onboarding response: %v", err)
	}
	config, ok := response["config"].(map[string]any)
	if !ok {
		t.Fatalf("onboarding config = %#v", response["config"])
	}
	if _, exists := config["child"]; exists {
		t.Fatalf("onboarding config still exposes managed-host child metadata: %#v", config)
	}
	if _, exists := config["swarm_role"]; exists {
		t.Fatalf("onboarding config still exposes managed-host role metadata: %#v", config)
	}
}
