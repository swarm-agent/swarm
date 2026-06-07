package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/identity"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/security"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestPeerSessionOpenRejectsNonLocalManagedHostContainerRoute(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "child-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "managed child",
		Relationship:         "child",
		BackendURL:           "http://127.0.0.1:7782",
		OwnerHostSwarmID:     "managed-swarm",
		OwnerHostContainerID: "managed-container",
	}); err != nil {
		t.Fatalf("upsert child runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:        "managed-swarm",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Name:           "managed host",
		Relationship:   "managed",
		BackendURL:     "https://managed.example.test",
	}); err != nil {
		t.Fatalf("upsert managed runtime: %v", err)
	}

	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-managed-child",
		Request: func() sessionCreateRequest {
			req := sessionCreateRequest{
				Title:                "managed child",
				WorkspacePath:        "/workspaces/swarm-go",
				HostWorkspacePath:    "/workspaces/swarm-go",
				RuntimeWorkspacePath: "/workspaces/swarm-go",
				WorkspaceBindingID:   "binding-managed-child",
				WorkspaceName:        "swarm-go",
				Mode:                 sessionruntime.ModeAuto,
				AgentName:            "swarm",
				WorktreeMode:         runruntime.RunWorktreeModeOff,
			}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Hosted: sessionruntime.HostedSessionDescriptor{
			HostSwarmID:          "managed-swarm",
			HostBackendURL:       "https://primary.example.test",
			HostWorkspacePath:    "/host/swarm-go",
			RuntimeWorkspacePath: "/workspaces/swarm-go",
			ChildSwarmID:         "child-swarm",
			OwnerTransport:       "routed_session_peer",
		},
		Route: pebblestore.SessionRouteRecord{
			SessionID:            "session-managed-child",
			UserID:               testPrincipal().UserID,
			AccountScopeID:       testPrincipal().AccountScopeID,
			ChildSwarmID:         "child-swarm",
			ChildBackendURL:      "http://127.0.0.1:7782",
			HostSwarmID:          "managed-swarm",
			HostContainerID:      "managed-container",
			HostWorkspacePath:    "/host/swarm-go",
			RuntimeWorkspacePath: "/workspaces/swarm-go",
			WorkspaceBindingID:   "binding-managed-child",
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "managed-swarm"}))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if _, ok, err := sessionSvc.GetSession("session-managed-child"); err != nil || ok {
		t.Fatalf("get child session ok=%t err=%v, want no session", ok, err)
	}
	if _, ok, err := routeStore.Get("session-managed-child"); err != nil || ok {
		t.Fatalf("get session route ok=%t err=%v, want no route", ok, err)
	}
}

func TestProxyBackendURLUsesChildLoopbackOnOwnerHost(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		SwarmID:        "managed-swarm",
		Relationship:   "managed",
		BackendURL:     "http://127.0.0.1:7782",
	}); err != nil {
		t.Fatalf("upsert managed runtime: %v", err)
	}
	got := server.proxyBackendURLForTarget(swarmTarget{
		SwarmID:     "child-swarm",
		Kind:        "mirrored",
		HostSwarmID: "managed-swarm",
		BackendURL:  "http://127.0.0.1:7782",
	})
	if got != "http://127.0.0.1:7782" {
		t.Fatalf("backend url = %q, want child loopback backend", got)
	}
}

func TestRoutedRunStreamControlProxiesHostedMirrorSession(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var requestPath atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		requestPath.Store(r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": "session-routed", "run_id": "run-1"})
	}))
	defer child.Close()

	sessionID := seedRoutedSession(t, sessionSvc)
	if _, _, err := sessionSvc.UpdateMetadata(sessionID, map[string]any{
		"swarm_managed_host_session":  "true",
		"swarm_managed_host_swarm_id": "managed-swarm",
	}); err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:        "host-swarm-id",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Name:           "host",
		Relationship:   "self",
		BackendURL:     child.URL,
		Status:         "online",
	}); err != nil {
		t.Fatalf("upsert authority runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		HostSwarmID:          "host-swarm-id",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put session execution: %v", err)
	}

	body := bytes.NewBufferString(`{"type":"run.start","prompt":"hello","background":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sessionID+"/run/stream", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

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

func TestManagedHostContainerWorktreeSessionRejectsLegacyPathAuthorityRoute(t *testing.T) {
	primary, _, _, routeStore := newRoutedSessionTestServer(t)
	createManagedHostContainerRouteSyncFixture(t, primary, nil, routeStore)

	body := bytes.NewBufferString(`{"title":"managed container worktree","mode":"auto","workspace_path":"/workspaces/swarm-go","host_workspace_path":"/workspaces/swarm-go","runtime_workspace_path":"/workspaces/swarm-go","workspace_name":"swarm-go","agent_name":"swarm","worktree_mode":"on","worktree_branch_name":"agent/session-container-worktree","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=managed-container-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	primary.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("session create status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routed session workspace paths must be resolved from workspace binding") {
		t.Fatalf("body = %s, want path-authority rejection", rec.Body.String())
	}
}

func TestManagedHostContainerWorktreeOffRejectsLegacyPathAuthorityRoute(t *testing.T) {
	primary, _, _, routeStore := newRoutedSessionTestServer(t)
	createManagedHostContainerRouteSyncFixture(t, primary, nil, routeStore)
	if _, err := primary.swarmDesktopTargetSelection.PutForAccount(testPrincipal().AccountScopeID, testPrincipal().UserID, "managed-container-swarm"); err != nil {
		t.Fatalf("select managed container target: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"managed container regular","mode":"auto","workspace_path":"/workspaces/swarm-go","host_workspace_path":"/workspaces/swarm-go","runtime_workspace_path":"/workspaces/swarm-go","workspace_name":"swarm-go","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	primary.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("session create status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routed session workspace paths must be resolved from workspace binding") {
		t.Fatalf("body = %s, want path-authority rejection", rec.Body.String())
	}
}

func createManagedHostContainerRouteSyncFixture(t *testing.T, primary *Server, childWorktrees *fakeWorktreeService, routeStore *pebblestore.SessionRouteStore) {
	t.Helper()
	if childWorktrees == nil {
		childWorktrees = &fakeWorktreeService{
			config: worktreeruntime.Config{Enabled: false, UseCurrentBranch: true},
			allocation: worktreeruntime.Allocation{
				RepoRoot:      "/workspaces/swarm-go",
				WorkspacePath: "/workspaces/swarm-go/.swarm/worktrees/session-container-worktree",
				BaseBranch:    "main",
				BranchName:    "agent/session-container-worktree",
				WorkspaceID:   "ws_container_worktree",
			},
		}
	}
	child, _, _, _, childSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, child, childSwarmStore, "managed-container-swarm", "managed-swarm", testPrincipal().UserID, testPrincipal().AccountScopeID)
	child.SetWorktreeService(childWorktrees)
	childHandler := child.Handler()
	childHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		childHandler.ServeHTTP(w, withTestPrincipal(r))
	}))
	t.Cleanup(childHTTP.Close)

	ctx := identity.ContextWithPrincipal(context.Background(), testPrincipal())

	managedStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "managed.pebble"))
	if err != nil {
		t.Fatalf("open managed store: %v", err)
	}
	t.Cleanup(func() { _ = managedStore.Close() })
	managedSwarmStore := pebblestore.NewSwarmStore(managedStore)
	if _, err := managedSwarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{ParentSwarmID: "host-swarm-id", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, PairingState: startupconfig.PairingStatePaired}); err != nil {
		t.Fatalf("put managed pairing: %v", err)
	}
	managed := &Server{startupConfigPath: filepath.Join(t.TempDir(), "managed.conf")}
	managed.SetSwarmStore(managedSwarmStore)
	managed.SetSwarmService(fakeRoutedSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm", Name: "managed", Role: "managed"}}, token: "peer-token"})
	managed.SetLocalContainerService(&recordingLocalContainerService{containersByAccount: map[string][]localcontainers.Container{
		testPrincipal().AccountScopeID: {{ID: "managed-container-swarm", Name: "managed container", Status: "running", HostAPIBaseURL: childHTTP.URL}},
	}})
	managedMux := http.NewServeMux()
	managedMux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	managedMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	managedMux.HandleFunc("/v1/me", func(w http.ResponseWriter, r *http.Request) {
		actor := testActorContext()
		writeJSON(w, http.StatusOK, map[string]any{"type": actor.Principal.Type, "bootstrapped": true, "userID": actor.UserID, "user_id": actor.UserID, "accountScopeID": actor.AccountScopeID, "account_scope_id": actor.AccountScopeID, "account_scope": actor.AccountScope, "user": actor.User, "account_user": actor.AccountUser, "teamID": actor.TeamID, "team_id": actor.TeamID, "team": actor.Team, "membership": actor.Membership, "selection": actor.Selection})
	})
	managed.registerSwarmRoutes(managedMux)
	managed.registerPeerRoutes(managedMux)
	managedHandler := managed.withJSON(managedMux)
	managedHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		managedHandler.ServeHTTP(w, withTestPrincipal(r))
	}))
	t.Cleanup(managedHTTP.Close)

	primary.SetSwarmService(fakeRoutedSwarmService{state: swarmruntime.LocalState{
		Node: swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host", Role: "master"},
		TrustedPeers: []swarmruntime.TrustedPeer{{
			SwarmID: "managed-swarm", Name: "managed", Role: swarmruntime.RelationshipManaged, Relationship: swarmruntime.RelationshipManaged,
			RendezvousTransports: []swarmruntime.TransportSummary{{Kind: startupconfig.NetworkModeTailscale, Primary: managedHTTP.URL}},
		}},
		Groups: []swarmruntime.GroupState{{Group: swarmruntime.Group{ID: "group-1", HostSwarmID: "host-swarm-id"}, Members: []swarmruntime.GroupMember{
			{GroupID: "group-1", SwarmID: "host-swarm-id", MembershipRole: swarmruntime.GroupMembershipRoleHost},
			{GroupID: "group-1", SwarmID: "managed-swarm", MembershipRole: swarmruntime.GroupMembershipRoleMember},
			{GroupID: "group-1", SwarmID: "managed-container-swarm", MembershipRole: swarmruntime.GroupMembershipRoleMember},
		}}},
		CurrentGroupID: "group-1",
	}, token: "peer-token"})
	if err := primary.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:        "managed-swarm",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Name:           "managed",
		Relationship:   swarmruntime.RelationshipManaged,
		BackendURL:     managedHTTP.URL,
		Status:         "online",
	}); err != nil {
		t.Fatalf("upsert managed runtime: %v", err)
	}
	if err := primary.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "managed-container-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "managed container",
		Role:                 "child",
		Relationship:         swarmruntime.RelationshipChild,
		BackendURL:           childHTTP.URL,
		Status:               "attached",
		OwnerHostSwarmID:     "managed-swarm",
		OwnerHostContainerID: "managed-container-swarm",
	}); err != nil {
		t.Fatalf("upsert child runtime: %v", err)
	}
	if _, err := primary.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding-managed-container-swarm",
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
		SourceWorkspacePath:       "/workspaces/swarm-go",
		SourceWorkspaceName:       "swarm-go",
		DestinationRuntimeSwarmID: "managed-container-swarm",
		DestinationHostSwarmID:    "managed-swarm",
		DestinationWorkspacePath:  "/workspaces/swarm-go",
		LegacyTargetKind:          "local-container",
	}); err != nil {
		t.Fatalf("upsert workspace binding: %v", err)
	}

	createContainerReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/swarm/containers/local/create?swarm_id=managed-swarm", bytes.NewBufferString(`{"name":"managed-container-swarm","runtime":"docker","host_api_base_url":"`+childHTTP.URL+`","image":"swarm-child:test"}`))
	createContainerReq.Header.Set("Content-Type", "application/json")
	createContainerRec := httptest.NewRecorder()
	primary.Handler().ServeHTTP(createContainerRec, withTestPrincipal(createContainerReq))
	if createContainerRec.Code != http.StatusOK {
		t.Fatalf("container create status = %d, want %d, body=%s", createContainerRec.Code, http.StatusOK, createContainerRec.Body.String())
	}
}

func TestRoutedSessionTargetDoesNotRetireRoutesOnLookupWhenReplacementChildExists(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	server.SetDeployContainerService(&fakeReplicateDeployService{lastMirroredDeployment: deployruntime.ContainerDeployment{
		ID:              "replacement-deployment",
		Name:            "replacement child",
		Status:          "running",
		AttachStatus:    "attached",
		ChildSwarmID:    "replacement-child-swarm",
		ChildBackendURL: "http://127.0.0.1:7782",
	}})

	sessionID := "session-stale-route-lookup"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "managed-swarm",
		HostContainerID:      "managed-swarm:container-1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/swarm-go",
	}); err != nil {
		t.Fatalf("put session route: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		SwarmID:              "child-swarm",
		Name:                 "managed child",
		Role:                 "child",
		Relationship:         "child",
		BackendURL:           "http://127.0.0.1:7782",
		Status:               "attached",
		OwnerHostSwarmID:     "managed-swarm",
		OwnerHostContainerID: "managed-swarm:container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-swarm", Name: "managed host", Relationship: "managed", BackendURL: "http://127.0.0.1:7782", Status: "online"}); err != nil {
		t.Fatalf("upsert authority runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "managed-swarm",
		HostContainerID:      "managed-swarm:container-1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/swarm-go",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if !ok || target == nil {
		t.Fatalf("routed target not found")
	}
	if target.SwarmID != "child-swarm" {
		t.Fatalf("target swarm id = %q, want child-swarm", target.SwarmID)
	}
	if _, ok, err := routeStore.Get(sessionID); err != nil {
		t.Fatalf("get session route: %v", err)
	} else if !ok {
		t.Fatalf("session route was deleted during lookup")
	}
	if _, ok, err := server.topology.GetSessionRouteForAccount(testPrincipal().AccountScopeID, sessionID); err != nil {
		t.Fatalf("get topology route: %v", err)
	} else if !ok {
		t.Fatalf("topology route was deleted during lookup")
	}
}

func TestRoutedSessionTargetUsesAuthorityHostTransportWithoutStoredBackendURL(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	managedHost := httptest.NewServer(http.NotFoundHandler())
	defer managedHost.Close()
	if _, err := server.swarmMirror.UpsertRemoteResource("managed-swarm", pebblestore.SwarmMirrorEventRecord{
		Sequence:  1,
		EventType: pebblestore.SwarmMirrorEventTypeUpsert,
		Kind:      mirrorResourceTarget,
		ID:        "target:child-swarm",
		Resource:  []byte(`{"swarm_id":"child-swarm","name":"managed child","role":"child","relationship":"child","kind":"remote","backend_url":"` + managedHost.URL + `","online":true,"selectable":true}`),
	}); err != nil {
		t.Fatalf("upsert mirrored target: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		SwarmID:              "child-swarm",
		Name:                 "managed child",
		Role:                 "child",
		Relationship:         "child",
		BackendURL:           "http://127.0.0.1:7782",
		Status:               "attached",
		OwnerHostSwarmID:     "managed-swarm",
		OwnerHostContainerID: "managed-swarm:container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		SwarmID:        "managed-swarm",
		Name:           "managed host",
		Relationship:   "managed",
		BackendURL:     managedHost.URL,
		Status:         "online",
	}); err != nil {
		t.Fatalf("upsert managed runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            "session-managed-child",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "managed-swarm",
		HostContainerID:      "managed-swarm:container-1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/swarm-go",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), "session-managed-child")
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if !ok || target == nil {
		t.Fatal("routed target not found")
	}
	if target.BackendURL != managedHost.URL {
		t.Fatalf("backend url = %q, want live authority host backend %q", target.BackendURL, managedHost.URL)
	}
	if target.HostSwarmID != "managed-swarm" {
		t.Fatalf("host swarm id = %q, want managed-swarm", target.HostSwarmID)
	}

}

var errTestRemoteUpdateFailure = errors.New("remote update failed")

func TestPeerSessionEventStoresAndPublishesMirroredRunEvent(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-live-peer", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/host/workspace", WorkspaceName: "workspace", Title: "Flow live", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	payload := []byte(`{"session_id":"session-live-peer","event_type":"run.assistant.delta","payload":{"type":"assistant.delta","session_id":"session-live-peer","run_id":"run-live","delta":"hello"},"causation_id":"run-live"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/event", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.handlePeerSessionEvent(rec, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	events, err := server.events.ReadFrom(1, 20)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	foundDelta := false
	for _, event := range events {
		if event.EventType == "run.assistant.delta" && event.EntityID == "session-live-peer" {
			foundDelta = true
			break
		}
	}
	if !foundDelta {
		t.Fatalf("events = %+v", events)
	}
}

func TestPeerSessionMetadataDerivesPrincipalFromPersistedSessionAndRoute(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-peer-metadata", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/runtime/workspace", WorkspaceName: "workspace", Title: "Peer metadata", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{SessionID: "session-peer-metadata", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/workspace", RuntimeWorkspacePath: "/runtime/workspace"}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/metadata", bytes.NewReader([]byte(`{"session_id":"session-peer-metadata","metadata":{"background_run":{"active":true}}}`)))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionMetadata(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	updated, ok, err := sessionSvc.GetSession("session-peer-metadata")
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if _, ok := updated.Metadata["background_run"]; !ok {
		t.Fatalf("metadata = %+v", updated.Metadata)
	}
}

func TestPeerSessionMetadataRejectsWithoutPersistedRoute(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-peer-metadata-orphan", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/runtime/workspace", WorkspaceName: "workspace", Title: "Peer metadata", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/metadata", bytes.NewReader([]byte(`{"session_id":"session-peer-metadata-orphan","metadata":{"background_run":{"active":true}}}`)))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionMetadata(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestPeerSessionEventRejectsCrossAccountPrincipal(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-account-a", UserID: "user-a", AccountScopeID: "account-a", WorkspacePath: "/host/workspace", WorkspaceName: "workspace", Title: "Flow live", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	payload := []byte(`{"session_id":"session-account-a","event_type":"run.assistant.delta","payload":{"type":"assistant.delta","session_id":"session-account-a","run_id":"run-live","delta":"hello"},"causation_id":"run-live"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/event", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.handlePeerSessionEvent(rec, requestWithTestPrincipalForAccount(req, "user-b", "account-b"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	stored, ok, err := sessionSvc.GetSession("session-account-a")
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if stored.AccountScopeID != "account-a" || stored.UserID != "user-a" {
		t.Fatalf("stored session = %+v", stored)
	}
}

func TestPeerSessionOpenRejectsSpoofedForwardedPrincipalWithoutAccountOwnedRuntime(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "child-swarm", "host-swarm-id", "user-a", "account-a")
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "child-swarm", UserID: "user-a", AccountScopeID: "account-a", Name: "child", Relationship: "child", BackendURL: "http://127.0.0.1:7782"}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-spoofed",
		Request:   sessionCreateRequest{Title: "spoofed", WorkspacePath: "/workspaces/swarm-go", HostWorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-spoofed", WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto, WorktreeMode: runruntime.RunWorktreeModeOff},
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: "session-spoofed", UserID: "user-b", AccountScopeID: "account-b", ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-spoofed"},
		Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-b", AccountScopeID: "account-b", AccountScopeSource: identity.AccountScopeSourceSession},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionOpen(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestPeerSessionOpenAcceptsPairedChildPrincipalClaimWithoutLocalTopologyRuntime(t *testing.T) {
	server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "child-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)
	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-paired-child",
		Request: func() sessionCreateRequest {
			req := sessionCreateRequest{Title: "paired child", WorkspacePath: "/workspaces/swarm-go", HostWorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-paired-child", WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto, WorktreeMode: runruntime.RunWorktreeModeOff}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: "session-paired-child", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-paired-child"},
		Principal: testPrincipal(),
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionOpen(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	session, ok, err := sessionSvc.GetSession("session-paired-child")
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if session.UserID != testPrincipal().UserID || session.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("session principal = %q/%q", session.UserID, session.AccountScopeID)
	}
	route, ok, err := routeStore.Get("session-paired-child")
	if err != nil || !ok {
		t.Fatalf("get session route ok=%t err=%v", ok, err)
	}
	if route.UserID != testPrincipal().UserID || route.AccountScopeID != testPrincipal().AccountScopeID || route.ChildSwarmID != "child-swarm" || route.HostSwarmID != "host-swarm-id" {
		t.Fatalf("session route = %+v", route)
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/message", bytes.NewBufferString(`{"session_id":"session-paired-child","role":"user","content":"hello"}`))
	messageReq = messageReq.WithContext(context.WithValue(messageReq.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	messageRec := httptest.NewRecorder()
	server.handlePeerSessionAppendMessage(messageRec, messageReq)
	if messageRec.Code != http.StatusOK {
		t.Fatalf("message status = %d, want %d, body=%s", messageRec.Code, http.StatusOK, messageRec.Body.String())
	}
}

func TestPeerSessionOpenAllowsRequestedWorktreeForPairedLocalChild(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "child-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)
	sessionSvc.SetLocalSwarmIDResolver(func() string { return "child-swarm" })
	hostedSync := &recordingHostedSessionSync{session: pebblestore.SessionSnapshot{
		ID:            "session-paired-worktree",
		WorkspacePath: "/workspaces/swarm-go",
		WorkspaceName: "swarm-go",
		Title:         "mirrored parent response without child-local worktree fields",
		Mode:          sessionruntime.ModeAuto,
		Metadata: sessionruntime.HostedSessionDescriptor{
			HostSwarmID:          "host-swarm-id",
			HostWorkspacePath:    "/host/swarm-go",
			RuntimeWorkspacePath: "/workspaces/swarm-go",
			ChildSwarmID:         "child-swarm",
			OwnerTransport:       "routed_session_peer",
		}.WithMetadata(nil),
		CreatedAt: 1,
		UpdatedAt: 2,
	}}
	sessionSvc.SetHostedSync(hostedSync)
	server.SetWorktreeService(&fakeWorktreeService{
		config: worktreeruntime.Config{Enabled: true, UseCurrentBranch: true},
		allocation: worktreeruntime.Allocation{
			WorkspacePath: "/var/cache/swarmd/workspaces/swarm-go-test/worktrees/ws_peer_open",
			RepoRoot:      "/var/cache/swarmd/workspaces/swarm-go-test",
			BaseBranch:    "main",
			BranchName:    "agent/session-paired-worktree",
			WorkspaceID:   "ws_peer_open",
		},
		attachBranch: testStringPtr("agent/session-paired-worktree"),
	})

	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-paired-worktree",
		Request: func() sessionCreateRequest {
			req := sessionCreateRequest{Title: "paired child worktree", WorkspacePath: "/workspaces/swarm-go", HostWorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-paired-worktree", WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto, WorktreeMode: "on"}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: "session-paired-worktree", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-paired-worktree"},
		Principal: testPrincipal(),
	})
	var _ sessionruntime.HostedSessionSync = hostedSync
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionOpen(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if hostedSync.updateMetadataCalls != 0 {
		t.Fatalf("hosted metadata sync calls = %d, want 0 for local-only worktree metadata update", hostedSync.updateMetadataCalls)
	}
	session, ok, err := sessionSvc.GetSession("session-paired-worktree")
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if !session.WorktreeEnabled {
		t.Fatalf("WorktreeEnabled = false, session = %+v", session)
	}
	if session.WorkspacePath != "/var/cache/swarmd/workspaces/swarm-go-test/worktrees/ws_peer_open" {
		t.Fatalf("workspace path = %q", session.WorkspacePath)
	}
	if session.WorktreeRootPath != "/var/cache/swarmd/workspaces/swarm-go-test" || session.WorktreeBranch != "agent/session-paired-worktree" {
		t.Fatalf("worktree fields = root %q branch %q", session.WorktreeRootPath, session.WorktreeBranch)
	}
	if strings.TrimSpace(session.WorktreeRootPath) == "" || strings.TrimSpace(session.WorktreeBranch) == "" {
		t.Fatalf("worktree root/branch must be non-empty: root=%q branch=%q", session.WorktreeRootPath, session.WorktreeBranch)
	}
	descriptor, hosted := sessionruntime.HostedSessionFromMetadata(session.Metadata)
	if !hosted {
		t.Fatalf("hosted metadata missing after local-only update: %+v", session.Metadata)
	}
	if descriptor.RuntimeWorkspacePath != session.WorkspacePath {
		t.Fatalf("runtime workspace metadata = %q, want preserved worktree workspace %q", descriptor.RuntimeWorkspacePath, session.WorkspacePath)
	}
}

func TestPeerSessionOpenDiagnosesMissingCanonicalWorktreeField(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "child-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)
	attachBranch := ""
	server.SetWorktreeService(&fakeWorktreeService{
		config:       worktreeruntime.Config{Enabled: true, UseCurrentBranch: true},
		attachBranch: &attachBranch,
		allocation: worktreeruntime.Allocation{
			WorkspacePath: "/var/cache/swarmd/workspaces/swarm-go-test/worktrees/ws_missing_branch",
			RepoRoot:      "/var/cache/swarmd/workspaces/swarm-go-test",
			BaseBranch:    "main",
			BranchName:    "",
			WorkspaceID:   "ws_missing_branch",
		},
	})

	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-missing-worktree-branch",
		Request: func() sessionCreateRequest {
			req := sessionCreateRequest{Title: "paired child missing worktree branch", WorkspacePath: "/workspaces/swarm-go", HostWorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-missing-worktree-branch", WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto, WorktreeMode: "on"}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: "session-missing-worktree-branch", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", HostSwarmID: "host-swarm-id", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-missing-worktree-branch"},
		Principal: testPrincipal(),
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionOpen(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"worktree_mode on did not create canonical worktree session state",
		"missing=worktree_branch",
		`session_id=\"session-missing-worktree-branch\"`,
		`session_workspace_path=\"/var/cache/swarmd/workspaces/swarm-go-test/worktrees/ws_missing_branch\"`,
		"session_worktree_enabled=true",
		`create_workspace_path=\"/var/cache/swarmd/workspaces/swarm-go-test/worktrees/ws_missing_branch\"`,
		"create_worktree_present=true",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if _, ok, err := sessionSvc.GetSession("session-missing-worktree-branch"); err != nil || ok {
		t.Fatalf("get session ok=%t err=%v, want no persisted session after failed open", ok, err)
	}
}

func TestPeerSessionOpenRejectsInheritedWorktreeForPairedLocalChild(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "child-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)
	server.SetWorktreeService(&fakeWorktreeService{
		config: worktreeruntime.Config{Enabled: true, UseCurrentBranch: true},
		allocation: worktreeruntime.Allocation{
			WorkspacePath: "/var/cache/swarmd/workspaces/swarm-go-test/worktrees/ws_peer_inherit",
			RepoRoot:      "/var/cache/swarmd/workspaces/swarm-go-test",
			BaseBranch:    "main",
			BranchName:    "agent/session-paired-inherit",
			WorkspaceID:   "ws_peer_inherit",
		},
	})

	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-paired-inherit",
		Request: func() sessionCreateRequest {
			req := sessionCreateRequest{Title: "paired child inherited worktree", WorkspacePath: "/workspaces/swarm-go", HostWorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-paired-inherit", WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto, WorktreeMode: "inherit"}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: "session-paired-inherit", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-paired-inherit"},
		Principal: testPrincipal(),
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionOpen(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if _, ok, err := sessionSvc.GetSession("session-paired-inherit"); err != nil || ok {
		t.Fatalf("get session ok=%t err=%v, want no session", ok, err)
	}
}

func TestPeerSessionOpenRejectsTerminalContractMismatches(t *testing.T) {
	cases := []struct {
		name          string
		sessionID     string
		mutate        func(*peerSessionOpenRequest)
		wantStatus    int
		wantErrSubstr string
	}{
		{
			name:      "missing workspace binding id",
			sessionID: "session-missing-binding-id",
			mutate: func(req *peerSessionOpenRequest) {
				req.Route.WorkspaceBindingID = ""
				req.Request.WorkspaceBindingID = ""
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "route workspace binding id is required",
		},
		{
			name:      "request runtime path mismatch",
			sessionID: "session-runtime-path-mismatch",
			mutate: func(req *peerSessionOpenRequest) {
				req.Request.RuntimeWorkspacePath = "/frontend/stale/runtime"
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "request runtime workspace path does not match route runtime workspace path",
		},
		{
			name:      "request host path mismatch",
			sessionID: "session-host-path-mismatch",
			mutate: func(req *peerSessionOpenRequest) {
				req.Request.HostWorkspacePath = "/frontend/stale/host"
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "request host workspace path does not match route runtime workspace path",
		},
		{
			name:      "host swarm mismatch",
			sessionID: "session-host-mismatch",
			mutate: func(req *peerSessionOpenRequest) {
				req.Hosted.HostSwarmID = "other-host"
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "route host swarm id does not match hosted host swarm id",
		},
		{
			name:      "child swarm mismatch",
			sessionID: "session-child-mismatch",
			mutate: func(req *peerSessionOpenRequest) {
				req.Hosted.ChildSwarmID = "other-child"
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "route child swarm id does not match hosted child swarm id",
		},
		{
			name:      "blank worktree mode",
			sessionID: "session-blank-worktree-mode",
			mutate: func(req *peerSessionOpenRequest) {
				req.Request.WorktreeMode = ""
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "worktree_mode must be explicitly set to on or off",
		},
		{
			name:      "off with worktree fields",
			sessionID: "session-off-worktree-fields",
			mutate: func(req *peerSessionOpenRequest) {
				req.Request.WorktreeMode = runruntime.RunWorktreeModeOff
				req.Request.WorktreeBranchName = "agent/should-not-exist"
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "worktree fields are not allowed when worktree_mode is off",
		},
		{
			name:      "on without worktree allocator",
			sessionID: "session-on-no-worktree-service",
			mutate: func(req *peerSessionOpenRequest) {
				req.Request.WorktreeMode = runruntime.RunWorktreeModeOn
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "worktree service not configured",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			configureRoutedSessionTestServerAsChild(t, server, swarmStore, "child-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)
			openReq := checkpoint1PeerSessionOpenRequest(tc.sessionID)
			if tc.mutate != nil {
				tc.mutate(&openReq)
			}
			payload, err := json.Marshal(openReq)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
			req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
			rec := httptest.NewRecorder()
			server.handlePeerSessionOpen(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantErrSubstr != "" && !strings.Contains(rec.Body.String(), tc.wantErrSubstr) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tc.wantErrSubstr)
			}
			if _, ok, err := sessionSvc.GetSession(tc.sessionID); err != nil || ok {
				t.Fatalf("get session ok=%t err=%v, want no session", ok, err)
			}
			if _, ok, err := routeStore.Get(tc.sessionID); err != nil || ok {
				t.Fatalf("get route ok=%t err=%v, want no route", ok, err)
			}
		})
	}
}

func checkpoint1PeerSessionOpenRequest(sessionID string) peerSessionOpenRequest {
	return peerSessionOpenRequest{
		SessionID: sessionID,
		Request: func() sessionCreateRequest {
			req := sessionCreateRequest{Title: sessionID, WorkspacePath: "/workspaces/swarm-go", HostWorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-" + sessionID, WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto, WorktreeMode: runruntime.RunWorktreeModeOff}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: sessionID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-" + sessionID},
		Principal: testPrincipal(),
	}
}

func TestPeerSessionOpenRejectsPairedChildPrincipalClaimForWrongAccount(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "child-swarm", "host-swarm-id", "user-a", "account-a")
	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-wrong-account",
		Request:   sessionCreateRequest{Title: "wrong account", WorkspacePath: "/workspaces/swarm-go", HostWorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-wrong-account", WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto, WorktreeMode: runruntime.RunWorktreeModeOff},
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: "session-wrong-account", UserID: "user-b", AccountScopeID: "account-b", ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceBindingID: "binding-wrong-account"},
		Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-b", AccountScopeID: "account-b", AccountScopeSource: identity.AccountScopeSourceSession},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionOpen(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestStoreMirroredSessionWithEventPublishesCreatedEvent(t *testing.T) {
	_, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	session, event, err := sessionSvc.StoreMirroredSessionWithEvent(pebblestore.SessionSnapshot{ID: "session-live-created", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/host/workspace", WorkspaceName: "workspace", Title: "Flow live", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatalf("store mirrored session: %v", err)
	}
	if event == nil || event.EventType != "session.created" || event.EntityID != session.ID {
		t.Fatalf("event = %+v, session = %+v", event, session)
	}
	_, event, err = sessionSvc.StoreMirroredSessionWithEvent(session)
	if err != nil {
		t.Fatalf("store mirrored session again: %v", err)
	}
	if event != nil {
		t.Fatalf("duplicate store emitted event: %+v", event)
	}
}

func TestPeerSessionEventStoresMirroredPayloadMessage(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-live-message", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/host/workspace", WorkspaceName: "workspace", Title: "Flow live", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	payload := []byte(`{"session_id":"session-live-message","event_type":"run.message.stored","payload":{"type":"message.stored","session_id":"session-live-message","run_id":"run-live","message":{"id":"msg_00000000000000000007","session_id":"session-live-message","global_seq":7,"role":"assistant","content":"mirrored payload","created_at":123}}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/event", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.handlePeerSessionEvent(rec, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	messages, err := sessionSvc.ListMessages("session-live-message", 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "mirrored payload" || messages[0].GlobalSeq != 7 {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestRoutedSessionMessagesReloadFromHostWithoutProxy(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)
	if _, _, _, err := sessionSvc.AppendMessage(sessionID, "user", "hello from host mirror", nil); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/messages?limit=10", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

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

func TestRoutedRunStreamControlMissingStoredRouteFailsClosed(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer child.Close()
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:        "selected-child",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Name:           "selected child",
		Relationship:   "child",
		BackendURL:     child.URL,
		Status:         "attached",
	}); err != nil {
		t.Fatalf("upsert selected runtime: %v", err)
	}
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{
		ID:             "session-run-missing-route",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Title:          "Routed run without route",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"owner_transport": "routed_session_peer",
			sessionruntime.HostedSessionMetadataHostWorkspacePath:    "/host/workspace",
			sessionruntime.HostedSessionMetadataRuntimeWorkspacePath: "/runtime/workspace",
			sessionruntime.HostedSessionMetadataChildSwarmID:         "missing-child",
			"swarm_routed_workspace_binding_id":                      "binding-missing-route",
		},
		CreatedAt: 1,
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("store mirrored session: %v", err)
	}
	if _, err := server.swarmDesktopTargetSelection.PutForAccount(testPrincipal().AccountScopeID, testPrincipal().UserID, "selected-child"); err != nil {
		t.Fatalf("select target: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session-run-missing-route/run/stream?swarm_id=selected-child", bytes.NewBufferString(`{"type":"run.start","prompt":"hello","background":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routed session is missing canonical stored route") {
		t.Fatalf("body = %s, want missing canonical route error", rec.Body.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("selected target hits = %d, want 0", hits.Load())
	}
}

func TestRoutedSessionProxyMissingStoredRouteDoesNotUseRequestTarget(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer child.Close()
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:        "selected-child",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Name:           "selected child",
		Relationship:   "child",
		BackendURL:     child.URL,
		Status:         "attached",
	}); err != nil {
		t.Fatalf("upsert selected runtime: %v", err)
	}
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{
		ID:             "session-missing-route",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Title:          "Routed mirror without route",
		Mode:           sessionruntime.ModePlan,
		Metadata: map[string]any{
			"owner_transport": "routed_session_peer",
			sessionruntime.HostedSessionMetadataHostWorkspacePath:    "/host/workspace",
			sessionruntime.HostedSessionMetadataRuntimeWorkspacePath: "/runtime/workspace",
			sessionruntime.HostedSessionMetadataChildSwarmID:         "missing-child",
		},
		CreatedAt: 1,
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("store mirrored session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session-missing-route/messages?swarm_id=selected-child", bytes.NewBufferString(`{"role":"user","content":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routed session is missing canonical stored route") {
		t.Fatalf("body = %s, want missing canonical route error", rec.Body.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("selected target hits = %d, want 0", hits.Load())
	}
}

func TestRoutedSessionGetUsesStoredRouteWithoutSwarmID(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var requestPath atomic.Value
	var requestQuery atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		requestPath.Store(r.URL.Path)
		requestQuery.Store(r.URL.RawQuery)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             "session-routed",
				"title":          "Remote child session",
				"workspace_path": "/workspaces/swarm",
				"workspace_name": "child swarm",
				"mode":           "auto",
				"created_at":     1,
				"updated_at":     2,
				"metadata": map[string]any{
					"swarm_routed_session":                true,
					"swarm_routed_host_workspace_path":    "/host/workspace",
					"swarm_routed_runtime_workspace_path": "/workspaces/swarm",
				},
			},
		})
	}))
	defer child.Close()

	sessionID := seedRoutedSession(t, server.sessions)
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "child-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "child", Relationship: "child", BackendURL: child.URL}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "host-swarm-id", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "host", Relationship: "self", BackendURL: child.URL, Status: "online"}); err != nil {
		t.Fatalf("upsert authority runtime: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      child.URL,
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/swarm",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      child.URL,
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/swarm",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1", hits.Load())
	}
	if got, _ := requestPath.Load().(string); got != "/v1/sessions/"+sessionID {
		t.Fatalf("child path = %q, want %q", got, "/v1/sessions/"+sessionID)
	}
	if got, _ := requestQuery.Load().(string); got != "" {
		t.Fatalf("child query = %q, want empty", got)
	}
	var payload struct {
		Session struct {
			WorkspacePath string `json:"workspace_path"`
			WorkspaceName string `json:"workspace_name"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Session.WorkspacePath != "/workspaces/swarm" || payload.Session.WorkspaceName != "child swarm" {
		t.Fatalf("session identity = %q/%q, want remote child", payload.Session.WorkspacePath, payload.Session.WorkspaceName)
	}
}

func TestLocalChildTargetUsesChildPeerAuthWhenOwnerHostIsSelf(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	target := swarmTarget{
		SwarmID:     "child-swarm",
		Kind:        "local",
		HostSwarmID: "host-swarm-id",
		BackendURL:  "http://127.0.0.1:7782",
	}
	if peerSwarmID := server.peerAuthSwarmIDForTarget(target); peerSwarmID != "child-swarm" {
		t.Fatalf("peer auth swarm id = %q, want child-swarm", peerSwarmID)
	}
	if token, err := server.outgoingPeerAuthTokenForTarget(nil, target); err != nil || token != "peer-token" {
		t.Fatalf("target token = %q err=%v, want peer-token", token, err)
	}
}

func TestPrincipalForManagedHostSessionRunUsesStoredSessionPrincipal(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)

	principal := server.principalForManagedHostSessionRun(sessionID)
	if principal.UserID != testPrincipal().UserID || principal.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("principal = %q/%q, want session principal %q/%q", principal.UserID, principal.AccountScopeID, testPrincipal().UserID, testPrincipal().AccountScopeID)
	}
}

func TestRoutedSessionTargetSkipsLocalHostSelfRoute(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-local-runtime-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "host-swarm-id",
		ChildBackendURL:      "http://127.0.0.1:7781",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/host/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "host-swarm-id",
		ChildBackendURL:      "http://127.0.0.1:7781",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/host/workspace",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if ok || target != nil {
		t.Fatalf("routed target ok=%t target=%+v, want local handling", ok, target)
	}
}

func TestRoutedSessionTargetKeepsLocalContainerRuntimeEvenWhenHostIsSelf(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-local-container-runtime-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/workspace",
		WorkspaceBindingID:   "binding-local-container",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		SwarmID:              "child-swarm",
		Name:                 "local container child",
		Relationship:         "child",
		BackendURL:           "http://127.0.0.1:7782",
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "host-swarm-id", Name: "host", Relationship: "self", BackendURL: "http://127.0.0.1:7782", Status: "online"}); err != nil {
		t.Fatalf("upsert authority runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/workspace",
		WorkspaceBindingID:   "binding-local-container",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if !ok || target == nil {
		t.Fatalf("routed target not found")
	}
	if target.SwarmID != "child-swarm" {
		t.Fatalf("target swarm id = %q, want child-swarm", target.SwarmID)
	}
	if target.BackendURL != "http://127.0.0.1:7782" {
		t.Fatalf("target backend url = %q, want child backend", target.BackendURL)
	}
}

func TestRoutedSessionTargetKeepsLocalChildLoopbackBackendWhenOwnerHostIsSelf(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-local-child-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		SwarmID:              "child-swarm",
		Name:                 "child",
		Relationship:         "child",
		BackendURL:           "http://127.0.0.1:7782",
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "host-swarm-id", Name: "host", Relationship: "self", BackendURL: "http://127.0.0.1:7782", Status: "online"}); err != nil {
		t.Fatalf("upsert authority runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if !ok || target == nil {
		t.Fatalf("routed target not found")
	}
	if target.BackendURL != "http://127.0.0.1:7782" {
		t.Fatalf("target backend url = %q, want child loopback backend", target.BackendURL)
	}
	if token, err := server.outgoingPeerAuthTokenForTarget(nil, *target); err != nil || token != "peer-token" {
		t.Fatalf("target token = %q err=%v, want peer-token", token, err)
	}
}

func TestRoutedSessionTargetUsesOwnerHostPeerAuth(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		SwarmID:              "child-swarm",
		Name:                 "child",
		Relationship:         "child",
		BackendURL:           "http://127.0.0.1:7782",
		Status:               "attached",
		OwnerHostSwarmID:     "managed-swarm",
		OwnerHostContainerID: "managed-swarm:container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-swarm", Name: "managed host", Relationship: "managed", BackendURL: "http://127.0.0.1:7782", Status: "online"}); err != nil {
		t.Fatalf("upsert authority runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if !ok || target == nil {
		t.Fatalf("routed target not found")
	}
	if target.HostSwarmID != "managed-swarm" {
		t.Fatalf("target host swarm id = %q, want managed-swarm", target.HostSwarmID)
	}
	if target.Kind != "mirrored" {
		t.Fatalf("target kind = %q, want mirrored", target.Kind)
	}
	if token, err := server.outgoingPeerAuthTokenForTarget(nil, *target); err != nil || token != "peer-token" {
		t.Fatalf("target token = %q err=%v, want peer-token", token, err)
	}
}

func TestRoutedRunStreamControlUsesStoredRouteWithoutSwarmID(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var requestPath atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		requestPath.Store(r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": "session-routed", "run_id": "run-1"})
	}))
	defer child.Close()

	sessionID := seedRoutedSession(t, server.sessions)
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:        "host-swarm-id",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Name:           "host",
		Relationship:   "self",
		BackendURL:     child.URL,
		Status:         "online",
	}); err != nil {
		t.Fatalf("upsert authority runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		HostSwarmID:          "host-swarm-id",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put session execution: %v", err)
	}

	body := bytes.NewBufferString(`{"type":"run.start","prompt":"hello","background":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sessionID+"/run/stream", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

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
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
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
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
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
	server.Handler().ServeHTTP(resolveRec, withTestPrincipal(resolveReq))
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
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

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

func TestRoutedFlowSessionFetchReturnsCanonicalHostMirror(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-flow-routed"
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Title:          "Flow smoke",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"source":            "flow",
			"lineage_kind":      "flow",
			"owner_transport":   "flow_scheduler",
			"flow_id":           "flow-routed",
			"swarm_target_name": "swarm child 4",
			sessionruntime.HostedSessionMetadataEnabled:              true,
			sessionruntime.HostedSessionMetadataHostSwarmID:          "host-swarm-id",
			sessionruntime.HostedSessionMetadataChildSwarmID:         "child-swarm",
			sessionruntime.HostedSessionMetadataHostWorkspacePath:    "/host/workspace",
			sessionruntime.HostedSessionMetadataRuntimeWorkspacePath: "/runtime/workspace",
		},
		CreatedAt: 1,
		UpdatedAt: 2,
	}); err != nil {
		t.Fatalf("store flow mirror: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Session.WorkspacePath != "/host/workspace" || payload.Session.Metadata["swarm_target_name"] != "swarm child 4" || payload.Session.Metadata["source"] != "flow" {
		t.Fatalf("session payload = %+v", payload.Session)
	}
}

func TestRoutedSessionPreferenceReadFromHostWithoutProxy(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/preference", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

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

func TestPrimaryRoutedSessionCreateRejectsMissingExplicitWorktreeMode(t *testing.T) {
	cases := []struct {
		name             string
		worktreeModeJSON string
	}{
		{name: "missing", worktreeModeJSON: ""},
		{name: "empty", worktreeModeJSON: `,"worktree_mode":""`},
		{name: "invalid", worktreeModeJSON: `,"worktree_mode":"legacy-default"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, routeStore := newRoutedSessionTestServer(t)
			var childOpenCalled atomic.Bool
			child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				childOpenCalled.Store(true)
				t.Fatalf("child open should not be called for invalid primary contract")
			}))
			defer child.Close()

			if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
				SwarmID:              "container-swarm",
				UserID:               testPrincipal().UserID,
				AccountScopeID:       testPrincipal().AccountScopeID,
				Name:                 "container child",
				Relationship:         swarmruntime.RelationshipChild,
				Transport:            "remote",
				BackendURL:           child.URL,
				Status:               "attached",
				OwnerHostSwarmID:     "host-swarm-id",
				OwnerHostContainerID: "host-container-1",
			}); err != nil {
				t.Fatalf("upsert runtime: %v", err)
			}
			if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
				BindingID:                 "binding-primary-missing-worktree",
				UserID:                    testPrincipal().UserID,
				AccountScopeID:            testPrincipal().AccountScopeID,
				SourceWorkspacePath:       "/host/swarm-go",
				SourceWorkspaceName:       "swarm-go",
				DestinationRuntimeSwarmID: "container-swarm",
				DestinationHostSwarmID:    "host-swarm-id",
				DestinationWorkspacePath:  "/workspaces/swarm-go",
				LegacyTargetKind:          "local-container",
			}); err != nil {
				t.Fatalf("upsert binding: %v", err)
			}

			body := bytes.NewBufferString(`{"title":"missing worktree","mode":"auto","workspace_binding_id":"binding-primary-missing-worktree","workspace_name":"swarm-go","agent_name":"swarm"` + tc.worktreeModeJSON + `,"preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=container-swarm", body)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "worktree_mode must be explicitly set to on or off") {
				t.Fatalf("body = %s, want explicit worktree mode error", rec.Body.String())
			}
			if childOpenCalled.Load() {
				t.Fatal("child open was called for invalid primary contract")
			}
			assertNoPrimaryCreateResidue(t, server, routeStore)
		})
	}
}

func TestPrimaryRoutedSessionCreatePeerOpenFailureRollsBackEarlyRoute(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	var openedSessionID string
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		var opened peerSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&opened); err != nil {
			t.Fatalf("decode child open: %v", err)
		}
		openedSessionID = opened.SessionID
		if route, ok, err := routeStore.Get(opened.SessionID); err != nil || !ok {
			t.Fatalf("route available during failed child open ok=%t err=%v", ok, err)
		} else if route.WorkspaceBindingID != "binding-primary-open-failure" || route.ChildSwarmID != "container-swarm" {
			t.Fatalf("route available during failed child open = %+v", route)
		}
		writeError(w, http.StatusBadGateway, errors.New("child open failed"))
	}))
	defer child.Close()

	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "container-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "container child",
		Relationship:         swarmruntime.RelationshipChild,
		Transport:            "remote",
		BackendURL:           child.URL,
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding-primary-open-failure",
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
		SourceWorkspacePath:       "/host/swarm-go",
		SourceWorkspaceName:       "swarm-go",
		DestinationRuntimeSwarmID: "container-swarm",
		DestinationHostSwarmID:    "host-swarm-id",
		DestinationWorkspacePath:  "/workspaces/swarm-go",
		LegacyTargetKind:          "local-container",
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"open fail","mode":"auto","workspace_binding_id":"binding-primary-open-failure","workspace_name":"swarm-go","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=container-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if strings.TrimSpace(openedSessionID) == "" {
		t.Fatal("child open was not called")
	}
	if sessions, err := sessionSvc.ListSessionsForAccount(testPrincipal().AccountScopeID, 10); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions = %+v err=%v, want no primary residue", sessions, err)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want no route residue", routes, err)
	}
	if _, ok, err := routeStore.Get(openedSessionID); err != nil {
		t.Fatalf("get rolled back route: %v", err)
	} else if ok {
		t.Fatalf("route %q still exists after peer-open failure rollback", openedSessionID)
	}
}

func TestPrimaryRoutedSessionCreateMirrorOpenFailureRollsBackRegularSession(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	var openedSessionID string
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		var opened peerSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&opened); err != nil {
			t.Fatalf("decode child open: %v", err)
		}
		openedSessionID = opened.SessionID
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             opened.SessionID + "-child-mismatch",
				"title":          opened.Request.Title,
				"workspace_path": opened.Request.RuntimeWorkspacePath,
				"workspace_name": opened.Request.WorkspaceName,
				"mode":           opened.Request.Mode,
				"created_at":     1,
				"updated_at":     2,
			},
		})
	}))
	defer child.Close()

	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "container-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "container child",
		Relationship:         swarmruntime.RelationshipChild,
		Transport:            "remote",
		BackendURL:           child.URL,
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding-primary-sync-failure",
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
		SourceWorkspacePath:       "/host/swarm-go",
		SourceWorkspaceName:       "swarm-go",
		DestinationRuntimeSwarmID: "container-swarm",
		DestinationHostSwarmID:    "host-swarm-id",
		DestinationWorkspacePath:  "/workspaces/swarm-go",
		LegacyTargetKind:          "local-container",
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"sync fail","mode":"auto","workspace_binding_id":"binding-primary-sync-failure","workspace_name":"swarm-go","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=container-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does not match sync source") {
		t.Fatalf("body = %s, want mirror sync failure", rec.Body.String())
	}
	if sessions, err := sessionSvc.ListSessionsForAccount(testPrincipal().AccountScopeID, 10); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions = %+v err=%v, want no primary residue", sessions, err)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want no route residue", routes, err)
	}
	if strings.TrimSpace(openedSessionID) == "" {
		t.Fatal("child open was not called")
	}
	if _, ok, err := routeStore.Get(openedSessionID); err != nil {
		t.Fatalf("get rolled back route: %v", err)
	} else if ok {
		t.Fatalf("route %q still exists after peer-open failure rollback", openedSessionID)
	}
	if topologyRoute, ok, err := server.topology.GetSessionRouteForAccount(testPrincipal().AccountScopeID, openedSessionID); err != nil {
		t.Fatalf("get topology route: %v", err)
	} else if ok {
		t.Fatalf("unexpected topology route residue: %+v", topologyRoute)
	}
}

func TestDesktopPrimaryToLocalContainerRoutedSessionE2EUsesPairingIdentity(t *testing.T) {
	primary, _, _, routeStore := newRoutedSessionTestServer(t)
	child, childSessionSvc, _, childRouteStore, childSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, child, childSwarmStore, "container-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)
	child.security = security.NewService(nil, nil)
	child.topology = nil // local containers do not need topology to trust an account-bound paired parent.
	childHandler := child.Handler()
	childHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		childHandler.ServeHTTP(w, r)
	}))
	defer childHTTP.Close()

	if err := primary.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "container-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "container child",
		Relationship:         swarmruntime.RelationshipChild,
		Transport:            "remote",
		BackendURL:           childHTTP.URL,
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if _, err := primary.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding-desktop-e2e",
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
		SourceWorkspacePath:       "/host/swarm-go",
		SourceWorkspaceName:       "swarm-go",
		DestinationRuntimeSwarmID: "container-swarm",
		DestinationHostSwarmID:    "host-swarm-id",
		DestinationWorkspacePath:  "/workspaces/swarm-go",
		LegacyTargetKind:          "local-container",
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"desktop e2e","mode":"auto","workspace_binding_id":"binding-desktop-e2e","workspace_name":"swarm-go","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"fireworks","model":"accounts/fireworks/models/kimi-k2p5","thinking":"high"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=container-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	primary.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("desktop routed session status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Session.ID == "" {
		t.Fatalf("missing session in payload: %s", rec.Body.String())
	}
	if payload.Session.UserID != testPrincipal().UserID || payload.Session.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("primary session identity = %q/%q", payload.Session.UserID, payload.Session.AccountScopeID)
	}
	primaryRoute, ok, err := routeStore.Get(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("primary route ok=%t err=%v", ok, err)
	}
	if primaryRoute.WorkspaceBindingID != "binding-desktop-e2e" || primaryRoute.HostSwarmID != "host-swarm-id" || primaryRoute.ChildSwarmID != "container-swarm" {
		t.Fatalf("primary route = %+v", primaryRoute)
	}
	childSession, ok, err := childSessionSvc.GetSession(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("child session ok=%t err=%v", ok, err)
	}
	if childSession.UserID != testPrincipal().UserID || childSession.AccountScopeID != testPrincipal().AccountScopeID || childSession.WorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("child session = %+v", childSession)
	}
	childRoute, ok, err := childRouteStore.Get(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("child route ok=%t err=%v", ok, err)
	}
	if childRoute.UserID != testPrincipal().UserID || childRoute.AccountScopeID != testPrincipal().AccountScopeID || childRoute.WorkspaceBindingID != "binding-desktop-e2e" {
		t.Fatalf("child route = %+v", childRoute)
	}
}

func TestPrimaryRoutedSessionCreateBuildsExplicitBindingContract(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var opened peerSessionOpenRequest
	var metadataCallbackStatus int
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&opened); err != nil {
			t.Fatalf("decode child open: %v", err)
		}
		route, ok, err := routeStore.Get(opened.SessionID)
		if err != nil || !ok {
			t.Fatalf("route available during child open ok=%t err=%v", ok, err)
		}
		if route.WorkspaceBindingID != "binding-primary-explicit" || route.ChildSwarmID != "container-swarm" || route.RuntimeWorkspacePath != "/workspaces/swarm-go" {
			t.Fatalf("route available during child open = %+v", route)
		}
		metadataReq := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/metadata", bytes.NewReader([]byte(`{"session_id":"`+opened.SessionID+`","metadata":{"background_run":{"active":true}}}`)))
		metadataReq = metadataReq.WithContext(context.WithValue(metadataReq.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "container-swarm"}))
		metadataRec := httptest.NewRecorder()
		server.handlePeerSessionMetadata(metadataRec, metadataReq)
		metadataCallbackStatus = metadataRec.Code
		if metadataRec.Code != http.StatusOK {
			t.Fatalf("metadata callback during child open status = %d, body=%s", metadataRec.Code, metadataRec.Body.String())
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             opened.SessionID,
				"title":          opened.Request.Title,
				"workspace_path": opened.Request.RuntimeWorkspacePath,
				"workspace_name": opened.Request.WorkspaceName,
				"mode":           opened.Request.Mode,
				"created_at":     1,
				"updated_at":     2,
			},
		})
	}))
	defer child.Close()

	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "container-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "container child",
		Relationship:         swarmruntime.RelationshipChild,
		Transport:            "remote",
		BackendURL:           child.URL,
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding-primary-explicit",
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
		SourceWorkspacePath:       "/host/swarm-go",
		SourceWorkspaceName:       "swarm-go",
		DestinationRuntimeSwarmID: "container-swarm",
		DestinationHostSwarmID:    "host-swarm-id",
		DestinationWorkspacePath:  "/workspaces/swarm-go",
		LegacyTargetKind:          "local-container",
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"explicit","mode":"auto","workspace_binding_id":"binding-primary-explicit","workspace_name":"swarm-go","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=container-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if metadataCallbackStatus != http.StatusOK {
		t.Fatalf("metadata callback during child open status = %d, want %d", metadataCallbackStatus, http.StatusOK)
	}
	if opened.Request.RuntimeWorkspacePath != "/workspaces/swarm-go" || opened.Request.WorkspacePath != "/workspaces/swarm-go" || opened.Request.HostWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("child request paths = workspace %q host %q runtime %q", opened.Request.WorkspacePath, opened.Request.HostWorkspacePath, opened.Request.RuntimeWorkspacePath)
	}
	if opened.Hosted.HostSwarmID != "host-swarm-id" || opened.Route.HostSwarmID != "host-swarm-id" || opened.Route.ChildSwarmID != "container-swarm" {
		t.Fatalf("open contract hosted=%+v route=%+v", opened.Hosted, opened.Route)
	}
	if opened.Route.RuntimeWorkspacePath != "/workspaces/swarm-go" || opened.Route.HostWorkspacePath != "" || opened.Route.ChildBackendURL != "" || opened.Route.WorkspaceBindingID != "binding-primary-explicit" {
		t.Fatalf("route contract = %+v", opened.Route)
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
	if err != nil || !ok {
		t.Fatalf("route ok=%t err=%v", ok, err)
	}
	if route.RuntimeWorkspacePath != "/workspaces/swarm-go" || route.HostSwarmID != "host-swarm-id" || route.WorkspaceBindingID != "binding-primary-explicit" {
		t.Fatalf("stored route = %+v", route)
	}
	targets, _, err := server.swarmTargetsForRequest(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/swarms/targets", nil)))
	if err != nil {
		t.Fatalf("swarm targets: %v", err)
	}
	var targetRoute targetWorkspaceRoute
	for _, target := range targets {
		if !strings.EqualFold(target.SwarmID, "container-swarm") || len(target.WorkspaceRoutes) == 0 {
			continue
		}
		targetRoute = target.WorkspaceRoutes[0]
	}
	if targetRoute.AccountScopeID != testPrincipal().AccountScopeID || targetRoute.TargetSwarmID != "container-swarm" || targetRoute.RuntimeSwarmID != "container-swarm" || targetRoute.HostSwarmID != "host-swarm-id" || targetRoute.WorkspaceBindingID != "binding-primary-explicit" || targetRoute.WorkspaceName != "swarm-go" {
		t.Fatalf("target workspace route identity = %+v", targetRoute)
	}
	if targetRoute.HostWorkspacePath != "/host/swarm-go" || targetRoute.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("target workspace route metadata = %+v", targetRoute)
	}
}

func TestPrimaryRoutedSessionCreateRejectsPathAuthorityForBindingRoute(t *testing.T) {
	cases := []struct {
		name      string
		fieldJSON string
	}{
		{name: "workspace path", fieldJSON: `"workspace_path":"/frontend/stale/swarm-go"`},
		{name: "host workspace path", fieldJSON: `"host_workspace_path":"/frontend/stale-host/swarm-go"`},
		{name: "runtime workspace path", fieldJSON: `"runtime_workspace_path":"/frontend/stale-runtime/swarm-go"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, routeStore := newRoutedSessionTestServer(t)
			var childOpenCalled atomic.Bool
			child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				childOpenCalled.Store(true)
				t.Fatalf("child open should not be called when frontend sends route path authority")
			}))
			defer child.Close()
			if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
				SwarmID:              "container-swarm",
				UserID:               testPrincipal().UserID,
				AccountScopeID:       testPrincipal().AccountScopeID,
				Name:                 "container child",
				Relationship:         swarmruntime.RelationshipChild,
				Transport:            "remote",
				BackendURL:           child.URL,
				Status:               "attached",
				OwnerHostSwarmID:     "host-swarm-id",
				OwnerHostContainerID: "host-container-1",
			}); err != nil {
				t.Fatalf("upsert runtime: %v", err)
			}
			if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
				BindingID:                 "binding-path-rejected",
				UserID:                    testPrincipal().UserID,
				AccountScopeID:            testPrincipal().AccountScopeID,
				SourceWorkspacePath:       "/host/swarm-go",
				SourceWorkspaceName:       "swarm-go",
				DestinationRuntimeSwarmID: "container-swarm",
				DestinationHostSwarmID:    "host-swarm-id",
				DestinationWorkspacePath:  "/workspaces/swarm-go",
				LegacyTargetKind:          "local-container",
			}); err != nil {
				t.Fatalf("upsert binding: %v", err)
			}

			body := bytes.NewBufferString(`{"title":"path authority","mode":"auto",` + tc.fieldJSON + `,"workspace_binding_id":"binding-path-rejected","workspace_name":"swarm-go","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=container-swarm", body)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "routed session workspace paths must be resolved from workspace binding") {
				t.Fatalf("body = %s, want path authority rejection", rec.Body.String())
			}
			if childOpenCalled.Load() {
				t.Fatal("child open was called when frontend sent route path authority")
			}
			assertNoPrimaryCreateResidue(t, server, routeStore)
		})
	}
}

func assertNoPrimaryCreateResidue(t *testing.T, _ *Server, routeStore *pebblestore.SessionRouteStore) {
	t.Helper()
	if routeStore != nil {
		routes, err := routeStore.List(10)
		if err != nil {
			t.Fatalf("list routes: %v", err)
		}
		if len(routes) != 0 {
			t.Fatalf("routes = %+v, want no route", routes)
		}
	}
}

func TestPrimaryRoutedSessionCreateRejectsWorkspaceNameOnlyWhenBindingExists(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var opened peerSessionOpenRequest
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&opened); err != nil {
			t.Fatalf("decode child open: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             opened.SessionID,
				"title":          opened.Request.Title,
				"workspace_path": opened.Request.RuntimeWorkspacePath,
				"workspace_name": opened.Request.WorkspaceName,
				"mode":           opened.Request.Mode,
				"metadata":       opened.Request.Metadata,
				"created_at":     1,
				"updated_at":     2,
			},
		})
	}))
	defer child.Close()

	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "container-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "container child",
		Relationship:         swarmruntime.RelationshipChild,
		Transport:            "remote",
		BackendURL:           child.URL,
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-name-match",
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspacePath:             "/real/host/path/swarm-go",
		SourceWorkspaceName:             "swarm-go",
		DestinationRuntimeSwarmID:       "container-swarm",
		DestinationAuthorityHostSwarmID: "host-swarm-id",
		DestinationHostSwarmID:          "host-swarm-id",
		DestinationContainerID:          "host-container-1",
		DestinationWorkspacePath:        "/real/container/path/swarm-go",
		PlacementGeneration:             1,
		LegacyTargetKind:                "local-container",
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"name route","mode":"auto","workspace_name":"swarm-go","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=container-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routed session workspace binding is required") {
		t.Fatalf("body = %s, want required binding error", rec.Body.String())
	}
	if strings.TrimSpace(opened.SessionID) != "" {
		t.Fatalf("child open should not be called without binding id: %+v", opened)
	}
	if routeStore != nil {
		routes, err := routeStore.List(100)
		if err != nil {
			t.Fatalf("list routes: %v", err)
		}
		if len(routes) != 0 {
			t.Fatalf("routes = %+v, want none", routes)
		}
	}
}

func TestPrimaryRoutedSessionCreateRequiresBindingIdentityForLocalContainerWhenWorkspaceNameMissing(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("child open should not be called without workspace identity")
	}))
	defer child.Close()
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "container-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "container child",
		Relationship:         swarmruntime.RelationshipChild,
		Transport:            "remote",
		BackendURL:           child.URL,
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"no identity","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=container-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routed session workspace binding is required") {
		t.Fatalf("body = %s, want required binding error", rec.Body.String())
	}
}

func TestPrimaryRoutedSessionCreateRejectsWorkspaceNameOnlyWithoutAmbiguityLookup(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "container-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "container child",
		Relationship:         swarmruntime.RelationshipChild,
		Transport:            "remote",
		BackendURL:           "https://child.example.test",
		Status:               "attached",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	body := bytes.NewBufferString(`{"title":"name-only","mode":"auto","workspace_name":"swarm-go","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=container-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routed session workspace binding is required") {
		t.Fatalf("body = %s, want required binding error", rec.Body.String())
	}
}

func TestRemoteDeploySessionCreateUsesRemotePayloadTargetPath(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var openedWorkspacePath atomic.Value
	var openedRequestWorkspacePath atomic.Value
	var openedHostWorkspacePath atomic.Value
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
		openedRequestWorkspacePath.Store(req.Request.WorkspacePath)
		openedHostWorkspacePath.Store(req.Request.HostWorkspacePath)
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

	body := bytes.NewBufferString(`{"title":"remote","mode":"plan","workspace_path":"/frontend/stale/swarm-go","host_workspace_path":"/frontend/stale-host/swarm-go","runtime_workspace_path":"/frontend/stale-runtime/swarm-go","workspace_name":"swarm-go","preference":{"provider":"fireworks","model":"accounts/fireworks/models/kimi-k2p5","thinking":"high"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=child-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, _ := openedWorkspacePath.Load().(string); got != "/workspaces/swarm-go" {
		t.Fatalf("child workspace path = %q, want %q", got, "/workspaces/swarm-go")
	}
	if got, _ := openedRequestWorkspacePath.Load().(string); got != "/workspaces/swarm-go" {
		t.Fatalf("child request workspace path = %q, want resolved runtime path", got)
	}
	if got, _ := openedHostWorkspacePath.Load().(string); got != "/workspaces/swarm-go" {
		t.Fatalf("child host workspace path = %q, want resolved runtime path", got)
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

func TestRemoteSessionCreateUsesRegistryMagicDNSBackend(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var openedWorkspacePath atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		var req peerSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode child request: %v", err)
		}
		openedWorkspacePath.Store(req.Request.RuntimeWorkspacePath)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             req.SessionID,
				"title":          req.Request.Title,
				"workspace_path": req.Request.RuntimeWorkspacePath,
				"workspace_name": req.Request.WorkspaceName,
				"mode":           req.Request.Mode,
				"created_at":     1,
				"updated_at":     2,
			},
		})
	}))
	defer child.Close()

	nodes := server.swarmNodes
	if nodes == nil {
		t.Fatal("swarm node store not configured")
	}
	if _, err := nodes.Put(pebblestore.SwarmNodeRecord{
		SwarmID:      "registry-child",
		Name:         "registry child",
		Role:         "child",
		Kind:         "remote",
		Transport:    "tailscale",
		BackendURL:   child.URL,
		MagicDNSName: "registry-child.tailnet.ts.net",
		DeploymentID: "remote-deploy-registry",
		Source:       "remote-deploy",
		Status:       "online",
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}
	server.SetSwarmNodeStore(nodes)
	server.swarmTargetHealth.entries = map[string]swarmTargetHealthEntry{
		"remote|registry-child|" + child.URL: {online: true, checkedAt: time.Now()},
	}

	body := bytes.NewBufferString(`{"title":"remote","mode":"plan","workspace_path":"/src/swarm-go","workspace_name":"swarm-go","preference":{"provider":"fireworks","model":"accounts/fireworks/models/kimi-k2p5","thinking":"high"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=registry-child", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1", hits.Load())
	}
	if got, _ := openedWorkspacePath.Load().(string); got != "/src/swarm-go" {
		t.Fatalf("child workspace path = %q, want %q", got, "/src/swarm-go")
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
	if route.ChildSwarmID != "registry-child" || route.ChildBackendURL != "" || route.HostWorkspacePath != "" {
		t.Fatalf("route execution = %+v, want registry-child with no stored backend/host path authority", route)
	}
}

func TestPrimaryRoutedSessionCreateDispatchesWorkspaceBackedOpenToAuthorityHost(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var opened atomic.Int32
	var openedPath atomic.Value
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		opened.Add(1)
		var req peerSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode authority open: %v", err)
		}
		if req.Route.ChildSwarmID != "managed-container" {
			t.Fatalf("route child = %q, want managed-container", req.Route.ChildSwarmID)
		}
		if req.Route.HostSwarmID != "managed-host" {
			t.Fatalf("route host = %q, want managed-host", req.Route.HostSwarmID)
		}
		if req.Route.HostContainerID != "managed-host:container-1" {
			t.Fatalf("route container = %q, want managed-host:container-1", req.Route.HostContainerID)
		}
		openedPath.Store(req.Request.RuntimeWorkspacePath)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             req.SessionID,
				"title":          req.Request.Title,
				"workspace_path": req.Request.RuntimeWorkspacePath,
				"workspace_name": req.Request.WorkspaceName,
				"mode":           req.Request.Mode,
				"created_at":     1,
				"updated_at":     2,
			},
		})
	}))
	defer authority.Close()

	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-host", Name: "managed host", Relationship: "managed", Status: "online"}); err != nil {
		t.Fatalf("upsert managed host: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-container", Name: "managed container", Relationship: "child", OwnerHostSwarmID: "managed-host", OwnerHostContainerID: "managed-host:container-1", Status: "attached"}); err != nil {
		t.Fatalf("upsert managed container: %v", err)
	}
	if err := server.RegisterAuthorityConnection(AuthorityConnection{AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "managed-host", TransportKind: authorityConnectionTransportHTTP, TransportRef: authority.URL}); err != nil {
		t.Fatalf("register authority: %v", err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-managed-container",
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               "workspace-managed-container",
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             "/source/workspace",
		SourceWorkspaceName:             "workspace",
		DestinationRuntimeSwarmID:       "managed-container",
		DestinationAuthorityHostSwarmID: "managed-host",
		DestinationHostSwarmID:          "managed-host",
		DestinationContainerID:          "managed-host:container-1",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		DestinationWorkspacePath:        "/runtime/workspace",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           "managed-host",
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"managed","mode":"plan","workspace_name":"workspace","workspace_binding_id":"binding-managed-container","worktree_mode":"off","preference":{"provider":"fireworks","model":"accounts/fireworks/models/kimi-k2p5","thinking":"high"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=managed-container", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if opened.Load() != 1 {
		t.Fatalf("authority opens = %d, want 1", opened.Load())
	}
	if got, _ := openedPath.Load().(string); got != "/runtime/workspace" {
		t.Fatalf("runtime path = %q, want binding runtime path", got)
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
	if err != nil || !ok {
		t.Fatalf("get route ok=%t err=%v", ok, err)
	}
	if route.ChildSwarmID != "managed-container" || route.HostSwarmID != "managed-host" || route.ChildBackendURL != "" || route.HostWorkspacePath != "" {
		t.Fatalf("unexpected session execution route: %+v", route)
	}
}

func TestPeerSessionOpenRejectsStalePlacementGeneration(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-swarm", Name: "managed host", Relationship: "managed", Status: "online"}); err != nil {
		t.Fatalf("upsert managed host: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "child-swarm", Name: "managed child", Relationship: "child", OwnerHostSwarmID: "managed-swarm", OwnerHostContainerID: "managed-swarm:container-1", Status: "attached"}); err != nil {
		t.Fatalf("upsert child: %v", err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-stale-placement",
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               "workspace-stale-placement",
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             "/source/workspace",
		SourceWorkspaceName:             "workspace",
		DestinationRuntimeSwarmID:       "child-swarm",
		DestinationAuthorityHostSwarmID: "managed-swarm",
		DestinationHostSwarmID:          "managed-swarm",
		DestinationContainerID:          "managed-swarm:container-1",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		DestinationWorkspacePath:        "/runtime/workspace",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AttestedByHostSwarmID:           "managed-swarm",
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}
	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-stale-placement",
		Request: func() sessionCreateRequest {
			req := sessionCreateRequest{Title: "stale", WorkspacePath: "/runtime/workspace", HostWorkspacePath: "/runtime/workspace", RuntimeWorkspacePath: "/runtime/workspace", WorkspaceBindingID: "binding-stale-placement", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, AgentName: "swarm", WorktreeMode: runruntime.RunWorktreeModeOff}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Hosted: sessionruntime.HostedSessionDescriptor{HostSwarmID: "managed-swarm", RuntimeWorkspacePath: "/runtime/workspace", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:  pebblestore.SessionRouteRecord{SessionID: "session-stale-placement", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", HostSwarmID: "managed-swarm", HostContainerID: "managed-swarm:container-1", RuntimeWorkspacePath: "/runtime/workspace", WorkspaceBindingID: "binding-stale-placement", PlacementGeneration: 2, BindingGeneration: 1},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "managed-swarm"}))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if _, ok, err := sessionSvc.GetSession("session-stale-placement"); err != nil || ok {
		t.Fatalf("get session ok=%t err=%v, want no session", ok, err)
	}
	if _, ok, err := routeStore.Get("session-stale-placement"); err != nil || ok {
		t.Fatalf("get route ok=%t err=%v, want no route", ok, err)
	}
}

func TestRemoteDeploySessionStartIsRetired(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	fake := &fakeRemoteDeployService{}
	server.SetRemoteDeployService(fake)

	body := bytes.NewBufferString(`{"session_id":"remote-start-1","tailscale_auth_key":"tskey-launch-only"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/remote/session/start", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusGone, rec.Body.String())
	}
	if fake.lastStartInput.SessionID != "" || fake.lastStartInput.TailscaleAuthKey != "" {
		t.Fatalf("retired start path called remote deploy service: %+v", fake.lastStartInput)
	}
	var payload struct {
		PathID string `json:"path_id"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.PathID != remotedeploy.PathSessionStart {
		t.Fatalf("path_id = %q, want %q", payload.PathID, remotedeploy.PathSessionStart)
	}
	if !strings.Contains(payload.Error, "SSH remote deploy is retired") {
		t.Fatalf("error = %q, want retired guidance", payload.Error)
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
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

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

func configureRoutedSessionTestServerAsChild(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, childSwarmID, parentSwarmID, userID, accountScopeID string) {
	t.Helper()
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: childSwarmID, Name: "child-swarm", Role: "child"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{PairingState: startupconfig.PairingStatePaired, ParentSwarmID: parentSwarmID, UserID: userID, AccountScopeID: accountScopeID}); err != nil {
		t.Fatalf("put local pairing: %v", err)
	}
	server.SetSwarmStore(swarmStore)
	server.SetSwarmService(fakeRoutedSwarmService{
		state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: childSwarmID, Name: "child-swarm", Role: "child"}},
		token: "peer-token",
	})
	startupPath := filepath.Join(t.TempDir(), "child-swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.Child = true
	cfg.SwarmName = "child-swarm"
	cfg.ParentSwarmID = parentSwarmID
	cfg.PairingState = startupconfig.PairingStatePaired
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7782
	cfg.AdvertisePort = 7782
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write child startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
}

func newRoutedSessionTestServer(t *testing.T) (*Server, *sessionruntime.Service, *permission.Service, *pebblestore.SessionRouteStore) {
	server, sessionSvc, permissionSvc, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
	return server, sessionSvc, permissionSvc, routeStore
}

func newRoutedSessionTestServerWithSwarmStore(t *testing.T) (*Server, *sessionruntime.Service, *permission.Service, *pebblestore.SessionRouteStore, *pebblestore.SwarmStore) {
	t.Helper()
	t.Setenv("SWARM_API_NO_AUTH", "1")

	var server *Server
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "routed-session-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if server != nil {
			server.CancelInFlightRuns()
			server.WaitForInFlightRuns(2 * time.Second)
		}
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	permissionSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permissionSvc.SetSessionResolver(sessionSvc)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure agent defaults: %v", err)
	}
	if _, _, _, err := agentSvc.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm test primary prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}}); err != nil {
		t.Fatalf("create swarm agent: %v", err)
	}
	routeStore := pebblestore.NewSessionRouteStore(store)
	nodeStore := pebblestore.NewSwarmNodeStore(store)
	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	server = NewServer(nil, agentSvc, modelSvc, nil, sessionSvc, nil, nil, nil, nil, permissionSvc, nil, eventLog, stream.NewHub(eventLog))
	server.v3SessionExecutor = nil
	server.SetTopologyService(topologyruntime.NewService(topologyStore, swarmStore, nil, nil, nil, nil, routeStore, pebblestore.NewWorkspaceStore(store)))
	server.SetSessionRouteStore(routeStore)
	server.SetSwarmNodeStore(nodeStore)
	server.SetSwarmStore(swarmStore)
	server.SetSwarmMirrorStore(pebblestore.NewSwarmMirrorStore(store))
	server.SetSwarmDesktopTargetSelectionStore(pebblestore.NewSwarmDesktopTargetSelectionStore(store))
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
	cfg.SwarmName = "host-swarm"
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)

	return server, sessionSvc, permissionSvc, routeStore, swarmStore
}

func seedRoutedSession(t *testing.T, sessionSvc *sessionruntime.Service) string {
	t.Helper()

	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-routed",
		Title:          "Routed Session",
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModePlan,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
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

func testStringPtr(value string) *string {
	return &value
}

type recordingHostedSessionSync struct {
	updateMetadataCalls int
	session             pebblestore.SessionSnapshot
}

func (f *recordingHostedSessionSync) AppendMessage(context.Context, sessionruntime.HostedSessionDescriptor, string, string, string, map[string]any) (pebblestore.MessageSnapshot, pebblestore.SessionSnapshot, error) {
	return pebblestore.MessageSnapshot{}, f.session, nil
}

func (f *recordingHostedSessionSync) SetMode(context.Context, sessionruntime.HostedSessionDescriptor, string, string) (pebblestore.SessionSnapshot, error) {
	return f.session, nil
}

func (f *recordingHostedSessionSync) SetTitle(context.Context, sessionruntime.HostedSessionDescriptor, string, string) (pebblestore.SessionSnapshot, error) {
	return f.session, nil
}

func (f *recordingHostedSessionSync) UpdateMetadata(context.Context, sessionruntime.HostedSessionDescriptor, string, map[string]any) (pebblestore.SessionSnapshot, error) {
	f.updateMetadataCalls++
	return f.session, nil
}

func (f *recordingHostedSessionSync) UpsertLifecycle(context.Context, sessionruntime.HostedSessionDescriptor, pebblestore.SessionLifecycleSnapshot) error {
	return nil
}

func (f *recordingHostedSessionSync) PublishEvent(context.Context, sessionruntime.HostedSessionDescriptor, string, string, map[string]any, string, string) (pebblestore.EventEnvelope, error) {
	return pebblestore.EventEnvelope{}, nil
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

func (f *fakeRemoteDeployService) Get(_ context.Context, sessionID string, _ bool) (remotedeploy.Session, error) {
	for _, session := range f.sessions {
		if session.ID == sessionID {
			return session, nil
		}
	}
	return remotedeploy.Session{}, errors.New("remote deploy session not found")
}

func (f *fakeRemoteDeployService) Create(_ context.Context, input remotedeploy.CreateSessionInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeRemoteDeployService) UpdateSettings(_ context.Context, input remotedeploy.UpdateSettingsInput) (remotedeploy.Session, error) {
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

func (f fakeRoutedSwarmService) RenameLocalSwarm(input swarmruntime.RenameLocalSwarmInput) (swarmruntime.LocalState, error) {
	state := f.state
	state.Node.Name = strings.TrimSpace(input.Name)
	return state, nil
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

func (f fakeRoutedSwarmService) ValidateIncomingPeerAuth(_ string, token string) (bool, error) {
	return strings.TrimSpace(token) == strings.TrimSpace(f.token), nil
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

func (f fakeRoutedSwarmService) ApproveManagedPairing(swarmruntime.ApproveManagedPairingInput) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeRoutedSwarmService) TrustManagedPeer(swarmruntime.TrustManagedPeerInput) (swarmruntime.TrustedPeer, error) {
	return swarmruntime.TrustedPeer{}, nil
}

func (f fakeRoutedSwarmService) RemoveManagedPeer(swarmruntime.RemoveManagedPeerInput) (swarmruntime.RemoveManagedPeerResult, error) {
	return swarmruntime.RemoveManagedPeerResult{}, nil
}

func (f fakeRoutedSwarmService) UpdateLocalPairingFromConfig(startupconfig.FileConfig, []swarmruntime.TransportSummary) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeRoutedSwarmService) DetachToStandalone(string) error {
	return nil
}

var _ swarmService = fakeRoutedSwarmService{}

func TestPeerSessionEventRejectsRequiredPayloadFailures(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantErrSubstr string
	}{
		{
			name:          "message event missing payload",
			body:          `{"session_id":"session-strict-event","event_type":"run.message.stored","payload":{"type":"message.stored","session_id":"session-strict-event"}}`,
			wantErrSubstr: "requires message payload",
		},
		{
			name:          "message event mismatched session",
			body:          `{"session_id":"session-strict-event","event_type":"run.message.stored","payload":{"type":"message.stored","session_id":"session-strict-event","message":{"id":"msg_00000000000000000008","session_id":"other-session","global_seq":8,"role":"assistant","content":"wrong session","created_at":123}}}`,
			wantErrSubstr: "does not match event session_id",
		},
		{
			name:          "message event zero global seq",
			body:          `{"session_id":"session-strict-event","event_type":"run.message.stored","payload":{"type":"message.stored","session_id":"session-strict-event","message":{"id":"msg_00000000000000000009","session_id":"session-strict-event","global_seq":0,"role":"assistant","content":"zero seq","created_at":123}}}`,
			wantErrSubstr: "global_seq is required",
		},
		{
			name:          "message event missing payload session",
			body:          `{"session_id":"session-strict-event","event_type":"run.message.stored","payload":{"type":"message.stored","session_id":"session-strict-event","message":{"id":"msg_00000000000000000011","global_seq":11,"role":"assistant","content":"missing session","created_at":123}}}`,
			wantErrSubstr: "message payload session_id is required",
		},
		{
			name:          "lifecycle event missing payload",
			body:          `{"session_id":"session-strict-event","event_type":"session.lifecycle.updated","payload":{"type":"session.lifecycle.updated","session_id":"session-strict-event"}}`,
			wantErrSubstr: "requires lifecycle payload",
		},
		{
			name:          "lifecycle event mismatched session",
			body:          `{"session_id":"session-strict-event","event_type":"session.lifecycle.updated","payload":{"type":"session.lifecycle.updated","session_id":"session-strict-event","lifecycle":{"session_id":"other-session","run_id":"run-live","active":true,"phase":"running","updated_at":123}}}`,
			wantErrSubstr: "does not match event session_id",
		},
		{
			name:          "lifecycle event missing payload session",
			body:          `{"session_id":"session-strict-event","event_type":"session.lifecycle.updated","payload":{"type":"session.lifecycle.updated","session_id":"session-strict-event","lifecycle":{"run_id":"run-live","active":true,"phase":"running","updated_at":123}}}`,
			wantErrSubstr: "lifecycle payload session_id is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
			if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-strict-event", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/host/workspace", WorkspaceName: "workspace", Title: "Strict event", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
				t.Fatalf("store session: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/event", bytes.NewReader([]byte(tc.body)))
			rec := httptest.NewRecorder()
			server.handlePeerSessionEvent(rec, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantErrSubstr) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tc.wantErrSubstr)
			}
			events, err := server.events.ReadFrom(1, 20)
			if err != nil {
				t.Fatalf("read events: %v", err)
			}
			for _, event := range events {
				if event.EntityID == "session-strict-event" && (event.EventType == "run.message.stored" || event.EventType == "session.lifecycle.updated") {
					t.Fatalf("event was persisted despite payload failure: %+v", event)
				}
			}
		})
	}
}

func TestPeerSessionEventRejectsMessagePayloadWhenLocalSessionMissing(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{SessionID: "session-route-only", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/workspace", RuntimeWorkspacePath: "/runtime/workspace"}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	body := []byte(`{"session_id":"session-route-only","event_type":"run.message.stored","payload":{"type":"message.stored","session_id":"session-route-only","message":{"id":"msg_00000000000000000010","session_id":"session-route-only","global_seq":10,"role":"assistant","content":"no local session","created_at":123}}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/event", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handlePeerSessionEvent(rec, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session-route-only") || !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("body = %s, want missing session error", rec.Body.String())
	}
}
