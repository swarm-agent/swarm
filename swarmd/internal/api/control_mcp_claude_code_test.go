package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/claudecode"
	"swarm/packages/swarmd/internal/provider/registry"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a person's AI sets up the Claude Code provider through Swarm
// Control alone. swarm_connect_claude_code status reports whether the CLI is
// installed for the daemon's user and signed in (with the installer command
// when it is not), and activate makes the claudecode provider active for the
// caller's account with the same effect as the setup page's "Save and make
// active" (POST /v1/auth/credentials, type cli, active), only under the
// provider-changing authority swarm_connect_chatgpt uses.
// Threats: a wrong state sending the AI down the wrong path; activation that
// differs from the setup page (a second authority) or that succeeds while the
// CLI cannot run; a read key or a token without admin changing the provider.
// Owners: controlMCPConnectClaudeCode, handleAuthCredentials, the claudecode
// Adapter/CLI and remote.ToolScope. The real CLI probe, adapter, auth service
// and credential handler run against a temporary store; only the `claude`
// binary is a stub script, which is the narrowest layer that proves the
// probe-to-activation path without Anthropic's service.

type claudeCodeFixture struct {
	server    *Server
	authStore *pebblestore.AuthStore
	stateDir  string
}

func newClaudeCodeFixture(t *testing.T) *claudeCodeFixture {
	t.Helper()
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	store := sessions.Store().Underlying()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	authStore := pebblestore.NewAuthStore(store)
	server.auth = auth.NewService(authStore, events)
	cli := claudecode.NewCLI()
	server.providers = registry.New(claudecode.NewAdapter(authStore, cli))
	server.SetClaudeCodeUsageClient(cli)
	// Startup supplies this store for the onboarding defaults write.
	server.agentModelSettingsStore = pebblestore.NewAgentModelSettingsStore(store)
	return &claudeCodeFixture{server: server, authStore: authStore, stateDir: t.TempDir()}
}

// installFakeCLI writes a stub `claude` whose sign-in state is a file, and
// points the daemon at it as the installer does (SWARM_CLAUDE_CODE_CLI_PATH).
func (f *claudeCodeFixture) installFakeCLI(t *testing.T, version string) {
	t.Helper()
	signedIn := filepath.Join(f.stateDir, "signed-in")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  --version) echo '" + version + " (Claude Code)' ;;\n" +
		"  'auth status') if [ -f '" + signedIn + "' ]; then echo '{\"loggedIn\": true, \"authMethod\": \"claude.ai\", \"subscriptionType\": \"max\"}'; else echo '{\"loggedIn\": false, \"authMethod\": \"none\"}'; exit 1; fi ;;\n" +
		"  *) exit 2 ;;\n" +
		"esac\n"
	path := filepath.Join(f.stateDir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudecode.CLIPathEnv, path)
}

func (f *claudeCodeFixture) signIn(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.stateDir, "signed-in"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *claudeCodeFixture) call(t *testing.T, scopes []string, action string) (map[string]any, string, bool) {
	t.Helper()
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopes})
	c := &controlMCPCall{request: req, next: f.server.internalRouteHandler(), server: f.server}
	raw, _ := json.Marshal(map[string]any{"name": "swarm_connect_claude_code", "arguments": map[string]any{"action": action}})
	result, rpcErr := c.callTool(raw)
	if rpcErr != nil {
		t.Fatalf("tools/call: %v", rpcErr)
	}
	content, _ := result["content"].([]map[string]any)
	text := ""
	if len(content) > 0 {
		text, _ = content[0]["text"].(string)
	}
	isError, _ := result["isError"].(bool)
	out := map[string]any{}
	if !isError {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("result %q: %v", text, err)
		}
	}
	return out, text, isError
}

// records is the account's Claude Code credentials as the store holds them.
func (f *claudeCodeFixture) records(t *testing.T) ([]pebblestore.AuthCredentialRecord, string) {
	t.Helper()
	account := testPrincipal().AccountScopeID
	list, err := f.authStore.ListCredentialsForAccount(account, claudecode.ProviderID, 50)
	if err != nil {
		t.Fatal(err)
	}
	active, ok, err := f.authStore.GetActiveCredentialForAccount(account, claudecode.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return list, ""
	}
	return list, active.ID
}

func (f *claudeCodeFixture) providerReady(t *testing.T) bool {
	t.Helper()
	adapter, _ := f.server.providers.Get(claudecode.ProviderID)
	ctx := identity.ContextWithPrincipal(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/", nil)).Context(), testPrincipal())
	status, err := adapter.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return status.Ready
}

var claudeCodeWriteKey = []string{"swarm:write", "sessions:read", "sessions:write"}

func TestControlMCPConnectClaudeCodeReportsCLIState(t *testing.T) {
	f := newClaudeCodeFixture(t)

	// Not installed: the configured CLI path does not exist.
	t.Setenv(claudecode.CLIPathEnv, filepath.Join(f.stateDir, "missing", "claude"))
	out, text, isError := f.call(t, claudeCodeWriteKey, "status")
	if isError || out["state"] != "not_installed" || out["cli_installed"] != false || out["signed_in"] != false || out["provider_active"] != false {
		t.Fatalf("not installed status = %s", text)
	}
	if out["run_on_the_machine"] != controlMCPClaudeCodeSetupCommand || !strings.Contains(controlMCPClaudeCodeSetupCommand, "install.sh claude-code") {
		t.Fatalf("not installed status lacks the installer command: %s", text)
	}

	// Installed, signed out.
	f.installFakeCLI(t, "2.1.300")
	out, text, isError = f.call(t, claudeCodeWriteKey, "status")
	if isError || out["state"] != "signed_out" || out["cli_installed"] != true || out["cli_version"] != "2.1.300" || out["signed_in"] != false {
		t.Fatalf("signed out status = %s", text)
	}
	if out["run_on_the_machine"] != controlMCPClaudeCodeSetupCommand {
		t.Fatalf("signed out status lacks the installer command: %s", text)
	}

	// Too old for Swarm.
	f.installFakeCLI(t, "2.0.1")
	f.signIn(t)
	out, text, _ = f.call(t, claudeCodeWriteKey, "status")
	if out["state"] != "needs_update" || out["signed_in"] != false {
		t.Fatalf("old CLI status = %s", text)
	}

	// Signed in (the sign-in just happened: no cached state hides it).
	f.installFakeCLI(t, "2.1.300")
	out, text, isError = f.call(t, claudeCodeWriteKey, "status")
	if isError || out["state"] != "signed_in" || out["signed_in"] != true || out["plan"] != "max" || out["provider_active"] != false {
		t.Fatalf("signed in status = %s", text)
	}
	if _, ok := out["run_on_the_machine"]; ok {
		t.Fatalf("signed in status still asks for the installer: %s", text)
	}
	if list, _ := f.records(t); len(list) != 0 {
		t.Fatalf("status stored credentials: %+v", list)
	}
}

func TestControlMCPConnectClaudeCodeActivateMatchesSetupPage(t *testing.T) {
	// Activation before sign-in fails and stores nothing.
	f := newClaudeCodeFixture(t)
	f.installFakeCLI(t, "2.1.300")
	if _, text, isError := f.call(t, claudeCodeWriteKey, "activate"); !isError || !strings.Contains(text, "install.sh claude-code") {
		t.Fatalf("activate while signed out = %v %s", isError, text)
	}
	if list, active := f.records(t); len(list) != 0 || active != "" {
		t.Fatalf("failed activation stored %+v (active %q)", list, active)
	}

	// Activation once signed in.
	f.signIn(t)
	out, text, isError := f.call(t, claudeCodeWriteKey, "activate")
	if isError || out["provider_active"] != true || out["state"] != "signed_in" {
		t.Fatalf("activate = %s", text)
	}
	viaTool, activeID := f.records(t)
	if len(viaTool) != 1 || activeID != viaTool[0].ID || viaTool[0].Type != pebblestore.AuthTypeCLI ||
		viaTool[0].Connection == nil || !viaTool[0].Connection.Connected || viaTool[0].APIKey != "" || viaTool[0].AccessToken != "" {
		t.Fatalf("activated records = %+v (active %q)", viaTool, activeID)
	}
	if !f.providerReady(t) {
		t.Fatal("claudecode provider is not ready after activate")
	}
	// Again: the same record stays active; nothing is added.
	if _, text, isError := f.call(t, claudeCodeWriteKey, "activate"); isError {
		t.Fatalf("second activate = %s", text)
	}
	if again, id := f.records(t); len(again) != 1 || id != activeID {
		t.Fatalf("second activate changed records: %+v (active %q)", again, id)
	}
	if out, text, _ := f.call(t, claudeCodeWriteKey, "status"); out["provider_active"] != true {
		t.Fatalf("status after activate = %s", text)
	}

	// The setup page's Save and make active, on another fresh account store.
	page := newClaudeCodeFixture(t)
	page.installFakeCLI(t, "2.1.300")
	page.signIn(t)
	body, _ := json.Marshal(map[string]any{"provider": "claudecode", "type": "cli", "label": "", "active": true})
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/auth/credentials", bytes.NewReader(body))), &pebblestore.ScopedTokenRecord{Scopes: []string{"admin"}})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	page.server.internalRouteHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup page save = %d %s", rec.Code, rec.Body.String())
	}
	viaPage, pageActive := page.records(t)
	if len(viaPage) != 1 || pageActive != viaPage[0].ID || !page.providerReady(t) {
		t.Fatalf("setup page records = %+v (active %q)", viaPage, pageActive)
	}
	a, b := viaTool[0], viaPage[0]
	if a.Provider != b.Provider || a.Type != b.Type || a.Label == "" || b.Label == "" || a.AccountScopeID != b.AccountScopeID ||
		a.Connection.Connected != b.Connection.Connected || a.Connection.Method != b.Connection.Method || a.Connection.Message != b.Connection.Message ||
		!sameStrings(a.Tags, b.Tags) || a.Managed != b.Managed {
		t.Fatalf("tool and setup page differ:\n tool %+v %+v\n page %+v %+v", a, *a.Connection, b, *b.Connection)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestControlMCPConnectClaudeCodeRefusesWithoutProviderScope(t *testing.T) {
	f := newClaudeCodeFixture(t)
	f.installFakeCLI(t, "2.1.300")
	f.signIn(t)
	for name, scopes := range map[string][]string{
		"read key":                  {"swarm:read", "sessions:read"},
		"token without admin":       {"sessions:read", "sessions:write"},
		"approve key without write": {"swarm:approve", "sessions:read", "sessions:write"},
	} {
		for _, action := range []string{"status", "activate"} {
			if _, text, isError := f.call(t, scopes, action); !isError {
				t.Fatalf("%s %s was allowed: %s", name, action, text)
			}
		}
	}
	if list, active := f.records(t); len(list) != 0 || active != "" {
		t.Fatalf("refused calls stored %+v (active %q)", list, active)
	}
	// A token without levels but with admin, as the credential routes take.
	if out, text, isError := f.call(t, []string{"admin"}, "activate"); isError || out["provider_active"] != true {
		t.Fatalf("admin token activate = %s", text)
	}
}
