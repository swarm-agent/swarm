package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Purpose: durable writer identity must survive daemon restart and only a
// confirmed stopped container may release the cleanup guard. This engine
// protocol fixture tests recovery without relying on a live Docker daemon.
func TestReviewWriterRestart(t *testing.T) {
	root := t.TempDir()
	engine := filepath.Join(root, "engine")
	if err := os.WriteFile(engine, []byte("#!/bin/sh\necho false\n"), 0700); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(root, "review.review-writer")
	if err := os.WriteFile(guard, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(engine, "exec", "container", "timeout", "--kill-after=2", "1", "git")
	if err := recordReviewWriter(guard, cmd); err != nil {
		t.Fatal(err)
	}
	if err := recoverReviewWriter(guard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(guard); !os.IsNotExist(err) {
		t.Fatal("guard retained", err)
	}
	if err := recoverReviewWriter(guard); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordReviewWriter(guard, cmd); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(engine, []byte("#!/bin/sh\necho true\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := recoverReviewWriter(guard); err == nil {
		t.Fatal("live writer accepted")
	}
	if _, err := os.Stat(guard); err != nil {
		t.Fatal("guard lost", err)
	}
}

// Purpose: explicit engine absence releases a durable guard, whereas a daemon
// outage cannot prove termination. The protocol boundary is the narrowest test.
func TestReviewWriterMissingContainer(t *testing.T) {
	for _, tc := range []struct {
		response string
		gone     bool
	}{{"Error: No such object: container", true}, {"Cannot connect to daemon", false}} {
		t.Run(tc.response, func(t *testing.T) {
			root := t.TempDir()
			engine := filepath.Join(root, "engine")
			guard := filepath.Join(root, "guard")
			if err := os.WriteFile(engine, []byte("#!/bin/sh\necho '"+tc.response+"' >&2\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(guard, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := recordReviewWriter(guard, exec.Command(engine, "exec", "container", "timeout", "1", "git")); err != nil {
				t.Fatal(err)
			}
			err := recoverReviewWriter(guard)
			if (err == nil) != tc.gone {
				t.Fatalf("recovery: %v", err)
			}
			_, err = os.Stat(guard)
			if os.IsNotExist(err) != tc.gone {
				t.Fatalf("guard: %v", err)
			}
		})
	}
}
