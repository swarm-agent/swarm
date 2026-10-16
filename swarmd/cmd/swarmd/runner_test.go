package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose: runner credentials must never be read from a permissive file or
// symlink. The descriptor-based readRunnerCredential boundary is the narrowest
// layer proving rejection without starting a daemon or contacting a hub.
func TestRunnerCredentialPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential")
	if err := os.WriteFile(path, []byte("test-only-token\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunnerCredential(path); err == nil {
		t.Fatal("accepted group-readable credential")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	token, err := readRunnerCredential(path)
	if os.Getuid() == 0 {
		if err != nil || token != "test-only-token" {
			t.Fatalf("private root credential: %q %v", token, err)
		}
	} else if err == nil {
		t.Fatal("accepted non-root owner")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunnerCredential(link); err == nil {
		t.Fatal("accepted symlink")
	}
}
