package main

import "testing"

// Requirement: launcher help remains available without resolving installed
// runtime state, so users can recover even when installation is incomplete.
func TestHelpDispatchDoesNotRequireInstalledRuntime(t *testing.T) {
	if err := run("swarm", []string{"--help"}); err != nil {
		t.Fatalf("run --help: %v", err)
	}
}
