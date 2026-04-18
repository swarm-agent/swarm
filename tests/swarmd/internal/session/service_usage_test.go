package session

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRecordTurnUsageAggregatesAndReplacesByRunID(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-usage.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := svc.CreateSession("Usage Session", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	turn1, summary1, _, err := svc.RecordTurnUsage(session.ID, pebblestore.SessionTurnUsageSnapshot{
		RunID:            "run_1",
		Provider:         "codex",
		Model:            "gpt-5-codex",
		Source:           "codex_api_usage",
		ContextWindow:    400000,
		Steps:            2,
		InputTokens:      100,
		OutputTokens:     20,
		ThinkingTokens:   10,
		CacheReadTokens:  50,
		CacheWriteTokens: 5,
		TotalTokens:      120,
	})
	if err != nil {
		t.Fatalf("record turn1 usage: %v", err)
	}
	if turn1.RunID != "run_1" {
		t.Fatalf("turn1 run id = %q", turn1.RunID)
	}
	if summary1.TurnCount != 1 {
		t.Fatalf("summary turn count = %d, want 1", summary1.TurnCount)
	}
	if summary1.TotalTokens != 120 {
		t.Fatalf("summary total tokens = %d, want 120", summary1.TotalTokens)
	}
	if summary1.RemainingTokens != 399880 {
		t.Fatalf("summary remaining tokens = %d, want 399880", summary1.RemainingTokens)
	}

	_, summary2, _, err := svc.RecordTurnUsage(session.ID, pebblestore.SessionTurnUsageSnapshot{
		RunID:            "run_2",
		Provider:         "codex",
		Model:            "gpt-5-codex",
		Source:           "codex_api_usage",
		ContextWindow:    400000,
		Steps:            3,
		InputTokens:      80,
		OutputTokens:     40,
		ThinkingTokens:   5,
		CacheReadTokens:  30,
		CacheWriteTokens: 0,
		TotalTokens:      120,
	})
	if err != nil {
		t.Fatalf("record turn2 usage: %v", err)
	}
	if summary2.TurnCount != 2 {
		t.Fatalf("summary turn count = %d, want 2", summary2.TurnCount)
	}
	if summary2.InputTokens != 180 {
		t.Fatalf("summary input tokens = %d, want 180", summary2.InputTokens)
	}
	if summary2.OutputTokens != 60 {
		t.Fatalf("summary output tokens = %d, want 60", summary2.OutputTokens)
	}
	if summary2.ThinkingTokens != 15 {
		t.Fatalf("summary thinking tokens = %d, want 15", summary2.ThinkingTokens)
	}
	if summary2.CacheReadTokens != 80 {
		t.Fatalf("summary cache read tokens = %d, want 80", summary2.CacheReadTokens)
	}
	if summary2.TotalTokens != 240 {
		t.Fatalf("summary total tokens = %d, want 240", summary2.TotalTokens)
	}

	_, summary3, _, err := svc.RecordTurnUsage(session.ID, pebblestore.SessionTurnUsageSnapshot{
		RunID:            "run_2",
		Provider:         "codex",
		Model:            "gpt-5-codex",
		Source:           "codex_api_usage",
		ContextWindow:    400000,
		Steps:            1,
		InputTokens:      60,
		OutputTokens:     10,
		ThinkingTokens:   3,
		CacheReadTokens:  10,
		CacheWriteTokens: 2,
		TotalTokens:      70,
	})
	if err != nil {
		t.Fatalf("replace turn2 usage: %v", err)
	}
	if summary3.TurnCount != 2 {
		t.Fatalf("summary turn count = %d, want 2 after replacement", summary3.TurnCount)
	}
	if summary3.InputTokens != 160 {
		t.Fatalf("summary input tokens = %d, want 160", summary3.InputTokens)
	}
	if summary3.OutputTokens != 30 {
		t.Fatalf("summary output tokens = %d, want 30", summary3.OutputTokens)
	}
	if summary3.ThinkingTokens != 13 {
		t.Fatalf("summary thinking tokens = %d, want 13", summary3.ThinkingTokens)
	}
	if summary3.CacheReadTokens != 60 {
		t.Fatalf("summary cache read tokens = %d, want 60", summary3.CacheReadTokens)
	}
	if summary3.CacheWriteTokens != 7 {
		t.Fatalf("summary cache write tokens = %d, want 7", summary3.CacheWriteTokens)
	}
	if summary3.TotalTokens != 190 {
		t.Fatalf("summary total tokens = %d, want 190", summary3.TotalTokens)
	}
	if summary3.RemainingTokens != 399810 {
		t.Fatalf("summary remaining tokens = %d, want 399810", summary3.RemainingTokens)
	}

	storedSummary, ok, err := svc.GetUsageSummary(session.ID)
	if err != nil {
		t.Fatalf("get usage summary: %v", err)
	}
	if !ok {
		t.Fatal("expected usage summary to exist")
	}
	if storedSummary.TotalTokens != 190 {
		t.Fatalf("stored summary total tokens = %d, want 190", storedSummary.TotalTokens)
	}

	turns, err := svc.ListTurnUsage(session.ID, 10)
	if err != nil {
		t.Fatalf("list turn usage: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turn usage records = %d, want 2", len(turns))
	}
	if turns[0].RunID != "run_2" {
		t.Fatalf("latest run id = %q, want run_2", turns[0].RunID)
	}
	if turns[0].TotalTokens != 70 {
		t.Fatalf("latest run total tokens = %d, want 70", turns[0].TotalTokens)
	}
}

func TestRecordTurnUsageGoogleRemainingUsesAPISnapshotOnly(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-usage-google-remaining.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := svc.CreateSession("Usage Session", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, _, _, err := svc.RecordTurnUsage(session.ID, pebblestore.SessionTurnUsageSnapshot{
		RunID:         "run_1",
		Provider:      "codex",
		Model:         "gpt-5-codex",
		Source:        "codex_api_usage",
		ContextWindow: 400000,
		InputTokens:   120,
		OutputTokens:  30,
		TotalTokens:   150,
	}); err != nil {
		t.Fatalf("record run_1 usage: %v", err)
	}

	_, summary, _, err := svc.RecordTurnUsage(session.ID, pebblestore.SessionTurnUsageSnapshot{
		RunID:         "run_2",
		Provider:      "google",
		Model:         "gemini-2.5-pro",
		Source:        "google_api_usage",
		ContextWindow: 400000,
		InputTokens:   90,
		OutputTokens:  25,
		TotalTokens:   0, // Simulate missing totalTokenCount in API payload.
	})
	if err != nil {
		t.Fatalf("record run_2 usage: %v", err)
	}

	if summary.TotalTokens != 150 {
		t.Fatalf("summary total tokens = %d, want 150", summary.TotalTokens)
	}
	if summary.RemainingTokens != 399910 {
		t.Fatalf("summary remaining tokens = %d, want 399910", summary.RemainingTokens)
	}
}

func TestRecordTurnUsageDoesNotPersistRawAPIUsagePayloads(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-usage-redaction.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), eventLog)
	session, _, err := svc.CreateSession("Usage", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	turn, _, _, err := svc.RecordTurnUsage(session.ID, pebblestore.SessionTurnUsageSnapshot{
		RunID:            "run_1",
		Provider:         "codex",
		Model:            "gpt-5-codex",
		Source:           "codex_api_usage",
		ContextWindow:    400000,
		Steps:            1,
		InputTokens:      90,
		OutputTokens:     30,
		CacheReadTokens:  40,
		CacheWriteTokens: 8,
		TotalTokens:      120,
		APIUsageRaw: map[string]any{
			"access_token": "secret-token",
			"total_tokens": float64(120),
		},
		APIUsageRawPath: "response.usage",
		APIUsageHistory: []map[string]any{{"access_token": "secret-token"}},
		APIUsagePaths:   []string{"response.usage"},
	})
	if err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if turn.APIUsageRaw != nil || turn.APIUsageRawPath != "" || len(turn.APIUsageHistory) != 0 || len(turn.APIUsagePaths) != 0 {
		t.Fatalf("expected raw API usage fields to be cleared, got %+v", turn)
	}

	stored, ok, err := svc.GetTurnUsage(session.ID, "run_1")
	if err != nil {
		t.Fatalf("get turn usage: %v", err)
	}
	if !ok {
		t.Fatal("expected stored turn usage")
	}
	if stored.APIUsageRaw != nil || stored.APIUsageRawPath != "" || len(stored.APIUsageHistory) != 0 || len(stored.APIUsagePaths) != 0 {
		t.Fatalf("stored turn usage should not retain raw api payloads: %+v", stored)
	}
}

func TestAppendMessageSanitizesAuthCommandAndMetadata(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-message-redaction.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := svc.CreateSession("Auth", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	message, _, _, err := svc.AppendMessage(session.ID, "user", "/auth key codex sk-secret-1234567890", map[string]any{
		"source":       "command",
		"access_token": "token-secret",
	})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
	if strings.Contains(message.Content, "sk-secret") {
		t.Fatalf("message content leaked secret: %s", message.Content)
	}
	if strings.Contains(fmt.Sprint(message.Metadata), "token-secret") {
		t.Fatalf("message metadata leaked secret: %#v", message.Metadata)
	}

	stored, err := svc.ListMessages(session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored messages = %d, want 1", len(stored))
	}
	if strings.Contains(stored[0].Content, "sk-secret") {
		t.Fatalf("stored message content leaked secret: %s", stored[0].Content)
	}
}
