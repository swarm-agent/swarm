package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSessionV3ArtifactFileRejectsSymlinkAndEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gallery"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gallery", "index.html"), []byte("<h1>preview</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, info, err := openSessionV3ArtifactFile(root, "gallery/index.html")
	if err != nil {
		t.Fatalf("open declared artifact: %v", err)
	}
	file.Close()
	if !info.Mode().IsRegular() {
		t.Fatalf("artifact mode = %v", info.Mode())
	}
	if _, _, err := openSessionV3ArtifactFile(root, "../outside.html"); err == nil {
		t.Fatal("workspace escape was accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "gallery", "linked.html")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openSessionV3ArtifactFile(root, "gallery/linked.html"); err == nil {
		t.Fatal("symlink artifact was accepted")
	}
	if _, _, err := openSessionV3ArtifactFile(root, "gallery"); err == nil {
		t.Fatal("directory artifact was accepted")
	}
}
