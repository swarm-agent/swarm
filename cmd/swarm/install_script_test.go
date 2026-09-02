package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: install.sh must reject a host without Git before prompting,
// downloading, or mutating installation/service paths. Threat: installation can
// appear successful but leave interactive workspace startup unusable. Running
// the public installer with a controlled PATH is the narrowest hermetic test of
// ordering and actionable shell output; marker shims prove later prerequisites
// were never invoked.
func TestInstallerFailsBeforeSideEffectsWhenGitMissing(t *testing.T) {
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
	marker := filepath.Join(tmp, "later-command-ran")
	for _, name := range []string{"uname", "curl", "tar", "sed", "grep", "awk", "mktemp", "id", "install"} {
		shim := "#!/bin/sh\n: > " + shellQuote(marker) + "\nexit 99\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(filepath.Join(bin, "sh"), "../../install.sh", "--yes", "--no-service")
	cmd.Env = []string{"PATH=" + bin}
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh succeeded without git")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("a later command ran before the Git check: %v", statErr)
	}
	text := string(output)
	for _, want := range []string{
		"Swarm requires Git",
		"sudo apt update && sudo apt install -y git",
		"sudo dnf install -y git",
		"sudo pacman -S git",
		"does not install system packages automatically",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output %q does not contain %q", text, want)
		}
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
