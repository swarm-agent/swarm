package pebblestore

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
)

// UsageRequestHistoryKey tags a provider usage history sample that describes
// exactly one provider request. A provider adapter adds it only when it saw
// that request's own usage; untagged history keeps the one-receipt-per-record
// meaning, so no request is ever inferred.
const UsageRequestHistoryKey = "swarm_request"

// maxStoredUsageRequests bounds the per-request receipts kept on one usage
// record. Totals, RequestCount and nominal cost always cover every request;
// only the stored detail is bounded.
const maxStoredUsageRequests = 512

// SessionUsageRequestReceipt is the usage one provider request reported. It is
// the unit a usage record's billed totals are summed from.
type SessionUsageRequestReceipt struct {
	Index            int     `json:"index"`
	Step             int     `json:"step,omitempty"`
	Model            string  `json:"model,omitempty"`
	AccountID        string  `json:"account_id,omitempty"`
	MessageID        string  `json:"message_id,omitempty"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	PriceStatus      string  `json:"price_status,omitempty"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	// What the request would cost at the provider's metered catalog price when
	// it was paid by a subscription; reporting only, never counted as spend.
	NominalCostUSD float64 `json:"nominal_cost_usd,omitempty"`
}

// SessionContextGrowthTurn reports, for one recorded turn, the context its
// first provider request re-read and how many requests the turn made. A
// replaying session shows as a growing first-request size.
type SessionContextGrowthTurn struct {
	RunID                       string `json:"run_id"`
	Step                        int    `json:"step,omitempty"`
	Provider                    string `json:"provider,omitempty"`
	Model                       string `json:"model,omitempty"`
	CreatedAt                   int64  `json:"created_at"`
	FirstRequestInputTokens     int64  `json:"first_request_input_tokens"`
	FirstRequestCacheReadTokens int64  `json:"first_request_cache_read_tokens"`
	FirstRequestContextTokens   int64  `json:"first_request_context_tokens"`
	RequestCount                int64  `json:"request_count"`
}

// UsageRequestReceiptsFromHistory returns one receipt per tagged history
// sample, in provider order, attributed to step.
func UsageRequestReceiptsFromHistory(history []map[string]any, step int) []SessionUsageRequestReceipt {
	var out []SessionUsageRequestReceipt
	for _, sample := range history {
		request, ok := sample[UsageRequestHistoryKey].(map[string]any)
		if !ok || len(request) == 0 {
			continue
		}
		receipt := SessionUsageRequestReceipt{
			Step:             step,
			Model:            usageRequestString(request["model"]),
			AccountID:        usageRequestString(request["account_id"]),
			MessageID:        usageRequestString(request["message_id"]),
			InputTokens:      usageRequestInt(request["input_tokens"]),
			OutputTokens:     usageRequestInt(request["output_tokens"]),
			CacheReadTokens:  usageRequestInt(request["cache_read_tokens"]),
			CacheWriteTokens: usageRequestInt(request["cache_write_tokens"]),
		}
		receipt.TotalTokens = receipt.InputTokens + receipt.OutputTokens + receipt.CacheReadTokens + receipt.CacheWriteTokens
		out = append(out, receipt)
	}
	return out
}

// ApplyUsageRequestReceipts makes a usage record the sum of its request
// receipts: billed totals are the sum, context occupancy is the last request
// (what the context holds now) and the first request is the context re-read.
// A record with no receipts is left unchanged.
func ApplyUsageRequestReceipts(record *SessionTurnUsageSnapshot, requests []SessionUsageRequestReceipt) {
	if record == nil || len(requests) == 0 {
		return
	}
	receipts := make([]SessionUsageRequestReceipt, len(requests))
	copy(receipts, requests)
	var input, output, read, write int64
	for i := range receipts {
		receipts[i].Index = i + 1
		receipts[i].InputTokens = clampUsageTokenCount(receipts[i].InputTokens)
		receipts[i].OutputTokens = clampUsageTokenCount(receipts[i].OutputTokens)
		receipts[i].CacheReadTokens = clampUsageTokenCount(receipts[i].CacheReadTokens)
		receipts[i].CacheWriteTokens = clampUsageTokenCount(receipts[i].CacheWriteTokens)
		receipts[i].TotalTokens = receipts[i].InputTokens + receipts[i].OutputTokens + receipts[i].CacheReadTokens + receipts[i].CacheWriteTokens
		input += receipts[i].InputTokens
		output += receipts[i].OutputTokens
		read += receipts[i].CacheReadTokens
		write += receipts[i].CacheWriteTokens
	}
	first, last := receipts[0], receipts[len(receipts)-1]
	// Request receipts carry no thinking split; keep the billed thinking the
	// caller recorded, if any.
	if !record.BilledUsagePresent {
		record.BilledThinkingTokens = record.ThinkingTokens
	}
	record.BilledUsagePresent = true
	record.BilledInputTokens = input
	record.BilledOutputTokens = output
	record.BilledCacheReadTokens = read
	record.BilledCacheWriteTokens = write
	record.BilledThinkingTokens = clampUsageTokenCount(record.BilledThinkingTokens)
	record.BilledTokens = input + output + read + write + record.BilledThinkingTokens
	record.InputTokens = last.InputTokens
	record.OutputTokens = last.OutputTokens
	record.CacheReadTokens = last.CacheReadTokens
	record.CacheWriteTokens = last.CacheWriteTokens
	record.TotalTokens = last.TotalTokens
	record.FirstRequestInputTokens = first.InputTokens
	record.FirstRequestCacheReadTokens = first.CacheReadTokens
	record.FirstRequestContextTokens = first.InputTokens + first.CacheReadTokens
	record.RequestCount = int64(len(receipts))
	record.Requests = receipts
}

// usageReceiptWeight is the number of provider requests a usage record stands
// for: its request receipts when the provider reported them, else one.
func usageReceiptWeight(record SessionTurnUsageSnapshot) int64 {
	if record.RequestCount > 0 {
		return record.RequestCount
	}
	return 1
}

// priceUsageRequests prices each request receipt on its own and bounds the
// stored detail. Subscription receipts keep cost 0; their nominal metered
// cost is computed per receipt and summed onto the record for reporting.
// Called before the record is persisted, so totals cover every request.
func (s *SessionStore) priceUsageRequests(record *SessionTurnUsageSnapshot) {
	if record == nil || len(record.Requests) == 0 {
		return
	}
	nominal := 0.0
	for i := range record.Requests {
		request := &record.Requests[i]
		request.Model = strings.TrimSpace(request.Model)
		model := request.Model
		if model == "" {
			model = record.Model
		}
		cost, status := s.CalculateCostWithStatus(record.Provider, model, request.InputTokens, request.OutputTokens, request.CacheReadTokens, request.CacheWriteTokens, 0)
		request.EstimatedCostUSD, request.PriceStatus = cost, status
		request.NominalCostUSD = 0
		if status == "subscription" && strings.EqualFold(record.Provider, "claudecode") {
			// Claude Code runs Anthropic models; the nominal figure is the
			// Anthropic catalog price of this request's own tokens.
			if value, known := s.CalculateCostWithStatus("anthropic", model, request.InputTokens, request.OutputTokens, request.CacheReadTokens, request.CacheWriteTokens, 0); known == "known" && value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
				request.NominalCostUSD = value
			}
		}
		nominal += request.NominalCostUSD
	}
	record.NominalCostUSD = nominal
	if record.RequestCount < int64(len(record.Requests)) {
		record.RequestCount = int64(len(record.Requests))
	}
	if len(record.Requests) > maxStoredUsageRequests {
		record.RequestsOmitted = int64(len(record.Requests) - maxStoredUsageRequests)
		record.Requests = append([]SessionUsageRequestReceipt(nil), record.Requests[:maxStoredUsageRequests]...)
	}
}

// SessionContextGrowth lists recorded turns oldest first with their first
// request's context. Turns without request receipts are omitted rather than
// reported as a measured size.
func SessionContextGrowth(records []SessionTurnUsageSnapshot) []SessionContextGrowthTurn {
	out := make([]SessionContextGrowthTurn, 0, len(records))
	for _, record := range records {
		if record.RequestCount <= 0 {
			continue
		}
		out = append(out, SessionContextGrowthTurn{
			RunID:                       record.RunID,
			Step:                        record.Steps,
			Provider:                    record.Provider,
			Model:                       record.Model,
			CreatedAt:                   record.CreatedAt,
			FirstRequestInputTokens:     record.FirstRequestInputTokens,
			FirstRequestCacheReadTokens: record.FirstRequestCacheReadTokens,
			FirstRequestContextTokens:   record.FirstRequestContextTokens,
			RequestCount:                record.RequestCount,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].RunID < out[j].RunID
	})
	return out
}

func usageRequestString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func usageRequestInt(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return max(typed, 0)
	case int:
		return int64(max(typed, 0))
	case float64:
		if typed < 0 || math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0
		}
		return int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed < 0 {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
