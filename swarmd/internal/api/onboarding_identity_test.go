package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/notification"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/workspace"
)

func TestOnboardingGetReportsUnbootstrappedIdentityWithoutCreatingRecords(t *testing.T) {
	server, identityStore := newOnboardingIdentityTestServer(t, false)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newSameOriginDesktopRequest(http.MethodGet, "/v1/onboarding"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/onboarding status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status onboardingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode onboarding response: %v", err)
	}
	if !status.NeedsOnboarding || status.Identity.Bootstrapped || status.Identity.UserID != "" || status.Identity.Username != "" {
		t.Fatalf("identity status = %+v needs=%v", status.Identity, status.NeedsOnboarding)
	}
	counts, err := identityStore.IdentityCounts()
	if err != nil {
		t.Fatalf("identity counts: %v", err)
	}
	if counts != (pebblestore.IdentityCounts{}) {
		t.Fatalf("GET /v1/onboarding created identity counts=%+v", counts)
	}
}

func TestOnboardingPostRequiresUsernameAndSwarmNameBeforeBootstrap(t *testing.T) {
	server, identityStore := newOnboardingIdentityTestServer(t, false)

	testCases := []struct {
		name    string
		payload map[string]any
		wantErr string
	}{
		{
			name:    "missing username",
			payload: map[string]any{"swarm_name": "Device One"},
			wantErr: "username",
		},
		{
			name:    "missing swarm name",
			payload: map[string]any{"username": "alice"},
			wantErr: "swarm name",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, newJSONSameOriginDesktopRequest(t, tc.payload))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Fatalf("status=%d body=%s want error containing %q", rec.Code, rec.Body.String(), tc.wantErr)
			}
			counts, err := identityStore.IdentityCounts()
			if err != nil {
				t.Fatalf("identity counts: %v", err)
			}
			if counts != (pebblestore.IdentityCounts{}) {
				t.Fatalf("failed onboarding created identity counts=%+v", counts)
			}
		})
	}
}

func TestOnboardingPostBootstrapsIdentityAndIssuesSession(t *testing.T) {
	server, identityStore, swarmCalls := newOnboardingIdentityTestServerWithCalls(t, false)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newJSONSameOriginDesktopRequest(t, map[string]any{"username": "Alice", "swarm_name": "Alice Laptop"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookieFromRecorder(t, rec)
	if parts := strings.Split(cookie.Value, "."); len(parts) != 3 {
		t.Fatalf("session cookie is not compact JWT: %q", cookie.Value)
	}
	var status onboardingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if status.NeedsOnboarding || !status.Identity.Bootstrapped || status.Identity.UserID != "user_onboarding_test" || status.Identity.Username != "alice" {
		t.Fatalf("bootstrap response identity=%+v needs=%v", status.Identity, status.NeedsOnboarding)
	}
	if status.Session == nil || strings.TrimSpace(status.Session.ExpiresAt) == "" {
		t.Fatalf("bootstrap response missing session metadata: %+v", status.Session)
	}
	if swarmCalls.ensureLocalState != 1 || swarmCalls.upsertGroup != 0 {
		t.Fatalf("identity onboarding called swarm group/linking APIs: ensureLocalState=%d upsertGroup=%d", swarmCalls.ensureLocalState, swarmCalls.upsertGroup)
	}
	counts, err := identityStore.IdentityCounts()
	if err != nil {
		t.Fatalf("identity counts: %v", err)
	}
	want := pebblestore.IdentityCounts{Users: 1, AccountScopes: 1, AccountUsers: 1, CurrentSelections: 1}
	if counts != want {
		t.Fatalf("identity counts=%+v want %+v", counts, want)
	}
	user, ok, err := identityStore.GetUser("user_onboarding_test")
	if err != nil || !ok {
		t.Fatalf("get bootstrapped user ok=%v err=%v", ok, err)
	}
	if user.Username != "alice" || status.Config.SwarmName != "Alice Laptop" {
		t.Fatalf("username/swarmName not separate: user=%+v config=%+v", user, status.Config)
	}
}

func TestOnboardingPostRejectsRebootstrapAndSwarmNameUpdateDoesNotMutateUsername(t *testing.T) {
	server, identityStore := newOnboardingIdentityTestServer(t, true)

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newJSONSameOriginDesktopRequest(t, map[string]any{"username": "bob", "swarm_name": "New Device"}))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), identity.ErrBootstrapExists.Error()) {
		t.Fatalf("rebootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, newJSONSameOriginDesktopRequest(t, map[string]any{"swarm_name": "Renamed Device"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("swarm name update status=%d body=%s", rec.Code, rec.Body.String())
	}
	user, ok, err := identityStore.GetUser("user_onboarding_test")
	if err != nil || !ok {
		t.Fatalf("get user after update ok=%v err=%v", ok, err)
	}
	if user.Username != "alice" {
		t.Fatalf("username mutated to %q", user.Username)
	}
	var status onboardingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if status.Config.SwarmName != "Renamed Device" || status.Identity.Username != "alice" {
		t.Fatalf("update response=%+v", status)
	}
	counts, err := identityStore.IdentityCounts()
	if err != nil {
		t.Fatalf("identity counts: %v", err)
	}
	want := pebblestore.IdentityCounts{Users: 1, AccountScopes: 1, AccountUsers: 1, CurrentSelections: 1}
	if counts != want {
		t.Fatalf("identity counts=%+v want %+v", counts, want)
	}
}

func newOnboardingIdentityTestServer(t *testing.T, bootstrap bool) (*Server, *pebblestore.IdentityStore) {
	t.Helper()
	server, identityStore, _ := newOnboardingIdentityTestServerWithCalls(t, bootstrap)
	return server, identityStore
}

func newOnboardingIdentityTestServerWithCalls(t *testing.T, bootstrap bool) (*Server, *pebblestore.IdentityStore, *fakeLocalAuthSwarmCalls) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "onboarding-identity.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	authSvc := auth.NewService(pebblestore.NewAuthStore(store), eventLog)
	securitySvc := security.NewService(pebblestore.NewClientAuthStore(store), eventLog)
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		t.Fatalf("ensure attach auth: %v", err)
	}
	workspaceSvc := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	hub := stream.NewHub(eventLog)
	notificationSvc := notification.NewService(pebblestore.NewNotificationStore(store), eventLog, hub.Publish)
	identityStore := pebblestore.NewIdentityStore(store)
	identitySvc := identity.NewService(identityStore, identity.WithIDGenerator(onboardingIdentityTestIDGenerator))
	if bootstrap {
		if _, err := identitySvc.BootstrapFirstIdentity("alice"); err != nil {
			t.Fatalf("bootstrap identity: %v", err)
		}
	}
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	server := NewServer("test", authSvc, agentSvc, nil, nil, nil, workspaceSvc, nil, securitySvc, nil, nil, notificationSvc, eventLog, hub)
	server.SetIdentityService(identitySvc)
	server.SetIdentitySessionService(identity.NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(store)))
	server.SetUISettingsService(uisettings.NewService(pebblestore.NewUISettingsStore(store)))
	swarmCalls := &fakeLocalAuthSwarmCalls{}
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "onboarding-identity-test", Name: "Onboarding Identity Test", Role: "standalone"}}, calls: swarmCalls}

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmName = ""
	if bootstrap {
		cfg.SwarmName = "Original Device"
	}
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	return server, identityStore, swarmCalls
}

func onboardingIdentityTestIDGenerator(prefix string) (string, error) {
	switch prefix {
	case "user":
		return "user_onboarding_test", nil
	case "acct":
		return "acct_onboarding_test", nil
	default:
		return "", errors.New("unexpected identity prefix")
	}
}

func newJSONSameOriginDesktopRequest(t *testing.T, payload map[string]any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := newSameOriginDesktopRequest(http.MethodPost, "/v1/onboarding")
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}
