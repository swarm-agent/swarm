package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

func TestPeerManagedWorkspaceInventoryReturnsSavedDiscoveredAndCWDs(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	savedPath := filepath.Join(home, "saved-workspace")
	if err := os.MkdirAll(savedPath, 0o755); err != nil {
		t.Fatalf("mkdir saved: %v", err)
	}
	if _, err := handler.workspace.AddForPrincipal(testPrincipal(), savedPath, "saved-workspace", "", false); err != nil {
		t.Fatalf("add saved: %v", err)
	}
	discoveredPath := filepath.Join(home, "discovered-workspace")
	if err := os.MkdirAll(filepath.Join(discoveredPath, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir discovered git: %v", err)
	}
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open sessions store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), nil)
	handler.sessions = sessionSvc
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-1", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: discoveredPath, WorkspaceName: "discovered-workspace", Title: "Active work", UpdatedAt: 42}); err != nil {
		t.Fatalf("store session: %v", err)
	}
	if _, err := handler.swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{ParentSwarmID: "manager-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, PairingState: "paired"}); err != nil {
		t.Fatalf("put local pairing: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, peerManagedWorkspaceInventoryPath, nil)
	req.Header.Set(peerAuthSwarmIDHeader, "manager-swarm")
	req.Header.Set(peerAuthTokenHeader, "manager-token")
	response, status, err := handler.peerManagedWorkspaceInventory(req)
	if err != nil || status != http.StatusOK {
		t.Fatalf("inventory status=%d err=%v", status, err)
	}
	if filepath.Clean(response.ManagedHome) != filepath.Clean(home) {
		t.Fatalf("managed home=%q want %q", response.ManagedHome, home)
	}
	if !managedInventoryHasSavedWorkspace(response.SavedWorkspaces, savedPath) {
		t.Fatalf("saved workspace %q missing from %#v", savedPath, response.SavedWorkspaces)
	}
	if !managedInventoryHasDiscoveredDirectory(response.DiscoveredDirectories, discoveredPath) {
		t.Fatalf("discovered workspace %q missing from %#v", discoveredPath, response.DiscoveredDirectories)
	}
	if !managedInventoryHasActiveCWD(response.ActiveCWDs, discoveredPath, "session-1") {
		t.Fatalf("active cwd %q missing from %#v", discoveredPath, response.ActiveCWDs)
	}
}

func TestManagedWorkspaceInventoryUsesSelectedManagedHostWithoutTargetQuery(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	var sawInventory bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.URL.Path != peerManagedWorkspaceInventoryPath {
			t.Fatalf("unexpected peer path %q", r.URL.Path)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "host-to-managed-token" {
			t.Fatalf("peer auth headers id=%q token=%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		sawInventory = true
		managedHome := filepath.Join(string(os.PathSeparator), "srv", "managed")
		writeJSON(w, http.StatusOK, peerManagedWorkspaceInventoryResponse{OK: true, ManagedHome: managedHome, SavedWorkspaces: []workspace.Entry{{Path: filepath.Join(managedHome, "swarm-go"), WorkspaceName: "swarm-go"}}})
	}))
	t.Cleanup(remote.Close)
	state := swarmStateWithManagedPeer(remote.URL, "host-to-managed-token")
	handler.SetSwarmService(fakeReplicateSwarmService{state: state, outgoingTokens: map[string]string{"managed-swarm-1": "host-to-managed-token"}, incomingTokens: map[string]string{"manager-swarm": "manager-token"}})
	req := httptest.NewRequest(http.MethodGet, managedWorkspaceInventoryPath, nil)
	recorder := httptest.NewRecorder()
	handler.Handler().ServeHTTP(recorder, withTestPrincipal(req))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !sawInventory {
		t.Fatal("peer inventory was not called")
	}
	var response managedWorkspaceInventoryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.ManagedHome != filepath.Join(string(os.PathSeparator), "srv", "managed") || len(response.SavedWorkspaces) != 1 {
		t.Fatalf("response=%+v", response)
	}
}

func TestManagedWorkspaceInventoryCallsPeerWithAuth(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	var sawInventory bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.URL.Path != peerManagedWorkspaceInventoryPath {
			t.Fatalf("unexpected peer path %q", r.URL.Path)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "host-to-managed-token" {
			t.Fatalf("peer auth headers id=%q token=%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		sawInventory = true
		managedHome := filepath.Join(string(os.PathSeparator), "srv", "managed")
		writeJSON(w, http.StatusOK, peerManagedWorkspaceInventoryResponse{OK: true, ManagedHome: managedHome, SavedWorkspaces: []workspace.Entry{{Path: filepath.Join(managedHome, "swarm-go"), WorkspaceName: "swarm-go"}}})
	}))
	t.Cleanup(remote.Close)
	setReplicateFakeSwarmState(handler, swarmStateWithManagedPeer(remote.URL, "host-to-managed-token"))
	req := httptest.NewRequest(http.MethodGet, managedWorkspaceInventoryPath+"?target_swarm_id=managed-swarm-1", nil)
	recorder := httptest.NewRecorder()
	handler.Handler().ServeHTTP(recorder, withTestPrincipal(req))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !sawInventory {
		t.Fatal("peer inventory was not called")
	}
	var response managedWorkspaceInventoryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.ManagedHome != filepath.Join(string(os.PathSeparator), "srv", "managed") || len(response.SavedWorkspaces) != 1 {
		t.Fatalf("response=%+v", response)
	}
}

func managedInventoryHasSavedWorkspace(items []workspace.Entry, path string) bool {
	for _, item := range items {
		if filepath.Clean(item.Path) == filepath.Clean(path) {
			return true
		}
	}
	return false
}

func managedInventoryHasDiscoveredDirectory(items []workspace.DiscoverEntry, path string) bool {
	for _, item := range items {
		if filepath.Clean(item.Path) == filepath.Clean(path) {
			return true
		}
	}
	return false
}

func managedInventoryHasActiveCWD(items []managedWorkspaceActiveCWDResponse, path, sessionID string) bool {
	for _, item := range items {
		if filepath.Clean(item.Path) == filepath.Clean(path) && item.SessionID == sessionID {
			return true
		}
	}
	return false
}

func TestPeerManagedWorkspacePreflightPlansImportLinkAndConflict(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	root := t.TempDir()
	registered := filepath.Join(root, "registered")
	if err := os.MkdirAll(registered, 0o755); err != nil {
		t.Fatalf("mkdir registered: %v", err)
	}
	if _, err := handler.workspace.AddForPrincipal(peerManagedWorkspacePrincipal(), registered, "registered", "", false); err != nil {
		t.Fatalf("add registered: %v", err)
	}
	unknown := filepath.Join(root, "unknown")
	if err := os.MkdirAll(unknown, 0o755); err != nil {
		t.Fatalf("mkdir unknown: %v", err)
	}

	response, status, err := handler.peerManagedWorkspacePreflight(peerManagedWorkspacePreflightRequest{
		DestinationRoot: root,
		Workspaces: []peerManagedWorkspacePlanItem{
			{SourceWorkspacePath: "/src/new", WorkspaceName: "new", GitWorkspace: true},
			{SourceWorkspacePath: "/src/registered", WorkspaceName: "registered", GitWorkspace: true},
			{SourceWorkspacePath: "/src/unknown", WorkspaceName: "unknown", GitWorkspace: true},
		},
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("preflight status=%d err=%v", status, err)
	}
	if response.Ready {
		t.Fatalf("preflight ready = true, want false due conflict")
	}
	if got := response.Workspaces[0].Action; got != managedWorkspaceActionImportBundle {
		t.Fatalf("new action=%q", got)
	}
	if got := response.Workspaces[1].Action; got != managedWorkspaceActionLinkExisting {
		t.Fatalf("registered action=%q", got)
	}
	if got := response.Workspaces[2].Action; got != managedWorkspaceActionConflict {
		t.Fatalf("unknown action=%q", got)
	}
}

func TestPeerManagedWorkspacePreflightRejectsOutOfContractRoot(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	root := t.TempDir()
	response, status, err := handler.peerManagedWorkspacePreflight(peerManagedWorkspacePreflightRequest{
		DestinationRoot: root,
		Workspaces:      []peerManagedWorkspacePlanItem{{SourceWorkspacePath: "/src/x", WorkspaceName: "x", DestinationPath: filepath.Join(filepath.Dir(root), "outside"), GitWorkspace: true}},
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("preflight status=%d err=%v", status, err)
	}
	if response.Ready || len(response.Workspaces) != 1 || response.Workspaces[0].Action != managedWorkspaceActionConflict {
		t.Fatalf("response=%+v", response)
	}
}

func TestPeerManagedWorkspacePreflightDefaultsToHomeAndPreservesHomeRelativeSource(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspacePath := filepath.Join(home, "workspaces", "demo")
	response, status, err := handler.peerManagedWorkspacePreflight(peerManagedWorkspacePreflightRequest{
		DestinationRoot: "",
		Workspaces: []peerManagedWorkspacePlanItem{{
			SourceWorkspacePath:    workspacePath,
			SourceHomeRelativePath: sourceHomeRelativePath(workspacePath),
			WorkspaceName:          "demo",
			GitWorkspace:           true,
		}},
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("preflight status=%d err=%v", status, err)
	}
	want := filepath.Join(home, "workspaces", "demo")
	if !response.Ready || len(response.Workspaces) != 1 {
		t.Fatalf("response=%+v", response)
	}
	if got := response.DestinationRoot; filepath.Clean(got) != filepath.Clean(home) {
		t.Fatalf("root=%q want %q", got, home)
	}
	if got := response.Workspaces[0].DestinationPath; filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("destination=%q want %q", got, want)
	}
}

func TestNormalizeManagedWorkspaceDestinationRootAcceptsHomeRelativeInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspaces := filepath.Join(home, "workspaces")
	if err := os.MkdirAll(workspaces, 0o755); err != nil {
		t.Fatalf("mkdir workspaces: %v", err)
	}
	root, err := normalizeManagedWorkspaceDestinationRoot("workspaces")
	if err != nil {
		t.Fatalf("normalize relative: %v", err)
	}
	if filepath.Clean(root) != filepath.Clean(workspaces) {
		t.Fatalf("root=%q want %q", root, workspaces)
	}
}

func TestPeerManagedWorkspaceImportBundleRequiresExactDestination(t *testing.T) {
	handler, _, workspacePath := newReplicateTestHandler(t)
	setReplicateFakeSwarmState(handler, swarmStateWithManagedPeer("", ""))
	initGitRepoForManagedWorkspaceTest(t, workspacePath)
	bundlePath := createManagedWorkspaceTestBundle(t, workspacePath)
	root := t.TempDir()

	recorder := postPeerManagedImportBundle(t, handler, root, "", "workspace-one", bundlePath)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing destination status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	outside := filepath.Join(filepath.Dir(root), "workspace-one")
	recorder = postPeerManagedImportBundle(t, handler, root, outside, "workspace-one", bundlePath)
	if recorder.Code != http.StatusConflict && recorder.Code != http.StatusBadRequest {
		t.Fatalf("outside status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPeerManagedWorkspaceImportBundleClonesExactDestination(t *testing.T) {
	handler, _, workspacePath := newReplicateTestHandler(t)
	setReplicateFakeSwarmState(handler, swarmStateWithManagedPeer("", ""))
	initGitRepoForManagedWorkspaceTest(t, workspacePath)
	bundlePath := createManagedWorkspaceTestBundle(t, workspacePath)
	root := t.TempDir()
	destination := filepath.Join(root, "workspace-one")

	recorder := postPeerManagedImportBundle(t, handler, root, destination, "workspace-one", bundlePath)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(destination, ".git")); err != nil {
		t.Fatalf("destination was not cloned exactly: %v", err)
	}
	var response peerManagedWorkspaceImportBundleResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if filepath.Clean(response.DestinationPath) != filepath.Clean(destination) {
		t.Fatalf("destination=%q want %q", response.DestinationPath, destination)
	}
}

func TestManagedWorkspacePreflightUsesDedicatedPeerAPIAndAuth(t *testing.T) {
	handler, fakeDeploy, workspacePath := newReplicateTestHandler(t)
	initGitRepoForManagedWorkspaceTest(t, workspacePath)
	root := t.TempDir()
	t.Setenv("HOME", filepath.Dir(workspacePath))
	var sawPeerPreflight bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		if r.URL.Path != peerManagedWorkspacePreflightPath {
			t.Fatalf("unexpected peer path %q", r.URL.Path)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "host-to-managed-token" {
			t.Fatalf("peer auth headers id=%q token=%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		sawPeerPreflight = true
		var req peerManagedWorkspacePreflightRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode peer preflight: %v", err)
		}
		if len(req.Workspaces) != 1 || req.Workspaces[0].SourceHomeRelativePath == "" {
			t.Fatalf("missing source home relative path in peer request: %+v", req.Workspaces)
		}
		writeJSON(w, http.StatusOK, peerManagedWorkspacePreflightResponse{OK: true, Ready: true, DestinationRoot: root, Workspaces: []managedWorkspacePlanResponse{{OK: true, SourceWorkspacePath: workspacePath, SourceWorkspaceName: "workspace-one", DestinationRoot: root, DestinationPath: filepath.Join(root, "workspace-one"), Action: managedWorkspaceActionImportBundle, GitWorkspace: true}}})
	}))
	t.Cleanup(remote.Close)
	setReplicateFakeSwarmState(handler, swarmStateWithManagedPeer(remote.URL, "host-to-managed-token"))

	recorder := postManagedWorkspacePreflightRequest(t, handler, map[string]any{
		"target_swarm_id":  "managed-swarm-1",
		"destination_root": "",
		"workspaces": []map[string]any{{
			"source_workspace_path": workspacePath,
		}},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !sawPeerPreflight {
		t.Fatal("peer preflight was not called")
	}
	if fakeDeploy.lastCreateInput.Name != "" {
		t.Fatalf("managed host API used deploy container service: %+v", fakeDeploy.lastCreateInput)
	}
}

func postManagedWorkspacePreflightRequest(t *testing.T, server *Server, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, managedWorkspacePreflightPath, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, withTestPrincipal(request))
	return recorder
}

func postPeerManagedImportBundle(t *testing.T, server *Server, root, destination, name, bundlePath string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("source_workspace_path", "/src/"+name)
	_ = writer.WriteField("workspace_name", name)
	_ = writer.WriteField("destination_root", root)
	_ = writer.WriteField("destination_path", destination)
	part, err := writer.CreateFormFile("bundle", filepath.Base(bundlePath))
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	file, err := os.Open(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		file.Close()
		t.Fatalf("copy bundle: %v", err)
	}
	file.Close()
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, peerManagedWorkspaceImportBundlePath, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set(peerAuthSwarmIDHeader, "manager-swarm")
	request.Header.Set(peerAuthTokenHeader, "manager-token")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, withTestPrincipal(request))
	return recorder
}

func initGitRepoForManagedWorkspaceTest(t *testing.T, path string) {
	t.Helper()
	runGitForManagedWorkspaceTest(t, path, "init")
	runGitForManagedWorkspaceTest(t, path, "config", "user.email", "test@example.invalid")
	runGitForManagedWorkspaceTest(t, path, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("managed workspace test\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGitForManagedWorkspaceTest(t, path, "add", "README.md")
	runGitForManagedWorkspaceTest(t, path, "commit", "-m", "initial")
}

func createManagedWorkspaceTestBundle(t *testing.T, path string) string {
	t.Helper()
	bundlePath := filepath.Join(t.TempDir(), "workspace.bundle")
	runGitForManagedWorkspaceTest(t, path, "bundle", "create", bundlePath, "--all")
	return bundlePath
}

func runGitForManagedWorkspaceTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
}

func TestPeerManagedWorkspaceEnsureLinkPersistsPeerLocalBinding(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	setReplicateFakeSwarmState(handler, swarmStateWithManagedPeer("", ""))
	root := t.TempDir()
	destination := filepath.Join(root, "swarm-go")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	if _, err := handler.swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{ParentSwarmID: "manager-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, PairingState: "paired"}); err != nil {
		t.Fatalf("put local pairing: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, peerManagedWorkspaceEnsureLinkPath, nil)
	req.Header.Set(peerAuthSwarmIDHeader, "manager-swarm")
	req.Header.Set(peerAuthTokenHeader, "manager-token")
	response, status, err := handler.peerManagedWorkspaceEnsureLink(req, peerManagedWorkspaceEnsureLinkRequest{
		DestinationRoot:     root,
		DestinationPath:     destination,
		WorkspaceName:       "swarm-go",
		SourceWorkspacePath: "/srv/primary/swarm-go",
		Provision:           false,
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("ensure link status=%d err=%v", status, err)
	}
	if !response.Registered || filepath.Clean(response.DestinationPath) != filepath.Clean(destination) {
		t.Fatalf("response=%+v", response)
	}
	bindings, err := handler.topology.ListWorkspaceBindingsBySourcePathForAccount(testPrincipal().AccountScopeID, "/srv/primary/swarm-go", 10)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings=%+v", bindings)
	}
	binding := bindings[0]
	if binding.DestinationRuntimeSwarmID != "host-swarm-id" || binding.DestinationHostSwarmID != "host-swarm-id" {
		t.Fatalf("binding swarm ids runtime=%q host=%q", binding.DestinationRuntimeSwarmID, binding.DestinationHostSwarmID)
	}
	if filepath.Clean(binding.DestinationWorkspacePath) != filepath.Clean(destination) {
		t.Fatalf("binding destination=%q want %q", binding.DestinationWorkspacePath, destination)
	}
}

func TestPeerManagedWorkspacePreflightDetectsExistingGitDirectory(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	root := t.TempDir()
	destination := filepath.Join(root, "swarm-go")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	initGitRepoForManagedWorkspaceTest(t, destination)

	response, status, err := handler.peerManagedWorkspacePreflight(peerManagedWorkspacePreflightRequest{
		DestinationRoot: root,
		Workspaces: []peerManagedWorkspacePlanItem{{
			SourceWorkspacePath:    filepath.Join(string(os.PathSeparator), "srv", "primary", "swarm-go"),
			SourceHomeRelativePath: "swarm-go",
			WorkspaceName:          "swarm-go",
			GitWorkspace:           true,
		}},
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("preflight status=%d err=%v", status, err)
	}
	if !response.Ready || len(response.Workspaces) != 1 {
		t.Fatalf("response=%+v", response)
	}
	plan := response.Workspaces[0]
	if plan.Action != managedWorkspaceActionLinkExisting || !plan.OK {
		t.Fatalf("action=%q ok=%v err=%q", plan.Action, plan.OK, plan.Error)
	}
	if filepath.Clean(plan.DestinationPath) != filepath.Clean(destination) {
		t.Fatalf("destination=%q want %q", plan.DestinationPath, destination)
	}
}

func TestPeerManagedWorkspacePreflightUsesPeerHomeForPrimaryHomeRelativeSource(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	primaryHome := filepath.Join(t.TempDir(), "primary-home")
	peerHome := filepath.Join(t.TempDir(), "peer-home")
	if err := os.MkdirAll(primaryHome, 0o755); err != nil {
		t.Fatalf("mkdir primary home: %v", err)
	}
	if err := os.MkdirAll(peerHome, 0o755); err != nil {
		t.Fatalf("mkdir peer home: %v", err)
	}
	primaryWorkspace := filepath.Join(primaryHome, "swarm-go")
	t.Setenv("HOME", primaryHome)
	relative := sourceHomeRelativePath(primaryWorkspace)
	if relative != "swarm-go" {
		t.Fatalf("relative=%q", relative)
	}
	t.Setenv("HOME", peerHome)

	response, status, err := handler.peerManagedWorkspacePreflight(peerManagedWorkspacePreflightRequest{
		DestinationRoot: "~",
		Workspaces: []peerManagedWorkspacePlanItem{{
			SourceWorkspacePath:    primaryWorkspace,
			SourceHomeRelativePath: relative,
			WorkspaceName:          "swarm-go",
			GitWorkspace:           true,
		}},
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("preflight status=%d err=%v", status, err)
	}
	want := filepath.Join(peerHome, "swarm-go")
	if got := response.Workspaces[0].DestinationPath; filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("destination=%q want %q", got, want)
	}
	if strings.Contains(response.Workspaces[0].DestinationPath, primaryHome) {
		t.Fatalf("destination leaked primary home: %q", response.Workspaces[0].DestinationPath)
	}
}
