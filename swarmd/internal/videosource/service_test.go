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

func TestServiceDiscoversTypedAudioWithStableOpaqueIdentity(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "audio-source.pebble"))
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
	fixtures := map[string][]byte{
		"song.mp3":  append([]byte("ID3"), make([]byte, 32)...),
		"sound.wav": append(append([]byte("RIFF"), make([]byte, 4)...), []byte("WAVEfmt ")...),
		"music.m4a": append(append(make([]byte, 4), []byte("ftyp")...), []byte("M4A ")...),
		"voice.aac": append([]byte{0xff, 0xf1}, make([]byte, 16)...),
		"loss.flac": append([]byte("fLaC"), make([]byte, 16)...),
		"mix.ogg":   append([]byte("OggS"), []byte("vorbis")...),
	}
	for name, data := range fixtures {
		if err := os.WriteFile(filepath.Join(mediaPath, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Neither an unsupported extension nor a spoofed supported extension may be indexed.
	if err := os.WriteFile(filepath.Join(mediaPath, "notes.txt"), []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaPath, "spoof.mp3"), []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(workspaceService, pebblestore.NewSessionStore(db))
	_, roots, err := service.ListRoots(principal, workspacePath)
	if err != nil || len(roots) != 1 {
		t.Fatalf("roots=%+v err=%v", roots, err)
	}
	first, err := service.Browse(principal, workspacePath, roots[0].Ref, ".")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Browse(principal, workspacePath, roots[0].Ref, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.AudioClips) != len(fixtures) || len(second.AudioClips) != len(fixtures) {
		t.Fatalf("first=%+v second=%+v", first.AudioClips, second.AudioClips)
	}
	for i, clip := range first.AudioClips {
		if clip.MediaKind != MediaKindAudio || !strings.HasPrefix(clip.Ref, "audiosrc_") || clip.SourceFingerprint == "" || clip.FingerprintVersion != pebblestore.AudioSourceFingerprintV1 {
			t.Fatalf("audio clip=%+v", clip)
		}
		if clip.Ref != second.AudioClips[i].Ref || clip.SourceFingerprint != second.AudioClips[i].SourceFingerprint {
			t.Fatalf("audio identity changed: first=%+v second=%+v", clip, second.AudioClips[i])
		}
	}
	_, records, err := service.ResolveAudioClips(principal, workspacePath, []string{first.AudioClips[0].Ref})
	if err != nil || len(records) != 1 || records[0].Ref != first.AudioClips[0].Ref {
		t.Fatalf("records=%+v err=%v", records, err)
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
