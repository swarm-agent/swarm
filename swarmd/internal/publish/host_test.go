package publish

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func writeTestScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// Requirement: OpenOwnedByRoot returns the file it checked, open: after the
// file at the path is swapped (by rename), reading the returned file still
// gives the checked contents.
// Threat: whoever can write the script's directory replacing the script
// between the owner check and its use, so a file that never passed the
// check is used.
// Owner: OpenOwnedByRoot (the job automation run step's executor copies
// and runs exactly this file). The publish command does not use it; its
// own tests are unchanged.
func TestOpenOwnedByRootKeepsTheCheckedFileAcrossASwap(t *testing.T) {
	t.Cleanup(AllowOwnerForTests(os.Getuid()))
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "push.sh")
	writeTestScript(t, path, "echo checked\n")
	file, _, err := OpenOwnedByRoot(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writeTestScript(t, filepath.Join(dir, "swap.sh"), "echo swapped\n")
	if err := os.Rename(filepath.Join(dir, "swap.sh"), path); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\necho checked\n" {
		t.Fatalf("the open file reads %q, want the checked contents", data)
	}
}

// Requirement: OpenOwnedByRoot refuses a path with a symlink in it (it is
// given an already-resolved path, so a symlink there means the path was
// changed after it was resolved), a path that is itself a symlink, a FIFO
// (without blocking), and a file that fails the owner rules, with the same
// reasons as OwnedByRoot.
// Threat: a directory on the script's path swapped for a link to a
// root-owned program elsewhere after the path was resolved; a FIFO hanging
// the step before its time limit applies.
// Owner: OpenOwnedByRoot.
func TestOpenOwnedByRootRefusesLinksAndFilesThatFailTheOwnerCheck(t *testing.T) {
	restore := AllowOwnerForTests(os.Getuid())
	t.Cleanup(restore)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "real", "push.sh")
	writeTestScript(t, script, "echo ok\n")
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(script, filepath.Join(dir, "push.sh")); err != nil {
		t.Fatal(err)
	}
	if f, _, err := OpenOwnedByRoot(script, true); err != nil {
		t.Fatalf("a passing file: %v", err)
	} else {
		f.Close()
	}
	if _, _, err := OpenOwnedByRoot(filepath.Join(dir, "link", "push.sh"), true); !errors.Is(err, ErrChanged) {
		t.Fatalf("a directory link on the path: err = %v, want ErrChanged", err)
	}
	if _, _, err := OpenOwnedByRoot(filepath.Join(dir, "push.sh"), true); !errors.Is(err, ErrChanged) {
		t.Fatalf("a path that is itself a link: err = %v, want ErrChanged", err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenOwnedByRoot(filepath.Join(dir, "fifo"), true); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("a FIFO: err = %v, want ErrNotRegular", err)
	}
	if err := os.Chmod(script, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenOwnedByRoot(script, true); !errors.Is(err, ErrWritable) {
		t.Fatalf("a group-writable file: err = %v", err)
	}
	if err := os.Chmod(script, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenOwnedByRoot(script, true); !errors.Is(err, ErrNotExecutable) {
		t.Fatalf("a file that is not executable: err = %v", err)
	}
	if os.Getuid() != 0 {
		if err := os.Chmod(script, 0o755); err != nil {
			t.Fatal(err)
		}
		AllowOwnerForTests(-1)
		if _, _, err := OpenOwnedByRoot(script, true); !errors.Is(err, ErrNotRootOwned) {
			t.Fatalf("a file the test user owns: err = %v", err)
		}
	}
}
