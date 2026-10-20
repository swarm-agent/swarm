package security

import (
	"crypto/sha256"
	"encoding/hex"
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

// putKey stores record as an older or current daemon would have and returns
// the raw key that validates to it.
func putKey(t *testing.T, store *pebblestore.ClientAuthStore, id string, record pebblestore.ScopedTokenRecord) string {
	t.Helper()
	raw := "swk_test_" + id
	sum := sha256.Sum256([]byte(raw))
	record.ID, record.TokenHash = id, hex.EncodeToString(sum[:])
	if record.AccountScopeID == "" {
		record.AccountScopeID = "acct-1"
	}
	if record.UserID == "" {
		record.UserID = "user-1"
	}
	if record.Scopes == nil {
		record.Scopes = []string{"sessions:read"}
	}
	if err := store.PutScopedToken(record); err != nil {
		t.Fatal(err)
	}
	return raw
}

func daysAgo(days float64) int64 {
	return time.Now().Add(-time.Duration(days * float64(24*time.Hour))).UnixMilli()
}

func signalsOf(t *testing.T, feed *pebblestore.SignalStore, account, kind string) []pebblestore.Signal {
	t.Helper()
	page, err := feed.ListAfter(0, 200, pebblestore.SignalFilter{Account: account, KindPrefixes: []string{kind}})
	if err != nil {
		t.Fatal(err)
	}
	return page.Signals
}

// Purpose: every kind of key (app, AI, bot) is made without a lifetime and
// keeps working while it is used; it stops only after 90 days without use,
// and the refusal says why. Threat: keys in daily use (bots, AI connections,
// apps) dying on a calendar date and breaking whatever uses them.
// Boundary: Service.Create*TokenUnder, ValidateScopedToken, KeyLapse.
func TestKeysWorkWhileUsedAndLapseAfter90DaysUnused(t *testing.T) {
	svc, authStore, feed := newExpiryTestService(t)
	const account, user = "acct-1", "user-1"
	_, app, err := svc.CreateScopedTokenUnder(nil, "app", []string{"sessions:read"}, account, user, 0, "", "")
	if err != nil || app.ExpiresAt != 0 || app.ExpiryRule != "idle" {
		t.Fatalf("app key: %v %+v", err, app)
	}
	_, ai, err := svc.CreateScopedTokenUnder(nil, "ai", []string{"swarm:read", "swarm:write"}, account, user, 0, "", "")
	if err != nil || ai.ExpiresAt != 0 || KeyKind(ai) != KeyKindAI {
		t.Fatalf("AI key: %v %+v", err, ai)
	}
	_, bot, err := svc.CreateAgentBoundTokenUnder(nil, "bot", account, user, 0, "frontdesk", AgentBoundLimits{})
	if err != nil || bot.ExpiresAt != 0 || KeyKind(bot) != KeyKindBot {
		t.Fatalf("bot key: %v %+v", err, bot)
	}

	recent := putKey(t, authStore, "tok_recent", pebblestore.ScopedTokenRecord{ExpiryRule: "idle", CreatedAt: daysAgo(400), LastUsedAt: daysAgo(89)})
	stale := putKey(t, authStore, "tok_stale", pebblestore.ScopedTokenRecord{ExpiryRule: "idle", CreatedAt: daysAgo(400), LastUsedAt: daysAgo(91)})
	neverUsed := putKey(t, authStore, "tok_never_used", pebblestore.ScopedTokenRecord{ExpiryRule: "idle", CreatedAt: daysAgo(91)})

	before := time.Now().UnixMilli()
	if record, err := svc.ValidateScopedToken(recent); err != nil || record == nil {
		t.Fatalf("key used 89 days ago refused: %v", err)
	}
	stored, _, _ := authStore.GetScopedToken(account, "tok_recent")
	if stored.LastUsedAt < before {
		t.Fatalf("use did not restart the clock: last_used_at=%d", stored.LastUsedAt)
	}
	if at, reason := KeyLapse(stored); reason != "unused" || at < before+KeyIdleLimit.Milliseconds() {
		t.Fatalf("lapse after use = %d %q", at, reason)
	}
	for _, raw := range []string{stale, neverUsed} {
		if record, err := svc.ValidateScopedToken(raw); err == nil || record != nil || !strings.Contains(err.Error(), "unused for 90 days") {
			t.Fatalf("key unused for 91 days: record=%v err=%v", record, err)
		}
	}
	denied := signalsOf(t, feed, account, "token.denied")
	if len(denied) != 2 || denied[0].Attrs["reason"] != "expired: unused for 90 days" {
		t.Fatalf("token.denied = %+v", denied)
	}
}

// Purpose: a fixed end date is the owner's option, the same for every kind
// (no per-kind caps): at most 365 days, never negative, and when it passes
// the key stops even if it is in use. Boundary: checkFixedDate, KeyLapse.
func TestFixedEndDateIsOptionalAndTheSameForEveryKind(t *testing.T) {
	svc, authStore, _ := newExpiryTestService(t)
	const account, user = "acct-1", "user-1"
	for _, lifetime := range []time.Duration{-time.Second, KeyFixedDateMax + time.Second} {
		if _, _, err := svc.CreateScopedTokenUnder(nil, "k", []string{"sessions:read"}, account, user, lifetime, "", ""); err == nil {
			t.Fatalf("app key minted with fixed date %v", lifetime)
		}
		if _, _, err := svc.CreateAgentBoundTokenUnder(nil, "gw", account, user, lifetime, "frontdesk", AgentBoundLimits{}); err == nil {
			t.Fatalf("bot key minted with fixed date %v", lifetime)
		}
	}
	if stored, _ := authStore.ListScopedTokens(account); len(stored) != 0 {
		t.Fatalf("refused mints stored %d keys", len(stored))
	}
	_, bot, err := svc.CreateAgentBoundTokenUnder(nil, "gw", account, user, 200*24*time.Hour, "frontdesk", AgentBoundLimits{})
	if err != nil || bot.ExpiryRule != "date" || !HasFixedDate(bot) {
		t.Fatalf("bot key with a 200-day date: %v %+v", err, bot)
	}
	if _, reason := KeyLapse(bot); reason != "unused" {
		t.Fatalf("a used key's idle lapse comes before a 200-day date, got %q", reason)
	}
	pastDate := putKey(t, authStore, "tok_past", pebblestore.ScopedTokenRecord{ExpiryRule: "date", CreatedAt: daysAgo(10), LastUsedAt: daysAgo(0.01), ExpiresAt: daysAgo(1)})
	if _, err := svc.ValidateScopedToken(pastDate); err == nil || !strings.Contains(err.Error(), "fixed end date") {
		t.Fatalf("key past its fixed date: %v", err)
	}
}

// Purpose: keys stored before the rule move to it by themselves: a key that
// had a date only because one was required (30-day bot keys, 365-day AI keys)
// keeps working while used past that date; a key made to be temporary (an
// hour or a day) keeps its date. Nothing is rewritten. Boundary: fixedDate.
func TestKeysFromBeforeTheRuleMoveToItWithoutBreaking(t *testing.T) {
	svc, authStore, _ := newExpiryTestService(t)
	bot := putKey(t, authStore, "tok_bot30", pebblestore.ScopedTokenRecord{AgentName: "frontdesk", Scopes: []string{"sessions:read", "sessions:write"},
		CreatedAt: daysAgo(31), ExpiresAt: daysAgo(1), LastUsedAt: daysAgo(0.1)})
	ai := putKey(t, authStore, "tok_ai365", pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "swarm:write"},
		CreatedAt: daysAgo(366), ExpiresAt: daysAgo(1), LastUsedAt: daysAgo(2)})
	never := putKey(t, authStore, "tok_never", pebblestore.ScopedTokenRecord{CreatedAt: daysAgo(500), LastUsedAt: daysAgo(3)})
	hour := putKey(t, authStore, "tok_hour", pebblestore.ScopedTokenRecord{CreatedAt: daysAgo(1), ExpiresAt: daysAgo(1) + time.Hour.Milliseconds()})
	for _, raw := range []string{bot, ai, never} {
		if record, err := svc.ValidateScopedToken(raw); err != nil || record == nil {
			t.Fatalf("%s refused after moving to the idle rule: %v", raw, err)
		}
	}
	if _, err := svc.ValidateScopedToken(hour); err == nil {
		t.Fatal("a one-hour key from before the rule lost its date")
	}
	record, _, _ := authStore.GetScopedToken("acct-1", "tok_bot30")
	if record.ExpiryRule != "" || record.ExpiresAt == 0 {
		t.Fatalf("the stored key was rewritten: %+v", record)
	}
}

// Purpose: a key used from a place it has not been used from before raises
// key.new_address (once per place) and is marked for the summary; the first
// place raises nothing; the remembered places stay bounded. Threat: a stolen
// key being used elsewhere without anyone noticing. Boundary:
// ValidateScopedTokenFrom, ClientAuthStore.UpdateScopedTokenUse.
func TestKeyUsedFromANewPlaceRaisesKeyNewAddress(t *testing.T) {
	svc, authStore, feed := newExpiryTestService(t)
	raw := putKey(t, authStore, "tok_app", pebblestore.ScopedTokenRecord{Name: "my-app", ExpiryRule: "idle", CreatedAt: daysAgo(1)})
	for _, from := range []string{"this machine", "this machine", "203.0.113.0/24", "203.0.113.0/24"} {
		if _, err := svc.ValidateScopedTokenFrom(raw, from); err != nil {
			t.Fatal(err)
		}
	}
	raised := signalsOf(t, feed, "acct-1", "key.new_address")
	if len(raised) != 1 || raised[0].Attrs["from"] != "203.0.113.0/24" || raised[0].Attrs["kind"] != "app" || !strings.Contains(raised[0].Summary, "revoke") {
		t.Fatalf("key.new_address = %+v", raised)
	}
	record, _, _ := authStore.GetScopedToken("acct-1", "tok_app")
	if record.LastUsedFrom != "203.0.113.0/24" || record.NewSourceAt == 0 || strings.Join(record.SeenFrom, ",") != "this machine,203.0.113.0/24" {
		t.Fatalf("record after uses: %+v", record)
	}
	for i := 0; i < 40; i++ {
		if _, err := svc.ValidateScopedTokenFrom(raw, fmt.Sprintf("198.51.%d.0/24", i)); err != nil {
			t.Fatal(err)
		}
	}
	record, _, _ = authStore.GetScopedToken("acct-1", "tok_app")
	if len(record.SeenFrom) != maxKeySources || record.SeenFrom[maxKeySources-1] != "198.51.39.0/24" {
		t.Fatalf("seen_from not bounded to the newest %d: %v", maxKeySources, record.SeenFrom)
	}
}

// Purpose: a key about to stop working on its own (almost 90 days unused, or
// near its fixed date) raises key.expiring once per lapse, a week ahead; keys
// in use, revoked keys and Swarm's internal keys raise nothing. Nothing is
// revoked. Boundary: WarnExpiringKeys.
func TestWarnExpiringKeysWarnsOnceAWeekAhead(t *testing.T) {
	svc, authStore, feed := newExpiryTestService(t)
	putKey(t, authStore, "tok_idle", pebblestore.ScopedTokenRecord{Name: "old-laptop", ExpiryRule: "idle", CreatedAt: daysAgo(200), LastUsedAt: daysAgo(85)})
	putKey(t, authStore, "tok_dated", pebblestore.ScopedTokenRecord{ExpiryRule: "date", CreatedAt: daysAgo(20), LastUsedAt: daysAgo(0.1), ExpiresAt: time.Now().Add(3 * 24 * time.Hour).UnixMilli()})
	putKey(t, authStore, "tok_busy", pebblestore.ScopedTokenRecord{ExpiryRule: "idle", CreatedAt: daysAgo(200), LastUsedAt: daysAgo(1)})
	putKey(t, authStore, "tok_revoked", pebblestore.ScopedTokenRecord{ExpiryRule: "idle", CreatedAt: daysAgo(200), LastUsedAt: daysAgo(85), Revoked: true})
	putKey(t, authStore, "tok_internal", pebblestore.ScopedTokenRecord{Kind: KeyKindInternal, CreatedAt: daysAgo(400)})

	ids, err := svc.WarnExpiringKeys(time.Now())
	if err != nil || strings.Join(ids, ",") != "tok_dated,tok_idle" {
		t.Fatalf("warned %v %v", ids, err)
	}
	if again, _ := svc.WarnExpiringKeys(time.Now()); len(again) != 0 {
		t.Fatalf("warned twice for the same lapse: %v", again)
	}
	raised := signalsOf(t, feed, "acct-1", "key.expiring")
	if len(raised) != 2 {
		t.Fatalf("key.expiring = %+v", raised)
	}
	for _, sig := range raised {
		if sig.Severity != pebblestore.SignalSeverityWarning || !strings.Contains(sig.Summary, "stops working on") {
			t.Fatalf("signal %+v", sig)
		}
	}
	if record, _, _ := authStore.GetScopedToken("acct-1", "tok_idle"); record.Revoked {
		t.Fatal("the warning revoked the key")
	}
}

// Purpose: Swarm's own internal keys (remote transport) never lapse and are
// told apart from a person's keys, including ones minted before they were
// marked. Boundary: CreateInternalToken, KeyKind, KeyLapse.
func TestInternalKeysNeverLapse(t *testing.T) {
	svc, authStore, _ := newExpiryTestService(t)
	_, record, err := svc.CreateInternalToken("swarm-remote pc", []string{"sessions:read"}, "acct-1", "user-1")
	if err != nil || KeyKind(record) != KeyKindInternal || !NeverExpires(record) {
		t.Fatalf("internal key: %v %+v", err, record)
	}
	legacy := putKey(t, authStore, "tok_remote", pebblestore.ScopedTokenRecord{Name: "swarm-remote social", CreatedAt: daysAgo(400)})
	if record, err := svc.ValidateScopedToken(legacy); err != nil || KeyKind(*record) != KeyKindInternal {
		t.Fatalf("legacy remote key: %v", err)
	}
}

// Purpose: the three kinds people see and their options. Boundary: KeyKind,
// KeyOptions.
func TestKeyKindsAndOptions(t *testing.T) {
	cases := []struct {
		record  pebblestore.ScopedTokenRecord
		kind    string
		options string
	}{
		{pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "swarm:write", "swarm:approve", "swarm:manage", "admin"}}, KeyKindAI, "full"},
		{pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "swarm:write"}}, KeyKindAI, "write"},
		{pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read"}}, KeyKindAI, "read"},
		{pebblestore.ScopedTokenRecord{AgentName: "frontdesk"}, KeyKindBot, "gateway"},
		{pebblestore.ScopedTokenRecord{AgentName: "frontdesk", AgentOperator: true}, KeyKindBot, "operator"},
		{pebblestore.ScopedTokenRecord{Scopes: []string{"signals:read"}}, KeyKindApp, "monitor"},
		{pebblestore.ScopedTokenRecord{Scopes: []string{"automations:trigger"}, WorkerID: "w1"}, KeyKindApp, "trigger"},
		{pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "sessions:write"}}, KeyKindApp, ""},
	}
	for _, c := range cases {
		if KeyKind(c.record) != c.kind || strings.Join(KeyOptions(c.record), ",") != c.options {
			t.Fatalf("%+v: kind %q options %v", c.record, KeyKind(c.record), KeyOptions(c.record))
		}
	}
}

// Purpose: a key made with another key stops when its parent is revoked or
// past its fixed date, but not because the parent merely went unused (each
// keeps its own clock). Boundary: ValidateScopedToken's issuer chain.
func TestChildKeysFollowParentRevocationNotParentDisuse(t *testing.T) {
	svc, authStore, _ := newExpiryTestService(t)
	putKey(t, authStore, "tok_parent_idle", pebblestore.ScopedTokenRecord{ExpiryRule: "idle", CreatedAt: daysAgo(200), LastUsedAt: daysAgo(120)})
	putKey(t, authStore, "tok_parent_revoked", pebblestore.ScopedTokenRecord{ExpiryRule: "idle", CreatedAt: daysAgo(10), Revoked: true})
	child := putKey(t, authStore, "tok_child", pebblestore.ScopedTokenRecord{ExpiryRule: "idle", CreatedAt: daysAgo(100), LastUsedAt: daysAgo(1), ParentTokenID: "tok_parent_idle"})
	orphan := putKey(t, authStore, "tok_orphan", pebblestore.ScopedTokenRecord{ExpiryRule: "idle", CreatedAt: daysAgo(1), ParentTokenID: "tok_parent_revoked"})
	if _, err := svc.ValidateScopedToken(child); err != nil {
		t.Fatalf("child of an unused parent refused: %v", err)
	}
	if _, err := svc.ValidateScopedToken(orphan); err == nil {
		t.Fatal("child of a revoked parent still works")
	}
}
