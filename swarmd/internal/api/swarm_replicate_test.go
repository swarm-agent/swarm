package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestSwarmReplicateLeavesRuntimeEmptyWhenCallerOmitsIt(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":       "local",
		"swarm_name": "replica-a",
		"sync": map[string]any{
			"enabled": true,
			"mode":    "managed",
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeDeploy.lastCreateInput.Runtime != "" {
		t.Fatalf("create runtime = %q, want empty", fakeDeploy.lastCreateInput.Runtime)
	}
	if fakeDeploy.lastCreateInput.BypassPermissions {
		t.Fatalf("create bypass = %t, want false", fakeDeploy.lastCreateInput.BypassPermissions)
	}
}

func TestSwarmReplicatePassesRequestedRuntimeThroughToDeployCreate(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":               "local",
		"swarm_name":         "replica-b",
		"runtime":            "docker",
		"bypass_permissions": true,
		"sync": map[string]any{
			"enabled": true,
			"mode":    "managed",
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeDeploy.lastCreateInput.Runtime != "docker" {
		t.Fatalf("create runtime = %q, want %q", fakeDeploy.lastCreateInput.Runtime, "docker")
	}
	if !fakeDeploy.lastCreateInput.BypassPermissions {
		t.Fatalf("create bypass = %t, want true", fakeDeploy.lastCreateInput.BypassPermissions)
	}
	var response struct {
		Swarm struct {
			BypassPermissions bool `json:"bypass_permissions"`
		} `json:"swarm"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Swarm.BypassPermissions {
		t.Fatalf("response swarm bypass_permissions = %t, want true", response.Swarm.BypassPermissions)
	}
}

func TestSwarmReplicatePassesVaultPasswordThroughToDeployCreate(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":       "local",
		"swarm_name": "replica-c",
		"sync": map[string]any{
			"enabled":        true,
			"mode":           "managed",
			"vault_password": "vault-password",
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeDeploy.lastCreateInput.SyncVaultPassword != "vault-password" {
		t.Fatalf("create sync vault password = %q, want %q", fakeDeploy.lastCreateInput.SyncVaultPassword, "vault-password")
	}
}

func TestSwarmReplicateDefaultsSyncModulesToManagedDefaults(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":       "local",
		"swarm_name": "replica-d",
		"sync": map[string]any{
			"enabled": true,
			"mode":    "managed",
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got, want := fakeDeploy.lastCreateInput.SyncModules, []string{"credentials", "agents", "custom_tools", "skills", "permissions", "model_defaults"}; len(got) != len(want) {
		t.Fatalf("create sync modules = %#v, want %#v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("create sync modules = %#v, want %#v", got, want)
			}
		}
	}
}

func TestSwarmReplicatePassesRequestedSyncModulesThroughToDeployCreate(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":       "local",
		"swarm_name": "replica-e",
		"sync": map[string]any{
			"enabled": true,
			"mode":    "managed",
			"modules": []string{"credentials", "agents", "custom_tools", "agents"},
		},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	got := fakeDeploy.lastCreateInput.SyncModules
	want := []string{"credentials", "agents", "custom_tools"}
	if len(got) != len(want) {
		t.Fatalf("create sync modules length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("create sync modules = %#v, want %#v", got, want)
		}
	}
}

func TestSwarmReplicateLocalManagedTargetValidatesTargetWithoutRemoteMode(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	var sawHealth bool
	managedHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz", "/healthz":
			sawHealth = true
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case peerWorkspaceDiscoverPath:
			writeJSON(w, http.StatusOK, peerWorkspaceDiscoverResponse{OK: true, Workspaces: []peerWorkspaceInfo{{Path: workspacePath, Name: "workspace", GitWorkspace: true}}})
		case "/v1/deploy/container/create":
			var payload deployContainerCreatePayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode managed host create payload: %v", err)
			}
			if payload.GroupID != "" || payload.GroupName != "" || payload.GroupNetworkName != "" {
				t.Fatalf("managed host create got primary group fields: group_id=%q group_name=%q group_network_name=%q", payload.GroupID, payload.GroupName, payload.GroupNetworkName)
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment": deployruntime.ContainerDeployment{ID: "deployment-1", Name: "replica-managed-host-target", Status: "running", AttachStatus: "attached", GroupID: "managed-host-local-group", GroupName: "Managed Host Local Group", ChildSwarmID: "child-swarm-1", ChildDisplayName: "replica-managed-host-target"}})
		default:
			t.Fatalf("managed host path = %q", r.URL.Path)
		}
	}))
	t.Cleanup(managedHost.Close)
	setReplicateFakeSwarmState(handler, swarmStateWithManagedPeer(managedHost.URL, "host-to-managed-token"))

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":                 "local",
		"swarm_name":           "replica-managed-host-target",
		"target_host_swarm_id": "managed-swarm-1",
		"sync":                 map[string]any{"enabled": true, "mode": "managed"},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !sawHealth {
		t.Fatal("managed host target was not health checked")
	}
	if fakeDeploy.lastCreateInput.Name != "" {
		t.Fatalf("create input name = %q, want managed target request to avoid primary local create path", fakeDeploy.lastCreateInput.Name)
	}
	if fakeDeploy.lastMirroredDeployment.HostSwarmID != "managed-swarm-1" {
		t.Fatalf("mirrored host swarm id = %q, want managed-swarm-1", fakeDeploy.lastMirroredDeployment.HostSwarmID)
	}
	if fakeDeploy.lastMirroredDeployment.GroupID != "" {
		t.Fatalf("mirrored group id = %q, want empty for managed host target", fakeDeploy.lastMirroredDeployment.GroupID)
	}
	if fakeDeploy.lastMirroredDeployment.HostContainerID == "" {
		t.Fatal("expected mirrored deployment host_container_id")
	}
	if fakeDeploy.lastMirroredDeployment.AttachmentID == "" {
		t.Fatal("expected mirrored deployment attachment_id")
	}
	var response swarmReplicateResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Swarm.Mode != "local" {
		t.Fatalf("response mode = %q, want local", response.Swarm.Mode)
	}
	if len(response.Workspaces) != 1 {
		t.Fatalf("workspaces len = %d", len(response.Workspaces))
	}
	binding := response.Workspaces[0].Binding
	if binding.DestinationHostSwarmID != "managed-swarm-1" || binding.DestinationRuntimeSwarmID != "child-swarm-1" {
		t.Fatalf("binding destination chain = host %q runtime %q", binding.DestinationHostSwarmID, binding.DestinationRuntimeSwarmID)
	}
	if binding.DestinationContainerID != fakeDeploy.lastMirroredDeployment.HostContainerID {
		t.Fatalf("binding container id = %q, want %q", binding.DestinationContainerID, fakeDeploy.lastMirroredDeployment.HostContainerID)
	}
	if binding.DestinationWorkspacePath == workspacePath {
		t.Fatalf("binding destination workspace path = %q, want child-internal path", binding.DestinationWorkspacePath)
	}
}

func TestSwarmReplicateLocalManagedTargetDoesNotFallbackToPrimary(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":                 "local",
		"swarm_name":           "replica-missing-managed-host",
		"target_host_swarm_id": "missing-managed-host",
		"sync":                 map[string]any{"enabled": true, "mode": "managed"},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if fakeDeploy.lastCreateInput.Name != "" {
		t.Fatalf("managed target failure fell back to primary create: %+v", fakeDeploy.lastCreateInput)
	}
}

func TestSwarmReplicateRejectsTargetHostForRemoteMode(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode":                 "remote",
		"target_host_swarm_id": "managed-swarm-1",
		"sync":                 map[string]any{"enabled": true, "mode": "managed"},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	}, "managed-swarm-1")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if fakeDeploy.lastCreateInput.Name != "" {
		t.Fatalf("remote mode with target_host_swarm_id should not create local container: %+v", fakeDeploy.lastCreateInput)
	}
}

func TestSwarmReplicateRemoteMatchingWorkspaceCreatesLink(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)
	_ = fakeDeploy

	var sawDiscover bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.URL.Path != peerWorkspaceDiscoverPath {
			t.Fatalf("remote path = %q", r.URL.Path)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "host-to-managed-token" {
			t.Fatalf("missing peer auth headers: id=%q token=%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		sawDiscover = true
		writeJSON(w, http.StatusOK, peerWorkspaceDiscoverResponse{OK: true, Workspaces: []peerWorkspaceInfo{{Path: "/managed/workspace-one", Name: "workspace-one", GitWorkspace: true}}})
	}))
	t.Cleanup(remote.Close)
	setReplicateFakeSwarmState(handler, swarmStateWithManagedPeer(remote.URL, "host-to-managed-token"))

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode": "remote",
		"sync": map[string]any{"enabled": true, "mode": "managed"},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "bundle",
			"writable":              true,
		}},
	}, "managed-swarm-1")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !sawDiscover {
		t.Fatal("remote discover was not called")
	}
	if fakeDeploy.lastCreateInput.Name != "" {
		t.Fatalf("remote replicate should not create local container, got create input %+v", fakeDeploy.lastCreateInput)
	}
	var response swarmReplicateResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || response.Swarm.ID != "managed-swarm-1" || response.Swarm.Mode != "remote" {
		t.Fatalf("response swarm = %+v", response.Swarm)
	}
	if len(response.Workspaces) != 1 {
		t.Fatalf("workspaces len = %d", len(response.Workspaces))
	}
	binding := response.Workspaces[0].Binding
	if binding.LegacyTargetKind != "remote" || binding.DestinationRuntimeSwarmID != "managed-swarm-1" || binding.DestinationWorkspacePath != "/managed/workspace-one" {
		t.Fatalf("binding = %+v", binding)
	}
}

func TestRewriteReplicationBootstrapForTargetHostKeepsChildTargetPath(t *testing.T) {
	items := []deployruntime.ContainerWorkspaceBootstrap{{
		SourceWorkspacePath: "/source/primary/swarm-go",
		SourceWorkspaceName: "swarm-go",
		TargetWorkspacePath: "/workspaces/swarm-go",
		Directories: []deployruntime.ContainerWorkspaceBootstrapDirectory{{
			SourcePath: "/source/primary/swarm-go/pkg",
			TargetPath: "/workspaces/swarm-go/pkg",
		}},
	}}

	rewritten := rewriteReplicationBootstrapForTargetHost(items, map[string]string{
		"/source/primary/swarm-go": "/source/managed/swarm-go",
	})
	if len(rewritten) != 1 {
		t.Fatalf("rewritten len = %d, want 1", len(rewritten))
	}
	item := rewritten[0]
	if item.SourceWorkspacePath != "/source/managed/swarm-go" {
		t.Fatalf("SourceWorkspacePath = %q, want managed host materialized path", item.SourceWorkspacePath)
	}
	if item.TargetWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("TargetWorkspacePath = %q, want container mount path", item.TargetWorkspacePath)
	}
	if len(item.Directories) != 1 {
		t.Fatalf("directories len = %d, want 1", len(item.Directories))
	}
	if item.Directories[0].SourcePath != "/source/managed/swarm-go/pkg" {
		t.Fatalf("directory SourcePath = %q, want managed host materialized subpath", item.Directories[0].SourcePath)
	}
	if item.Directories[0].TargetPath != "/workspaces/swarm-go/pkg" {
		t.Fatalf("directory TargetPath = %q, want container mount subpath", item.Directories[0].TargetPath)
	}
}

func TestSwarmReplicateRemoteCopyModeFailsClearly(t *testing.T) {
	handler, _, workspacePath := newReplicateTestHandler(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, peerWorkspaceDiscoverResponse{OK: true})
	}))
	t.Cleanup(remote.Close)
	setReplicateFakeSwarmState(handler, swarmStateWithManagedPeer(remote.URL, "host-to-managed-token"))

	recorder := postReplicateRequest(t, handler, map[string]any{
		"mode": "remote",
		"sync": map[string]any{"enabled": false},
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
			"replication_mode":      "copy",
			"writable":              true,
		}},
	}, "managed-swarm-1")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "archive copy is not implemented") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestPeerWorkspaceDiscoverRequiresPeerAuth(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	recorder := httptest.NewRecorder()
	handler.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, peerWorkspaceDiscoverPath, nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
}

func TestPeerWorkspaceImportBundleRequiresPeerAuth(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, peerWorkspaceImportBundlePath, strings.NewReader("not a bundle"))
	recorder := httptest.NewRecorder()
	handler.Handler().ServeHTTP(recorder, withTestPrincipal(request))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
}

func TestBuildReplicationPlanUsesStandaloneWorkspaceTargetForLinkedDirectory(t *testing.T) {
	parent := "/src/parent"
	child := "/src/child"
	workspaces := []workspaceruntime.NormalizedReplicationWorkspace{
		{SourceWorkspacePath: parent, ReplicationMode: workspaceruntime.ReplicationModeBundle, Writable: true},
		{SourceWorkspacePath: child, ReplicationMode: workspaceruntime.ReplicationModeBundle, Writable: true},
	}
	catalog := map[string]replicateWorkspaceCatalogEntry{
		parent: {Name: "parent", Directories: []string{parent, child}, Active: true},
		child:  {Name: "child", Directories: []string{child}},
	}

	mounts, childPaths, bootstrap := buildReplicationPlan(workspaces, catalog, workspaceruntime.NormalizedReplicationSync{})

	if childPaths[child] != "/workspaces/child" {
		t.Fatalf("child target = %q, want /workspaces/child", childPaths[child])
	}
	if len(bootstrap) != 2 {
		t.Fatalf("bootstrap len = %d, want 2", len(bootstrap))
	}
	if len(bootstrap[0].Directories) != 1 {
		t.Fatalf("parent linked directories = %#v, want one child link", bootstrap[0].Directories)
	}
	if bootstrap[0].Directories[0].SourcePath != child || bootstrap[0].Directories[0].TargetPath != childPaths[child] {
		t.Fatalf("parent linked child = %+v, want %s at %s", bootstrap[0].Directories[0], child, childPaths[child])
	}
	var linkedMount localcontainers.Mount
	for _, mount := range mounts {
		if mount.SourcePath == child && mount.WorkspacePath == parent {
			linkedMount = mount
			break
		}
	}
	if linkedMount.TargetPath != childPaths[child] {
		t.Fatalf("linked child mount target = %q, want %q", linkedMount.TargetPath, childPaths[child])
	}
}

func newReplicateTestHandler(t *testing.T) (*Server, *fakeReplicateDeployService, string) {
	t.Helper()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "replicate-api.pebble"))
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

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	workspacePath := filepath.Join(t.TempDir(), "workspace-one")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if _, err := workspaceSvc.AddForPrincipal(testPrincipal(), workspacePath, "workspace-one", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	server := NewServer("test", nil, nil, nil, nil, nil, workspaceSvc, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	server.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(store), nil, nil, nil, nil, nil, nil, nil))

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = true
	cfg.SwarmName = "host-swarm"
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

	server.SetSwarmService(fakeReplicateSwarmService{
		state: swarmruntime.LocalState{
			Node: swarmruntime.LocalNodeState{
				SwarmID: "host-swarm-id",
				Name:    "host-swarm",
				Role:    "master",
			},
			CurrentGroupID: "group-1",
			Groups: []swarmruntime.GroupState{{
				Group: swarmruntime.Group{
					ID:          "group-1",
					Name:        "Primary Group",
					NetworkName: "group-net",
					HostSwarmID: "host-swarm-id",
				},
			}},
		},
	})

	fakeDeploy := &fakeReplicateDeployService{}
	server.SetDeployContainerService(fakeDeploy)

	return server, fakeDeploy, workspacePath
}

func postReplicateRequest(t *testing.T, server *Server, body map[string]any, swarmID ...string) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	path := "/v1/swarm/replicate"
	if len(swarmID) > 0 && strings.TrimSpace(swarmID[0]) != "" {
		path += "?swarm_id=" + strings.TrimSpace(swarmID[0])
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, withTestPrincipal(request))
	return recorder
}

func setReplicateFakeSwarmState(server *Server, state swarmruntime.LocalState) {
	if server != nil {
		server.SetSwarmService(fakeReplicateSwarmService{state: state, outgoingTokens: map[string]string{"managed-swarm-1": "host-to-managed-token"}, incomingTokens: map[string]string{"manager-swarm": "manager-token"}})
	}
}

func swarmStateWithManagedPeer(backendURL, token string) swarmruntime.LocalState {
	_ = token
	return swarmruntime.LocalState{
		Node: swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"},
		TrustedPeers: []swarmruntime.TrustedPeer{{
			SwarmID:       "managed-swarm-1",
			Name:          "managed-host",
			Role:          swarmruntime.RelationshipManaged,
			Relationship:  swarmruntime.RelationshipManaged,
			TransportMode: startupconfig.NetworkModeTailscale,
			RendezvousTransports: []swarmruntime.TransportSummary{{
				Kind:    startupconfig.NetworkModeTailscale,
				Primary: backendURL,
				All:     []string{backendURL},
			}},
		}},
		CurrentGroupID: "group-1",
		Groups: []swarmruntime.GroupState{{
			Group: swarmruntime.Group{ID: "group-1", Name: "Primary Group", NetworkName: "group-net", HostSwarmID: "host-swarm-id"},
			Members: []swarmruntime.GroupMember{
				{GroupID: "group-1", SwarmID: "host-swarm-id", Name: "host-swarm", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost},
				{GroupID: "group-1", SwarmID: "managed-swarm-1", Name: "managed-host", SwarmRole: swarmruntime.RelationshipManaged, MembershipRole: swarmruntime.GroupMembershipRoleMember},
			},
		}},
	}
}

type fakeReplicateDeployService struct {
	lastCreateInput               deployruntime.ContainerCreateInput
	lastAttachApproveInput        deployruntime.ContainerAttachApproveInput
	lastSyncCredentialBundleInput deployruntime.ContainerSyncCredentialRequestInput
	lastSyncAgentBundleInput      deployruntime.ContainerSyncCredentialRequestInput
	lastAppliedCredentialBundle   deployruntime.ContainerSyncCredentialBundle
	lastAppliedAgentBundle        deployruntime.ContainerSyncAgentBundle
	lastMirroredDeployment        deployruntime.ContainerDeployment
	lastManagedHostCanonicalIDs   []string
	lastActionInput               deployruntime.ContainerActionInput
	lastSettingsInput             deployruntime.ContainerSettingsUpdateInput
}

func (f *fakeReplicateDeployService) RuntimeStatus(context.Context) (deployruntime.ContainerRuntimeStatus, error) {
	return deployruntime.ContainerRuntimeStatus{Recommended: "podman", Available: []string{"podman", "docker"}}, nil
}

func (f *fakeReplicateDeployService) List(context.Context) ([]deployruntime.ContainerDeployment, error) {
	if strings.TrimSpace(f.lastMirroredDeployment.ID) != "" {
		return []deployruntime.ContainerDeployment{f.lastMirroredDeployment}, nil
	}
	return []deployruntime.ContainerDeployment{{
		ID:                "deployment-1",
		Name:              "replica",
		Status:            "running",
		AttachStatus:      "attached",
		BypassPermissions: f.lastCreateInput.BypassPermissions,
		ChildSwarmID:      "child-swarm-1",
		ChildDisplayName:  "replica",
	}}, nil
}

func (f *fakeReplicateDeployService) Create(_ context.Context, input deployruntime.ContainerCreateInput) (deployruntime.ContainerDeployment, error) {
	f.lastCreateInput = input
	return deployruntime.ContainerDeployment{
		ID:                "deployment-1",
		Name:              input.Name,
		Runtime:           input.Runtime,
		GroupID:           input.GroupID,
		GroupName:         input.GroupName,
		SyncEnabled:       input.SyncEnabled,
		BypassPermissions: input.BypassPermissions,
		Status:            "running",
		AttachStatus:      "starting",
	}, nil
}

func (f *fakeReplicateDeployService) Act(_ context.Context, input deployruntime.ContainerActionInput) (deployruntime.ContainerDeployment, error) {
	f.lastActionInput = input
	return deployruntime.ContainerDeployment{ID: input.ID, Name: input.ID, Status: input.Action, ContainerName: input.ID}, nil
}

func (f *fakeReplicateDeployService) Delete(context.Context, []string) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f *fakeReplicateDeployService) UpdateSettings(_ context.Context, input deployruntime.ContainerSettingsUpdateInput) (deployruntime.ContainerDeployment, error) {
	f.lastSettingsInput = input
	deployment := deployruntime.ContainerDeployment{ID: input.ID, Name: input.ID, Status: "running", ContainerName: input.ID}
	if input.BypassPermissions != nil {
		deployment.BypassPermissions = *input.BypassPermissions
	}
	if input.SyncEnabled != nil {
		deployment.SyncEnabled = *input.SyncEnabled
	}
	if input.SyncModulesSet {
		deployment.SyncModules = append([]string(nil), input.SyncModules...)
	}
	return deployment, nil
}

func (f *fakeReplicateDeployService) MirrorDeployment(_ context.Context, deployment deployruntime.ContainerDeployment) (deployruntime.ContainerDeployment, error) {
	f.lastMirroredDeployment = deployment
	return deployment, nil
}

func TestSwarmManagedHostContainerDeleteResolvesCanonicalHostContainerIDs(t *testing.T) {
	server, fakeDeploy, _ := newReplicateTestHandler(t)
	managedHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.URL.Path != "/v1/swarm/containers/local/delete" {
			t.Fatalf("managed host path = %q", r.URL.Path)
		}
		var payload struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode delete payload: %v", err)
		}
		fakeDeploy.lastManagedHostCanonicalIDs = append([]string(nil), payload.IDs...)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": localcontainers.DeleteResult{Deleted: payload.IDs, Count: len(payload.IDs)}})
	}))
	t.Cleanup(managedHost.Close)
	setReplicateFakeSwarmState(server, swarmStateWithManagedPeer(managedHost.URL, "host-to-managed-token"))
	topologyBackingStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "managed-host-delete-topology.pebble"))
	if err != nil {
		t.Fatalf("open topology store: %v", err)
	}
	defer func() { _ = topologyBackingStore.Close() }()
	topologyStore := pebblestore.NewTopologyStore(topologyBackingStore)
	topologySvc := topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil, nil, nil)
	server.SetTopologyService(topologySvc)
	if err := topologySvc.UpsertHostContainer(pebblestore.TopologyHostContainerRecord{
		HostContainerID:     "managed-swarm-1:ctr-123",
		HostSwarmID:         "managed-swarm-1",
		RuntimeContainerRef: "ctr-123",
		Name:                "managed-child",
		ContainerName:       "managed-child",
		ContainerID:         "ctr-123",
		ObservedSources:     []string{pebblestore.TopologyHostContainerSourceDeployContainer},
	}); err != nil {
		t.Fatalf("upsert topology host container: %v", err)
	}
	if err := topologySvc.UpsertAttachment(pebblestore.TopologyAttachmentRecord{
		AttachmentID:    "managed-swarm-1:ctr-123=>child-swarm-1",
		HostContainerID: "managed-swarm-1:ctr-123",
		RuntimeSwarmID:  "child-swarm-1",
		DeploymentID:    "deployment-1",
	}); err != nil {
		t.Fatalf("upsert topology attachment: %v", err)
	}
	fakeDeploy.lastMirroredDeployment = deployruntime.ContainerDeployment{
		ID:              "deployment-1",
		HostSwarmID:     "managed-swarm-1",
		HostContainerID: "managed-swarm-1:ctr-123",
		ContainerID:     "ctr-123",
		ContainerName:   "managed-child",
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/managed-host/container/delete", strings.NewReader(`{"managed_swarm_id":"managed-swarm-1","ids":["deployment-1"]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, withTestPrincipal(req))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if len(fakeDeploy.lastManagedHostCanonicalIDs) != 1 || fakeDeploy.lastManagedHostCanonicalIDs[0] != "managed-child" {
		t.Fatalf("canonical delete ids = %#v", fakeDeploy.lastManagedHostCanonicalIDs)
	}
}

func TestDeleteTargetHostDeploymentUsesManagedDeployDeletePath(t *testing.T) {
	server, fakeDeploy, _ := newReplicateTestHandler(t)
	managedHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.URL.Path != "/v1/deploy/container/delete" {
			t.Fatalf("managed host path = %q", r.URL.Path)
		}
		var payload struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode delete payload: %v", err)
		}
		fakeDeploy.lastManagedHostCanonicalIDs = append([]string(nil), payload.IDs...)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	t.Cleanup(managedHost.Close)
	setReplicateFakeSwarmState(server, swarmStateWithManagedPeer(managedHost.URL, "host-to-managed-token"))

	err := server.deleteTargetHostDeployment(context.Background(), deployruntime.ContainerDeployment{
		ID:              "launch-gate-cp4-verify-20260516013708",
		HostSwarmID:     "managed-swarm-1",
		HostBackendURL:  managedHost.URL,
		HostContainerID: "managed-swarm-1:0c2f-runtime-id",
		ContainerID:     "0c2f-runtime-id",
		ContainerName:   "launch-gate-cp4-verify-20260516013708",
	})
	if err != nil {
		t.Fatalf("deleteTargetHostDeployment() error = %v", err)
	}
	if len(fakeDeploy.lastManagedHostCanonicalIDs) != 1 || fakeDeploy.lastManagedHostCanonicalIDs[0] != "launch-gate-cp4-verify-20260516013708" {
		t.Fatalf("managed host delete ids = %#v", fakeDeploy.lastManagedHostCanonicalIDs)
	}
}

func TestSwarmManagedHostContainerDeleteResolvesDeploymentIDToManagedLocalIdentity(t *testing.T) {
	server, fakeDeploy, _ := newReplicateTestHandler(t)
	managedHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.URL.Path != "/v1/swarm/containers/local/delete" {
			t.Fatalf("managed host path = %q", r.URL.Path)
		}
		var payload struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode delete payload: %v", err)
		}
		fakeDeploy.lastManagedHostCanonicalIDs = append([]string(nil), payload.IDs...)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": localcontainers.DeleteResult{Deleted: payload.IDs, Count: len(payload.IDs)}})
	}))
	t.Cleanup(managedHost.Close)
	setReplicateFakeSwarmState(server, swarmStateWithManagedPeer(managedHost.URL, "host-to-managed-token"))
	topologyBackingStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "managed-host-delete-topology.pebble"))
	if err != nil {
		t.Fatalf("open topology store: %v", err)
	}
	defer func() { _ = topologyBackingStore.Close() }()
	topologyStore := pebblestore.NewTopologyStore(topologyBackingStore)
	topologySvc := topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil, nil, nil)
	server.SetTopologyService(topologySvc)
	if err := topologySvc.UpsertHostContainer(pebblestore.TopologyHostContainerRecord{
		HostContainerID:     "managed-swarm-1:ctr-123",
		HostSwarmID:         "managed-swarm-1",
		RuntimeContainerRef: "ctr-123",
		Name:                "managed-child",
		ContainerName:       "managed-child",
		ContainerID:         "ctr-123",
		ObservedSources:     []string{pebblestore.TopologyHostContainerSourceDeployContainer},
	}); err != nil {
		t.Fatalf("upsert topology host container: %v", err)
	}
	if err := topologySvc.UpsertAttachment(pebblestore.TopologyAttachmentRecord{
		AttachmentID:    "managed-swarm-1:ctr-123=>child-swarm-1",
		HostContainerID: "managed-swarm-1:ctr-123",
		RuntimeSwarmID:  "child-swarm-1",
		DeploymentID:    "deployment-1",
	}); err != nil {
		t.Fatalf("upsert topology attachment: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/managed-host/container/delete", strings.NewReader(`{"managed_swarm_id":"managed-swarm-1","ids":["deployment-1"]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, withTestPrincipal(req))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if len(fakeDeploy.lastManagedHostCanonicalIDs) != 1 || fakeDeploy.lastManagedHostCanonicalIDs[0] != "managed-child" {
		t.Fatalf("managed host delete ids = %#v", fakeDeploy.lastManagedHostCanonicalIDs)
	}
}

func (f *fakeReplicateDeployService) ChildAttachState(context.Context, deployruntime.ContainerAttachStatusInput) (swarmruntime.LocalState, error) {
	return swarmruntime.LocalState{}, nil
}

func (f *fakeReplicateDeployService) AttachRequest(context.Context, deployruntime.ContainerAttachRequestInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f *fakeReplicateDeployService) AttachStatus(context.Context, deployruntime.ContainerAttachStatusInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f *fakeReplicateDeployService) AttachApprove(_ context.Context, input deployruntime.ContainerAttachApproveInput) (deployruntime.ContainerAttachState, error) {
	f.lastAttachApproveInput = input
	return deployruntime.ContainerAttachState{}, nil
}

func (f *fakeReplicateDeployService) FinalizeAttachFromHost(context.Context, deployruntime.ContainerAttachFinalizeInput) error {
	return nil
}

func (f *fakeReplicateDeployService) SyncCredentialBundle(_ context.Context, input deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error) {
	f.lastSyncCredentialBundleInput = input
	return deployruntime.ContainerSyncCredentialBundle{}, nil
}

func (f *fakeReplicateDeployService) SyncAgentBundle(_ context.Context, input deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncAgentBundle, error) {
	f.lastSyncAgentBundleInput = input
	return deployruntime.ContainerSyncAgentBundle{}, nil
}

func (f *fakeReplicateDeployService) ApplyManagedCredentialBundle(_ context.Context, bundle deployruntime.ContainerSyncCredentialBundle) error {
	f.lastAppliedCredentialBundle = bundle
	return nil
}

func (f *fakeReplicateDeployService) ApplyManagedAgentBundle(_ context.Context, bundle deployruntime.ContainerSyncAgentBundle) error {
	f.lastAppliedAgentBundle = bundle
	return nil
}

func (f *fakeReplicateDeployService) WorkspaceBootstrap(context.Context, deployruntime.ContainerWorkspaceBootstrapRequestInput) ([]deployruntime.ContainerWorkspaceBootstrap, error) {
	return nil, nil
}

func (f *fakeReplicateDeployService) AutoAttachChild(context.Context) error {
	return nil
}

func (f *fakeReplicateDeployService) UnlockManagedLocalChildVaults(context.Context) error {
	return nil
}

type fakeReplicateSwarmService struct {
	state          swarmruntime.LocalState
	outgoingTokens map[string]string
	incomingTokens map[string]string
}

func (f fakeReplicateSwarmService) EnsureLocalState(swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	return f.state, nil
}

func (f fakeReplicateSwarmService) RenameLocalSwarm(input swarmruntime.RenameLocalSwarmInput) (swarmruntime.LocalState, error) {
	state := f.state
	state.Node.Name = strings.TrimSpace(input.Name)
	return state, nil
}

func (f fakeReplicateSwarmService) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) {
	return nil, "", nil
}

func (f fakeReplicateSwarmService) UpsertGroup(swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	return swarmruntime.Group{}, nil
}

func (f fakeReplicateSwarmService) DeleteGroup(string) error {
	return nil
}

func (f fakeReplicateSwarmService) SetCurrentGroup(string, string) (swarmruntime.GroupState, error) {
	return swarmruntime.GroupState{}, nil
}

func (f fakeReplicateSwarmService) OutgoingPeerAuthToken(swarmID string) (string, bool, error) {
	token := strings.TrimSpace(f.outgoingTokens[strings.TrimSpace(swarmID)])
	return token, token != "", nil
}

func (f fakeReplicateSwarmService) ValidateIncomingPeerAuth(swarmID, token string) (bool, error) {
	want := strings.TrimSpace(f.incomingTokens[strings.TrimSpace(swarmID)])
	return want != "" && want == strings.TrimSpace(token), nil
}

func (f fakeReplicateSwarmService) UpsertGroupMember(swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{}, nil
}

func (f fakeReplicateSwarmService) RemoveGroupMember(swarmruntime.RemoveGroupMemberInput) error {
	return nil
}

func (f fakeReplicateSwarmService) CreateInvite(swarmruntime.CreateInviteInput) (swarmruntime.Invite, error) {
	return swarmruntime.Invite{}, nil
}

func (f fakeReplicateSwarmService) SubmitEnrollment(swarmruntime.SubmitEnrollmentInput) (swarmruntime.Enrollment, error) {
	return swarmruntime.Enrollment{}, nil
}

func (f fakeReplicateSwarmService) ListPendingEnrollments(int) ([]swarmruntime.Enrollment, error) {
	return nil, nil
}

func (f fakeReplicateSwarmService) DecideEnrollment(swarmruntime.DecideEnrollmentInput) (swarmruntime.Enrollment, []swarmruntime.TrustedPeer, error) {
	return swarmruntime.Enrollment{}, nil, nil
}

func (f fakeReplicateSwarmService) PrepareRemoteBootstrapParentPeer(swarmruntime.PrepareRemoteBootstrapParentPeerInput) error {
	return nil
}

func (f fakeReplicateSwarmService) ApproveManagedPairing(swarmruntime.ApproveManagedPairingInput) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeReplicateSwarmService) TrustManagedPeer(swarmruntime.TrustManagedPeerInput) (swarmruntime.TrustedPeer, error) {
	return swarmruntime.TrustedPeer{}, nil
}

func (f fakeReplicateSwarmService) RemoveManagedPeer(swarmruntime.RemoveManagedPeerInput) (swarmruntime.RemoveManagedPeerResult, error) {
	return swarmruntime.RemoveManagedPeerResult{}, nil
}

func (f fakeReplicateSwarmService) UpdateLocalPairingFromConfig(startupconfig.FileConfig, []swarmruntime.TransportSummary) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeReplicateSwarmService) DetachToStandalone(string) error {
	return nil
}

var (
	_ deployContainerService = (*fakeReplicateDeployService)(nil)
	_ swarmService           = fakeReplicateSwarmService{}
)

func TestDeployContainerSyncCredentialsPreservesPeerAuthBeforeTrustedNetworkExemption(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "auth.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	server := NewServer("test", nil, nil, nil, nil, nil, nil, nil, security.NewService(pebblestore.NewClientAuthStore(store), events), nil, nil, nil, events, stream.NewHub(events))
	fakeDeploy := &fakeReplicateDeployService{}
	server.SetDeployContainerService(fakeDeploy)
	server.SetSwarmService(fakeReplicateSwarmService{incomingTokens: map[string]string{"manager-swarm": "manager-token"}})

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/sync/credentials", bytes.NewReader([]byte(`{}`)))
	req.RemoteAddr = "100.64.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, "manager-swarm")
	req.Header.Set(peerAuthTokenHeader, "manager-token")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !fakeDeploy.lastSyncCredentialBundleInput.PeerAuthorized {
		t.Fatalf("peer auth was not propagated to sync credentials handler")
	}
	if got := fakeDeploy.lastSyncCredentialBundleInput.PeerSwarmID; got != "manager-swarm" {
		t.Fatalf("peer swarm id = %q, want manager-swarm", got)
	}
}

func TestDeployContainerActionForMirroredManagedDeploymentForwardsToManagedHost(t *testing.T) {
	server, fakeDeploy, _ := newReplicateTestHandler(t)
	managedHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.URL.Path != "/v1/deploy/container/action" {
			t.Fatalf("managed host path = %q", r.URL.Path)
		}
		var payload struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode action payload: %v", err)
		}
		if payload.ID != "deployment-1" || payload.Action != "stop" {
			t.Fatalf("payload = %#v", payload)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment": deployruntime.ContainerDeployment{ID: payload.ID, Name: "replica", Status: "stopped", ContainerName: "replica", HostSwarmID: "managed-swarm-1"}})
	}))
	t.Cleanup(managedHost.Close)
	setReplicateFakeSwarmState(server, swarmStateWithManagedPeer(managedHost.URL, "host-to-managed-token"))
	fakeDeploy.lastMirroredDeployment = deployruntime.ContainerDeployment{ID: "deployment-1", Name: "replica", Status: "running", HostSwarmID: "managed-swarm-1", HostBackendURL: managedHost.URL, ContainerName: "replica", SyncOwnerSwarmID: "managed-swarm-1"}

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/action", strings.NewReader(`{"id":"deployment-1","action":"stop"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, withTestPrincipal(req))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if fakeDeploy.lastActionInput.ID != "" {
		t.Fatalf("primary local deploy Act was called: %#v", fakeDeploy.lastActionInput)
	}
	if fakeDeploy.lastMirroredDeployment.Status != "stopped" {
		t.Fatalf("mirrored status = %q, want stopped", fakeDeploy.lastMirroredDeployment.Status)
	}
}

func TestDeployContainerSettingsForMirroredManagedDeploymentForwardsToManagedHost(t *testing.T) {
	server, fakeDeploy, _ := newReplicateTestHandler(t)
	managedHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.URL.Path != "/v1/deploy/container/settings" {
			t.Fatalf("managed host path = %q", r.URL.Path)
		}
		var payload struct {
			ID                string   `json:"id"`
			BypassPermissions *bool    `json:"bypass_permissions"`
			SyncModules       []string `json:"sync_modules"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode settings payload: %v", err)
		}
		if payload.ID != "deployment-1" || payload.BypassPermissions == nil || !*payload.BypassPermissions {
			t.Fatalf("payload = %#v", payload)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment": deployruntime.ContainerDeployment{ID: payload.ID, Name: "replica", Status: "running", ContainerName: "replica", HostSwarmID: "managed-swarm-1", BypassPermissions: true, SyncModules: payload.SyncModules}})
	}))
	t.Cleanup(managedHost.Close)
	setReplicateFakeSwarmState(server, swarmStateWithManagedPeer(managedHost.URL, "host-to-managed-token"))
	fakeDeploy.lastMirroredDeployment = deployruntime.ContainerDeployment{ID: "deployment-1", Name: "replica", Status: "running", HostSwarmID: "managed-swarm-1", HostBackendURL: managedHost.URL, ContainerName: "replica", SyncOwnerSwarmID: "managed-swarm-1"}

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/settings", strings.NewReader(`{"id":"deployment-1","bypass_permissions":true,"sync_modules":["permissions"]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, withTestPrincipal(req))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if fakeDeploy.lastSettingsInput.ID != "" {
		t.Fatalf("primary local deploy UpdateSettings was called: %#v", fakeDeploy.lastSettingsInput)
	}
	if !fakeDeploy.lastMirroredDeployment.BypassPermissions {
		t.Fatalf("mirrored bypass_permissions = false, want true")
	}
}
