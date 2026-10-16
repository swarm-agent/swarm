package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Land must expose push refusals from both command streams without
// credentials, while preserving strict receipts. Executing a local failing
// command tests the actual delivery error boundary without a forge or network.
func TestLandFailureDetails(t *testing.T) {
	old := allowOwner
	allowOwner = os.Getuid()
	t.Cleanup(func() { allowOwner = old })
	for _, tc := range []struct{ name, output, want string }{
		{"push", "echo 'stdout rejection ghp_fixtureToken'\necho 'remote: protected branch rejected https://user:pass@example.test/repo x-access-token:opaque-token' >&2", "remote: protected branch rejected"},
		{"receipt", "echo 'APP_RUNS_FAILED:health'\necho 'Authorization: Bearer hidden' >&2", "app-runs landing check failed: health"},
		{"moved", "echo MOVED", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "land")
			exit := "\nexit 1\n"
			if tc.name == "moved" {
				exit = "\n"
			}
			if err := os.WriteFile(script, []byte("#!/bin/sh\n"+tc.output+exit), 0700); err != nil {
				t.Fatal(err)
			}
			result, err := Land(context.Background(), Rule{Mode: "land", Command: script, Folder: dir}, Request{Head: strings.Repeat("a", 40), BaseCommit: strings.Repeat("b", 40), BaseBranch: "dev"})
			if tc.name == "moved" {
				if err != nil || !result.Moved {
					t.Fatalf("%+v %v", result, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wanted %q, got %v", tc.want, err)
			}
			if tc.name == "receipt" && err.Error() != tc.want {
				t.Fatal(err)
			}
			if tc.name == "push" && !strings.Contains(err.Error(), "stdout rejection [REDACTED]") {
				t.Fatal(err)
			}
			for _, secret := range []string{"ghp_fixtureToken", "user:pass", "opaque-token", "hidden"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("credential leaked: %v", err)
				}
			}
		})
	}
}

// Purpose: the diagnostic redactor must remove every supported credential form
// and exact sensitive environment value before truncation. A direct unit test
// isolates that security boundary, including overlapping and empty env values.
func TestLandRedactionAndTail(t *testing.T) {
	env := []string{"API_TOKEN=env-token", "CLIENT_SECRET=env-secret", "PRIVATE_KEY=env-key", "db_password=env-password", "EMPTY_TOKEN=", "LONG_TOKEN=env-token-long", "NORMAL=visible"}
	input := "Authorization: Bearer header-secret\nx-access-token:opaque-secret\nghp_example github_pat_example gho_example\nhttps://name:password@example.test/repo\nenv-token-long env-token env-secret env-key env-password visible"
	redacted := redactLandOutput(input, env)
	for _, secret := range []string{"header-secret", "opaque-secret", "ghp_example", "github_pat_example", "gho_example", "name:password", "env-token", "env-secret", "env-key", "env-password", "-long"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("leaked %q in %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "visible") || !strings.Contains(redacted, "https://[REDACTED]@example.test/repo") {
		t.Fatal(redacted)
	}
	details := landFailure("discard-me\n" + strings.Repeat("context\n", 11) + "rejection").Error()
	if strings.Contains(details, "discard-me") || len(strings.Split(details, "\n")) != 12 || !strings.HasSuffix(details, "rejection") {
		t.Fatal(details)
	}
	details = landFailure("LAND_REFUSED: protected branch\nother detail").Error()
	if details != "land command failed: LAND_REFUSED: protected branch\nother detail" {
		t.Fatal(details)
	}
}

// Purpose: command must redact values from the environment actually passed to
// the child, including values without token-like syntax. This process test
// guards the environment-to-diagnostic wiring, not just the matching helper.
func TestLandCommandEnvironmentRedaction(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "land")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'remote: rejected %s\\n' \"$DEPLOY_TOKEN\" >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := command(context.Background(), Rule{Command: script, Folder: dir}, dir, landTimeout(), "land", "DEPLOY_TOKEN=opaque-fixture-credential")
	if err == nil || output != "remote: rejected [REDACTED]" || strings.Contains(err.Error(), "opaque-fixture-credential") {
		t.Fatalf("output=%q err=%v", output, err)
	}
}
