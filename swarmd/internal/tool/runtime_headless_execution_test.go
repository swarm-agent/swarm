package tool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/config"
	"swarm/packages/swarmd/internal/sandbox"
)

func TestHeadlessRequiredSandboxRejectsHostExecution(t *testing.T) {
	// Requirement: default headless Bash and Git on an agent-writable repository
	// must not execute on the host when the engine is unavailable. Threat: a
	// fallback executes shell/Git hooks despite unavailable isolation. Owners:
	// HeadlessSandboxMode, executeBashCommand and sandbox.RouteGit. Real failed
	// engine discovery and marker-writing commands exercise the narrow execution
	// boundary without a provider, fake transport, running service or Docker socket.
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	mode, err := config.HeadlessSandboxMode("")
	if err != nil {
		t.Fatal(err)
	}
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{
		Mode: sandbox.Mode(mode), Engine: filepath.Join(root, "missing-engine"),
		StateDir: filepath.Join(root, "state"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.SetLayout(sandbox.Layout{Projects: func() ([]string, error) { return []string{project}, nil }})
	old := sandbox.Default()
	sandbox.SetDefault(m)
	defer sandbox.SetDefault(old)
	if m.Active() || !m.Enforced() {
		t.Fatal("unavailable sandbox did not remain required")
	}
	marker := filepath.Join(project, "host-executed")
	_, err = executeBash(context.Background(), WorkspaceScope{PrimaryPath: project}, map[string]any{"command": "printf escaped > host-executed", "category": "write", "critical": false, "explanation": []any{"Write a marker in the isolated test workspace"}}, nil)
	if !errors.Is(err, sandbox.ErrInactive) {
		t.Fatalf("Bash did not refuse unavailable sandbox: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Bash ran on host")
	}
	cmd := exec.Command("git", "-c", "alias.escape=!printf escaped > host-executed", "escape")
	cmd.Dir = project
	if err := sandbox.RouteGit(context.Background(), cmd); !errors.Is(err, sandbox.ErrSandboxRequired) {
		t.Fatalf("Git did not refuse unavailable sandbox: %v", err)
	}
	_, err = executeGitCommand(context.Background(), WorkspaceScope{PrimaryPath: project}, "git_status", cmd.Args[1:])
	if !errors.Is(err, sandbox.ErrSandboxRequired) {
		t.Fatalf("Git execution did not preserve routing refusal: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Git executed on host")
	}
	// Positive control: the same shell payload is executable in the isolated
	// test workspace. Absence above is refusal, not a broken command fixture.
	control := exec.Command("bash", "-lc", "printf escaped > host-executed")
	control.Dir = project
	if err := control.Run(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "escaped" {
		t.Fatal("invalid marker control")
	}
}
