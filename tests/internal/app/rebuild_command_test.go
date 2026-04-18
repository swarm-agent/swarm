package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleRebuildCommand_BinaryFailureSetsStatus(t *testing.T) {
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", "")
	t.Cleanup(func() {
		_ = os.Setenv("PATH", originalPath)
	})

	dir := t.TempDir()
	t.Setenv("SWARM_REBUILD_BIN", filepath.Join(dir, "missing-rebuild"))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%q) error = %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	a := newCommandTestApp()
	a.handleRebuildCommand()

	status := a.home.Status()
	if !strings.HasPrefix(status, "rebuild failed:") {
		t.Fatalf("status = %q, want prefix %q", status, "rebuild failed:")
	}
	if a.quitRequested {
		t.Fatalf("quitRequested = true, want false")
	}
}

func TestHandleRebuildCommand_SuccessRequestsQuit(t *testing.T) {
	t.Setenv("SWARM_LANE", "main")

	dir := t.TempDir()
	binPath := filepath.Join(dir, "rebuild")
	if err := os.WriteFile(binPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", binPath, err)
	}
	t.Setenv("SWARM_REBUILD_BIN", binPath)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%q) error = %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	a := newCommandTestApp()
	a.handleRebuildCommand()

	if !a.quitRequested {
		t.Fatalf("quitRequested = false, want true")
	}
	if got := a.home.Status(); got != "rebuild complete for lane=main; exiting swarmtui" {
		t.Fatalf("status = %q, want %q", got, "rebuild complete for lane=main; exiting swarmtui")
	}
}

func TestHandleRebuildCommand_SuccessRequestsQuitForDevLane(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "rebuild")
	if err := os.WriteFile(binPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", binPath, err)
	}
	t.Setenv("SWARM_REBUILD_BIN", binPath)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%q) error = %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})
	t.Setenv("SWARM_LANE", "dev")

	a := newCommandTestApp()
	a.handleRebuildCommand()

	if !a.quitRequested {
		t.Fatalf("quitRequested = false, want true")
	}
	if got := a.home.Status(); got != "rebuild complete for lane=dev; exiting swarmtui" {
		t.Fatalf("status = %q, want %q", got, "rebuild complete for lane=dev; exiting swarmtui")
	}
}
