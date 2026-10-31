package claudecode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func accountFixture(t *testing.T) (*Runner, context.Context, *time.Time, string, string) {
	t.Helper()
	root := t.TempDir()
	db, err := pebblestore.Open(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	auth := pebblestore.NewAuthStore(db)
	if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{AccountScopeID: "account-a", Provider: ProviderID, Type: "cli", SetActive: true}); err != nil {
		t.Fatal(err)
	}
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, id := range []string{a, b} {
		if err := auth.AddClaudeCodeSlot(id); err != nil {
			t.Fatal(err)
		}
	}
	// Keep the migrated login in a known window; these tests dispatch only A/B.
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if err := auth.RecordClaudeCodeLimit("default", now.Add(7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "claude")
	body := `#!/bin/sh
case "$1" in
 --version) printf '2.1.248\n'; exit 0;;
 auth) printf '{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}\n'; exit 0;;
esac
IFS= read -r first
printf '%s\n' "$first" >> "$CLAUDE_CONFIG_DIR/input"
if [ -f "$CLAUDE_CONFIG_DIR/fail" ]; then
 IFS= read -r failure < "$CLAUDE_CONFIG_DIR/fail"
 printf '{"type":"result","is_error":true,"result":"%s"}\n' "$failure"
else
 printf '{"type":"result","subtype":"success","result":"continued"}\n'
fi
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	cli := NewCLI()
	cli.lookupEnv = func(key string) (string, bool) {
		if key == CLIPathEnv {
			return script, true
		}
		return "", false
	}
	cli.now = func() time.Time { return now }
	cli.ConfigureSlots(auth, root)
	for _, id := range []string{a, b} {
		dir := filepath.Join(cli.slotsRoot, id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, globalConfigName), []byte(`{"oauthAccount":{"emailAddress":"`+id+`@example.com","organizationName":"Test Org"}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	ctx = identity.ContextWithPrincipal(ctx, identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-a", AccountScopeID: "account-a"})
	return NewRunner(auth, cli), ctx, &now, a, b
}

// Purpose: Runner.runAccounts must retry the same durable turn on B when A's
// actual CLI result states a reset, and recover A after that exact clock. A real
// subprocess fixture plus Pebble is the narrowest layer proving routing, input,
// identity and durable postconditions together without live provider credentials.
// Prevents machine-wide parking and accidental loss of history on account change.
func TestAccountLimitFailoverAndRecovery(t *testing.T) {
	r, ctx, now, a, b := accountFixture(t)
	dir := func(id string) string { return filepath.Join(r.cli.slotsRoot, id) }
	if err := os.WriteFile(filepath.Join(dir(a), "fail"), []byte("You've hit your session limit · resets 1pm (UTC)"), 0600); err != nil {
		t.Fatal(err)
	}
	req := provideriface.Request{SessionID: "test-session", Model: "test-model", Input: []map[string]any{{"role": "user", "content": "durable history marker"}}}
	var selected []string
	result, err := r.CreateResponseStreaming(ctx, req, func(event provideriface.StreamEvent) {
		if event.Type == provideriface.StreamEventAccountSelected {
			selected = append(selected, event.Metadata["account_id"].(string))
		}
	})
	if err != nil || result.Text != "continued" || strings.Join(selected, ",") != a+","+b {
		t.Fatalf("response=%+v err=%v selected=%v", result, err, selected)
	}
	for _, id := range []string{a, b} {
		raw, err := os.ReadFile(filepath.Join(dir(id), "input"))
		if err != nil || !strings.Contains(string(raw), "durable history marker") {
			t.Fatalf("%s input=%s err=%v", id, raw, err)
		}
	}
	accounts, err := r.cli.AccountStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if accounts[1].LimitState != "exhausted" || accounts[1].LimitUntil.Format(time.RFC3339) != "2026-10-31T13:00:00Z" || accounts[2].LimitState != "ok" || accounts[2].Plan != "max" || accounts[2].Organization != "Test Org" {
		t.Fatalf("accounts=%+v", accounts)
	}
	before, _ := os.ReadFile(filepath.Join(dir(a), "input"))
	if _, err := r.CreateResponse(ctx, req); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir(a), "input"))
	if string(before) != string(after) {
		t.Fatal("A retried before reset")
	}
	*now = now.Add(time.Hour)
	// Reaching the reset retries A (it still rejects, and then B continues).
	if _, err := r.CreateResponse(ctx, req); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(filepath.Join(dir(a), "input"))
	if string(before) == string(after) {
		t.Fatal("A not retried at reset")
	}
	if err := r.cli.OrderAccounts([]string{b, a, "default"}); err != nil {
		t.Fatal(err)
	}
	selected = nil
	if _, err := r.CreateResponseStreaming(ctx, req, func(e provideriface.StreamEvent) {
		if e.Type == provideriface.StreamEventAccountSelected {
			selected = append(selected, e.Metadata["account_id"].(string))
		}
	}); err != nil || len(selected) != 1 || selected[0] != b {
		t.Fatalf("reordered dispatch=%v err=%v", selected, err)
	}
}

// Purpose: runAccounts returns the earliest parsed reset, not the last rejection
// or a guessed window. Unknown reset/auth failures must leave routing metadata
// unchanged. The subprocess/store boundary owns both error and postconditions.
func TestAccountsAllExhaustedAndUnknownReset(t *testing.T) {
	r, ctx, _, a, b := accountFixture(t)
	for id, msg := range map[string]string{a: "You've hit your session limit · resets 1pm (UTC)", b: "You've hit your weekly limit · resets Nov 2, 4pm (UTC)"} {
		if err := os.WriteFile(filepath.Join(r.cli.slotsRoot, id, "fail"), []byte(msg), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.CreateResponse(ctx, provideriface.Request{Model: "test-model"})
	var wait *AccountLimitWait
	if !errors.As(err, &wait) || wait.Until.Format(time.RFC3339) != "2026-10-31T13:00:00Z" {
		t.Fatalf("wait=%v err=%v", wait, err)
	}
	r2, ctx2, _, a2, _ := accountFixture(t)
	if err := os.WriteFile(filepath.Join(r2.cli.slotsRoot, a2, "fail"), []byte("You've hit your session limit; reset unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.CreateResponse(ctx2, provideriface.Request{Model: "test-model"}); err == nil {
		t.Fatal("unknown reset succeeded")
	}
	slots, err := r2.authStore.ClaudeCodeSlots()
	if err != nil {
		t.Fatal(err)
	}
	if !slots[1].LimitUntil.IsZero() {
		t.Fatal("unknown reset created a guessed window")
	}
	// A principal without an enablement credential cannot dispatch any CLI account.
	foreign := identity.ContextWithPrincipal(ctx2, identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-b", AccountScopeID: "account-b"})
	if _, err := r2.CreateResponse(foreign, provideriface.Request{Model: "test-model"}); err == nil {
		t.Fatal("foreign principal dispatched")
	}
	if _, err := os.Stat(filepath.Join(r2.cli.slotsRoot, strings.Repeat("b", 32), "input")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unauthorized dispatch changed B")
	}
}
