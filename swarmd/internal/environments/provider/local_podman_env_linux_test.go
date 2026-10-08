package provider

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: localPodmanEnvironment must recognize a real owned Unix socket and
// reject a missing or symlinked bus through its Linux Lstat ownership boundary.
// Temporary filesystem objects are the narrowest proof of production stat
// wiring; this does not claim a live systemd or Podman connection succeeded.
func TestLocalPodmanEnvironmentLinuxSocket(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("rootless provider requires a non-root user")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	if env, err := localPodmanEnvironment(); err == nil || env != nil {
		t.Fatal("missing bus accepted")
	}
	bus := filepath.Join(dir, "bus")
	listener, err := net.Listen("unix", bus)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	env, err := localPodmanEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range env {
		if strings.HasPrefix(entry, "DBUS_SESSION_BUS_ADDRESS=unix:path=") {
			found = true
		}
	}
	if !found || os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		t.Fatal("missing child bus address or mutated daemon environment")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "other-bus"), bus); err != nil {
		t.Fatal(err)
	}
	if env, err := localPodmanEnvironment(); err == nil || env != nil {
		t.Fatal("symlink bus accepted")
	}
}
