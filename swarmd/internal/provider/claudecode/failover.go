package claudecode

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/providerlimit"
)

var errNoSignedInAccounts = errors.New("no Claude Code accounts are signed in")

// runAccounts retries a rejected turn with the identical durable input. It never
// replays a tool side effect: after any tool invocation it asks the run to rebuild
// its input from durable history before dispatching the next account.
func (r *Runner) runAccounts(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	slots, err := r.cli.slots.ClaudeCodeSlots()
	if err != nil {
		return provideriface.Response{}, err
	}
	var earliest time.Time
	attempted := ""
	var rejectedUsage provideriface.TokenUsage
	for _, slot := range slots {
		if err := ctx.Err(); err != nil {
			return provideriface.Response{}, err
		}
		if slot.LimitUntil.After(r.cli.now()) {
			if earliest.IsZero() || slot.LimitUntil.Before(earliest) {
				earliest = slot.LimitUntil
			}
			continue
		}
		cli := r.cli.clientForSlot(slot)
		// Unsigned additional slots must not break the existing login.
		if !cli.Status(ctx).Ready {
			continue
		}
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventAccountSelected, ProviderID: ProviderID, Metadata: map[string]any{"account_id": slot.ID, "previous_account_id": attempted}})
		}
		attempted = slot.ID
		turnReq := req
		// CLI history is account-local. Account failover must seed the durable
		// input rather than resume a transcript owned by another credential.
		turnReq.ProviderLineageID += "/claudecode-account/" + slot.ID
		tracker := &accountToolTracker{inner: req.ToolInvoker}
		if req.ToolInvoker != nil {
			turnReq.ToolInvoker = tracker
		}
		single := &Runner{authStore: r.authStore, cli: cli, mcp: r.mcp}
		response, turnErr := single.runSingle(ctx, turnReq, onEvent)
		if turnErr == nil {
			response.Usage = mergeAccountUsage(rejectedUsage, response.Usage)
			return response, nil
		}
		lower := strings.ToLower(turnErr.Error())
		if !(strings.Contains(lower, "session limit") || strings.Contains(lower, "weekly limit") || strings.Contains(lower, "hit your limit")) {
			return response, turnErr
		}
		// ResetAt's public contract adds a scheduling margin. Account metadata records
		// the provider's actual clock, not that margin and never a guessed backoff.
		until, known := providerlimit.ResetAt(turnErr.Error(), r.cli.now())
		if !known {
			return response, turnErr
		}
		until = until.Add(-time.Minute)
		if !until.After(r.cli.now()) {
			return response, turnErr
		}
		if err := r.cli.slots.RecordClaudeCodeLimit(slot.ID, until); err != nil {
			return response, err
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
		if tracker.called.Load() {
			// The result's text can be the limit notice, not assistant content.
			response.Text = ""
			response.Usage = mergeAccountUsage(rejectedUsage, response.Usage)
			response.RestartTurn = true
			return response, nil
		}
		rejectedUsage = mergeAccountUsage(rejectedUsage, response.Usage)
	}
	if !earliest.IsZero() {
		return provideriface.Response{Usage: rejectedUsage}, &AccountLimitWait{Until: earliest}
	}
	return provideriface.Response{}, errNoSignedInAccounts
}

type accountToolTracker struct {
	inner  provideriface.ToolInvoker
	called atomic.Bool
}

func (t *accountToolTracker) ExecuteTool(ctx context.Context, invocation provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	t.called.Store(true)
	return t.inner.ExecuteTool(ctx, invocation)
}

// mergeAccountUsage sums only provider-reported receipts; it never estimates a
// rejected attempt's usage. Preserve each raw receipt for downstream accounting.
func mergeAccountUsage(previous, current provideriface.TokenUsage) provideriface.TokenUsage {
	current.InputTokens += previous.InputTokens
	current.OutputTokens += previous.OutputTokens
	current.ThinkingTokens += previous.ThinkingTokens
	current.TotalTokens += previous.TotalTokens
	current.CacheReadTokens += previous.CacheReadTokens
	current.CacheWriteTokens += previous.CacheWriteTokens
	current.EstimatedCostUSD += previous.EstimatedCostUSD
	history := append([]map[string]any(nil), previous.APIUsageHistory...)
	if previous.APIUsageRaw != nil && len(previous.APIUsageHistory) == 0 {
		history = append(history, previous.APIUsageRaw)
	}
	if len(current.APIUsageHistory) > 0 {
		history = append(history, current.APIUsageHistory...)
	} else if current.APIUsageRaw != nil {
		history = append(history, current.APIUsageRaw)
	}
	current.APIUsageHistory = history
	return current
}
