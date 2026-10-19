package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/design"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Bounds of one design model call.
const (
	// Passing designs on gemini-3.8-flash took up to 136 s; 180 s failed long ones.
	designCallTimeout     = 300 * time.Second
	designMaxOutputRunes  = 512 * 1024
	designReceiptStepOnly = 1
)

// designModel is design.Model on the account's Designer model. Each call is
// one tool-free, fresh-context provider request; its reported usage is
// recorded on the job's receipt session under its own run id.
type designModel struct {
	s                *Service
	principal        identity.Principal
	receiptSessionID string
	jobID            string
	runtime          compactModelRuntime
	runner           provideriface.Runner
	contextWindow    int
	timeout          time.Duration
	maxRunes         int
}

// DesignModel resolves the account's Designer model (the canonical
// agent-model settings, no fallback) and returns a design.Model whose calls
// record usage on receiptSessionID.
func (s *Service) DesignModel(p identity.Principal, receiptSessionID, jobID string) (design.Model, error) {
	if s == nil || s.sessions == nil || s.providers == nil {
		return nil, errors.New("design model: run service is not configured")
	}
	if !p.Valid() {
		return nil, errors.New("design model: a user principal is required")
	}
	receiptSessionID, jobID = strings.TrimSpace(receiptSessionID), strings.TrimSpace(jobID)
	if receiptSessionID == "" || jobID == "" {
		return nil, errors.New("design model: receipt session and job id are required")
	}
	resolved, _, err := agentmodel.ResolveSystemAgent(s.model, s.agents, s.agentModelSettings, p.AccountScopeID, agentruntime.DesignerAgentID, "")
	if err != nil {
		return nil, fmt.Errorf("resolve the account's Designer model: %w", err)
	}
	modelRuntime, err := resolveCompactModelRuntime(s.model, resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve the Designer model runtime: %w", err)
	}
	runner, ok := s.providers.GetRunner(modelRuntime.ProviderID)
	if !ok {
		return nil, fmt.Errorf("configured Designer provider %q is not runnable", modelRuntime.ProviderID)
	}
	return &designModel{
		s: s, principal: p, receiptSessionID: receiptSessionID, jobID: jobID,
		runtime: modelRuntime, runner: runner, contextWindow: resolved.ContextWindow,
		timeout: designCallTimeout, maxRunes: designMaxOutputRunes,
	}, nil
}

func (m *designModel) Info() design.ModelInfo {
	return design.ModelInfo{Provider: m.runtime.ProviderID, Model: m.runtime.Preference.Model, Thinking: m.runtime.Preference.Thinking}
}

// designAttempts is how many times one design call may be sent: the first
// attempt plus one retry when an attempt ended before any output arrived.
const designAttempts = 2

func (m *designModel) Complete(ctx context.Context, call design.ModelCall) (design.ModelReply, error) {
	started := time.Now()
	usage := design.CallUsage{Phase: call.Phase, Provider: m.runtime.ProviderID, Model: m.runtime.Preference.Model}
	reply := func(text string) design.ModelReply {
		usage.DurationMS = time.Since(started).Milliseconds()
		return design.ModelReply{Text: text, Usage: usage}
	}
	runID := fmt.Sprintf("design:%s:%d:%s", m.jobID, call.Item, call.Phase)
	var lastErr error
	for attempt := 1; attempt <= designAttempts; attempt++ {
		attemptRunID := runID
		if attempt > 1 {
			attemptRunID = fmt.Sprintf("%s:retry%d", runID, attempt-1)
		}
		text, attemptUsage, retryable, err := m.attempt(ctx, call, attemptRunID)
		usage.Attempts = attempt
		addDesignUsage(&usage, attemptUsage)
		if err == nil {
			return reply(text), nil
		}
		lastErr = err
		// Only an attempt that produced nothing is sent again, and only once:
		// a reply that had started streaming is never replayed.
		if !retryable || ctx.Err() != nil {
			break
		}
	}
	return reply(""), lastErr
}

// attempt sends the call once. retryable reports that it failed before any
// output arrived, by this call's own deadline or an empty incomplete stream.
func (m *designModel) attempt(ctx context.Context, call design.ModelCall, runID string) (string, design.CallUsage, bool, error) {
	var usage design.CallUsage
	if err := ctx.Err(); err != nil {
		return "", usage, false, err
	}
	// The account's daily spending limit refuses before anything is spent.
	exceeded, current, limit, err := m.s.sessions.CheckDailyLimit(m.principal.AccountScopeID)
	if err != nil {
		return "", usage, false, fmt.Errorf("check the daily spending limit: %w", err)
	}
	if exceeded {
		return "", usage, false, fmt.Errorf("%w: $%.4f spent today, limit is $%.2f", design.ErrBudgetExceeded, current, limit)
	}
	lineage := provideriface.ShortProviderLineageKey("design_job", m.receiptSessionID, runID, m.runtime.Preference.Model, m.runtime.Preference.Thinking)
	req := provideriface.Request{
		SessionID:                 m.receiptSessionID,
		ProviderLineageID:         lineage,
		ProviderCacheKey:          providerScopedKey("cache", lineage),
		SessionAffinityKey:        providerScopedKey("affinity", lineage),
		BoundaryReason:            "design_job_one_shot",
		NativeContinuationAllowed: false,
		ForceFreshProviderContext: true,
		Instructions:              call.Instructions,
		Input: []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": call.Input}},
		}},
		ToolChoice:        "none",
		Tools:             nil,
		ParallelToolCalls: false,
	}
	req = m.runtime.apply(req)

	callCtx, cancel := context.WithTimeout(identity.ContextWithPrincipal(ctx, m.principal), m.timeout)
	defer cancel()
	// A calling session under a worker budget (in-session swarm_design) owns one
	// fresh operation slot per attempt: reserve before dispatch, settle after the
	// receipt. A held reservation is the fail-closed one-unsettled-operation rule.
	operation := ""
	if budget, ok := ctx.Value(workerBudgetContextKey{}).(*workerBudgetContext); ok {
		callCtx = withWorkerBudget(callCtx, budget.repository, budget.account, budget.session)
		if err := checkProviderWorkerBudget(callCtx, m.runner, req); err != nil {
			if errors.Is(err, pebblestore.ErrWorkerBudget) {
				return "", usage, false, fmt.Errorf("%w: %w", design.ErrBudgetExceeded, err)
			}
			return "", usage, false, fmt.Errorf("check the worker budget: %w", err)
		}
		operation = providerBudgetOperation(callCtx)
	}
	var out strings.Builder
	runes, overLimit := 0, false
	response, runErr := m.runner.CreateResponseStreaming(callCtx, req, func(event provideriface.StreamEvent) {
		if overLimit || event.Type != provideriface.StreamEventOutputTextDelta {
			return
		}
		runes += utf8.RuneCountInString(event.Delta)
		if runes > m.maxRunes {
			overLimit = true
			cancel() // stops this call only
			return
		}
		out.WriteString(event.Delta)
	})
	timedOut := errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	// Usage the provider reported is recorded even when the call failed.
	response.Usage.BudgetOperationID = operation
	if err := m.record(runID, response.Usage, &usage); err != nil {
		return "", usage, false, fmt.Errorf("record design call usage: %w", err)
	}
	if operation != "" {
		if err := m.s.sessions.Store().ReleaseWorkerBudgetReservation(m.principal.AccountScopeID, m.receiptSessionID, operation); err != nil {
			return "", usage, false, fmt.Errorf("settle the worker budget operation: %w", err)
		}
	}
	if overLimit {
		return "", usage, false, fmt.Errorf("the model's reply exceeded %d characters", m.maxRunes)
	}
	if runErr != nil {
		noOutput := runes == 0 && strings.TrimSpace(response.Text) == ""
		retryable := noOutput && (timedOut || errors.Is(runErr, provideriface.ErrStreamIncomplete))
		if timedOut {
			runErr = fmt.Errorf("no complete reply within %s: %w", m.timeout, runErr)
		}
		return "", usage, retryable, runErr
	}
	text := response.Text
	if strings.TrimSpace(text) == "" {
		text = out.String()
	}
	if utf8.RuneCountInString(text) > m.maxRunes {
		return "", usage, false, fmt.Errorf("the model's reply exceeded %d characters", m.maxRunes)
	}
	return text, usage, false, nil
}

// addDesignUsage adds one attempt's reported usage to the call's total. The
// call counts as reported (and priced) only when every attempt that reported
// usage was priced; an attempt that reported nothing adds nothing.
func addDesignUsage(total *design.CallUsage, a design.CallUsage) {
	if !a.Reported {
		return
	}
	first := !total.Reported
	total.Reported = true
	total.Input += a.Input
	total.Output += a.Output
	total.Thinking += a.Thinking
	total.CacheRead += a.CacheRead
	total.CacheWrite += a.CacheWrite
	total.Total += a.Total
	if first {
		total.PriceStatus = a.PriceStatus
	} else if total.PriceStatus != a.PriceStatus {
		total.PriceStatus = "mixed"
	}
	switch {
	case a.CostUSD == nil:
		total.CostUSD = nil
		total.PriceStatus = a.PriceStatus
	case first:
		cost := *a.CostUSD
		total.CostUSD = &cost
	case total.CostUSD != nil:
		cost := *total.CostUSD + *a.CostUSD
		total.CostUSD = &cost
	}
}

// record commits the provider's reported usage on the receipt session and
// fills the call's usage from the committed receipt. Nothing reported means
// Reported false and no cost: numbers are never synthesized.
func (m *designModel) record(runID string, reported provideriface.TokenUsage, usage *design.CallUsage) error {
	if !hasConcreteUsageSnapshot(reported) {
		return nil
	}
	committed, _, _, err := m.s.recordProviderUsageSnapshot(m.receiptSessionID, runID, m.runtime.ProviderID, m.runtime.Preference.Model, m.contextWindow, designReceiptStepOnly, reported, m.principal, nil)
	if err != nil {
		return err
	}
	usage.Reported = true
	usage.Input, usage.Output, usage.Thinking = committed.InputTokens, committed.OutputTokens, committed.ThinkingTokens
	usage.CacheRead, usage.CacheWrite, usage.Total = committed.CacheReadTokens, committed.CacheWriteTokens, committed.TotalTokens
	usage.PriceStatus = committed.PriceStatus
	switch committed.PriceStatus {
	case "known", "free", "subscription":
		cost := committed.EstimatedCostUSD
		usage.CostUSD = &cost
	}
	return nil
}
