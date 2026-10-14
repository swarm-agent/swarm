package tool

// Purpose: worker bash must preserve Manager.Exec's shared-group umask through
// executeBashCommand and sandboxBashScript, including login shells and child
// commands. A fake engine executes the actual supervised payload locally; this
// is the narrowest tool-boundary test for the 0644 regression, not Docker proof.
import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/sandbox"
)

type bashUmaskRunner struct {
	dir      string
	payloads int
}

func (r *bashUmaskRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	switch strings.Join(args[:min(2, len(args))], " ") {
	case "image inspect":
		return []byte("sha256:fixture\n"), nil
	case "info --format":
		return []byte(`{"runc":{}}`), nil
	}
	return nil, nil
}
func (r *bashUmaskRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func (r *bashUmaskRunner) RunWithIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, _ string, args ...string) error {
	// Discard only the engine flags and supervisor bookkeeping. Execute its
	// positional command verbatim, including the production umask wrapper.
	for i, arg := range args {
		if arg == "swarm-supervisor" {
			payload := args[i+2:]
			r.payloads++
			cmd := exec.CommandContext(ctx, "sh", append([]string{"-c", `umask 0022; exec "$@"`, "fixture"}, payload...)...)
			cmd.Dir = r.dir
			cmd.Env = append(os.Environ(), "TMPDIR="+r.dir)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
			return cmd.Run()
		}
	}
	return nil
}

func TestWorkerBashCreatesGroupWritableFiles(t *testing.T) {
	dir := t.TempDir()
	runner := &bashUmaskRunner{dir: dir}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	manager, err := sandbox.NewManager(ctx, sandbox.Config{Mode: sandbox.ModeRequired, UID: 10001, GID: 10001}, runner)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetLayout(sandbox.Layout{Projects: func() ([]string, error) { return []string{dir}, nil }})
	old := sandbox.Default()
	sandbox.SetDefault(manager)
	defer sandbox.SetDefault(old)
	for _, tc := range []struct {
		name, command string
		exit          int
	}{
		{"direct", `printf data > direct; mkdir direct-dir`, 0},
		{"child", `bash -c 'printf data > child'; (printf data > background) & wait`, 0},
		{"failure", `printf data > failure; exit 7`, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := executeBashCommand(ctx, WorkspaceScope{PrimaryPath: dir}, map[string]any{"timeout_ms": 3000}, tc.command, nil)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				ExitCode int `json:"exit_code"`
			}
			if err := json.Unmarshal([]byte(response), &result); err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != tc.exit {
				t.Fatalf("exit=%d want=%d: %s", result.ExitCode, tc.exit, response)
			}
			info, err := os.Stat(filepath.Join(dir, tc.name))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0660 {
				t.Errorf("file mode=%04o want 0660", got)
			}
		})
	}
	for _, name := range []string{"background", "direct-dir"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0660)
		if info.IsDir() {
			want = 0770
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode=%04o want %04o", name, info.Mode().Perm(), want)
		}
	}
	if runner.payloads != 3 {
		t.Fatalf("executed %d supervised payloads", runner.payloads)
	}
}
