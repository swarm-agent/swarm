package main

import (
	"errors"
	"strings"
	"testing"
)

// Requirement: interactive Desktop/TUI entry points must stop before launcher
// setup when Git is unavailable, while maintenance commands remain usable.
// Threat: an older or partial installation otherwise reaches workspace code and
// reports an opaque downstream failure. The cmd/swarm dispatch boundary is the
// narrowest layer that proves which commands are guarded without starting a
// daemon, browser, or TUI.
func TestInteractiveLaunchRequiresGit(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "default tui", want: true},
		{name: "explicit run", args: []string{"run"}, want: true},
		{name: "desktop", args: []string{"--desktop"}, want: true},
		{name: "open", args: []string{"open"}, want: true},
		{name: "session tui", args: []string{"session", "tui"}, want: true},
		{name: "tui passthrough", args: []string{"--workspace", "."}, want: true},
		{name: "help", args: []string{"help"}, want: false},
		{name: "desktop help", args: []string{"--desktop", "--help"}, want: false},
		{name: "status", args: []string{"status"}, want: false},
		{name: "install", args: []string{"install"}, want: false},
		{name: "uninstall", args: []string{"uninstall"}, want: false},
		{name: "session api", args: []string{"session", "list"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := interactiveLaunchRequiresGit(tt.args); got != tt.want {
				t.Fatalf("interactiveLaunchRequiresGit(%q) = %t, want %t", tt.args, got, tt.want)
			}
		})
	}
}

// Requirement: missing Git must produce actionable, distribution-specific
// remediation and state that Swarm will not elevate or install packages itself.
// Threat: a raw exec.LookPath error does not explain the product prerequisite.
// Injecting command lookup at this helper is the narrowest hermetic proof of the
// user-facing failure contract.
func TestRequireGitActionableFailure(t *testing.T) {
	old := gitLookPath
	gitLookPath = func(string) (string, error) { return "", errors.New("missing") }
	t.Cleanup(func() { gitLookPath = old })

	err := requireGit()
	if err == nil {
		t.Fatal("requireGit() succeeded without git")
	}
	for _, want := range []string{
		"Swarm requires Git",
		"sudo apt update && sudo apt install -y git",
		"sudo dnf install -y git",
		"sudo pacman -S git",
		"does not install system packages automatically",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestRequireGitAvailable(t *testing.T) {
	old := gitLookPath
	gitLookPath = func(name string) (string, error) {
		if name != "git" {
			t.Fatalf("looked up %q, want git", name)
		}
		return "/test/bin/git", nil
	}
	t.Cleanup(func() { gitLookPath = old })

	if err := requireGit(); err != nil {
		t.Fatalf("requireGit() error = %v", err)
	}
}
