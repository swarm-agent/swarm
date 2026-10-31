package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/provider/claudecode"
	"swarm/packages/swarmd/internal/provider/registry"
)

type fakeClaudeAccounts struct {
	fakeClaudeCodeUsage
	accounts []claudecode.SlotStatus
}

func (f fakeClaudeAccounts) AccountStatuses(context.Context) ([]claudecode.SlotStatus, error) {
	return f.accounts, nil
}
func (f fakeClaudeAccounts) AddAccount() (string, string, error) {
	return "new-slot", "manual login command", nil
}
func (f fakeClaudeAccounts) OrderAccounts([]string) error { return nil }

// Purpose: both manage_setup and Swarm Control must expose the remote machine's
// account order, identity and observed limits, never accidentally substitute the
// home's accounts. The authenticated linked catalog harness is the narrowest
// existing end-to-end API layer that exercises both fleet surfaces and routing.
func TestClaudeCodeSlotsReachLinkedProviderAndUsageSurfaces(t *testing.T) {
	h := newCatalogParityHarness(t)
	h.seedSetup(t)
	h.remote.s.providers = registry.New(claudecode.NewAdapter(nil, nil))
	until := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)
	h.remote.s.SetClaudeCodeUsageClient(fakeClaudeAccounts{accounts: []claudecode.SlotStatus{
		{ID: "remote-a", Order: 1, Ready: true, Email: "a@example.com", Plan: "max", Organization: "Remote Org", LimitState: "exhausted", LimitUntil: &until},
		{ID: "remote-b", Order: 2, Ready: true, Email: "b@example.com", Plan: "pro", LimitState: "ok"},
	}})
	h.home.s.SetClaudeCodeUsageClient(fakeClaudeAccounts{accounts: []claudecode.SlotStatus{{ID: "home", Order: 1, Ready: true, LimitState: "ok"}}})
	control, orchestrator := setupSurfaces(t, h)
	for name, run := range map[string]func(map[string]any) (map[string]any, string){"control": control, "orchestrator": orchestrator} {
		remote, refused := run(map[string]any{"action": "plan_usage", "machine": "box"})
		if refused != "" {
			t.Fatalf("%s: %s", name, refused)
		}
		plan := controlMCPMap(remote["claude_code"])
		accounts, ok := plan["accounts"].([]any)
		if !ok || len(accounts) != 2 {
			t.Fatalf("%s remote accounts=%v", name, plan)
		}
		a, b := controlMCPMap(accounts[0]), controlMCPMap(accounts[1])
		if a["id"] != "remote-a" || a["account_email"] != "a@example.com" || a["limit_state"] != "exhausted" || a["limit_until"] != until.Format(time.RFC3339) || b["id"] != "remote-b" || b["limit_state"] != "ok" {
			t.Fatalf("%s accounts=%v", name, accounts)
		}
		if plan["limit_summary"] != "claudecode: 1 of 2 accounts in a limit window, next free at 13:00 UTC" {
			t.Fatalf("summary=%v", plan)
		}
		providers, refused := run(map[string]any{"action": "providers", "machine": "box"})
		if refused != "" {
			t.Fatalf("%s providers: %s", name, refused)
		}
		var providerAccounts []any
		for _, raw := range controlMCPList(providers["providers"]) {
			entry := controlMCPMap(raw)
			if entry["provider"] == "claudecode" {
				providerAccounts = controlMCPList(entry["accounts"])
			}
		}
		if len(providerAccounts) != 2 || controlMCPMap(providerAccounts[0])["id"] != "remote-a" || controlMCPMap(providerAccounts[1])["id"] != "remote-b" {
			t.Fatalf("%s providers=%v", name, providers)
		}
		local, refused := run(map[string]any{"action": "plan_usage"})
		if refused != "" {
			t.Fatal(refused)
		}
		localAccounts := controlMCPMap(local["claude_code"])["accounts"].([]any)
		if len(localAccounts) != 1 || controlMCPMap(localAccounts[0])["id"] != "home" {
			t.Fatalf("home=%v", local)
		}
	}
}

// Purpose: machine-account mutations require the existing admin scope, while
// read-scoped principals can observe but cannot change account order. Exercise
// the registered ownerOnlyScope route with a real store and check both rejection
// and unchanged routing metadata; direct handler tests cannot prove that gate.
func TestClaudeCodeAccountMutationRequiresAdmin(t *testing.T) {
	f := newClaudeCodeFixture(t)
	cli := f.server.claudeCodeUsage.(*claudecode.CLI)
	cli.ConfigureSlots(f.authStore, t.TempDir())
	id := strings.Repeat("a", 32)
	if err := f.authStore.AddClaudeCodeSlot(id); err != nil {
		t.Fatal(err)
	}
	for _, scopes := range [][]string{{"sessions:read"}, {"admin"}} {
		req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/claudecode/accounts", strings.NewReader(`{"order":["`+id+`","default"]}`))), &pebblestore.ScopedTokenRecord{Scopes: scopes})
		rec := httptest.NewRecorder()
		f.server.internalRouteHandler().ServeHTTP(rec, req)
		slots, err := f.authStore.ClaudeCodeSlots()
		if err != nil {
			t.Fatal(err)
		}
		if scopes[0] == "sessions:read" {
			if rec.Code != http.StatusForbidden || slots[0].ID != "default" {
				t.Fatalf("unauthorized code=%d slots=%v", rec.Code, slots)
			}
		} else if rec.Code != http.StatusOK || slots[0].ID != id {
			t.Fatalf("admin code=%d body=%s slots=%v", rec.Code, rec.Body.String(), slots)
		}
	}
}
