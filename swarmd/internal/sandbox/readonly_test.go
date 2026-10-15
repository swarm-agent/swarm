package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: ReadOnlyCommand must reject shell escapes and mutating commands
// before spawning. Real system cat proves the accepted argv reads unchanged
// bytes; this narrow parser/process test does not claim Docker confinement.
func TestReadOnlyCommand(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "input")
	if err := os.WriteFile(path, []byte("readable"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"touch output", "echo hi > output", "git commit -m hi", "cat input; touch output", "rg --pre=sh x", "cat $(touch output)", "cat input\nrm input", "/bin/sh -c true"} {
		if _, err := ReadOnlyCommand(command); err == nil || !strings.Contains(err.Error(), "read-only bash refused") {
			t.Fatalf("%q: %v", command, err)
		}
	}
	argv, err := ReadOnlyCommand("cat " + path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil || string(output) != "readable" {
		t.Fatalf("read: %q %v", output, err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("unexpected write")
	}
}

// Purpose: ExecReadOnly must reject mutations before the existing sandbox
// runner sees them; use the package's engine fake, not a fake policy boundary.
func TestReadOnlyExecRefusal(t *testing.T) {
	r := &lifecycleRunner{fakeRunner: &fakeRunner{}}
	m, scope := concurrencyManager(t, r)
	if _, err := m.ExecReadOnly(context.Background(), scope, "touch output", ExecRequest{}); err == nil {
		t.Fatal("write accepted")
	}
	if _, err := m.ExecReadOnly(context.Background(), scope, "git commit", ExecRequest{}); err == nil {
		t.Fatal("commit accepted")
	}
}

// Purpose: execute accepted readers through Manager.ExecReadOnly in the real
// Docker namespace and ensure rejected writes leave the job worktree intact.
func TestReadOnlyExecDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker unavailable: read-only exec integration requires Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skip("Docker daemon unavailable")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input"), []byte("readable"), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(ctx, Config{Mode: ModeRequired, UID: os.Getuid(), GID: os.Getgid()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{root}, nil }})
	scope, err := m.CommandScope(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.ExecReadOnly(ctx, scope, "cat input", ExecRequest{WorkingDir: root, Timeout: 10 * time.Second})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("read: %+v %v", result, err)
	}
	if _, err := m.ExecReadOnly(ctx, scope, "touch output", ExecRequest{WorkingDir: root}); err == nil {
		t.Fatal("write accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "output")); !os.IsNotExist(err) {
		t.Fatal("write changed worktree")
	}
}
