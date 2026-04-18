package api

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestListSessionsScopesToCWD(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions-scope.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer("test", nil, nil, nil, nil, sessionSvc, workspaceSvc, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	handler := server.Handler()

	root := t.TempDir()
	child := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child workspace: %v", err)
	}
	outside := t.TempDir()

	rootSession := createSessionViaAPI(t, handler, root)
	_ = createSessionViaAPI(t, handler, child)
	_ = createSessionViaAPI(t, handler, outside)

	resp := listSessionsScopedViaAPI(t, handler, root, false)
	if len(resp.Sessions) != 1 {
		t.Fatalf("expected 1 exact-scoped session, got %d", len(resp.Sessions))
	}
	if resp.Sessions[0].ID != rootSession.ID {
		t.Fatalf("expected exact-scoped session %q, got %q", rootSession.ID, resp.Sessions[0].ID)
	}
	if normalizePath(resp.Sessions[0].WorkspacePath) != normalizePath(root) {
		t.Fatalf("workspace_path = %q, want %q", resp.Sessions[0].WorkspacePath, root)
	}
}

func TestListSessionsScopesToWorkspaceWhenCWDMatchesSavedWorkspace(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions-workspace-scope.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	root := t.TempDir()
	child := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child workspace: %v", err)
	}
	if _, err := workspaceSvc.Add(root, "root", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if _, err := workspaceSvc.AddDirectory(root, child); err != nil {
		t.Fatalf("add directory: %v", err)
	}

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer("test", nil, nil, nil, nil, sessionSvc, workspaceSvc, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	handler := server.Handler()

	rootSession := createSessionViaAPI(t, handler, root)
	childSession := createSessionViaAPI(t, handler, child)

	resp := listSessionsScopedViaAPI(t, handler, root, false)
	if len(resp.Sessions) != 2 {
		t.Fatalf("expected 2 workspace-scoped sessions, got %d", len(resp.Sessions))
	}

	seen := map[string]bool{}
	for _, session := range resp.Sessions {
		seen[session.ID] = true
		if !pathWithinScope(session.WorkspacePath, root) {
			t.Fatalf("session %q escaped workspace root=%q path=%q", session.ID, root, session.WorkspacePath)
		}
	}
	if !seen[rootSession.ID] {
		t.Fatalf("expected root session %q in workspace-scoped list", rootSession.ID)
	}
	if !seen[childSession.ID] {
		t.Fatalf("expected child session %q in workspace-scoped list", childSession.ID)
	}
}

func TestListSessionsScopesToExactCWDWhenNoWorkspaceMatches(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions-exact-scope.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer("test", nil, nil, nil, nil, sessionSvc, workspaceSvc, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	handler := server.Handler()

	root := t.TempDir()
	child := filepath.Join(root, "swarm-go")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child repo: %v", err)
	}

	rootSession := createSessionViaAPI(t, handler, root)
	_ = createSessionViaAPI(t, handler, child)

	resp := listSessionsScopedViaAPI(t, handler, root, false)
	if len(resp.Sessions) != 1 {
		t.Fatalf("expected 1 exact-scoped session, got %d", len(resp.Sessions))
	}
	if resp.Sessions[0].ID != rootSession.ID {
		t.Fatalf("expected exact-scoped session %q, got %q", rootSession.ID, resp.Sessions[0].ID)
	}
	if normalizePath(resp.Sessions[0].WorkspacePath) != normalizePath(root) {
		t.Fatalf("workspace_path = %q, want %q", resp.Sessions[0].WorkspacePath, root)
	}
}

func TestListSessionsExactPathBypassesWorkspaceScope(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions-exact-path.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	root := t.TempDir()
	child := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child workspace: %v", err)
	}
	if _, err := workspaceSvc.Add(root, "root", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if _, err := workspaceSvc.AddDirectory(root, child); err != nil {
		t.Fatalf("add directory: %v", err)
	}

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer("test", nil, nil, nil, nil, sessionSvc, workspaceSvc, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	handler := server.Handler()

	_ = createSessionViaAPI(t, handler, root)
	childSession := createSessionViaAPI(t, handler, child)

	resp := listSessionsScopedViaAPI(t, handler, child, true)
	if len(resp.Sessions) != 1 {
		t.Fatalf("expected 1 exact-path session, got %d", len(resp.Sessions))
	}
	if resp.Sessions[0].ID != childSession.ID {
		t.Fatalf("expected child session %q, got %q", childSession.ID, resp.Sessions[0].ID)
	}
	if normalizePath(resp.Sessions[0].WorkspacePath) != normalizePath(child) {
		t.Fatalf("workspace_path = %q, want %q", resp.Sessions[0].WorkspacePath, child)
	}
}

func TestResolveWorkspaceReturnsCanonicalWorkspacePath(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-resolve.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	root := t.TempDir()
	child := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child workspace: %v", err)
	}
	if _, err := workspaceSvc.Add(root, "root", "", true); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if _, err := workspaceSvc.AddDirectory(root, child); err != nil {
		t.Fatalf("add directory: %v", err)
	}

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	server := NewServer("test", nil, nil, nil, nil, nil, workspaceSvc, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	handler := server.Handler()

	var resp struct {
		RequestedPath string `json:"requested_path"`
		ResolvedPath  string `json:"resolved_path"`
		WorkspacePath string `json:"workspace_path"`
		WorkspaceName string `json:"workspace_name"`
	}
	status := doJSONRequest(t, handler, http.MethodGet, "/v1/workspace/resolve?cwd="+url.QueryEscape(child), nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("resolve workspace status=%d", status)
	}
	if resp.ResolvedPath != child {
		t.Fatalf("resolved_path = %q, want %q", resp.ResolvedPath, child)
	}
	if resp.WorkspacePath != root {
		t.Fatalf("workspace_path = %q, want %q", resp.WorkspacePath, root)
	}
	if resp.WorkspaceName != "root" {
		t.Fatalf("workspace_name = %q, want root", resp.WorkspaceName)
	}
}

type apiScopedSession struct {
	ID            string `json:"id"`
	WorkspacePath string `json:"workspace_path"`
}

type apiScopedListSessionsResponse struct {
	OK       bool               `json:"ok"`
	Sessions []apiScopedSession `json:"sessions"`
}

func listSessionsScopedViaAPI(t *testing.T, handler http.Handler, cwd string, exact bool) apiScopedListSessionsResponse {
	t.Helper()
	resp := apiScopedListSessionsResponse{}
	path := "/v1/sessions?limit=100&cwd=" + url.QueryEscape(cwd)
	if exact {
		path += "&exact_path=true"
	}
	status := doJSONRequest(t, handler, http.MethodGet, path, nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("list scoped sessions status=%d", status)
	}
	return resp
}

func pathWithinScope(path, scope string) bool {
	path = normalizePath(path)
	scope = normalizePath(scope)
	if path == "" || scope == "" {
		return false
	}
	if path == scope {
		return true
	}
	rel, err := filepath.Rel(scope, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}
