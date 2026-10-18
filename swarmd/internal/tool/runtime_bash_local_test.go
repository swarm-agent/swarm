package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/sandbox"
)

func TestHeadlessLocalBashOffExplicitHostMarker(t *testing.T) {
	// Purpose: an explicit off choice runs real Bash as the daemon OS user.
	// Owner: executeBashCommand. Threat: off accidentally keeps confinement or
	// silently reports isolation while a host-visible write was performed.
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeOff, Backend: sandbox.BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old := sandbox.Default()
	sandbox.SetDefault(m)
	defer sandbox.SetDefault(old)
	marker := filepath.Join(root, "host-marker")
	response, err := executeBash(context.Background(), WorkspaceScope{PrimaryPath: workspace}, map[string]any{"command": "printf authorized > '" + marker + "'", "category": "write", "critical": false, "explanation": []any{"Write an authorized host marker in the temporary fixture"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(response), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["execution_mode"] != "host" || receipt["exit_code"] != float64(0) {
		t.Fatalf("incorrect receipt: %s", response)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "authorized" {
		t.Fatal("explicit host command did not write marker")
	}
}

func TestHeadlessLocalBashMissingBubblewrapRefuses(t *testing.T) {
	// Purpose: the actual headless backend refuses at the Bash tool boundary
	// when bwrap is missing. Owner: executeBashCommand/Manager.execLocal.
	// Threat: permission-authorized commands silently use direct host execution.
	root := t.TempDir()
	t.Setenv("PATH", root)
	m, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeRequired, Backend: sandbox.BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old := sandbox.Default()
	sandbox.SetDefault(m)
	defer sandbox.SetDefault(old)
	_, err = executeBash(context.Background(), WorkspaceScope{PrimaryPath: root}, map[string]any{"command": "printf escaped > marker", "category": "write", "critical": false, "explanation": []any{"Attempt a marker in the temporary fixture"}}, nil)
	if !errors.Is(err, sandbox.ErrSandboxRequired) {
		t.Fatalf("missing bwrap did not refuse: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
		t.Fatal("host fallback executed")
	}
}
