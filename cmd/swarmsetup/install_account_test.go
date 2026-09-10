package main

import (
	"bytes"
	"strings"
	"testing"
)

// Requirement: the setup terminal must require explicit creation consent and
// treat cancellation/EOF as a stop, never silently selecting a fallback account.
// The prompt layer handles only names; passwords remain in the OS subprocess.
func TestInstallationAccountPrompt(t *testing.T) {
	for _, tc := range []struct {
		input, existing, created string
		service, ok              bool
	}{
		{"1\n", "", "", false, true},
		{"2\ndeveloper\n", "developer", "", false, true},
		{"3\ndeveloper\ny\n", "", "developer", false, true},
		{"3\ndeveloper\nn\n", "", "", false, false},
		{"3\ndeveloper\n", "", "", false, false},
		{"4\n", "", "", true, true},
		{"5\n", "", "", false, false},
		{"", "", "", false, false},
		{"2\n\n", "", "", false, false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var output bytes.Buffer
			existing, created, service, err := readInstallationAccountChoice(strings.NewReader(tc.input), &output)
			if (err == nil) != tc.ok || existing != tc.existing || created != tc.created || service != tc.service {
				t.Fatalf("got %q %q %v err=%v", existing, created, service, err)
			}
		})
	}
}

// Requirement: conflicting identity modes and release-update identity injection
// are rejected before preflight/account/path mutation. No real install is run.
func TestInstallationAccountCLIRejectsConflicts(t *testing.T) {
	for _, args := range [][]string{
		{"--install-user"}, {"--create-user", "--service"},
		{"--install-user", "developer", "--create-user", "another"},
		{"--install-user", "developer", "--create-service-account"},
		{"--choose-account", "--create-service-account"},
		{"--apply-release", "--install-user", "developer"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
