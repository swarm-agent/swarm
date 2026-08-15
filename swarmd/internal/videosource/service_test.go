package videosource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

func TestServiceListsAndBrowsesRegisteredRootsWithoutPaths(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "video-source.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-1"}
	workspacePath, mediaPath := t.TempDir(), t.TempDir()
	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(db))
	if _, err := workspaceService.AddForPrincipal(principal, workspacePath, "workspace", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceService.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, mediaPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(mediaPath, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaPath, "clip.mp4"), []byte("synthetic video"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(workspaceService, pebblestore.NewSessionStore(db))
	workspaceID, roots, err := service.ListRoots(principal, workspacePath)
	if err != nil || workspaceID == "" || len(roots) != 1 || !strings.HasPrefix(roots[0].Ref, "videosource_root_") {
		t.Fatalf("workspace=%q roots=%+v err=%v", workspaceID, roots, err)
	}
	result, err := service.Browse(principal, workspacePath, roots[0].Ref, ".")
	if err != nil {
		t.Fatal(err)
	}
	if result.RootPath != mediaPath || len(result.Directories) != 1 || len(result.Clips) != 1 || !strings.HasPrefix(result.Clips[0].Ref, "videosrc_") {
		t.Fatalf("result=%+v", result)
	}
}

func TestServiceRejectsTraversalUnknownRootAndSymlink(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "video-source-security.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-1"}
	workspacePath, mediaPath := t.TempDir(), t.TempDir()
	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(db))
	if _, err := workspaceService.AddForPrincipal(principal, workspacePath, "workspace", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceService.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, mediaPath); err != nil {
		t.Fatal(err)
	}
	service := NewService(workspaceService, pebblestore.NewSessionStore(db))
	_, roots, err := service.ListRoots(principal, workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Browse(principal, workspacePath, "videosource_root_unknown", "."); err == nil {
		t.Fatal("expected unknown root rejection")
	}
	if _, err := service.Browse(principal, workspacePath, roots[0].Ref, "../escape"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(mediaPath, "linked")); err != nil {
		t.Fatal(err)
	}
	result, err := service.Browse(principal, workspacePath, roots[0].Ref, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Directories) != 0 {
		t.Fatalf("symlink directory exposed: %+v", result.Directories)
	}
}
