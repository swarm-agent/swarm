package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageWorkspaceSourceMediaDirectories(t *testing.T) {
	const sessionID = "sess-source-media"
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1", SessionID: sessionID}
	workspacePath := programFixtureRepo(t)
	mediaDir1 := filepath.Join(t.TempDir(), "media1")
	mediaDir2 := filepath.Join(t.TempDir(), "media2")
	if err := os.MkdirAll(mediaDir1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mediaDir2, 0o755); err != nil {
		t.Fatal(err)
	}

	workspaceSvc, _, rawStore, cleanup := newTestRunWorkspaceServiceWithRawStore(t)
	defer cleanup()

	entry, err := workspaceSvc.AddForPrincipal(principal, workspacePath, "repo", "", true)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	sessionStore := pebblestore.NewSessionStore(rawStore)
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{
		ID: sessionID, WorkspacePath: workspacePath, WorkspaceName: "repo", Title: "Media Test",
		Metadata: map[string]any{"workspace_id": entry.WorkspaceID},
	}, principal.UserID, principal.AccountScopeID); err != nil {
		t.Fatalf("create session: %v", err)
	}

	sessionSvc := sessionruntime.NewService(sessionStore, nil)
	runSvc := NewService(sessionSvc, nil, nil, nil, nil, nil, nil, nil)
	runSvc.SetWorkspaceService(workspaceSvc)

	// 1. List initially: should be empty
	listArgs, _ := json.Marshal(map[string]any{
		"action": "list_source_media_directories",
	})
	listOut, err := runSvc.executeManageWorkspaceTool(sessionID, string(listArgs), principal, sessionSvc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("list_source_media_directories: %v", err)
	}
	var listRes struct {
		Action                 string   `json:"action"`
		Status                 string   `json:"status"`
		SourceMediaDirectories []string `json:"source_media_directories"`
		Count                  int      `json:"count"`
	}
	if err := json.Unmarshal([]byte(listOut), &listRes); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if listRes.Count != 0 || len(listRes.SourceMediaDirectories) != 0 {
		t.Fatalf("expected 0 directories, got %d", listRes.Count)
	}

	// 2. Add source media directory
	addArgs, _ := json.Marshal(map[string]any{
		"action":         "add_source_media_directory",
		"directory_path": mediaDir1,
	})
	addOut, err := runSvc.executeManageWorkspaceTool(sessionID, string(addArgs), principal, sessionSvc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("add_source_media_directory: %v", err)
	}
	var addRes struct {
		Action                 string   `json:"action"`
		Status                 string   `json:"status"`
		SourceMediaDirectories []string `json:"source_media_directories"`
		Count                  int      `json:"count"`
	}
	if err := json.Unmarshal([]byte(addOut), &addRes); err != nil {
		t.Fatalf("unmarshal add: %v", err)
	}
	if addRes.Count != 1 || len(addRes.SourceMediaDirectories) != 1 {
		t.Fatalf("expected 1 directory after add, got %d", addRes.Count)
	}

	// 3. Add second directory using alias "directory"
	add2Args, _ := json.Marshal(map[string]any{
		"action":    "add_source_media_directory",
		"directory": mediaDir2,
	})
	add2Out, err := runSvc.executeManageWorkspaceTool(sessionID, string(add2Args), principal, sessionSvc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("add_source_media_directory 2: %v", err)
	}
	if err := json.Unmarshal([]byte(add2Out), &addRes); err != nil {
		t.Fatalf("unmarshal add 2: %v", err)
	}
	if addRes.Count != 2 {
		t.Fatalf("expected 2 directories after add 2, got %d", addRes.Count)
	}

	// 4. Remove first directory
	remArgs, _ := json.Marshal(map[string]any{
		"action":         "remove_source_media_directory",
		"directory_path": mediaDir1,
	})
	remOut, err := runSvc.executeManageWorkspaceTool(sessionID, string(remArgs), principal, sessionSvc.ApplySessionMutation)
	if err != nil {
		t.Fatalf("remove_source_media_directory: %v", err)
	}
	var remRes struct {
		Action                 string   `json:"action"`
		Status                 string   `json:"status"`
		SourceMediaDirectories []string `json:"source_media_directories"`
		Count                  int      `json:"count"`
	}
	if err := json.Unmarshal([]byte(remOut), &remRes); err != nil {
		t.Fatalf("unmarshal remove: %v", err)
	}
	if remRes.Count != 1 {
		t.Fatalf("expected 1 directory after remove, got %d", remRes.Count)
	}

	// 5. Error case: missing directory_path on add
	badAddArgs, _ := json.Marshal(map[string]any{
		"action": "add_source_media_directory",
	})
	if _, err := runSvc.executeManageWorkspaceTool(sessionID, string(badAddArgs), principal, sessionSvc.ApplySessionMutation); err == nil || !strings.Contains(err.Error(), "directory_path") {
		t.Fatalf("expected error on missing directory_path, got %v", err)
	}
}
