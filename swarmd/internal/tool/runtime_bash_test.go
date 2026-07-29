package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBashRunsInWorkspaceWithPrivateRunTempDirectory(t *testing.T) {
	workspace := t.TempDir()
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	payload, err := executeBashCommand(
		context.Background(),
		WorkspaceScope{PrimaryPath: workspace},
		map[string]any{"timeout_ms": 1000},
		`printf '%s\n%s\n%s\n%s\n' "$PWD" "$TMPDIR" "$TMP" "$TEMP"`,
		nil,
	)
	if err != nil {
		t.Fatalf("execute bash: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode bash payload: %v", err)
	}
	lines := strings.Fields(decoded["output"].(string))
	if len(lines) != 4 {
		t.Fatalf("bash environment output = %q", decoded["output"])
	}
	if lines[0] != workspace {
		t.Fatalf("bash working directory = %q, want workspace %q", lines[0], workspace)
	}
	if lines[1] != lines[2] || lines[2] != lines[3] {
		t.Fatalf("temporary environment differs: TMPDIR=%q TMP=%q TEMP=%q", lines[1], lines[2], lines[3])
	}
	if filepath.Dir(lines[1]) != filepath.Clean(tempRoot) {
		t.Fatalf("command temp parent = %q, want run temp %q", filepath.Dir(lines[1]), tempRoot)
	}
	if _, err := os.Stat(lines[1]); !os.IsNotExist(err) {
		t.Fatalf("command temp directory still exists after Bash completed: %v", err)
	}
}

func TestBashUsesPlatformTempDirectoryWithoutEnvironmentSetup(t *testing.T) {
	t.Setenv("TMPDIR", "")
	payload, err := executeBashCommand(
		context.Background(),
		WorkspaceScope{PrimaryPath: t.TempDir()},
		map[string]any{"timeout_ms": 1000},
		`printf '%s\n%s\n%s\n' "$TMPDIR" "$TMP" "$TEMP"`,
		nil,
	)
	if err != nil {
		t.Fatalf("execute bash without TMPDIR: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode bash payload: %v", err)
	}
	lines := strings.Fields(decoded["output"].(string))
	if len(lines) != 3 || lines[0] != lines[1] || lines[1] != lines[2] {
		t.Fatalf("private temporary environment = %q", decoded["output"])
	}
	if filepath.Dir(lines[0]) != filepath.Clean(os.TempDir()) {
		t.Fatalf("command temp parent = %q, want platform temp root %q", filepath.Dir(lines[0]), os.TempDir())
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatalf("command temp directory still exists after Bash completed: %v", err)
	}
}

func TestCommandEnvironmentRemovesSecretsAndPreservesRuntimeContext(t *testing.T) {
	tempDir := t.TempDir()
	env := commandEnvironment([]string{
		"PATH=/usr/bin", "HOME=/home/example", "LANG=C.UTF-8", "SHELL=/bin/bash",
		"SWARMD_TOKEN=secret", "CODEX_API_KEY=secret", "DATABASE_PASSWORD=secret",
		"AWS_SECRET_ACCESS_KEY=secret", "SESSION_COOKIE=secret", "TMPDIR=/shared",
	}, tempDir)
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, preserved := range []string{"PATH=/usr/bin", "HOME=/home/example", "LANG=C.UTF-8", "SHELL=/bin/bash"} {
		if !strings.Contains(joined, "\n"+preserved+"\n") {
			t.Fatalf("environment missing %q: %q", preserved, env)
		}
	}
	for _, secret := range []string{"SWARMD_TOKEN", "CODEX_API_KEY", "DATABASE_PASSWORD", "AWS_SECRET_ACCESS_KEY", "SESSION_COOKIE"} {
		if strings.Contains(joined, "\n"+secret+"=") {
			t.Fatalf("environment leaked %s: %q", secret, env)
		}
	}
	for _, tempName := range []string{"TMPDIR", "TMP", "TEMP"} {
		if !strings.Contains(joined, "\n"+tempName+"="+tempDir+"\n") {
			t.Fatalf("environment missing private %s: %q", tempName, env)
		}
	}
}

func TestBashKeepsLargeTextForOutputViewer(t *testing.T) {
	workspace := t.TempDir()
	line := strings.Repeat("x", 1024)
	repeat := 64
	expected := strings.Repeat(line+"\n", repeat)
	rt := NewRuntime(2)
	results := rt.ExecuteBatch(context.Background(), workspace, []Call{
		{
			CallID: "bash-large-text",
			Name:   "bash",
			Arguments: mustJSON(t, map[string]any{
				"command":     "yes " + strings.TrimSpace(line) + " | head -n " + strconv.Itoa(repeat),
				"explanation": []string{"Print a fixed number of large text lines to standard output to exercise the Bash output viewer."},
				"category":    "read",
				"critical":    false,
				"timeout_ms":  5000,
			}),
		},
	})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if strings.TrimSpace(results[0].Error) != "" {
		t.Fatalf("unexpected bash error: %s", results[0].Error)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(results[0].Output), &decoded); err != nil {
		t.Fatalf("decode bash payload: %v\n%s", err, results[0].Output)
	}
	if truncated, _ := decoded["truncated"].(bool); truncated {
		t.Fatalf("did not expect output viewer payload to be truncated")
	}
	got, _ := decoded["output"].(string)
	if got != expected {
		t.Fatalf("large bash output length = %d, want %d", len(got), len(expected))
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(encoded)
}
