package main

import (
	"errors"
	"strings"
	"testing"
)

// Requirement: missing Git must not block any interactive Swarm entry point.
// Threat: a global launcher prerequisite prevents first-time users from opening
// ordinary workspaces even though only Git-managed features need Git. This pure
// dispatch helper test proves the warning is limited to interactive launches.
func TestInteractiveGitWarningDoesNotBlockLaunch(t *testing.T) {
	old := gitLookPath
	gitLookPath = func(string) (string, error) { return "", errors.New("missing") }
	t.Cleanup(func() { gitLookPath = old })

	for _, args := range [][]string{nil, {"run"}, {"--desktop"}, {"open"}, {"session", "tui"}, {"--workspace", "."}} {
		warning := interactiveGitWarning(args)
		for _, want := range []string{"Git isn't installed", "will continue", "ordinary workspaces remain usable", "approve the system change"} {
			if !strings.Contains(warning, want) {
				t.Errorf("interactiveGitWarning(%q) = %q, missing %q", args, warning, want)
			}
		}
	}
	for _, args := range [][]string{{"help"}, {"status"}, {"install"}, {"uninstall"}, {"session", "list"}} {
		if warning := interactiveGitWarning(args); warning != "" {
			t.Errorf("interactiveGitWarning(%q) = %q, want no warning", args, warning)
		}
	}
}

func TestInteractiveGitWarningSilentWhenAvailable(t *testing.T) {
	old := gitLookPath
	gitLookPath = func(name string) (string, error) {
		if name != "git" {
			t.Fatalf("looked up %q, want git", name)
		}
		return "/test/bin/git", nil
	}
	t.Cleanup(func() { gitLookPath = old })

	if warning := interactiveGitWarning(nil); warning != "" {
		t.Fatalf("interactiveGitWarning() = %q", warning)
	}
}
