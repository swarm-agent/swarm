package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: the public installer must warn but continue when Git is missing.
// Threat: a first-time user without Git is blocked before reaching an otherwise
// usable ordinary workspace. A controlled PATH proves the script passes its Git
// check and reaches the next prerequisite without performing installation writes.
func TestInstallerWarnsAndContinuesWhenGitMissing(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sh", "cat"} {
		commandPath, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(commandPath, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	unamePath, err := exec.LookPath("uname")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unamePath, filepath.Join(bin, "uname")); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(filepath.Join(bin, "sh"), "../../install.sh", "--yes", "--no-service")
	cmd.Env = []string{"PATH=" + bin}
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly passed the intentionally missing curl prerequisite")
	}
	text := string(output)
	for _, want := range []string{"Warning: Git is not installed", "will still install", "ordinary workspaces remain usable", "approve the system change when prompted", "missing required command: curl"} {
		if !strings.Contains(text, want) {
			t.Errorf("output %q does not contain %q", text, want)
		}
	}
	if strings.Contains(text, "Swarm requires Git") {
		t.Fatalf("installer retained blocking Git prerequisite: %q", text)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
