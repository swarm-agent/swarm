package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestOnboardingReportsManagedSwarmRole(t *testing.T) {
	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmMode = true
		cfg.Child = true
		cfg.SwarmRole = startupconfig.SwarmRoleManaged
		cfg.PairingState = "paired"
		cfg.ParentSwarmID = "manager-swarm-1"
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

func TestLocalSwarmRoleLegacyFallbacks(t *testing.T) {
	tests := []struct {
		name string
		cfg  startupconfig.FileConfig
		want string
	}{
		{
			name: "standalone when swarm off",
			cfg:  startupconfig.FileConfig{SwarmMode: false, Child: true, SwarmRole: startupconfig.SwarmRoleManaged},
			want: bootstrapRoleStandalone,
		},
		{
			name: "managed overrides child when swarm on",
			cfg:  startupconfig.FileConfig{SwarmMode: true, Child: true, SwarmRole: startupconfig.SwarmRoleManaged},
			want: bootstrapRoleManaged,
		},
		{
			name: "legacy child",
			cfg:  startupconfig.FileConfig{SwarmMode: true, Child: true},
			want: bootstrapRoleChild,
		},
		{
			name: "legacy master",
			cfg:  startupconfig.FileConfig{SwarmMode: true},
			want: bootstrapRoleMaster,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := localSwarmRole(tt.cfg); got != tt.want {
				t.Fatalf("localSwarmRole() = %q, want %q", got, tt.want)
			}
		})
	}
}
