package pebblestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionVideoAttachmentRejectsStaleAndForgedReferences(t *testing.T) {
	_, sessions := openSessionMediaTestStore(t)
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, []byte("00000000ftypisomvideo"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := sessions.PutVideoSourceRecord(VideoSourceRecord{
		AccountScopeID: "account", WorkspaceID: "workspace", RootPath: root, RelativePath: "clip.mp4",
		DisplayName: "clip.mp4", MIMEType: "video/mp4", SizeBytes: info.Size(), ModifiedAt: info.ModTime().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := SessionSnapshot{AccountScopeID: "account", Metadata: map[string]any{"workspace_id": "workspace"}}
	refs, err := sessions.ValidateSessionVideoAttachments("account", session, []SessionVideoAttachmentReference{{Ref: record.Ref}})
	if err != nil || len(refs) != 1 || refs[0].Name != "clip.mp4" || refs[0].SourceFingerprint != record.SourceFingerprint {
		t.Fatalf("validated refs=%+v err=%v", refs, err)
	}
	if _, err := sessions.ValidateSessionVideoAttachments("account", session, []SessionVideoAttachmentReference{{Ref: "videosrc_" + videoSourceDigest("forged")}}); err == nil {
		t.Fatal("expected forged reference rejection")
	}
	if err := os.WriteFile(path, []byte("00000000ftypisomchanged-video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.ValidateSessionVideoAttachments("account", session, []SessionVideoAttachmentReference{{Ref: record.Ref}}); err == nil {
		t.Fatal("expected stale source fingerprint rejection")
	}
}
