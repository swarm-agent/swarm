package security

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func newExpiryTestService(t *testing.T) (*Service, *pebblestore.ClientAuthStore, *pebblestore.SignalStore) {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	authStore := pebblestore.NewClientAuthStore(db)
	feed := pebblestore.NewSignalStore(db)
	svc := NewService(authStore, events)
	svc.SetSignalEmitter(signals.NewEmitter(feed))
	return svc, authStore, feed
}

// Purpose: every key an API caller mints (client, SDK, monitoring, AI,
// gateway and operator keys) must expire within its kind's maximum, at the
// service boundary too, so no future caller of CreateScopedTokenUnder or
// CreateAgentBoundTokenUnder can mint a key that never expires. A refused
// mint stores nothing. Boundary: Service.CreateScopedTokenUnder,
// Service.CreateAgentBoundTokenUnder (requireLifetime). Narrowest layer: the
// service over a temporary store.
func TestAPIMintedKeysMustExpireWithinTheirMaximum(t *testing.T) {
	svc, authStore, _ := newExpiryTestService(t)
	const account, user = "acct-1", "user-1"
	for _, lifetime := range []time.Duration{0, -time.Second, ScopedTokenMaxLifetime + time.Second} {
		if _, _, err := svc.CreateScopedTokenUnder(nil, "k", []string{"sessions:read"}, account, user, lifetime, "", ""); err == nil {
			t.Fatalf("scoped key minted with lifetime %v", lifetime)
		}
	}
	for _, lifetime := range []time.Duration{0, -time.Second, AgentBoundMaxLifetime + time.Second} {
		if _, _, err := svc.CreateAgentBoundTokenUnder(nil, "gw", account, user, lifetime, "frontdesk", AgentBoundLimits{}); err == nil {
			t.Fatalf("agent-bound token minted with lifetime %v", lifetime)
		}
	}
	if stored, _ := authStore.ListScopedTokens(account); len(stored) != 0 {
		t.Fatalf("refused mints stored %d tokens", len(stored))
	}
	before := time.Now()
	_, scoped, err := svc.CreateScopedTokenUnder(nil, "k", []string{"sessions:read"}, account, user, ScopedTokenMaxLifetime, "", "")
	if err != nil || scoped.ExpiresAt < before.Add(ScopedTokenMaxLifetime).UnixMilli() {
		t.Fatalf("365-day key: %v expires_at=%d", err, scoped.ExpiresAt)
	}
	_, gateway, err := svc.CreateAgentBoundTokenUnder(nil, "gw", account, user, AgentBoundMaxLifetime, "frontdesk", AgentBoundLimits{})
	if err != nil || gateway.ExpiresAt < before.Add(AgentBoundMaxLifetime).UnixMilli() {
		t.Fatalf("30-day gateway token: %v expires_at=%d", err, gateway.ExpiresAt)
	}
}

// Purpose: keys already stored without an expiry keep working (no silent
// breakage, no auto-revoke) but are made visible: at start the daemon raises
// one token.never_expires warning per account listing exactly the unrevoked
// never-expiring key ids, and nothing for an account without any. Boundary:
// Service.WarnNeverExpiringTokens and NeverExpires. Narrowest layer: the
// service with a real signal feed over a temporary store.
func TestWarnNeverExpiringTokensListsThemAndRevokesNothing(t *testing.T) {
	svc, authStore, feed := newExpiryTestService(t)
	put := func(id, account string, expiresAt int64, revoked bool) {
		t.Helper()
		if err := authStore.PutScopedToken(pebblestore.ScopedTokenRecord{ID: id, TokenHash: "hash-" + id, AccountScopeID: account, UserID: "u", Scopes: []string{"sessions:read"}, ExpiresAt: expiresAt, Revoked: revoked}); err != nil {
			t.Fatal(err)
		}
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	put("tok_old_b", "acct-1", 0, false)
	put("tok_old_a", "acct-1", 0, false)
	put("tok_revoked", "acct-1", 0, true)
	put("tok_expiring", "acct-1", future, false)
	put("tok_other", "acct-2", 0, false)
	put("tok_fine", "acct-3", future, false)

	ids, err := svc.WarnNeverExpiringTokens()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids, ","); got != "tok_old_a,tok_old_b,tok_other" {
		t.Fatalf("never-expiring ids = %q", got)
	}
	// Each account's reader sees only its own warning.
	warnings := map[string]pebblestore.Signal{}
	for _, account := range []string{"acct-1", "acct-2", "acct-3"} {
		page, err := feed.ListAfter(0, 100, pebblestore.SignalFilter{Account: account, KindPrefixes: []string{"token.never_expires"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, sig := range page.Signals {
			if sig.Account != account {
				t.Fatalf("%s read a warning for %q", account, sig.Account)
			}
			if _, dup := warnings[account]; dup {
				t.Fatalf("more than one warning for %s", account)
			}
			warnings[account] = sig
		}
	}
	if len(warnings) != 2 {
		t.Fatalf("want one warning for each of acct-1 and acct-2 and none for acct-3, got %+v", warnings)
	}
	one := warnings["acct-1"]
	if one.Severity != pebblestore.SignalSeverityWarning || one.Attrs["token_ids"] != "tok_old_a,tok_old_b" || one.Attrs["count"] != "2" || !strings.Contains(one.Summary, "tok_old_a,tok_old_b") {
		t.Fatalf("acct-1 warning: %+v", one)
	}
	if warnings["acct-2"].Attrs["token_ids"] != "tok_other" {
		t.Fatalf("acct-2 warning: %+v", warnings["acct-2"])
	}
	for _, id := range []string{"tok_old_a", "tok_old_b"} {
		record, found, err := authStore.GetScopedToken("acct-1", id)
		if err != nil || !found || record.Revoked {
			t.Fatalf("%s was changed by the startup check: found=%v revoked=%v err=%v", id, found, record.Revoked, err)
		}
	}
}

// Purpose: a long list of ids is cut at a whole id with "+N more", never mid-id,
// and stays within the signal value bound. Boundary: fitTokenIDs.
func TestFitTokenIDsCutsAtWholeIDs(t *testing.T) {
	var ids []string
	for i := 0; i < 30; i++ {
		ids = append(ids, fmt.Sprintf("tok_%016d", i))
	}
	got := fitTokenIDs(ids, pebblestore.MaxSignalValueRunes)
	if len(got) > pebblestore.MaxSignalValueRunes || !strings.HasSuffix(got, " more") {
		t.Fatalf("fit = %q (%d)", got, len(got))
	}
	listed := strings.Split(strings.SplitN(got, " +", 2)[0], ",")
	for i, id := range listed {
		if id != ids[i] {
			t.Fatalf("id %d cut: %q", i, id)
		}
	}
	if !strings.HasSuffix(got, fmt.Sprintf(" +%d more", len(ids)-len(listed))) {
		t.Fatalf("wrong remainder in %q", got)
	}
	if fitTokenIDs(ids[:2], pebblestore.MaxSignalValueRunes) != ids[0]+","+ids[1] {
		t.Fatal("a short list was not listed whole")
	}
}
