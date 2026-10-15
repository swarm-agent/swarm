package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/notification"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"

	"swarm/packages/swarmd/internal/workspace"
)

func sessionCookieFromRecorder(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, desktopLocalSessionCookieName+"_") {
			if strings.TrimSpace(cookie.Value) == "" {
				t.Fatalf("expected %q cookie to have a value", desktopLocalSessionCookieName)
			}
			return cookie
		}
	}
	t.Fatalf("expected %q cookie to be issued", desktopLocalSessionCookieName)
	return nil
}

func newLocalAuthTestServer(t *testing.T) *Server {
	t.Helper()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "local-auth-substrate.pebble"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("create event log: %v", err)
	}
	authSvc := auth.NewService(pebblestore.NewAuthStore(store), eventLog)
	identityStore := pebblestore.NewIdentityStore(store)
	if _, err := identity.NewService(identityStore, identity.WithIDGenerator(func(prefix string) (string, error) {
		switch prefix {
		case "user":
			return "user_local_auth_test", nil
		case "team":
			return "team_local_auth_test", nil
		default:
			return prefix + "_local_auth_test", nil
		}
	})).BootstrapFirstIdentity("local-auth-test-user"); err != nil {
		t.Fatalf("bootstrap local auth identity: %v", err)
	}
	securitySvc := security.NewService(pebblestore.NewClientAuthStore(store), eventLog)
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		t.Fatalf("ensure attach auth: %v", err)
	}
	workspaceSvc := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	hub := stream.NewHub(eventLog)
	notificationSvc := notification.NewService(pebblestore.NewNotificationStore(store), eventLog, hub.Publish)
	server := NewServer(authSvc, nil, nil, nil, nil, workspaceSvc, nil, securitySvc, nil, nil, notificationSvc, eventLog, hub)
	server.SetIdentityService(identity.NewService(identityStore))
	server.SetIdentitySessionService(identity.NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(store)))
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "local-auth-test", Name: "Local Auth Test", Role: "standalone", PublicKey: "local-auth-public-key", Fingerprint: "local-auth-fingerprint"}}}

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.Host = startupconfig.DefaultHost
	cfg.Port = startupconfig.DefaultPort
	cfg.DesktopPort = startupconfig.DefaultDesktopPort
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	server.SetSwarmDesktopTargetSelectionStore(nil)
	return server
}

type fakeLocalAuthSwarmCalls struct {
	ensureLocalState int
	upsertGroup      int
}

type fakeLocalAuthSwarmService struct {
	state                  swarmruntime.LocalState
	outgoingPeerAuthTokens map[string]string
	calls                  *fakeLocalAuthSwarmCalls
}

func (f fakeLocalAuthSwarmService) EnsureLocalState(swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	if f.calls != nil {
		f.calls.ensureLocalState++
	}
	return f.state, nil
}

func (f fakeLocalAuthSwarmService) RenameLocalSwarm(input swarmruntime.RenameLocalSwarmInput) (swarmruntime.LocalState, error) {
	state := f.state
	state.Node.Name = strings.TrimSpace(input.Name)
	return state, nil
}

func (f fakeLocalAuthSwarmService) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) {
	return nil, "", nil
}

func (f fakeLocalAuthSwarmService) UpsertGroup(swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	if f.calls != nil {
		f.calls.upsertGroup++
	}
	return swarmruntime.Group{}, nil
}

func (f fakeLocalAuthSwarmService) DeleteGroup(string) error { return nil }

func (f fakeLocalAuthSwarmService) SetCurrentGroup(string, string) (swarmruntime.GroupState, error) {
	return swarmruntime.GroupState{}, nil
}

func (f fakeLocalAuthSwarmService) UpsertGroupMember(swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{}, nil
}

func (f fakeLocalAuthSwarmService) RemoveGroupMember(swarmruntime.RemoveGroupMemberInput) error {
	return nil
}
