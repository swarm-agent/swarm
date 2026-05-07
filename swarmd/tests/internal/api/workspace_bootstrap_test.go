package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	api "swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/discovery"
	providerdefaults "swarm/packages/swarmd/internal/provider/defaults"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/workspace"
)

func TestWorkspaceBootstrapReturnsWorkspaceDesktopPayload(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-bootstrap.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	workspaceSvc := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	discoverySvc := discovery.NewService()
	server := newWorkspaceBootstrapTestServer(t, eventLog, sessionSvc, workspaceSvc, discoverySvc)
	handler := server.Handler()

	root := t.TempDir()
	workspaceA := filepath.Join(root, "workspace-a")
	workspaceB := filepath.Join(root, "workspace-b")
	for _, dir := range []string{workspaceA, workspaceB} {
		if err := osMkdirAll(dir); err != nil {
			t.Fatalf("mkdir workspace: %v", err)
		}
	}

	if _, err := workspaceSvc.Add(workspaceA, "Workspace A", "", true); err != nil {
		t.Fatalf("add workspace a: %v", err)
	}
	if _, err := workspaceSvc.Add(workspaceB, "Workspace B", "", false); err != nil {
		t.Fatalf("add workspace b: %v", err)
	}

	defaults := providerdefaults.MustLookup("codex")
	sessionA, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "A1",
		WorkspacePath: workspaceA,
		WorkspaceName: "Workspace A",
		Preference: &pebblestore.ModelPreference{
			Provider: defaults.ProviderID,
			Model:    defaults.PrimaryModel,
			Thinking: defaults.PrimaryThinking,
		},
	})
	if err != nil {
		t.Fatalf("create session a: %v", err)
	}
	sessionB, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "B1",
		WorkspacePath: workspaceB,
		WorkspaceName: "Workspace B",
		Preference: &pebblestore.ModelPreference{
			Provider: defaults.ProviderID,
			Model:    defaults.PrimaryModel,
			Thinking: defaults.PrimaryThinking,
		},
	})
	if err != nil {
		t.Fatalf("create session b: %v", err)
	}
	_, _, _, _ = sessionSvc.AppendMessage(sessionB.ID, "user", "hello", nil)

	var resp struct {
		OK               bool `json:"ok"`
		CurrentWorkspace struct {
			ResolvedPath string `json:"resolved_path"`
		} `json:"current_workspace"`
		Workspaces []struct {
			Path     string `json:"path"`
			Active   bool   `json:"active"`
			Sessions []struct {
				ID                     string `json:"id"`
				WorkspacePath          string `json:"workspace_path"`
				PendingPermissionCount int    `json:"pending_permission_count"`
			} `json:"sessions"`
		} `json:"workspaces"`
		Directories []struct {
			Path string `json:"path"`
		} `json:"directories"`
	}

	status := doJSONRequest(t, handler, http.MethodGet, "/v1/workspace/overview?session_limit=500&workspace_limit=200&discover_limit=200", nil, &resp)
	if status != http.StatusOK {
		status, body := doRequest(t, handler, http.MethodGet, "/v1/workspace/overview?session_limit=500&workspace_limit=200&discover_limit=200")
		t.Fatalf("bootstrap status=%d body=%s", status, body)
	}
	if !resp.OK {
		t.Fatal("expected ok=true")
	}
	if resp.CurrentWorkspace.ResolvedPath != workspaceA {
		t.Fatalf("current workspace = %q, want %q", resp.CurrentWorkspace.ResolvedPath, workspaceA)
	}
	if len(resp.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(resp.Workspaces))
	}
	totalSessions := 0
	seen := map[string]string{}
	for _, workspaceEntry := range resp.Workspaces {
		totalSessions += len(workspaceEntry.Sessions)
		for _, session := range workspaceEntry.Sessions {
			seen[session.ID] = session.WorkspacePath
		}
	}
	if totalSessions != 2 {
		t.Fatalf("sessions = %d, want 2", totalSessions)
	}
	if seen[sessionA.ID] != workspaceA {
		t.Fatalf("session A workspace = %q, want %q", seen[sessionA.ID], workspaceA)
	}
	if seen[sessionB.ID] != workspaceB {
		t.Fatalf("session B workspace = %q, want %q", seen[sessionB.ID], workspaceB)
	}
}

func TestWorkspaceBootstrapPrefersStandaloneChildWorkspaceForNestedLink(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-bootstrap-nested.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	workspaceSvc := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	discoverySvc := discovery.NewService()
	server := newWorkspaceBootstrapTestServer(t, eventLog, sessionSvc, workspaceSvc, discoverySvc)
	handler := server.Handler()

	root := t.TempDir()
	swarmWeb := filepath.Join(root, "swarm-web")
	swarmGo := filepath.Join(root, "swarm-go")
	swarmWebUI := filepath.Join(swarmWeb, "ui")
	for _, dir := range []string{swarmWeb, swarmGo, swarmWebUI} {
		if err := osMkdirAll(dir); err != nil {
			t.Fatalf("mkdir workspace: %v", err)
		}
	}

	if _, err := workspaceSvc.Add(swarmWeb, "swarm-web", "", false); err != nil {
		t.Fatalf("add swarm-web: %v", err)
	}
	if _, err := workspaceSvc.Add(swarmGo, "swarm-go", "", true); err != nil {
		t.Fatalf("add swarm-go: %v", err)
	}
	if _, err := workspaceSvc.AddDirectory(swarmWeb, swarmGo); err != nil {
		t.Fatalf("link swarm-go into swarm-web: %v", err)
	}
	if _, err := workspaceSvc.AddDirectory(swarmWeb, swarmWebUI); err != nil {
		t.Fatalf("link swarm-web/ui into swarm-web: %v", err)
	}

	defaults := providerdefaults.MustLookup("codex")
	swarmGoSession, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "go-1",
		WorkspacePath: swarmGo,
		WorkspaceName: "swarm-go",
		Preference: &pebblestore.ModelPreference{
			Provider: defaults.ProviderID,
			Model:    defaults.PrimaryModel,
			Thinking: defaults.PrimaryThinking,
		},
	})
	if err != nil {
		t.Fatalf("create swarm-go session: %v", err)
	}
	_, _, err = sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "web-1",
		WorkspacePath: swarmWeb,
		WorkspaceName: "swarm-web",
		Preference: &pebblestore.ModelPreference{
			Provider: defaults.ProviderID,
			Model:    defaults.PrimaryModel,
			Thinking: defaults.PrimaryThinking,
		},
	})
	if err != nil {
		t.Fatalf("create swarm-web session: %v", err)
	}

	var overviewResp struct {
		OK               bool `json:"ok"`
		CurrentWorkspace struct {
			WorkspacePath string `json:"workspace_path"`
			ResolvedPath  string `json:"resolved_path"`
		} `json:"current_workspace"`
		Workspaces []struct {
			Path        string   `json:"path"`
			Directories []string `json:"directories"`
			Active      bool     `json:"active"`
			Sessions    []struct {
				ID            string `json:"id"`
				WorkspacePath string `json:"workspace_path"`
			} `json:"sessions"`
		} `json:"workspaces"`
	}
	status := doJSONRequest(t, handler, http.MethodGet, "/v1/workspace/overview?cwd="+swarmGo+"&session_limit=500&workspace_limit=200&discover_limit=200", nil, &overviewResp)
	if status != http.StatusOK {
		status, body := doRequest(t, handler, http.MethodGet, "/v1/workspace/overview?cwd="+swarmGo+"&session_limit=500&workspace_limit=200&discover_limit=200")
		t.Fatalf("overview status=%d body=%s", status, body)
	}
	if !overviewResp.OK {
		t.Fatal("expected ok=true")
	}
	if overviewResp.CurrentWorkspace.WorkspacePath != swarmGo {
		t.Fatalf("current workspace path = %q, want %q", overviewResp.CurrentWorkspace.WorkspacePath, swarmGo)
	}
	childDirs := []string(nil)
	for _, ws := range overviewResp.Workspaces {
		if ws.Path != swarmGo {
			continue
		}
		childDirs = append([]string(nil), ws.Directories...)
		if len(ws.Sessions) != 1 || ws.Sessions[0].ID != swarmGoSession.ID {
			t.Fatalf("swarm-go sessions = %+v, want only %s", ws.Sessions, swarmGoSession.ID)
		}
	}
	if len(childDirs) != 1 || childDirs[0] != swarmGo {
		t.Fatalf("swarm-go directories = %#v, want only %q", childDirs, swarmGo)
	}

	var sessionsResp struct {
		Sessions []struct {
			ID            string `json:"id"`
			WorkspacePath string `json:"workspace_path"`
		} `json:"sessions"`
	}
	status = doJSONRequest(t, handler, http.MethodGet, "/v1/sessions?cwd="+swarmGo+"&limit=100", nil, &sessionsResp)
	if status != http.StatusOK {
		t.Fatalf("sessions status=%d", status)
	}
	if len(sessionsResp.Sessions) != 1 {
		t.Fatalf("sessions len = %d, want 1", len(sessionsResp.Sessions))
	}
	if sessionsResp.Sessions[0].WorkspacePath != swarmGo {
		t.Fatalf("session workspace path = %q, want %q", sessionsResp.Sessions[0].WorkspacePath, swarmGo)
	}
}

func newWorkspaceBootstrapTestServer(t *testing.T, eventLog *pebblestore.EventLog, sessionSvc *sessionruntime.Service, workspaceSvc *workspace.Service, discoverySvc *discovery.Service) *api.Server {
	t.Helper()
	server := api.NewServer("test", nil, nil, nil, nil, sessionSvc, workspaceSvc, discoverySvc, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = true
	cfg.SwarmName = "test-swarm"
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	cfg.DesktopPort = 5555
	cfg.NetworkMode = startupconfig.NetworkModeLAN
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	server.SetSwarmService(fakeWorkspaceBootstrapSwarmService{
		state: swarmruntime.LocalState{
			Node: swarmruntime.LocalNodeState{
				SwarmID: "test-swarm-id",
				Name:    "test-swarm",
				Role:    "master",
			},
		},
	})
	return server
}

type fakeWorkspaceBootstrapSwarmService struct {
	state swarmruntime.LocalState
}

func (f fakeWorkspaceBootstrapSwarmService) EnsureLocalState(input swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	return f.state, nil
}

func (f fakeWorkspaceBootstrapSwarmService) ListGroupsForSwarm(swarmID string, limit int) ([]swarmruntime.GroupState, string, error) {
	return nil, "", nil
}

func (f fakeWorkspaceBootstrapSwarmService) UpsertGroup(input swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	return swarmruntime.Group{}, nil
}

func (f fakeWorkspaceBootstrapSwarmService) DeleteGroup(groupID string) error {
	return nil
}

func (f fakeWorkspaceBootstrapSwarmService) SetCurrentGroup(groupID string, localSwarmID string) (swarmruntime.GroupState, error) {
	return swarmruntime.GroupState{}, nil
}

func (f fakeWorkspaceBootstrapSwarmService) OutgoingPeerAuthToken(swarmID string) (string, bool, error) {
	return "", false, nil
}

func (f fakeWorkspaceBootstrapSwarmService) ValidateIncomingPeerAuth(swarmID, rawToken string) (bool, error) {
	return false, nil
}

func (f fakeWorkspaceBootstrapSwarmService) UpsertGroupMember(input swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{}, nil
}

func (f fakeWorkspaceBootstrapSwarmService) RemoveGroupMember(input swarmruntime.RemoveGroupMemberInput) error {
	return nil
}

func (f fakeWorkspaceBootstrapSwarmService) CreateInvite(input swarmruntime.CreateInviteInput) (swarmruntime.Invite, error) {
	return swarmruntime.Invite{}, nil
}

func (f fakeWorkspaceBootstrapSwarmService) SubmitEnrollment(input swarmruntime.SubmitEnrollmentInput) (swarmruntime.Enrollment, error) {
	return swarmruntime.Enrollment{}, nil
}

func (f fakeWorkspaceBootstrapSwarmService) ListPendingEnrollments(limit int) ([]swarmruntime.Enrollment, error) {
	return nil, nil
}

func (f fakeWorkspaceBootstrapSwarmService) DecideEnrollment(input swarmruntime.DecideEnrollmentInput) (swarmruntime.Enrollment, []swarmruntime.TrustedPeer, error) {
	return swarmruntime.Enrollment{}, nil, nil
}

func (f fakeWorkspaceBootstrapSwarmService) PrepareRemoteBootstrapParentPeer(input swarmruntime.PrepareRemoteBootstrapParentPeerInput) error {
	return nil
}

func (f fakeWorkspaceBootstrapSwarmService) ApproveManagedPairing(input swarmruntime.ApproveManagedPairingInput) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeWorkspaceBootstrapSwarmService) UpdateLocalPairingFromConfig(cfg startupconfig.FileConfig, transports []swarmruntime.TransportSummary) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeWorkspaceBootstrapSwarmService) DetachToStandalone(localSwarmID string) error {
	return nil
}

func osMkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}

func doJSONRequest(t *testing.T, handler http.Handler, method, path string, body any, out any) int {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	handler.ServeHTTP(recorder, request)
	if out != nil {
		if err := json.NewDecoder(recorder.Body).Decode(out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return recorder.Code
}

func doRequest(t *testing.T, handler http.Handler, method, path string) (int, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}
