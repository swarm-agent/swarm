package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: the signal feed is what monitors, alert forwarders and AIs follow
// with a cursor, so it must (1) number signals durably and in order across a
// reopen, (2) show a reader only its own account's and machine-wide signals,
// (3) advance the cursor past entries a filter hides, (4) say so when
// retention dropped entries after a reader's cursor instead of skipping them
// silently, and (5) refuse malformed kinds and bound every free-text field so
// a producer cannot push transcripts or oversized payloads into it. Owner:
// SignalStore over a real temporary Pebble store, the layer that holds the
// sequence, retention and visibility rules.
func TestSignalStoreFeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signals")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	signals := NewSignalStore(store)

	mustAppend := func(sig Signal) Signal {
		t.Helper()
		out, err := signals.Append(sig)
		if err != nil {
			t.Fatalf("append %+v: %v", sig, err)
		}
		return out
	}
	first := mustAppend(Signal{Kind: "agent.blocked", Severity: "warning", Account: "acct_a", Summary: "waiting on approval", Refs: map[string]string{"session_id": "s1"}})
	mustAppend(Signal{Kind: "agent.blocked", Severity: "warning", Account: "acct_b", Summary: "other account"})
	mustAppend(Signal{Kind: "sandbox.inactive", Severity: "critical", Summary: "machine-wide"})
	mustAppend(Signal{Kind: "run.failed", Severity: "info", Account: "acct_a", Summary: "low"})
	if first.Seq != 1 || !strings.HasPrefix(first.ID, "sig_") || first.At == 0 || first.Source != SignalSourceSwarmd {
		t.Fatalf("first signal = %+v", first)
	}

	page, err := signals.ListAfter(0, 10, SignalFilter{Account: "acct_a"})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, sig := range page.Signals {
		if sig.Account == "acct_b" {
			t.Fatalf("acct_a saw acct_b's signal: %+v", sig)
		}
		kinds = append(kinds, sig.Kind)
	}
	if strings.Join(kinds, ",") != "agent.blocked,sandbox.inactive,run.failed" || page.NextAfter != 4 || page.LatestSeq != 4 || page.Gap {
		t.Fatalf("page = %+v", page)
	}

	// Severity and kind filters; the cursor still advances past hidden entries.
	page, err = signals.ListAfter(0, 10, SignalFilter{Account: "acct_a", MinSeverity: "critical"})
	if err != nil || len(page.Signals) != 1 || page.Signals[0].Kind != "sandbox.inactive" || page.NextAfter != 4 {
		t.Fatalf("critical page = %+v, %v", page, err)
	}
	page, err = signals.ListAfter(0, 10, SignalFilter{Account: "acct_a", KindPrefixes: []string{"agent"}})
	if err != nil || len(page.Signals) != 1 || page.Signals[0].Seq != 1 {
		t.Fatalf("agent page = %+v, %v", page, err)
	}
	if _, err := signals.ListAfter(0, 10, SignalFilter{MinSeverity: "loud"}); err == nil {
		t.Fatal("accepted an unknown severity filter")
	}

	// Numbering survives a reopen.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	signals = NewSignalStore(store)
	if next := mustAppend(Signal{Kind: "run.failed", Summary: "after reopen"}); next.Seq != 5 {
		t.Fatalf("seq after reopen = %d, want 5", next.Seq)
	}

	// Retention: keep the newest 3; a cursor behind them reports a gap.
	signals.keep = 3
	mustAppend(Signal{Kind: "run.failed", Summary: "six"})
	page, err = signals.ListAfter(1, 10, SignalFilter{Account: "acct_a"})
	if err != nil || !page.Gap || page.OldestSeq != 4 || len(page.Signals) != 3 || page.Signals[0].Seq != 4 {
		t.Fatalf("after retention = %+v, %v", page, err)
	}
	if page, err = signals.ListAfter(3, 10, SignalFilter{}); err != nil || page.Gap {
		t.Fatalf("cursor at the edge reported a gap: %+v, %v", page, err)
	}
	// A cursor ahead of the feed is reported, not silently resumed.
	if page, err = signals.ListAfter(99, 10, SignalFilter{}); err != nil || !page.Gap || page.NextAfter != 6 {
		t.Fatalf("future cursor = %+v, %v", page, err)
	}

	// Malformed input is refused and nothing is written.
	for _, bad := range []Signal{
		{Kind: "Agent", Summary: "x"},
		{Kind: "agent", Summary: "x"},
		{Kind: "a.b.c.d.e", Summary: "x"},
		{Kind: "agent.blocked", Summary: "   "},
		{Kind: "agent.blocked", Summary: "x", Severity: "loud"},
		{Kind: "agent.blocked", Summary: "x", Refs: map[string]string{"Bad-Key": "v"}},
	} {
		if _, err := signals.Append(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if page, _ = signals.ListAfter(6, 10, SignalFilter{}); len(page.Signals) != 0 || page.LatestSeq != 6 {
		t.Fatalf("a refused signal was written: %+v", page)
	}

	// Free text is flattened and bounded.
	long := mustAppend(Signal{Kind: "external.note", Summary: strings.Repeat("x", 1000) + "\nsecond line", Attrs: map[string]string{"detail": strings.Repeat("y", 1000)}})
	if n := len([]rune(long.Summary)); n != MaxSignalSummaryRunes || strings.Contains(long.Summary, "\n") {
		t.Fatalf("summary length %d / newline kept", n)
	}
	if n := len([]rune(long.Attrs["detail"])); n != MaxSignalValueRunes {
		t.Fatalf("attr length %d", n)
	}
}
