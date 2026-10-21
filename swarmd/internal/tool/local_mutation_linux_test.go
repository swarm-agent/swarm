//go:build linux

package tool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Requirement: each directory component of a daemon file mutation is opened
// without following a symlink. Threat: runtimes such as gVisor follow a
// symlinked component despite O_NOFOLLOW when O_DIRECTORY is set, letting a
// planted in-workspace symlink redirect writes outside the workspace. Owner:
// openNoFollowDirAt (used by openLocalMutationParent). The syscall helper is the
// narrowest layer; the design-file and write tool tests cover the callers.
func TestOpenNoFollowDirAtRefusesSymlinkedDirectory(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	fd, err := openNoFollowDirAt(int(dir.Fd()), "real")
	if err != nil {
		t.Fatalf("real directory refused: %v", err)
	}
	unix.Close(fd)

	if fd, err := openNoFollowDirAt(int(dir.Fd()), "linked"); err == nil {
		unix.Close(fd)
		t.Fatal("symlinked directory opened")
	} else if !errors.Is(err, unix.ELOOP) && !errors.Is(err, unix.ENOTDIR) {
		t.Fatalf("unexpected error for symlinked directory: %v", err)
	}
	if fd, err := openNoFollowDirAt(int(dir.Fd()), "file"); err == nil {
		unix.Close(fd)
		t.Fatal("regular file opened as directory")
	}
	if fd, err := openNoFollowDirAt(int(dir.Fd()), "missing"); err == nil || !errors.Is(err, unix.ENOENT) {
		if err == nil {
			unix.Close(fd)
		}
		t.Fatalf("missing entry: %v", err)
	}
}
