package session

import (
	"encoding/json"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: fallback runtime usage and compaction must share V3 atomic receipt,
// summary and outbox authority. Service.RecordTurnUsage/ResetUsage own this
// integration. An isolated store proves corrections, billing-zero, reset replay
// and foreign-principal rejection without any provider or daemon execution.
func TestRecordTurnUsageCanonicalScopeAndReset(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "usage.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, events)
	session, _, err := svc.CreateSessionWithOptions(CreateSessionOptions{UserID: "usage-user", AccountScopeID: "usage-account", WorkspacePath: t.TempDir(), Title: "usage", Mode: "auto", Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "fixture-model", Thinking: "low"}, Metadata: map[string]any{"project_id": "project", "task_id": "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(pebblestore.KeyProjectTask("usage-account", "project", "task"), pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "usage-account", SessionID: session.ID}); err != nil {
		t.Fatal(err)
	}
	turn := pebblestore.SessionTurnUsageSnapshot{RunID: "receipt", Provider: "fixture", Model: "fixture", TotalTokens: 900, ContextWindow: 1000, BilledUsagePresent: true, BilledTokens: 100, BilledInputTokens: 100, EstimatedCostUSD: 1, PriceStatus: "known"}
	_, summary, legacy, err := svc.RecordTurnUsage(session.ID, turn)
	if err != nil || legacy != nil || summary.TotalTokens != 900 || summary.TurnCount != 1 {
		t.Fatalf("record: %+v %v", summary, err)
	}
	summary, legacy, err = svc.ResetUsage(session.ID, 2000, "fixture", "fixture", "compaction")
	if err != nil || legacy != nil || summary.TotalTokens != 0 || summary.EstimatedCostUSD != 1 || summary.TurnCount != 1 {
		t.Fatalf("reset: %+v %v", summary, err)
	}
	for _, tokens := range []int64{100, 50, 100, 0} {
		turn.BilledTokens, turn.BilledInputTokens, turn.EstimatedCostUSD = tokens, tokens, float64(tokens)/100
		_, summary, _, err = svc.RecordTurnUsage(session.ID, turn)
		if err != nil || summary.EstimatedCostUSD != turn.EstimatedCostUSD || summary.TurnCount != 1 {
			t.Fatalf("correction: %+v %v", summary, err)
		}
		scope, found, err := store.GetUsageScope("usage-account", "task", "project", "task")
		if err != nil || !found || scope.TotalTokens != tokens || scope.ReceiptCount != 1 {
			t.Fatalf("scope: %+v %v", scope, err)
		}
	}
	before, _, _ := store.GetUsageScope("usage-account", "task", "project", "task")
	turn.AccountScopeID = "foreign"
	if _, _, _, err := svc.RecordTurnUsage(session.ID, turn); err == nil {
		t.Fatal("foreign usage accepted")
	}
	after, _, _ := store.GetUsageScope("usage-account", "task", "project", "task")
	if before != after {
		t.Fatal("foreign usage changed projection")
	}
	usageEvents, resetEvents := 0, 0
	if err := db.IteratePrefix(pebblestore.V3RealtimeOutboxPrefix(), 20, func(_ string, data []byte) error {
		var record pebblestore.V3RealtimeOutboxRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		switch record.Event.EventType {
		case "run.usage.updated":
			usageEvents++
		case "session.usage.reset":
			resetEvents++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if usageEvents != 5 || resetEvents != 1 {
		t.Fatalf("atomic outbox usage=%d reset=%d", usageEvents, resetEvents)
	}
}
