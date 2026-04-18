package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestSessionUsageEndpointReturnsSummaryAndTurnRecords(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-usage-api.pebble"))
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

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer("test", nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, eventLog, stream.NewHub(nil))
	handler := server.Handler()

	session, _, err := sessionSvc.CreateSession("Usage", t.TempDir(), "workspace")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, _, err := sessionSvc.RecordTurnUsage(session.ID, pebblestore.SessionTurnUsageSnapshot{
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
			"input_tokens":  float64(90),
			"output_tokens": float64(30),
			"total_tokens":  float64(120),
		},
		APIUsageRawPath: "response.usage",
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	var resp struct {
		OK               bool                                   `json:"ok"`
		SessionID        string                                 `json:"session_id"`
		HasUsageSummary  bool                                   `json:"has_usage_summary"`
		UsageSummary     pebblestore.SessionUsageSummary        `json:"usage_summary"`
		TurnUsageRecords []pebblestore.SessionTurnUsageSnapshot `json:"turn_usage_records"`
	}
	status := doJSONRequest(t, handler, http.MethodGet, fmt.Sprintf("/v1/sessions/%s/usage?limit=10", session.ID), nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("usage status = %d, want %d", status, http.StatusOK)
	}
	if !resp.OK {
		t.Fatalf("ok = false")
	}
	if !resp.HasUsageSummary {
		t.Fatalf("has_usage_summary = false")
	}
	if resp.UsageSummary.TotalTokens != 120 {
		t.Fatalf("summary total tokens = %d, want 120", resp.UsageSummary.TotalTokens)
	}
	if len(resp.TurnUsageRecords) != 1 {
		t.Fatalf("turn usage records = %d, want 1", len(resp.TurnUsageRecords))
	}
	if got := resp.TurnUsageRecords[0].CacheReadTokens; got != 40 {
		t.Fatalf("cache read tokens = %d, want 40", got)
	}
	if got := resp.TurnUsageRecords[0].APIUsageRawPath; got != "" {
		t.Fatalf("api usage raw path = %q, want empty", got)
	}
	if resp.TurnUsageRecords[0].APIUsageRaw != nil {
		t.Fatalf("expected api usage raw payload to be omitted, got %#v", resp.TurnUsageRecords[0].APIUsageRaw)
	}
}
