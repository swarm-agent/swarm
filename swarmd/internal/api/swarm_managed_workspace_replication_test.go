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
)

func TestPeerManagedWorkspacePreflightPlansImportLinkAndConflict(t *testing.T) {
	handler, _, _ := newReplicateTestHandler(t)
	root := t.TempDir()
	registered := filepath.Join(root, "registered")
	if err := os.MkdirAll(registered, 0o755); err != nil {
		t.Fatalf("mkdir registered: %v", err)
	}
	if _, err := handler.workspace.Add(registered, "registered", "", false); err != nil {
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
	server.Handler().ServeHTTP(recorder, request)
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
	server.Handler().ServeHTTP(recorder, request)
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
