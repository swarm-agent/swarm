package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/workspace"
)

func TestWorkspaceDefinitionRefreshOnlyMarksActiveSavedWorkspaces(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-definition-refresh.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspace.NewService(workspaceStore)
	server := NewServer(nil, nil, nil, nil, nil, workspaceSvc, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account", AccountScopeSource: identity.AccountScopeSourceServerState}

	activePath := filepath.Join(t.TempDir(), "active")
	archivedPath := filepath.Join(t.TempDir(), "archived")
	if _, err := workspaceStore.AddForAccount(principal.AccountScopeID, activePath, "Active"); err != nil {
		t.Fatalf("add active workspace: %v", err)
	}
	archived, err := workspaceStore.AddForAccount(principal.AccountScopeID, archivedPath, "Archived")
	if err != nil {
		t.Fatalf("add archived workspace: %v", err)
	}
	archived.State = "archived"
	if err := store.PutJSON(pebblestore.KeyWorkspaceEntryForAccount(principal.AccountScopeID, archived.Path), archived); err != nil {
		t.Fatalf("persist archived workspace: %v", err)
	}

	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, WorkspaceDefinitionRefreshPath, nil), principal.UserID, principal.AccountScopeID)
	rec := httptest.NewRecorder()
	server.handleWorkspaceDefinitionRefresh(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		WorkspaceCount int `json:"workspace_count"`
		LaunchedCount  int `json:"launched_count"`
		FailedCount    int `json:"failed_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	if payload.WorkspaceCount != 1 || payload.LaunchedCount != 0 || payload.FailedCount != 1 {
		t.Fatalf("unexpected refresh counts: %+v", payload)
	}

	active, ok, err := workspaceStore.GetForAccount(principal.AccountScopeID, activePath)
	if err != nil || !ok {
		t.Fatalf("read active workspace ok=%t err=%v", ok, err)
	}
	if active.DefinitionGeneration != 1 || active.DefinitionStatus != pebblestore.WorkspaceDefinitionStatusFailed {
		t.Fatalf("active definition state = %+v", active)
	}
	archived, ok, err = workspaceStore.GetForAccount(principal.AccountScopeID, archivedPath)
	if err != nil || !ok {
		t.Fatalf("read archived workspace ok=%t err=%v", ok, err)
	}
	if archived.DefinitionGeneration != 0 || archived.DefinitionStatus != "" {
		t.Fatalf("archived workspace was personalized: %+v", archived)
	}
}
