package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose:
// Requirement: an AI managing several machines can tell which Claude account
// each machine's Claude Code is signed into. CLI.Status reports the signed-in
// account's email, organization name and their uuids from the oauthAccount of
// Claude Code's global config (.claude.json in CLAUDE_CONFIG_DIR, otherwise
// HOME: the environment Swarm runs the CLI with), and only when the CLI is
// ready with a Claude account sign-in.
// Threats: a token, key or other config secret reaching the status (and from
// there the API, MCP or a linked home); a stale oauthAccount reported for a
// non-account sign-in; a missing or broken config turning into an error.
// Owners: CLI.probe, CLI.readAccount, parseAccount (account.go).
// Layer: the real probe with a stubbed command runner and a temporary config
// directory; the narrowest layer that covers path choice, gating and parsing
// without the real Claude Code binary or ambient HOME.

// fixtureTokens are fake secret values placed in the fixture config; none may
// appear anywhere in the reported status.
var fixtureTokens = []string{
	"sk-ant-oat01-FIXTUREACCESSTOKEN",
	"sk-ant-ort01-FIXTUREREFRESHTOKEN",
	"sk-ant-api03-FIXTUREPRIMARYAPIKEY",
	"fixture-custom-api-key-response",
}

const fixtureConfig = `{
  "numStartups": 12,
  "primaryApiKey": "sk-ant-api03-FIXTUREPRIMARYAPIKEY",
  "customApiKeyResponses": {"approved": ["fixture-custom-api-key-response"]},
  "oauthAccount": {
    "accountUuid": "00000000-0000-4000-8000-00000000000a",
    "emailAddress": "person@example.com",
    "organizationUuid": "00000000-0000-4000-8000-00000000000b",
    "organizationName": "Example Org",
    "organizationRole": "admin",
    "billingType": "stripe_subscription",
    "accessToken": "sk-ant-oat01-FIXTUREACCESSTOKEN",
    "refreshToken": "sk-ant-ort01-FIXTUREREFRESHTOKEN"
  },
  "projects": {"/workspace/example": {"allowedTools": []}}
}`

func writeFixtureConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, globalConfigName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixtureCLI is a CLI whose binary answers --version and auth status with
// authJSON, under exactly env.
func fixtureCLI(env map[string]string, authJSON string) *CLI {
	return &CLI{
		lookupEnv: func(key string) (string, bool) { value, ok := env[key]; return value, ok },
		lookPath:  func(string) (string, error) { return "/fixture/bin/claude", nil },
		run: func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "--version":
				return []byte("2.1.300 (Claude Code)"), nil
			case "auth status":
				return []byte(authJSON), nil
			}
			return nil, errors.New("unexpected command")
		},
		now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	}
}

const signedInClaudeAI = `{"loggedIn": true, "authMethod": "claude.ai", "subscriptionType": "team"}`

func assertNoFixtureTokens(t *testing.T, status CLIStatus) {
	t.Helper()
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range fixtureTokens {
		if strings.Contains(string(raw), token) {
			t.Fatalf("status leaks %q: %s", token, raw)
		}
	}
	for _, field := range []string{"accessToken", "refreshToken", "primaryApiKey", "billingType"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("status carries config field %q: %s", field, raw)
		}
	}
}

func TestStatusReportsSignedInAccountWithoutSecrets(t *testing.T) {
	home := t.TempDir()
	writeFixtureConfig(t, home, fixtureConfig)
	status := fixtureCLI(map[string]string{"HOME": home}, signedInClaudeAI).Status(context.Background())
	if !status.Ready {
		t.Fatalf("not ready: %+v", status)
	}
	want := Account{
		EmailAddress:     "person@example.com",
		OrganizationName: "Example Org",
		OrganizationUUID: "00000000-0000-4000-8000-00000000000b",
		AccountUUID:      "00000000-0000-4000-8000-00000000000a",
	}
	if status.Account != want {
		t.Fatalf("account = %+v, want %+v", status.Account, want)
	}
	assertNoFixtureTokens(t, status)
}

func TestStatusAccountPrefersClaudeConfigDir(t *testing.T) {
	home, configDir := t.TempDir(), t.TempDir()
	writeFixtureConfig(t, home, `{"oauthAccount": {"emailAddress": "home@example.com"}}`)
	writeFixtureConfig(t, configDir, fixtureConfig)
	status := fixtureCLI(map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": configDir}, signedInClaudeAI).Status(context.Background())
	if status.Account.EmailAddress != "person@example.com" || status.Account.OrganizationName != "Example Org" {
		t.Fatalf("account = %+v, want the CLAUDE_CONFIG_DIR config's", status.Account)
	}
	assertNoFixtureTokens(t, status)
}

func TestStatusAccountOmittedWhenAbsentOrNotAnAccountSignIn(t *testing.T) {
	cases := map[string]struct {
		config string // "" writes no config file
		auth   string
		ready  bool
	}{
		"no config file":          {auth: signedInClaudeAI, ready: true},
		"no oauthAccount":         {config: `{"numStartups": 1, "primaryApiKey": "sk-ant-api03-FIXTUREPRIMARYAPIKEY"}`, auth: signedInClaudeAI, ready: true},
		"unreadable config":       {config: `{"oauthAccount": {`, auth: signedInClaudeAI, ready: true},
		"partial account":         {config: `{"oauthAccount": {"organizationName": "Example Org"}}`, auth: signedInClaudeAI, ready: true},
		"api key sign-in":         {config: fixtureConfig, auth: `{"loggedIn": true, "authMethod": "api_key"}`, ready: true},
		"signed out":              {config: fixtureConfig, auth: `{"loggedIn": false, "authMethod": "none"}`},
		"long-lived token method": {config: fixtureConfig, auth: `{"loggedIn": true, "authMethod": "oauth_token"}`, ready: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if tc.config != "" {
				writeFixtureConfig(t, home, tc.config)
			}
			status := fixtureCLI(map[string]string{"HOME": home}, tc.auth).Status(context.Background())
			if status.Ready != tc.ready {
				t.Fatalf("ready = %v, want %v: %+v", status.Ready, tc.ready, status)
			}
			want := Account{}
			if name == "partial account" {
				want = Account{OrganizationName: "Example Org"}
			}
			if status.Account != want {
				t.Fatalf("account = %+v, want %+v", status.Account, want)
			}
			assertNoFixtureTokens(t, status)
		})
	}
}

func TestStatusAccountNeedsAConfigDirectory(t *testing.T) {
	status := fixtureCLI(map[string]string{}, signedInClaudeAI).Status(context.Background())
	if !status.Ready || status.Account != (Account{}) {
		t.Fatalf("status = %+v", status)
	}
}
