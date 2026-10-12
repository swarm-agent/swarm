package claudecode

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Purpose: the Claude Code provider may only run the person's own signed-in CLI
// and must never forward Claude credentials (Anthropic credential-use terms).
// Threat: daemon-level ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN /
// ANTHROPIC_AUTH_TOKEN leaking into the child would route Swarm work through a
// token Swarm handed over. Owner: CLI.Env (passthroughEnvKeys). A unit test on
// the env builder is the narrowest layer: every CLI child gets exactly this env.
func TestCLIEnvPassesOnlyAllowlistedVariables(t *testing.T) {
	values := map[string]string{
		"PATH":                    "/usr/bin",
		"HOME":                    "/home/swarm",
		"CLAUDE_CONFIG_DIR":       "/var/lib/swarmd/claude",
		"ANTHROPIC_API_KEY":       "sk-should-not-pass",
		"ANTHROPIC_AUTH_TOKEN":    "token-should-not-pass",
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth-should-not-pass",
		"SWARM_PRIVATE":           "private",
	}
	cli := &CLI{lookupEnv: func(key string) (string, bool) { value, ok := values[key]; return value, ok }}
	env := cli.Env("MAX_THINKING_TOKENS=0")
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/swarm", "CLAUDE_CONFIG_DIR=/var/lib/swarmd/claude", "MAX_THINKING_TOKENS=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env missing %q: %v", want, env)
		}
	}
	for _, forbidden := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "SWARM_PRIVATE", "should-not-pass"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("env leaked %q: %v", forbidden, env)
		}
	}
}

// Purpose: readiness must reflect the real local CLI state: installed, at least
// the version that supports --restricted, and signed in. Threat: reporting a
// logged-out, missing, or too-old CLI as ready makes turns fail late or run
// without the settings isolation the runner depends on. Owner: CLI.probe /
// parseAuthStatus. A fake command runner isolates the decision from the host.
func TestCLIStatusRequiresInstalledRecentSignedInCLI(t *testing.T) {
	signedIn := `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty"}`
	signedOut := `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`
	cases := []struct {
		name      string
		lookPath  error
		version   string
		auth      string
		authErr   error
		wantReady bool
		wantText  string
	}{
		{name: "missing binary", lookPath: errors.New("not found"), wantText: "not found on the daemon PATH"},
		{name: "too old", version: "2.1.100 (Claude Code)", auth: signedIn, wantText: "too old"},
		{name: "signed out", version: "2.1.296 (Claude Code)", auth: signedOut, authErr: errors.New("exit status 1"), wantText: "not signed in"},
		{name: "unreadable auth", version: "2.1.296 (Claude Code)", auth: "", authErr: errors.New("exit status 2"), wantText: "auth status check failed"},
		{name: "signed in", version: "2.1.296 (Claude Code)", auth: signedIn, wantReady: true, wantText: "Claude account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := &CLI{
				lookupEnv: func(string) (string, bool) { return "", false },
				lookPath: func(name string) (string, error) {
					if tc.lookPath != nil {
						return "", tc.lookPath
					}
					return "/fake/" + name, nil
				},
				run: func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
					switch strings.Join(args, " ") {
					case "--version":
						return []byte(tc.version), nil
					case "auth status":
						return []byte(tc.auth), tc.authErr
					}
					t.Fatalf("unexpected CLI invocation %v", args)
					return nil, nil
				},
				now: time.Now,
			}
			status := cli.Status(context.Background())
			if status.Ready != tc.wantReady {
				t.Fatalf("ready = %v, want %v (reason %q)", status.Ready, tc.wantReady, status.Reason)
			}
			if !strings.Contains(status.Reason, tc.wantText) {
				t.Fatalf("reason %q does not contain %q", status.Reason, tc.wantText)
			}
		})
	}
}
