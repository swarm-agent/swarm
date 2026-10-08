package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/cockroachdb/pebble"
)

// UsageScopeTotal is a projection of existing provider receipts, never another
// charge ledger. HistoryComplete remains false until an explicit audited migration
// establishes coverage; absent records must not be presented as measured zero.
type UsageScopeTotal struct {
	Kind                       string  `json:"kind"`
	ID                         string  `json:"id"`
	ProjectID                  string  `json:"project_id,omitempty"`
	TotalTokens                int64   `json:"total_tokens"`
	InputTokens                int64   `json:"input_tokens"`
	OutputTokens               int64   `json:"output_tokens"`
	CacheReadTokens            int64   `json:"cache_read_tokens"`
	CacheWriteTokens           int64   `json:"cache_write_tokens"`
	ThinkingTokens             int64   `json:"thinking_tokens"`
	MediaCostUSD               float64 `json:"media_cost_usd"`
	MediaReceipts              int64   `json:"media_receipts"`
	Coverage                   string  `json:"coverage"`
	CatalogCostUSD             float64 `json:"catalog_cost_usd"`
	ProviderCostUSD            float64 `json:"provider_cost_usd"`
	ProviderEstimateCostUSD    float64 `json:"provider_estimate_cost_usd"`
	NominalSubscriptionCostUSD float64 `json:"nominal_subscription_cost_usd"`
	UnknownReceipts            int64   `json:"unknown_receipts"`
	FreeReceipts               int64   `json:"free_receipts"`
	SubscriptionReceipts       int64   `json:"subscription_receipts"`
	ReceiptCount               int64   `json:"receipt_count"`
	HistoryComplete            bool    `json:"history_complete"`
	Revision                   uint64  `json:"revision"`
}

func usageScopeKey(account string, scope UsageScopeTotal) string {
	return fmt.Sprintf("usage_scope/%s/%s/%s/%s", keyPart(account), keyPart(scope.Kind), keyPart(scope.ProjectID), keyPart(scope.ID))
}

func (s *SessionStore) GetUsageScope(account, kind, project, id string) (UsageScopeTotal, bool, error) {
	total := UsageScopeTotal{Kind: kind, ID: id, ProjectID: project, Coverage: "no_records"}
	if strings.TrimSpace(account) == "" || strings.TrimSpace(id) == "" {
		return total, false, errors.New("usage scope account and id are required")
	}
	ok, err := s.store.GetJSON(usageScopeKey(account, total), &total)
	return total, ok, err
}

// Resolve only durable same-account lineage. Metadata names are hints; task
// attempts and worker-run receipts must corroborate ownership before attribution.
func (s *SessionStore) resolveUsageScopes(account, sessionID string) ([]UsageScopeTotal, error) {
	var scopes []UsageScopeTotal
	seenScopes := map[string]bool{}
	seenSessions := map[string]bool{}
	add := func(scope UsageScopeTotal) {
		key := usageScopeKey(account, scope)
		if !seenScopes[key] {
			scopes = append(scopes, scope)
			seenScopes[key] = true
		}
	}
	for depth := 0; sessionID != "" && depth < 32; depth++ {
		if seenSessions[sessionID] {
			return nil, errors.New("usage lineage cycle")
		}
		seenSessions[sessionID] = true
		snapshot, ok, err := s.GetSession(sessionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			if depth == 0 {
				return nil, nil
			} // Legacy unbound receipt; never infer an owner.
			return nil, errors.New("usage lineage session missing")
		}
		if snapshot.AccountScopeID != account {
			return nil, errors.New("usage lineage account mismatch")
		}
		var bound []UsageScopeTotal
		if found, err := s.store.GetJSON(usageBindingKey(account, sessionID), &bound); err != nil {
			return nil, err
		} else if found {
			for _, scope := range bound {
				add(scope)
			}
			return scopes, nil
		}
		text := func(key string) string { value, _ := snapshot.Metadata[key].(string); return strings.TrimSpace(value) }
		project, taskID := text("project_id"), text("task_id")
		if project != "" && taskID != "" {
			task, found, err := s.GetProjectTask(account, project, taskID)
			if err != nil {
				return nil, err
			}
			linked := found && task.AccountID == account && task.ProjectID == project && task.ID == taskID && task.SessionID == sessionID
			if found && task.AccountID == account && task.ProjectID == project && task.ID == taskID {
				for _, attempt := range task.Attempts {
					linked = linked || attempt.SessionID == sessionID
				}
			}
			if linked {
				add(UsageScopeTotal{Kind: "task", ProjectID: project, ID: taskID})
			}
		}
		worker, run := text("worker_id"), text("worker_run_id")
		if worker != "" && run != "" {
			record, found, err := NewWorkerStore(s.store).GetWorkerRun(account, worker, run)
			if err != nil {
				return nil, err
			}
			if found && record.AccountScopeID == account && record.WorkerID == worker && record.ID == run && record.SessionID == sessionID {
				add(UsageScopeTotal{Kind: "worker", ID: worker})
				add(UsageScopeTotal{Kind: "worker_run", ProjectID: worker, ID: run})
			}
		}
		generation, found, err := s.GetDelegatedChildGenerationBySession(account, sessionID)
		if err != nil {
			return nil, err
		}
		if found {
			if generation.AccountScopeID != account || generation.SessionID != sessionID {
				return nil, errors.New("invalid usage lineage receipt")
			}
			parentSnapshot, parentFound, err := s.GetSession(generation.ParentSessionID)
			if err != nil {
				return nil, err
			}
			if !parentFound || parentSnapshot.AccountScopeID != account || parentSnapshot.UserID != snapshot.UserID {
				return nil, errors.New("usage generation ownership mismatch")
			}
			sessionID = generation.ParentSessionID
			continue
		}
		// Metadata only locates a durable program receipt; it grants no lineage.
		parent, programID := text("parent_session_id"), text("task_program_id")
		if parent == "" {
			return scopes, nil
		}
		parentSnapshot, parentFound, err := s.GetSession(parent)
		if err != nil {
			return nil, err
		}
		if !parentFound {
			return scopes, nil
		}
		if parentSnapshot.AccountScopeID != account || parentSnapshot.UserID != snapshot.UserID {
			return nil, errors.New("usage parent ownership mismatch")
		}
		if calls, ok := parentSnapshot.Metadata["task_launches"].(map[string]any); ok {
			if call, ok := calls[text("parent_task_call_id")].(map[string]any); ok {
				if rows, ok := call["launches"].([]any); ok && len(rows) <= 50 {
					for _, raw := range rows {
						if row, ok := raw.(map[string]any); ok && (row["child_session_id"] == sessionID || row["session_id"] == sessionID) {
							sessionID = parent
							break
						}
					}
				}
			}
		}
		if sessionID == parent {
			continue
		}
		if programID == "" {
			return scopes, nil
		}
		program, found, err := s.GetTaskProgram(parent, programID)
		if err != nil {
			return nil, err
		}
		linked := false
		if found && program.ParentSessionID == parent && program.ProgramID == programID {
			for _, job := range program.Jobs {
				linked = linked || job.ChildSessionID == sessionID || job.CurrentSessionID == sessionID
				for _, generation := range job.GenerationHistory {
					linked = linked || generation.SessionID == sessionID
				}
			}
		}
		if !linked {
			return scopes, nil
		}
		sessionID = parent
	}
	if sessionID != "" {
		return nil, errors.New("usage lineage exceeds bounded depth")
	}
	return scopes, nil
}

// Called under the canonical account mutation lock. Signed replacement deltas
// remove the old contribution before adding the corrected receipt, including
// unknown-to-known and subscription/free price transitions.
func (s *SessionStore) prepareUsageScopeTotals(current, previous SessionTurnUsageSnapshot) ([]UsageScopeTotal, error) {
	if snapshot, found, err := s.GetSession(current.SessionID); err != nil {
		return nil, err
	} else if found && snapshot.AccountScopeID != current.AccountScopeID {
		return nil, errors.New("usage receipt session account mismatch")
	}
	tokens, input, output, read, write, thinking := billedComponents(current)
	if tokens < 0 || input < 0 || output < 0 || read < 0 || write < 0 || thinking < 0 || current.TotalTokens < 0 || current.EstimatedCostUSD < 0 || math.IsNaN(current.EstimatedCostUSD) || math.IsInf(current.EstimatedCostUSD, 0) {
		return nil, errors.New("invalid usage cost")
	}
	scopes := previous.ScopeTotals
	var err error
	if len(scopes) == 0 {
		scopes, err = s.resolveUsageScopes(current.AccountScopeID, current.SessionID)
	}
	if err != nil {
		return nil, err
	}
	out := make([]UsageScopeTotal, 0, len(scopes))
	for _, scope := range scopes {
		total, _, err := s.GetUsageScope(current.AccountScopeID, scope.Kind, scope.ProjectID, scope.ID)
		if err != nil {
			return nil, err
		}
		if previous.ScopeTotals != nil {
			applyScopeReceipt(&total, previous, -1)
		}
		applyScopeReceipt(&total, current, 1)
		if total.Coverage != "repaired_receipts_incomplete" {
			total.Coverage = "observed_receipts_only"
		}
		total.Revision++
		out = append(out, total)
	}
	return out, nil
}

func applyScopeReceipt(total *UsageScopeTotal, receipt SessionTurnUsageSnapshot, sign int64) {
	tokens, input, output, read, write, thinking := billedComponents(receipt)
	total.TotalTokens += sign * clampUsageTokenCount(tokens)
	if sign > 0 || receipt.ScopeProjectionVersion >= 2 {
		total.InputTokens += sign * clampUsageTokenCount(input)
		total.OutputTokens += sign * clampUsageTokenCount(output)
		total.CacheReadTokens += sign * clampUsageTokenCount(read)
		total.CacheWriteTokens += sign * clampUsageTokenCount(write)
		total.ThinkingTokens += sign * clampUsageTokenCount(thinking)
	}
	total.ReceiptCount += sign
	unknown := usageUnknownCount(receipt)
	if sign < 0 && receipt.ScopeProjectionVersion < 3 {
		unknown = 0
		if receipt.PriceStatus != "known" && receipt.PriceStatus != "free" && receipt.PriceStatus != "subscription" {
			unknown = 1
		}
	}
	if unknown > 0 {
		total.UnknownReceipts += sign
	}
	cost := float64(sign) * receipt.EstimatedCostUSD
	switch strings.ToLower(receipt.PriceStatus) {
	case "free":
		total.FreeReceipts += sign
	case "subscription":
		total.SubscriptionReceipts += sign
		if strings.EqualFold(receipt.Provider, "codex") {
			nominal := nominalUsageCost(receipt)
			if sign < 0 && receipt.ScopeProjectionVersion < 2 {
				nominal = CalculateBaselineCost("openai", receipt.Model, receipt.InputTokens, receipt.OutputTokens, receipt.CacheReadTokens, receipt.ThinkingTokens)
			}
			total.NominalSubscriptionCostUSD += float64(sign) * nominal
		}
	case "known":
		if receipt.CostProvenance == "provider" {
			total.ProviderCostUSD += cost
		} else if receipt.CostProvenance == "provider_estimate" {
			total.ProviderEstimateCostUSD += cost
		} else {
			total.CatalogCostUSD += cost
		}
	default: // Unknown/partial pricing is counted independently above.
	}
}

func setUsageScopeTotals(batch *pebble.Batch, account string, totals []UsageScopeTotal) error {
	for _, total := range totals {
		payload, err := json.Marshal(total)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(usageScopeKey(account, total)), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

// Pricing uses cumulative billing components, never current context occupancy.
func (s *SessionStore) calculateReceiptCost(u SessionTurnUsageSnapshot) (float64, string) {
	_, input, output, read, write, thinking := billedComponents(u)
	return s.CalculateCostWithStatus(u.Provider, u.Model, input, output, read, write, thinking)
}

func nominalUsageCost(u SessionTurnUsageSnapshot) float64 {
	_, input, output, read, _, thinking := billedComponents(u)
	return CalculateBaselineCost("openai", u.Model, input, output, read, thinking)
}

func usageUnknownCount(u SessionTurnUsageSnapshot) int {
	if u.PriceStatus == "" || strings.EqualFold(u.PriceStatus, "unknown") || strings.EqualFold(u.ServiceTierStatus, "unknown") {
		return 1
	}
	return 0
}

// Media contributes to the same projections in the canonical receipt batch.
// Media pricing is catalog pricing; no provider-reported cost is inferred.
func (s *SessionStore) prepareMediaScopeTotals(media SessionMediaUsageRecord) ([]UsageScopeTotal, error) {
	if media.CostUSD < 0 || math.IsNaN(media.CostUSD) || math.IsInf(media.CostUSD, 0) {
		return nil, errors.New("invalid media cost")
	}
	scopes, err := s.resolveUsageScopes(media.AccountScopeID, media.SessionID)
	if err != nil {
		return nil, err
	}
	for i, scope := range scopes {
		total, _, err := s.GetUsageScope(media.AccountScopeID, scope.Kind, scope.ProjectID, scope.ID)
		if err != nil {
			return nil, err
		}
		total.MediaCostUSD += media.CostUSD
		total.MediaReceipts++
		total.ReceiptCount++
		switch strings.ToLower(media.PriceStatus) {
		case "known":
			total.CatalogCostUSD += media.CostUSD
		case "free":
			total.FreeReceipts++
		case "subscription":
			total.SubscriptionReceipts++
		default:
			total.UnknownReceipts++
		}
		if total.Coverage != "repaired_receipts_incomplete" {
			total.Coverage = "observed_receipts_only"
		}
		total.Revision++
		scopes[i] = total
	}
	return scopes, nil
}

func usageBindingKey(account, session string) string {
	return "usage_scope_binding/" + keyPart(account) + "/" + keyPart(session)
}

func setUsageBinding(batch *pebble.Batch, account, session string, scopes []UsageScopeTotal) error {
	if len(scopes) == 0 {
		return nil
	}
	payload, err := json.Marshal(scopes)
	if err != nil {
		return err
	}
	return batch.Set([]byte(usageBindingKey(account, session)), payload, nil)
}
