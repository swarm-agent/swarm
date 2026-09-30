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
	Kind string `json:"kind"`
	ID string `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	TotalTokens int64 `json:"total_tokens"`
	CatalogCostUSD float64 `json:"catalog_cost_usd"`
	ProviderCostUSD float64 `json:"provider_cost_usd"`
	NominalSubscriptionCostUSD float64 `json:"nominal_subscription_cost_usd"`
	UnknownReceipts int64 `json:"unknown_receipts"`
	FreeReceipts int64 `json:"free_receipts"`
	SubscriptionReceipts int64 `json:"subscription_receipts"`
	ReceiptCount int64 `json:"receipt_count"`
	HistoryComplete bool `json:"history_complete"`
	Revision uint64 `json:"revision"`
}

func usageScopeKey(account string, scope UsageScopeTotal) string {
	return fmt.Sprintf("usage_scope/%s/%s/%s/%s", keyPart(account), keyPart(scope.Kind), keyPart(scope.ProjectID), keyPart(scope.ID))
}

func (s *SessionStore) GetUsageScope(account, kind, project, id string) (UsageScopeTotal, bool, error) {
	total := UsageScopeTotal{Kind: kind, ID: id, ProjectID: project}
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
		if !seenScopes[key] { scopes = append(scopes, scope); seenScopes[key] = true }
	}
	for depth := 0; sessionID != "" && depth < 32; depth++ {
		if seenSessions[sessionID] { return nil, errors.New("usage lineage cycle") }
		seenSessions[sessionID] = true
		snapshot, ok, err := s.GetSession(sessionID)
		if err != nil { return nil, err }
		if !ok { break }
		if snapshot.AccountScopeID != account { return nil, errors.New("usage lineage account mismatch") }
		text := func(key string) string { value, _ := snapshot.Metadata[key].(string); return strings.TrimSpace(value) }
		project, taskID := text("project_id"), text("task_id")
		if project != "" && taskID != "" {
			task, found, err := s.GetProjectTask(account, project, taskID)
			if err != nil { return nil, err }
			linked := found && task.SessionID == sessionID
			if found { for _, attempt := range task.Attempts { linked = linked || attempt.SessionID == sessionID } }
			if linked { add(UsageScopeTotal{Kind: "task", ProjectID: project, ID: taskID}) }
		}
		worker, run := text("worker_id"), text("worker_run_id")
		if worker != "" && run != "" {
			record, found, err := NewWorkerStore(s.store).GetWorkerRun(account, worker, run)
			if err != nil { return nil, err }
			if found && record.SessionID == sessionID {
				add(UsageScopeTotal{Kind: "worker", ID: worker})
				add(UsageScopeTotal{Kind: "worker_run", ProjectID: worker, ID: run})
			}
		}
		generation, found, err := s.GetDelegatedChildGenerationBySession(account, sessionID)
		if err != nil { return nil, err }
		if !found { return scopes, nil }
		sessionID = generation.ParentSessionID
	}
	if sessionID != "" { return nil, errors.New("usage lineage exceeds bounded depth") }
	return scopes, nil
}

// Called under the canonical account mutation lock. Signed replacement deltas
// remove the old contribution before adding the corrected receipt, including
// unknown-to-known and subscription/free price transitions.
func (s *SessionStore) prepareUsageScopeTotals(current, previous SessionTurnUsageSnapshot) ([]UsageScopeTotal, error) {
	if current.BilledTokens < 0 || current.TotalTokens < 0 || current.EstimatedCostUSD < 0 || math.IsNaN(current.EstimatedCostUSD) || math.IsInf(current.EstimatedCostUSD, 0) {
		return nil, errors.New("invalid usage cost")
	}
	scopes := previous.ScopeTotals
	var err error
	if len(scopes) == 0 { scopes, err = s.resolveUsageScopes(current.AccountScopeID, current.SessionID) }
	if err != nil { return nil, err }
	out := make([]UsageScopeTotal, 0, len(scopes))
	for _, scope := range scopes {
		total, _, err := s.GetUsageScope(current.AccountScopeID, scope.Kind, scope.ProjectID, scope.ID)
		if err != nil { return nil, err }
		if previous.ScopeTotals != nil { applyScopeReceipt(&total, previous, -1) }
		applyScopeReceipt(&total, current, 1)
		total.Revision++
		out = append(out, total)
	}
	return out, nil
}

func applyScopeReceipt(total *UsageScopeTotal, receipt SessionTurnUsageSnapshot, sign int64) {
	tokens, _, _, _, _, _ := billedComponents(receipt)
	total.TotalTokens += sign * clampUsageTokenCount(tokens)
	total.ReceiptCount += sign
	cost := float64(sign) * receipt.EstimatedCostUSD
	switch strings.ToLower(receipt.PriceStatus) {
	case "free": total.FreeReceipts += sign
	case "subscription":
		total.SubscriptionReceipts += sign
		if strings.EqualFold(receipt.Provider, "codex") {
			total.NominalSubscriptionCostUSD += float64(sign) * CalculateBaselineCost("openai", receipt.Model, receipt.InputTokens, receipt.OutputTokens, receipt.CacheReadTokens, receipt.ThinkingTokens)
		}
	case "known":
		if receipt.CostProvenance == "provider" { total.ProviderCostUSD += cost } else { total.CatalogCostUSD += cost }
	default: total.UnknownReceipts += sign
	}
}

func setUsageScopeTotals(batch *pebble.Batch, account string, totals []UsageScopeTotal) error {
	for _, total := range totals {
		payload, err := json.Marshal(total)
		if err != nil { return err }
		if err := batch.Set([]byte(usageScopeKey(account, total)), payload, nil); err != nil { return err }
	}
	return nil
}
