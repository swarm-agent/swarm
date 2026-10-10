package permission

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a monitor learns that an agent is stuck waiting on a person only
// from agent.blocked, and that it moved on from agent.unblocked, so a pending
// approval must raise exactly one agent.blocked and its resolution exactly one
// agent.unblocked, sharing one dedup key. Neither may carry the command or its
// arguments (here a URL with a token in it). Owner: Service.CreatePending and
// Service.Resolve with a real permission store and signal store, the layer
// where the transition happens.
func TestPermissionWaitRaisesBlockedAndUnblockedSignals(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	signalStore := pebblestore.NewSignalStore(store)
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSignalEmitter(signals.NewEmitter(signalStore))

	record, err := svc.CreatePending(CreateInput{
		SessionID:     "session-1",
		RunID:         "run-1",
		CallID:        "call-1",
		ToolName:      "bash",
		ToolArguments: `{"command":"git push https://tok_secret@example.invalid/repo"}`,
		Requirement:   "tool",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve("session-1", record.ID, "approve", "looks fine"); err != nil {
		t.Fatal(err)
	}

	page, err := signalStore.ListAfter(0, 50, pebblestore.SignalFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var blocked, unblocked []pebblestore.Signal
	for _, sig := range page.Signals {
		switch sig.Kind {
		case "agent.blocked":
			blocked = append(blocked, sig)
		case "agent.unblocked":
			unblocked = append(unblocked, sig)
		}
		raw := sig.Summary + sig.DedupKey + strings.Join(mapValues(sig.Refs), " ") + strings.Join(mapValues(sig.Attrs), " ")
		if strings.Contains(raw, "tok_secret") || strings.Contains(raw, "git push") || strings.Contains(raw, "looks fine") {
			t.Fatalf("signal carries command, arguments or reason: %+v", sig)
		}
	}
	if len(blocked) != 1 || len(unblocked) != 1 {
		t.Fatalf("blocked=%d unblocked=%d, want 1 each: %+v", len(blocked), len(unblocked), page.Signals)
	}
	b, u := blocked[0], unblocked[0]
	if b.Severity != pebblestore.SignalSeverityWarning || b.Refs["session_id"] != "session-1" || b.Refs["permission_id"] != record.ID || b.Refs["tool"] != "bash" {
		t.Fatalf("blocked = %+v", b)
	}
	if u.Attrs["outcome"] != pebblestore.PermissionStatusApproved || u.DedupKey != b.DedupKey || u.Seq <= b.Seq {
		t.Fatalf("unblocked = %+v (blocked %+v)", u, b)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
