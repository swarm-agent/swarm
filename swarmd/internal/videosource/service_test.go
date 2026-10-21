package videosource

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

func makeVideoSourceReadyRepository(t *testing.T, path string) {
	t.Helper()
	commands := [][]string{
		{"init", "--initial-branch=main"},
		{"-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@localhost", "commit", "--allow-empty", "--no-gpg-sign", "-m", "Initial commit"},
	}
	for _, args := range commands {
		command := exec.Command("git", append([]string{"-C", path}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("prepare Git workspace: %v: %s", err, output)
		}
	}
}

func TestAudioReferencesRejectUnregisteredRootsAndStaleFiles(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "audio-source-security.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-1"}
	workspacePath, mediaPath, outsidePath := t.TempDir(), t.TempDir(), t.TempDir()
	makeVideoSourceReadyRepository(t, workspacePath)
	workspaceService := workspace.NewService(pebblestore.NewWorkspaceStore(db))
	if _, err := workspaceService.AddForPrincipal(principal, workspacePath, "workspace", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceService.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, mediaPath); err != nil {
		t.Fatal(err)
	}
	sessionStore := pebblestore.NewSessionStore(db)
	service := NewService(workspaceService, sessionStore)
	workspaceID, roots, err := service.ListRoots(principal, workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	audioPath := filepath.Join(mediaPath, "song.mp3")
	if err := os.WriteFile(audioPath, append([]byte("ID3"), make([]byte, 16)...), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.Browse(principal, workspacePath, roots[0].Ref, ".")
	if err != nil || len(result.AudioClips) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := os.WriteFile(audioPath, append([]byte("ID3changed"), make([]byte, 32)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ResolveAudioClips(principal, workspacePath, []string{result.AudioClips[0].Ref}); err == nil || !strings.Contains(err.Error(), "stale or unavailable") {
		t.Fatalf("stale audio error=%v", err)
	}
	outsideFile := filepath.Join(outsidePath, "outside.mp3")
	outsideData := append([]byte("ID3"), make([]byte, 16)...)
	if err := os.WriteFile(outsideFile, outsideData, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := sessionStore.PutAudioSourceRecord(pebblestore.AudioSourceRecord{
		AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceID, RootPath: outsidePath,
		RelativePath: "outside.mp3", DisplayName: "outside.mp3", MIMEType: "audio/mpeg",
		SizeBytes: info.Size(), ModifiedAt: info.ModTime().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ResolveAudioClips(principal, workspacePath, []string{foreign.Ref}); err == nil || !strings.Contains(err.Error(), "registered source-media root") {
		t.Fatalf("outside-root audio error=%v", err)
	}
	record, found, err := sessionStore.GetAudioSourceRecord(principal.AccountScopeID, workspaceID, foreign.Ref)
	if err != nil || !found {
		t.Fatalf("get foreign record found=%v err=%v", found, err)
	}
	record.MIMEType = "audio/wav"
	if err := db.PutJSON(pebblestore.KeyAudioSourceRecord(record.AccountScopeID, record.WorkspaceID, record.Ref), record); err != nil {
		t.Fatal(err)
	}
	if err := pebblestore.ValidateAudioSourceRecord(record); err == nil || !strings.Contains(err.Error(), "MIME type") {
		t.Fatalf("tampered audio metadata error=%v", err)
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
	makeVideoSourceReadyRepository(t, workspacePath)
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
