package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

var errTestRemoteUpdateFailure = errors.New("remote update failed")

func TestRoutedSessionMessagesReloadFromHostWithoutProxy(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)
	if _, _, _, err := sessionSvc.AppendMessage(sessionID, "user", "hello from host mirror", nil); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/messages?limit=10", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(payload.Messages))
	}
	if payload.Messages[0].Content != "hello from host mirror" {
		t.Fatalf("message content = %q, want %q", payload.Messages[0].Content, "hello from host mirror")
	}
}

func TestRoutedSessionMessagePostUsesStoredRouteWithoutSwarmID(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var requestPath atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		requestPath.Store(r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer child.Close()

	sessionID := "session-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      child.URL,
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	body := bytes.NewBufferString(`{"role":"user","content":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sessionID+"/messages", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1", hits.Load())
	}
	if got, _ := requestPath.Load().(string); got != "/v1/sessions/"+sessionID+"/messages" {
		t.Fatalf("child path = %q, want %q", got, "/v1/sessions/"+sessionID+"/messages")
	}
}

func TestRoutedRunStreamControlUsesStoredRouteWithoutSwarmID(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var requestPath atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		requestPath.Store(r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": "session-routed", "run_id": "run-1"})
	}))
	defer child.Close()

	sessionID := "session-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      child.URL,
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	body := bytes.NewBufferString(`{"type":"run.start","prompt":"hello","background":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sessionID+"/run/stream", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1", hits.Load())
	}
	if got, _ := requestPath.Load().(string); got != "/v1/sessions/"+sessionID+"/run/stream" {
		t.Fatalf("child path = %q, want %q", got, "/v1/sessions/"+sessionID+"/run/stream")
	}
}

func TestRoutedSessionPermissionsReadAndResolveFromHostWithoutProxy(t *testing.T) {
	server, sessionSvc, permSvc, routeStore := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	record, err := permSvc.CreatePending(permission.CreateInput{
		SessionID:     sessionID,
		RunID:         "run-1",
		CallID:        "call-1",
		ToolName:      "bash",
		ToolArguments: `{"cmd":"pwd"}`,
		Requirement:   "tool",
		Mode:          "plan",
	})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/permissions?limit=200", nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("permission list status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	var getPayload struct {
		Count       int `json:"count"`
		Permissions []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode permission list: %v", err)
	}
	if getPayload.Count != 1 || len(getPayload.Permissions) != 1 {
		t.Fatalf("permission count = %d/%d, want 1", getPayload.Count, len(getPayload.Permissions))
	}
	if getPayload.Permissions[0].ID != record.ID || getPayload.Permissions[0].Status != pebblestore.PermissionStatusPending {
		t.Fatalf("unexpected permission payload: %+v", getPayload.Permissions[0])
	}

	resolveReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sessionID+"/permissions/"+record.ID+"/resolve", bytes.NewBufferString(`{"action":"approve","reason":"ok"}`))
	resolveReq.Header.Set("Content-Type", "application/json")
	resolveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(resolveRec, resolveReq)
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("permission resolve status = %d, want %d, body=%s", resolveRec.Code, http.StatusOK, resolveRec.Body.String())
	}
	var resolvePayload struct {
		Permission struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"permission"`
	}
	if err := json.Unmarshal(resolveRec.Body.Bytes(), &resolvePayload); err != nil {
		t.Fatalf("decode permission resolve: %v", err)
	}
	if resolvePayload.Permission.ID != record.ID || resolvePayload.Permission.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("unexpected resolved permission: %+v", resolvePayload.Permission)
	}
}

func TestSessionsListWithSwarmIDReadsHostWithoutProxy(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	server.SetDeployContainerService(&fakeReplicateDeployService{})
	sessionID := seedRoutedSession(t, sessionSvc)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions?swarm_id=child-swarm-1", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Sessions) != 1 || payload.Sessions[0].ID != sessionID {
		t.Fatalf("session list = %+v, want session %q", payload.Sessions, sessionID)
	}
}

func TestRoutedSessionPreferenceReadFromHostWithoutProxy(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/preference", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Preference struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Thinking string `json:"thinking"`
		} `json:"preference"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Preference.Provider != "codex" || payload.Preference.Model != "gpt-5.4" || payload.Preference.Thinking != "medium" {
		t.Fatalf("unexpected preference payload: %+v", payload.Preference)
	}
}

func TestRemoteDeploySessionCreateUsesRemotePayloadTargetPath(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var openedWorkspacePath atomic.Value
	var openedHostBackendURL atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		var req peerSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode child request: %v", err)
		}
		openedWorkspacePath.Store(req.Request.RuntimeWorkspacePath)
		hostBackendURL := req.Hosted.HostBackendURL
		openedHostBackendURL.Store(hostBackendURL)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer child.Close()

	server.SetRemoteDeployService(&fakeRemoteDeployService{
		sessions: []remotedeploy.Session{{
			ID:               "remote-deploy-1",
			Name:             "remote-child",
			Status:           "attached",
			ChildSwarmID:     "child-swarm",
			HostAPIBaseURL:   "https://remote-host.tailnet.ts.net",
			RemoteTailnetURL: child.URL,
			Preflight: remotedeploy.SessionPreflight{
				Payloads: []remotedeploy.SessionPayload{{
					WorkspacePath: "/src/swarm-go",
					WorkspaceName: "swarm-go",
					TargetPath:    "/workspaces/swarm-go",
				}},
			},
		}},
	})

	body := bytes.NewBufferString(`{"title":"remote","mode":"plan","workspace_path":"/src/swarm-go","workspace_name":"swarm-go","preference":{"provider":"fireworks","model":"accounts/fireworks/models/kimi-k2p5","thinking":"high"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=child-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, _ := openedWorkspacePath.Load().(string); got != "/workspaces/swarm-go" {
		t.Fatalf("child workspace path = %q, want %q", got, "/workspaces/swarm-go")
	}
	if got, _ := openedHostBackendURL.Load().(string); got != "https://remote-host.tailnet.ts.net" {
		t.Fatalf("child host backend url = %q, want %q", got, "https://remote-host.tailnet.ts.net")
	}
	var payload struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	route, ok, err := routeStore.Get(payload.Session.ID)
	if err != nil {
		t.Fatalf("load route: %v", err)
	}
	if !ok {
		t.Fatalf("route missing for session %q", payload.Session.ID)
	}
	if route.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("runtime workspace path = %q, want %q", route.RuntimeWorkspacePath, "/workspaces/swarm-go")
	}
}

func TestRemoteDeploySessionStartForwardsLaunchOnlyTailscaleAuthKey(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	fake := &fakeRemoteDeployService{
		startResult: remotedeploy.Session{
			ID:     "remote-start-1",
			Name:   "remote-child",
			Status: "waiting_for_approval",
		},
	}
	server.SetRemoteDeployService(fake)

	body := bytes.NewBufferString(`{"session_id":"remote-start-1","tailscale_auth_key":"tskey-launch-only"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/remote/session/start", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if fake.lastStartInput.SessionID != "remote-start-1" {
		t.Fatalf("start session id = %q, want %q", fake.lastStartInput.SessionID, "remote-start-1")
	}
	if fake.lastStartInput.TailscaleAuthKey != "tskey-launch-only" {
		t.Fatalf("start tailscale auth key = %q, want %q", fake.lastStartInput.TailscaleAuthKey, "tskey-launch-only")
	}
}

func TestRemoteDeploySessionUpdateJobReturnsPartialResultOnConflict(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	fake := &fakeRemoteDeployService{
		updateJobResult: remotedeploy.UpdateJobResult{
			PathID: remotedeploy.PathSessionUpdateJob,
			Summary: remotedeploy.UpdateJobSummary{
				Total:  1,
				Failed: 1,
			},
		},
		updateJobErr: errTestRemoteUpdateFailure,
	}
	server.SetRemoteDeployService(fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/remote/session/update-job", bytes.NewBufferString(`{"dev_mode":true,"post_rebuild_check":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var payload struct {
		OK     bool                         `json:"ok"`
		Result remotedeploy.UpdateJobResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.OK || payload.Result.Summary.Failed != 1 {
		t.Fatalf("payload = %+v", payload)
	}
}

func newRoutedSessionTestServer(t *testing.T) (*Server, *sessionruntime.Service, *permission.Service, *pebblestore.SessionRouteStore) {
	t.Helper()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "routed-session-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	permissionSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure agent defaults: %v", err)
	}
	routeStore := pebblestore.NewSessionRouteStore(store)
	server := NewServer("test", nil, agentSvc, modelSvc, nil, sessionSvc, nil, nil, nil, nil, permissionSvc, nil, eventLog, stream.NewHub(eventLog))
	server.SetSessionRouteStore(routeStore)
	server.SetSwarmService(fakeRoutedSwarmService{
		state: swarmruntime.LocalState{
			Node: swarmruntime.LocalNodeState{
				SwarmID: "host-swarm-id",
				Name:    "host-swarm",
				Role:    "master",
			},
		},
		token: "peer-token",
	})

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = true
	cfg.SwarmName = "host-swarm"
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)

	return server, sessionSvc, permissionSvc, routeStore
}

func seedRoutedSession(t *testing.T, sessionSvc *sessionruntime.Service) string {
	t.Helper()

	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     "session-routed",
		Title:         "Routed Session",
		WorkspacePath: "/host/workspace",
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModePlan,
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return session.ID
}

type fakeRoutedSwarmService struct {
	state swarmruntime.LocalState
	token string
}

type fakeRemoteDeployService struct {
	sessions        []remotedeploy.Session
	lastStartInput  remotedeploy.StartSessionInput
	startResult     remotedeploy.Session
	startErr        error
	updateJobResult remotedeploy.UpdateJobResult
	updateJobErr    error
}

func (f *fakeRemoteDeployService) List(_ context.Context) ([]remotedeploy.Session, error) {
	return append([]remotedeploy.Session(nil), f.sessions...), nil
}

func (f *fakeRemoteDeployService) ListCached(_ context.Context) ([]remotedeploy.Session, error) {
	return append([]remotedeploy.Session(nil), f.sessions...), nil
}

func (f *fakeRemoteDeployService) Create(_ context.Context, input remotedeploy.CreateSessionInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeRemoteDeployService) Delete(_ context.Context, input remotedeploy.DeleteSessionInput) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f *fakeRemoteDeployService) Start(_ context.Context, input remotedeploy.StartSessionInput) (remotedeploy.Session, error) {
	f.lastStartInput = input
	if f.startErr != nil {
		return remotedeploy.Session{}, f.startErr
	}
	return f.startResult, nil
}

func (f *fakeRemoteDeployService) RunUpdateJob(_ context.Context, input remotedeploy.UpdateJobInput) (remotedeploy.UpdateJobResult, error) {
	return f.updateJobResult, f.updateJobErr
}

func (f *fakeRemoteDeployService) Approve(_ context.Context, input remotedeploy.ApproveSessionInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeRemoteDeployService) ChildStatus(_ context.Context, input remotedeploy.ChildStatusInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeRemoteDeployService) SyncCredentialBundle(_ context.Context, input remotedeploy.SyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error) {
	return deployruntime.ContainerSyncCredentialBundle{}, nil
}

func (f fakeRoutedSwarmService) EnsureLocalState(swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	return f.state, nil
}

func (f fakeRoutedSwarmService) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) {
	return nil, "", nil
}

func (f fakeRoutedSwarmService) UpsertGroup(swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	return swarmruntime.Group{}, nil
}

func (f fakeRoutedSwarmService) DeleteGroup(string) error {
	return nil
}

func (f fakeRoutedSwarmService) SetCurrentGroup(string, string) (swarmruntime.GroupState, error) {
	return swarmruntime.GroupState{}, nil
}

func (f fakeRoutedSwarmService) OutgoingPeerAuthToken(string) (string, bool, error) {
	return f.token, true, nil
}

func (f fakeRoutedSwarmService) ValidateIncomingPeerAuth(string, string) (bool, error) {
	return true, nil
}

func (f fakeRoutedSwarmService) UpsertGroupMember(swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{}, nil
}

func (f fakeRoutedSwarmService) RemoveGroupMember(swarmruntime.RemoveGroupMemberInput) error {
	return nil
}

func (f fakeRoutedSwarmService) CreateInvite(swarmruntime.CreateInviteInput) (swarmruntime.Invite, error) {
	return swarmruntime.Invite{}, nil
}

func (f fakeRoutedSwarmService) SubmitEnrollment(swarmruntime.SubmitEnrollmentInput) (swarmruntime.Enrollment, error) {
	return swarmruntime.Enrollment{}, nil
}

func (f fakeRoutedSwarmService) ListPendingEnrollments(int) ([]swarmruntime.Enrollment, error) {
	return nil, nil
}

func (f fakeRoutedSwarmService) DecideEnrollment(swarmruntime.DecideEnrollmentInput) (swarmruntime.Enrollment, []swarmruntime.TrustedPeer, error) {
	return swarmruntime.Enrollment{}, nil, nil
}

func (f fakeRoutedSwarmService) PrepareRemoteBootstrapParentPeer(swarmruntime.PrepareRemoteBootstrapParentPeerInput) error {
	return nil
}

func (f fakeRoutedSwarmService) FinalizeRemoteBootstrapChildPairing(swarmruntime.FinalizeRemoteBootstrapChildPairingInput) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeRoutedSwarmService) UpdateLocalPairingFromConfig(startupconfig.FileConfig, []swarmruntime.TransportSummary) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeRoutedSwarmService) DetachToStandalone(string) error {
	return nil
}

var _ swarmService = fakeRoutedSwarmService{}
