package pebblestore

import (
	"testing"
	"time"
)

func requestSample(account string, input, read, write, output int64) map[string]any {
	return map[string]any{
		"input_tokens": input, "output_tokens": output,
		UsageRequestHistoryKey: map[string]any{
			"model": "claude-test", "account_id": account,
			"input_tokens": input, "output_tokens": output,
			"cache_read_tokens": read, "cache_write_tokens": write,
		},
	}
}

func claudeCodeTurnUsage(runID string, now int64, history []map[string]any) *SessionTurnUsageSnapshot {
	record := &SessionTurnUsageSnapshot{RunID: runID, Provider: "claudecode", Model: "claude-test", Source: "claudecode_cli_usage", ContextWindow: 200000, CreatedAt: now, APIUsageHistory: history}
	ApplyUsageRequestReceipts(record, UsageRequestReceiptsFromHistory(history, 1))
	return record
}

func recordTurnUsage(t *testing.T, s *SessionStore, sessionID, request string, now int64, usage *SessionTurnUsageSnapshot) V3SessionMutationResult {
	t.Helper()
	in := lockTestMessage(sessionID, request)
	in.NowUnixMs = now
	in.TurnUsage = usage
	result, err := s.ApplyV3SessionMutation(in)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Purpose: every usage view must be the sum of per-request receipts. A Claude
// Code run once recorded only its last request, so dashboards, task cost and
// the daily total showed a few percent of real consumption and a replaying
// session was invisible. ApplyV3SessionMutation over a real temporary Pebble
// store is the narrowest layer owning the receipt record, session summary,
// task scope, daily accumulator and account rollup in one batch. Negative
// cases: replacing a run's record with more requests must not double count,
// context occupancy must stay the last request (never the sum), and a
// subscription receipt keeps cost 0 with no invented nominal price.
func TestRequestReceiptsSumIntoEveryUsageView(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "session-1")
	task := &ProjectTaskRecord{ID: "task-1", ProjectID: "project-1", Agent: "coder", Status: "in_progress", Title: "usage", SessionID: "session-1"}
	if err := s.PutProjectTask("account-1", task); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := s.GetSession("session-1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Metadata = map[string]any{"project_id": "project-1", "task_id": "task-1"}
	if err := s.UpdateSession(snapshot); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	date := time.UnixMilli(now).UTC().Format("2006-01-02")

	// Turn 1 first records two requests, then its final record with three:
	// a replacement of the same run, as the run loop writes after each step.
	turnOne := []map[string]any{requestSample("slot-a", 10, 1000, 200, 50), requestSample("slot-a", 5, 1200, 0, 300)}
	recordTurnUsage(t, s, "session-1", "turn-1-partial", now, claudeCodeTurnUsage("run-1", now, turnOne))
	turnOne = append(turnOne, requestSample("slot-a", 3, 1500, 0, 700))
	recordTurnUsage(t, s, "session-1", "turn-1", now, claudeCodeTurnUsage("run-1", now, turnOne))
	turnTwo := []map[string]any{requestSample("slot-b", 20, 2000, 0, 40), requestSample("slot-b", 4, 2100, 0, 90)}
	result := recordTurnUsage(t, s, "session-1", "turn-2", now+1, claudeCodeTurnUsage("run-2", now+1, turnTwo))

	const turnOneTotal, turnTwoTotal = 1260 + 1505 + 2203, 2060 + 2194
	record, ok, err := s.GetTurnUsage("session-1", "run-1")
	if err != nil || !ok {
		t.Fatalf("run-1 record: ok=%v err=%v", ok, err)
	}
	if record.RequestCount != 3 || len(record.Requests) != 3 || record.BilledTokens != turnOneTotal || record.BilledCacheReadTokens != 3700 {
		t.Fatalf("run-1 receipts = %d/%d billed=%d read=%d", record.RequestCount, len(record.Requests), record.BilledTokens, record.BilledCacheReadTokens)
	}
	for i, request := range record.Requests {
		if request.Index != i+1 || request.AccountID != "slot-a" || request.PriceStatus != "subscription" || request.EstimatedCostUSD != 0 || request.NominalCostUSD != 0 {
			t.Fatalf("request %d = %+v", i, request)
		}
	}
	if record.TotalTokens != 2203 || record.FirstRequestContextTokens != 1010 || record.EstimatedCostUSD != 0 || record.PriceStatus != "subscription" {
		t.Fatalf("run-1 occupancy/first request/cost = %d/%d/%v/%s", record.TotalTokens, record.FirstRequestContextTokens, record.EstimatedCostUSD, record.PriceStatus)
	}

	summary := result.UsageSummary
	if summary == nil || summary.TurnCount != 2 || summary.RequestCount != 5 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.TotalTokens != 2194 || summary.RemainingTokens != 200000-2194 {
		t.Fatalf("occupancy must be the last request, got total=%d remaining=%d", summary.TotalTokens, summary.RemainingTokens)
	}

	acc, ok, err := s.GetDailyUsageAccumulator("account-1", date)
	if err != nil || !ok || acc.TotalTokens != turnOneTotal+turnTwoTotal || acc.TurnCount != 2 || acc.TotalCostUSD != 0 {
		t.Fatalf("daily = %+v ok=%v err=%v", acc, ok, err)
	}
	roll, ok, err := s.GetAccountUsageRollup("account-1", date, "session-1", "claudecode", "claude-test")
	if err != nil || !ok || roll.TotalTokens != turnOneTotal+turnTwoTotal || roll.Turns != 2 {
		t.Fatalf("rollup = %+v ok=%v err=%v", roll, ok, err)
	}
	scope, ok, err := s.GetUsageScope("account-1", "task", "project-1", "task-1")
	if err != nil || !ok {
		t.Fatalf("task scope ok=%v err=%v", ok, err)
	}
	if scope.ReceiptCount != 5 || scope.SubscriptionReceipts != 5 || scope.TotalTokens != turnOneTotal+turnTwoTotal || scope.CatalogCostUSD != 0 || scope.UnknownReceipts != 0 {
		t.Fatalf("task scope = %+v", scope)
	}
	day, ok, err := s.GetUsageScopeDay("account-1", "task", "project-1", "task-1", date)
	if err != nil || !ok || day.ReceiptCount != 5 || day.TotalTokens != turnOneTotal+turnTwoTotal {
		t.Fatalf("task scope day = %+v ok=%v err=%v", day, ok, err)
	}

	turns, err := s.ListTurnUsage("session-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	growth := SessionContextGrowth(turns)
	if len(growth) != 2 || growth[0].RunID != "run-1" || growth[0].FirstRequestContextTokens != 1010 || growth[0].RequestCount != 3 ||
		growth[1].RunID != "run-2" || growth[1].FirstRequestContextTokens != 2020 || growth[1].RequestCount != 2 {
		t.Fatalf("context growth = %+v", growth)
	}
}

// Purpose: a record without per-request receipts (other providers, or a turn
// whose requests were not observed) keeps its reported totals and counts as
// one receipt; it never appears in context growth as a measured size.
func TestRecordWithoutRequestReceiptsKeepsLegacyCounting(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "session-1")
	now := time.Now().UTC().UnixMilli()
	result := recordTurnUsage(t, s, "session-1", "plain", now, &SessionTurnUsageSnapshot{RunID: "run-1", Provider: "test", Model: "test", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, EstimatedCostUSD: 0.125, CostProvenance: "provider", PriceStatus: "known", CreatedAt: now})
	if result.UsageSummary == nil || result.UsageSummary.RequestCount != 1 || result.UsageSummary.TurnCount != 1 {
		t.Fatalf("summary = %+v", result.UsageSummary)
	}
	if growth := SessionContextGrowth([]SessionTurnUsageSnapshot{*result.TurnUsage}); len(growth) != 0 {
		t.Fatalf("growth = %+v, want none", growth)
	}
	var record SessionTurnUsageSnapshot
	ApplyUsageRequestReceipts(&record, nil)
	if record.BilledUsagePresent || record.RequestCount != 0 {
		t.Fatalf("empty receipts changed the record: %+v", record)
	}
}

// Purpose: the stored per-request detail is bounded, but the count and the
// billed totals still cover every request.
func TestRequestReceiptDetailIsBoundedNotTheTotals(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	createV3SessionForTest(t, s, "session-1")
	now := time.Now().UTC().UnixMilli()
	history := make([]map[string]any, 0, maxStoredUsageRequests+3)
	for i := 0; i < maxStoredUsageRequests+3; i++ {
		history = append(history, requestSample("slot-a", 1, 0, 0, 1))
	}
	result := recordTurnUsage(t, s, "session-1", "many", now, claudeCodeTurnUsage("run-1", now, history))
	record := result.TurnUsage
	if record == nil || record.RequestCount != maxStoredUsageRequests+3 || len(record.Requests) != maxStoredUsageRequests || record.RequestsOmitted != 3 || record.BilledTokens != 2*(maxStoredUsageRequests+3) {
		t.Fatalf("record = count %d stored %d omitted %d billed %d", record.RequestCount, len(record.Requests), record.RequestsOmitted, record.BilledTokens)
	}
	if result.UsageSummary.RequestCount != maxStoredUsageRequests+3 {
		t.Fatalf("summary requests = %d", result.UsageSummary.RequestCount)
	}
}
