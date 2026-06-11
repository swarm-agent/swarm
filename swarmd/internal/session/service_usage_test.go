package session

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRecordTurnUsageCodexRemainingUsesLatestProviderSnapshot(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-usage-codex-remaining.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := svc.CreateSessionWithOptions(CreateSessionOptions{
		Title:         "Codex usage repro",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Mode:          "auto",
		Preference:    &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.5", Thinking: "high", ServiceTier: "fast"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Exact run.usage.updated sequence from session 915479a20d8e41f71570440d461a4e17.
	// Codex response.usage is a current provider snapshot; summing these across turns
	// double-counts retained context and incorrectly drives remaining context to zero.
	turns := []pebblestore.SessionTurnUsageSnapshot{
		{RunID: "v3run_23a9e18d7192daf18d2bd23c0c7d2d72", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 57034, OutputTokens: 118, TotalTokens: 57152},
		{RunID: "v3run_cdcbc130ba77f5e12f5d8bf6a4f7f0ca", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 129463, OutputTokens: 196, TotalTokens: 129659, CacheReadTokens: 104960},
		{RunID: "v3run_3551b774e17e5d0d6c2ff5289fcd689e", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 145072, OutputTokens: 220, TotalTokens: 145292, CacheReadTokens: 135168},
		{RunID: "v3run_a88e579cc3711df8b317bcd62339cd90", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 153524, OutputTokens: 121, TotalTokens: 153645, CacheReadTokens: 153248},
		{RunID: "v3run_2f1511b7def3bb516447f3bb0f0c4184", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 153942, OutputTokens: 87, TotalTokens: 154029, CacheReadTokens: 145920},
		{RunID: "v3run_70899e12f13ceb518549a3cd126536c5", Provider: "codex", Model: "gpt-5.5", Source: "codex_api_usage", Transport: "websocket", ContextWindow: 272000, InputTokens: 154038, OutputTokens: 37, TotalTokens: 154075, CacheReadTokens: 153600},
	}

	var summary pebblestore.SessionUsageSummary
	for _, turn := range turns {
		_, gotSummary, _, err := svc.RecordTurnUsage(session.ID, turn)
		if err != nil {
			t.Fatalf("record %s: %v", turn.RunID, err)
		}
		summary = gotSummary
	}

	if summary.TotalTokens != 154075 {
		t.Fatalf("summary total tokens = %d, want latest normalized codex total 154075", summary.TotalTokens)
	}
	if summary.RemainingTokens != 117925 {
		t.Fatalf("summary remaining tokens = %d, want latest codex snapshot remaining 117925", summary.RemainingTokens)
	}
}
