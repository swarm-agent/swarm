package main

import "testing"

// Requirement: conflicting identity modes and release-update identity injection
// are rejected before preflight/account/path mutation. No real install is run.
func TestInstallationAccountCLIRejectsConflicts(t *testing.T) {
	for _, args := range [][]string{
		{"--install-user"}, {"--create-user", "--service"},
		{"--install-user", "developer", "--create-user", "another"},
		{"--install-user", "developer", "--create-service-account"},
		{"--choose-account", "--create-service-account"},
		{"--apply-release", "--install-user", "developer"},
		{"--onboarding", "--create-user", "developer"},
		{"--desktop"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
