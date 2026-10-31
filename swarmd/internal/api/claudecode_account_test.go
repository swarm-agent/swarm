package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/provider/claudecode"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose:
// Requirement: an AI managing several Swarm machines can tell which Claude
// account each machine's Claude Code is signed into. GET /v1/claudecode/usage
// reports the account's email, organization name and their uuids; the
// providers and plan_usage setup actions (swarm_list_providers,
// swarm_get_plan_usage, manage_setup) carry them as account_email,
// account_uuid, organization_name and organization_uuid, and a linked home
// gets the linked machine's own account back unchanged.
// Threats: a token, key or other config secret reaching an answer; the home
// reporting its own account (or none) for a linked machine; a signed-out CLI
// still naming an account.
// Owners: handleClaudeCodeUsage, controlMCPPlanUsage, controlMCPListProviders
// (controlMCPAddClaudeCodeAccount), ManageSetup and its linked route.
// Layer: the real CLI probe, adapter, auth service and handlers on a
// temporary store with a stub `claude` binary and a fixture .claude.json in
// CLAUDE_CONFIG_DIR (the narrowest layer that runs config to answer); the
// linked case uses the catalog parity harness's home and linked machine.

// claudeCodeReadKey reads setup: swarm_list_providers and swarm_get_plan_usage.
var claudeCodeReadKey = []string{"swarm:read", "sessions:read"}

const accountFixtureConfig = `{
  "primaryApiKey": "fixture-primary-api-key-value",
  "oauthAccount": {
    "accountUuid": "00000000-0000-4000-8000-00000000000a",
    "emailAddress": "person@example.com",
    "organizationUuid": "00000000-0000-4000-8000-00000000000b",
    "organizationName": "Example Org",
    "accessToken": "fixture-access-token-value",
    "refreshToken": "fixture-refresh-token-value"
  }
}`

var accountFixtureSecrets = []string{"fixture-primary-api-key-value", "fixture-access-token-value", "fixture-refresh-token-value", "accessToken", "refreshToken", "primaryApiKey"}

func assertAccountFields(t *testing.T, where string, got map[string]any) {
	t.Helper()
	want := map[string]string{
		"account_email":     "person@example.com",
		"account_uuid":      "00000000-0000-4000-8000-00000000000a",
		"organization_name": "Example Org",
		"organization_uuid": "00000000-0000-4000-8000-00000000000b",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s %s = %v, want %q: %v", where, key, got[key], value, got)
		}
	}
}

func assertNoAccountSecrets(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range accountFixtureSecrets {
		if strings.Contains(text, secret) {
			t.Fatalf("%s leaks %q: %s", where, secret, text)
		}
	}
}

func TestClaudeCodeAccountInUsageProvidersAndPlanUsage(t *testing.T) {
	f := newClaudeCodeFixture(t)
	if err := os.WriteFile(filepath.Join(f.configDir, ".claude.json"), []byte(accountFixtureConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	f.installFakeCLI(t, "2.1.300")
	f.signIn(t)
	if _, text, isError := f.call(t, claudeCodeWriteKey, "activate"); isError {
		t.Fatalf("activate = %s", text)
	}

	// The route.
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/claudecode/usage", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}})
	rec := httptest.NewRecorder()
	f.server.internalRouteHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage = %d %s", rec.Code, rec.Body.String())
	}
	assertNoAccountSecrets(t, "usage route", rec.Body.String())
	var usage struct {
		CLI map[string]any `json:"cli"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	assertAccountFields(t, "usage route cli", usage.CLI)

	// providers.
	out, text, isError := f.callTool(t, claudeCodeReadKey, "swarm_list_providers", map[string]any{"provider": claudecode.ProviderID})
	if isError {
		t.Fatalf("list providers = %s", text)
	}
	assertNoAccountSecrets(t, "providers", text)
	providers := controlMCPList(out["providers"])
	if len(providers) != 1 {
		t.Fatalf("providers = %s", text)
	}
	entry := controlMCPMap(providers[0])
	if entry["ready"] != true {
		t.Fatalf("claudecode not ready: %s", text)
	}
	assertAccountFields(t, "providers claudecode", entry)

	// plan_usage.
	out, text, isError = f.callTool(t, claudeCodeReadKey, "swarm_get_plan_usage", map[string]any{})
	if isError {
		t.Fatalf("plan usage = %s", text)
	}
	assertNoAccountSecrets(t, "plan_usage", text)
	plan := controlMCPMap(out["claude_code"])
	if plan["connected"] != true || plan["subscription_type"] != "max" {
		t.Fatalf("claude_code = %s", text)
	}
	assertAccountFields(t, "plan_usage claude_code", plan)
}

func TestClaudeCodeAccountOmittedWhenSignedOut(t *testing.T) {
	f := newClaudeCodeFixture(t)
	if err := os.WriteFile(filepath.Join(f.configDir, ".claude.json"), []byte(accountFixtureConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	f.installFakeCLI(t, "2.1.300")
	out, text, isError := f.callTool(t, claudeCodeReadKey, "swarm_get_plan_usage", map[string]any{})
	if isError {
		t.Fatalf("plan usage = %s", text)
	}
	plan := controlMCPMap(out["claude_code"])
	if plan["connected"] != false {
		t.Fatalf("claude_code = %s", text)
	}
	for _, key := range controlMCPClaudeCodeAccountFields {
		if _, ok := plan[key]; ok {
			t.Fatalf("signed-out claude_code names %s: %s", key, text)
		}
	}
	assertNoAccountSecrets(t, "signed-out plan_usage", text)
}

func TestClaudeCodeAccountReachesALinkedHome(t *testing.T) {
	h := newCatalogParityHarness(t)
	h.seedSetup(t)
	h.remote.s.SetClaudeCodeUsageClient(fakeClaudeCodeUsage{account: claudecode.Account{
		EmailAddress: "person@example.com", OrganizationName: "Example Org",
		OrganizationUUID: "00000000-0000-4000-8000-00000000000b", AccountUUID: "00000000-0000-4000-8000-00000000000a",
	}})
	h.home.s.SetClaudeCodeUsageClient(fakeClaudeCodeUsage{account: claudecode.Account{EmailAddress: "home@example.com", OrganizationName: "Home Org"}})
	control, orchestrator := setupSurfaces(t, h)
	for name, run := range map[string]func(map[string]any) (map[string]any, string){"Swarm Control": control, "Orchestrator": orchestrator} {
		linked, refused := run(map[string]any{"action": "plan_usage", "machine": "box"})
		if refused != "" {
			t.Fatalf("%s linked plan_usage: %s", name, refused)
		}
		assertAccountFields(t, name+" linked claude_code", controlMCPMap(linked["claude_code"]))
		local, refused := run(map[string]any{"action": "plan_usage"})
		if refused != "" {
			t.Fatalf("%s plan_usage: %s", name, refused)
		}
		if got := controlMCPMap(local["claude_code"]); got["account_email"] != "home@example.com" || got["organization_name"] != "Home Org" {
			t.Fatalf("%s home claude_code = %v", name, got)
		}
	}
}
