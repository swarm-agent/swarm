package run

import (
	"math"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func taggedUsage(samples ...[4]int64) provideriface.TokenUsage {
	usage := provideriface.TokenUsage{Source: "claudecode_cli_usage", APIUsageRawPath: "usage"}
	for _, s := range samples {
		input, read, write, output := s[0], s[1], s[2], s[3]
		usage.InputTokens += input
		usage.CacheReadTokens += read
		usage.CacheWriteTokens += write
		usage.OutputTokens += output
		usage.TotalTokens += input + read + write + output
		usage.APIUsageHistory = append(usage.APIUsageHistory, map[string]any{pebblestore.UsageRequestHistoryKey: map[string]any{
			"account_id": "slot-a", "input_tokens": input, "cache_read_tokens": read, "cache_write_tokens": write, "output_tokens": output,
		}})
	}
	return usage
}

// Purpose: the run loop records one usage record per run, and that record
// must be the sum of every provider request across the run's steps (and
// across attempts spent waiting on an account limit), with each request a
// priced receipt. Service.recordProviderUsageSnapshot over the real session
// service and store is the narrowest layer that turns run-loop receipts into
// the committed record. Negative cases: a step without receipts makes the run
// fall back to its reported totals instead of a partial receipt sum, and the
// limit-wait merge must not drop the waiting attempt's receipts.
func TestRunUsageRecordIsTheSumOfItsRequestReceipts(t *testing.T) {
	f := newDesignModelFixture(t)
	var requests runUsageRequests
	stepOne := mergeAttemptUsage(taggedUsage([4]int64{10, 1000, 200, 50}), taggedUsage([4]int64{5, 1200, 0, 300}))
	if receipts := pebblestore.UsageRequestReceiptsFromHistory(stepOne.APIUsageHistory, 1); len(receipts) != 2 || stepOne.TotalTokens != 1260+1505 {
		t.Fatalf("limit-wait merge kept %d receipts, total %d", len(receipts), stepOne.TotalTokens)
	}
	stepTwo := taggedUsage([4]int64{3, 1500, 0, 700})
	requests.observe(stepOne, 1)
	requests.observe(stepTwo, 2)
	receipts := requests.receipts()
	if len(receipts) != 3 || receipts[0].Step != 1 || receipts[2].Step != 2 {
		t.Fatalf("receipts = %+v", receipts)
	}
	accumulated := mergeTokenUsage(stepOne, stepTwo)
	const total = 1260 + 1505 + 2203
	committed, summary, _, err := f.service.recordProviderUsageSnapshot(f.sessionID, "run-1", "fixture-provider", "swarm-model", 100000, 2, accumulated, receipts, f.principal, nil, total, 18, 1050, 3700, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if committed.RequestCount != 3 || len(committed.Requests) != 3 || committed.BilledTokens != total || committed.TotalTokens != 2203 || committed.FirstRequestContextTokens != 1010 {
		t.Fatalf("committed = count %d stored %d billed %d occupancy %d first %d", committed.RequestCount, len(committed.Requests), committed.BilledTokens, committed.TotalTokens, committed.FirstRequestContextTokens)
	}
	var receiptCost float64
	for _, request := range committed.Requests {
		if request.PriceStatus != "known" || request.AccountID != "slot-a" {
			t.Fatalf("request = %+v", request)
		}
		receiptCost += request.EstimatedCostUSD
	}
	if committed.EstimatedCostUSD <= 0 || math.Abs(receiptCost-committed.EstimatedCostUSD) > 1e-12 {
		t.Fatalf("record cost %v != receipt cost sum %v", committed.EstimatedCostUSD, receiptCost)
	}
	if summary.RequestCount != 3 || summary.TurnCount != 1 {
		t.Fatalf("summary = %+v", summary)
	}

	requests.observe(provideriface.TokenUsage{Source: "claudecode_cli_usage", InputTokens: 4, TotalTokens: 4}, 3)
	if got := requests.receipts(); got != nil {
		t.Fatalf("incomplete run reported receipts %+v", got)
	}
	if got := requests.withLate(stepTwo, 4); got != nil {
		t.Fatalf("incomplete run reported late receipts %+v", got)
	}
}
