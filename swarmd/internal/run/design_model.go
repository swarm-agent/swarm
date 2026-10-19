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
)

// Bounds of one design model call.
const (
	designCallTimeout     = 180 * time.Second
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

func (m *designModel) Complete(ctx context.Context, call design.ModelCall) (design.ModelReply, error) {
	started := time.Now()
	usage := design.CallUsage{Phase: call.Phase, Provider: m.runtime.ProviderID, Model: m.runtime.Preference.Model}
	reply := func(text string) design.ModelReply {
		usage.DurationMS = time.Since(started).Milliseconds()
		return design.ModelReply{Text: text, Usage: usage}
	}
	if err := ctx.Err(); err != nil {
		return reply(""), err
	}
	// The account's daily spending limit refuses before anything is spent.
	exceeded, current, limit, err := m.s.sessions.CheckDailyLimit(m.principal.AccountScopeID)
	if err != nil {
		return reply(""), fmt.Errorf("check the daily spending limit: %w", err)
	}
	if exceeded {
		return reply(""), fmt.Errorf("%w: $%.4f spent today, limit is $%.2f", design.ErrBudgetExceeded, current, limit)
	}
	runID := fmt.Sprintf("design:%s:%d:%s", m.jobID, call.Item, call.Phase)
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
	// Usage the provider reported is recorded even when the call failed.
	if err := m.record(runID, response.Usage, &usage); err != nil {
		return reply(""), fmt.Errorf("record design call usage: %w", err)
	}
	if overLimit {
		return reply(""), fmt.Errorf("the model's reply exceeded %d characters", m.maxRunes)
	}
	if runErr != nil {
		return reply(""), runErr
	}
	text := response.Text
	if strings.TrimSpace(text) == "" {
		text = out.String()
	}
	if utf8.RuneCountInString(text) > m.maxRunes {
		return reply(""), fmt.Errorf("the model's reply exceeded %d characters", m.maxRunes)
	}
	return reply(text), nil
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
