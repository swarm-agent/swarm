package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	contextCompactionThresholdMetadataKey  = "context_compaction_threshold_percent"
	planContextGuardEnabledMetadataKey     = "plan_context_guard_enabled"
	planContextGuardThresholdMetadataKey   = "plan_context_guard_used_percent"
	planContextGuardMaxCompactsMetadataKey = "plan_context_guard_max_compactions"

	defaultPlanContextGuardUsedPercent  = 80.0
	defaultPlanContextGuardMaxCompacts  = 1
	defaultPlanContextGuardRefusalLimit = 1
)

type planContextGuardPolicy struct {
	Enabled        bool
	UsedPercent    float64
	MaxCompactions int
	RefusalLimit   int
}

func normalizedPlanContextGuardPolicy(metadata map[string]any) planContextGuardPolicy {
	policy := planContextGuardPolicy{
		Enabled:        true,
		UsedPercent:    defaultPlanContextGuardUsedPercent,
		MaxCompactions: defaultPlanContextGuardMaxCompacts,
		RefusalLimit:   defaultPlanContextGuardRefusalLimit,
	}
	if value, ok := metadata[planContextGuardEnabledMetadataKey]; ok {
		if parsed, valid := value.(bool); valid {
			policy.Enabled = parsed
		}
	}
	if value, ok := metadata[planContextGuardThresholdMetadataKey]; ok {
		if parsed, valid := parseContextCompactionThresholdValue(value); valid {
			if parsed < 1 {
				parsed = 1
			}
			if parsed > 100 {
				parsed = 100
			}
			policy.UsedPercent = parsed
		}
	}
	if value, ok := metadata[planContextGuardMaxCompactsMetadataKey]; ok {
		if parsed, valid := parseContextCompactionThresholdValue(value); valid {
			maximum := int(parsed)
			if maximum < 0 {
				maximum = 0
			}
			if maximum > 3 {
				maximum = 3
			}
			policy.MaxCompactions = maximum
		}
	}
	return policy
}

func trustedPlanContextUsagePercent(summary pebblestore.SessionUsageSummary) (float64, bool) {
	if summary.ContextWindow <= 0 || summary.TotalTokens < 0 {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(summary.Source)) {
	case "codex_api_usage", "google_api_usage", "copilot_session_usage", "anthropic_api_usage", "fireworks_api_usage", "openrouter_api_usage":
	default:
		return 0, false
	}
	used := summary.TotalTokens
	if used == 0 && summary.InputTokens > 0 {
		used = summary.InputTokens
	}
	if used > int64(summary.ContextWindow) {
		used = int64(summary.ContextWindow)
	}
	return (float64(used) * 100) / float64(summary.ContextWindow), true
}

type planContextGuardState struct {
	policy           planContextGuardPolicy
	aboveThreshold   bool
	warningPending   bool
	decisionActive   bool
	finalizationOnly bool
	compactions      int
	refusals         int
}

// PlanContextGuard is the run-local backend authority for the bounded
// plan-mode context-pressure decision. It is intentionally not persisted as a
// second plan authority; callers keep one instance across fresh provider
// chains belonging to the same parent run.
type PlanContextGuard struct {
	state *planContextGuardState
}

func newPlanContextGuardState(metadata map[string]any) *planContextGuardState {
	return &planContextGuardState{policy: normalizedPlanContextGuardPolicy(metadata)}
}

func newConfiguredPlanContextGuardState(enabled bool, usedPercent float64, maxCompactions int) *planContextGuardState {
	return NewConfiguredPlanContextGuard(enabled, usedPercent, maxCompactions).state
}

func NewPlanContextGuard(metadata map[string]any) *PlanContextGuard {
	return &PlanContextGuard{state: newPlanContextGuardState(metadata)}
}

// NewConfiguredPlanContextGuard builds the guard from normalized account policy.
// The metadata constructor remains available for session-level compatibility.
func NewConfiguredPlanContextGuard(enabled bool, usedPercent float64, maxCompactions int) *PlanContextGuard {
	return NewPlanContextGuard(map[string]any{
		planContextGuardEnabledMetadataKey:     enabled,
		planContextGuardThresholdMetadataKey:   usedPercent,
		planContextGuardMaxCompactsMetadataKey: maxCompactions,
	})
}

func (guard *PlanContextGuard) Observe(summary pebblestore.SessionUsageSummary) bool {
	return guard != nil && guard.state != nil && guard.state.observe(summary)
}

func (guard *PlanContextGuard) BeginDecision() bool {
	return guard != nil && guard.state != nil && guard.state.beginDecision()
}

func (guard *PlanContextGuard) DecisionActive() bool {
	return guard != nil && guard.state != nil && guard.state.decisionActive
}

func (guard *PlanContextGuard) FinalizationOnly() bool {
	return guard != nil && guard.state != nil && guard.state.finalizationOnly
}

func (guard *PlanContextGuard) WarningInstructions() string {
	if guard == nil || guard.state == nil {
		return ""
	}
	return guard.state.warningInstructions()
}

func (guard *PlanContextGuard) RecordCompaction() {
	if guard != nil && guard.state != nil {
		guard.state.recordCompaction()
	}
}

func (guard *PlanContextGuard) RecordRefusal() error {
	if guard == nil || guard.state == nil {
		return nil
	}
	return guard.state.recordRefusal()
}

func (state *planContextGuardState) observe(summary pebblestore.SessionUsageSummary) bool {
	if state == nil || !state.policy.Enabled {
		return false
	}
	usedPercent, trusted := trustedPlanContextUsagePercent(summary)
	if !trusted {
		return false
	}
	above := usedPercent >= state.policy.UsedPercent
	armed := above && !state.aboveThreshold && !state.warningPending && !state.decisionActive
	state.aboveThreshold = above
	if armed {
		state.warningPending = true
	}
	return armed
}

func (state *planContextGuardState) beginDecision() bool {
	if state == nil || !state.warningPending {
		return false
	}
	state.warningPending = false
	state.decisionActive = true
	state.finalizationOnly = state.compactions >= state.policy.MaxCompactions
	return true
}

func (state *planContextGuardState) recordCompaction() {
	if state == nil {
		return
	}
	state.compactions++
	state.aboveThreshold = false
	state.warningPending = false
	state.decisionActive = false
	state.finalizationOnly = false
	state.refusals = 0
}

func (state *planContextGuardState) recordRefusal() error {
	if state == nil {
		return nil
	}
	state.refusals++
	state.warningPending = true
	state.decisionActive = false
	if state.refusals > state.policy.RefusalLimit {
		return errors.New("plan context guard stopped the run after the required bounded finalization decision was refused")
	}
	return nil
}

func PlanContextGuardCompactHandoff(arguments string) (string, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("compact arguments invalid: %w", err)
	}
	handoff := strings.TrimSpace(mapString(args, "handoff"))
	if handoff == "" {
		return "", errors.New("compact requires a concise non-empty handoff")
	}
	const maxHandoffRunes = 9000
	if len([]rune(handoff)) > maxHandoffRunes {
		return "", fmt.Errorf("compact handoff exceeds %d characters", maxHandoffRunes)
	}
	return handoff, nil
}

func planContextGuardCompactHandoff(arguments string) (string, error) {
	return PlanContextGuardCompactHandoff(arguments)
}

func (state *planContextGuardState) warningInstructions() string {
	if state == nil {
		return ""
	}
	if state.finalizationOnly {
		return fmt.Sprintf("Plan context guard: trustworthy usage reached the configured %.1f%% used-context boundary. The compact allowance for this plan parent run is exhausted. Call exit_plan_mode now with the best complete actionable structured plan. No further research or compaction is available.", state.policy.UsedPercent)
	}
	return fmt.Sprintf("Plan context guard: trustworthy usage reached the configured %.1f%% used-context boundary. Before any further open-ended research, choose exactly one control: call exit_plan_mode with the best complete actionable structured plan, or call compact with a concise research handoff preserving the goal, decisions, evidence, relevant files, and immediate next action. Do not call both.", state.policy.UsedPercent)
}

func sessionContextCompactionThresholdPercent(metadata map[string]any) (float64, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	value, ok := metadata[contextCompactionThresholdMetadataKey]
	if !ok || value == nil {
		return 0, false
	}
	threshold, ok := parseContextCompactionThresholdValue(value)
	if !ok {
		return 0, false
	}
	if threshold <= 0 {
		return 0, false
	}
	if threshold > 100 {
		threshold = 100
	}
	return threshold, true
}

func parseContextCompactionThresholdValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case string:
		raw := strings.TrimSpace(strings.TrimSuffix(typed, "%"))
		if raw == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func shouldAutoCompactForThreshold(summary pebblestore.SessionUsageSummary, thresholdPercent float64) bool {
	if thresholdPercent <= 0 || summary.ContextWindow <= 0 {
		return false
	}
	remaining := summary.RemainingTokens
	if remaining < 0 {
		remaining = 0
	}
	if remaining > int64(summary.ContextWindow) {
		remaining = int64(summary.ContextWindow)
	}
	remainingPercent := (float64(remaining) * 100) / float64(summary.ContextWindow)
	return remainingPercent <= thresholdPercent
}

func remainingContextPercent(summary pebblestore.SessionUsageSummary) float64 {
	if summary.ContextWindow <= 0 {
		return 0
	}
	remaining := summary.RemainingTokens
	if remaining < 0 {
		remaining = 0
	}
	if remaining > int64(summary.ContextWindow) {
		remaining = int64(summary.ContextWindow)
	}
	return (float64(remaining) * 100) / float64(summary.ContextWindow)
}

func compactedContinuationLead(origin string) string {
	switch strings.ToLower(strings.TrimSpace(origin)) {
	case contextCompactionOriginThreshold:
		return "The previous conversation context was proactively compacted before the model hit its context limit."
	case contextCompactionOriginPlanGuard:
		return "The previous plan-mode context was compacted from the agent's explicit research handoff before the model hit its context limit."
	default:
		return "The previous conversation context exceeded the model context window and was compacted by the memory subagent."
	}
}

func formatThresholdCompactionStatus(summary pebblestore.SessionUsageSummary, thresholdPercent float64) string {
	return fmt.Sprintf(
		"remaining context %.1f%% is at or below the configured auto-compact threshold %.1f%%; compacting before the next provider step",
		remainingContextPercent(summary),
		thresholdPercent,
	)
}

type PlanContextGuardCompactionInput struct {
	SessionID            string
	RunID                string
	Handoff              string
	ContextWindow        int
	ProviderID           string
	Model                string
	Step                 int
	Principal            identity.Principal
	ApplySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)
}

func (s *Service) ApplyPlanContextGuardCompaction(input PlanContextGuardCompactionInput) (string, error) {
	handoff := strings.TrimSpace(input.Handoff)
	if handoff == "" {
		return "", errors.New("plan context guard compaction requires a handoff")
	}
	step := input.Step
	if step < 0 {
		step = 0
	}
	_, _, _, err := s.applyContextCompactionArtifacts(
		strings.TrimSpace(input.SessionID),
		handoff,
		contextCompactionOriginPlanGuard,
		input.ContextWindow,
		strings.TrimSpace(input.ProviderID),
		strings.TrimSpace(input.Model),
		step,
		nil,
		runAppendMessageInput{
			RunID:                strings.TrimSpace(input.RunID),
			Step:                 step,
			LogicalKey:           fmt.Sprintf("system:plan_context_guard_compaction:%d", step),
			Principal:            input.Principal,
			ApplySessionMutation: input.ApplySessionMutation,
		},
	)
	if err != nil {
		return "", err
	}
	epoch, ok, err := s.sessions.GetActiveExecutionEpoch(strings.TrimSpace(input.SessionID))
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(epoch.EpochID) == "" {
		return "", errors.New("plan context guard compaction did not create an active continuation epoch")
	}
	if strings.TrimSpace(epoch.Boundary.Reason) != "context_compaction_"+contextCompactionOriginPlanGuard || strings.TrimSpace(epoch.Boundary.RunID) != strings.TrimSpace(input.RunID) {
		return "", fmt.Errorf("plan context guard compaction created unexpected continuation epoch %q", epoch.EpochID)
	}
	return strings.TrimSpace(epoch.EpochID), nil
}

func (s *Service) maybeAutoCompactRunContext(ctx context.Context, sessionID, runPrompt, providerID, modelName string, metadata map[string]any, preference pebblestore.ModelPreference, contextWindow, maxOutputTokens, step int, emit StreamHandler, appendInput runAppendMessageInput) ([]map[string]any, *pebblestore.SessionUsageSummary, []pebblestore.EventEnvelope, error) {
	if s == nil || s.sessions == nil {
		return nil, nil, nil, errors.New("run service is not fully configured")
	}
	thresholdPercent, ok := sessionContextCompactionThresholdPercent(metadata)
	if !ok {
		return nil, nil, nil, nil
	}
	usageSummary, hasUsage, err := s.sessions.GetUsageSummary(sessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !hasUsage || !shouldAutoCompactForThreshold(usageSummary, thresholdPercent) {
		return nil, nil, nil, nil
	}

	emitMemoryCompactionStatus(emit, step, memoryCompactionOriginLabel(contextCompactionOriginThreshold))
	var compactionToolStream *memoryCompactionToolStream
	compactedSummary, compactErr := s.compactRunContextWithMemory(
		ctx,
		sessionID,
		runPrompt,
		"",
		preference,
		contextWindow,
		maxOutputTokens,
		false,
		contextCompactionOriginThreshold,
		appendInput.ApplySessionMutation != nil,
		step,
		1,
		emit,
		&compactionToolStream,
	)
	if compactErr != nil {
		return nil, nil, nil, fmt.Errorf("threshold auto compact failed: %w", compactErr)
	}
	compactEvents := make([]pebblestore.EventEnvelope, 0, 4)
	toolAppendInput := appendInput
	toolAppendInput.Step = step
	if compactionToolStream != nil {
		toolAppendInput.LogicalKey = fmt.Sprintf("tool:%d:%s", step, strings.TrimSpace(compactionToolStream.CallID))
	}
	if toolMessage, persistErr := s.persistMemoryCompactionToolMessage(sessionID, &compactEvents, nil, compactionToolStream, toolAppendInput); persistErr != nil {
		return nil, nil, nil, persistErr
	} else if toolMessage != nil {
		emit(StreamEvent{Type: StreamEventMessageStored, Step: step, Message: toolMessage})
	}
	resetSummary, _, artifactEvents, compactErr := s.applyContextCompactionArtifacts(
		sessionID,
		compactedSummary,
		contextCompactionOriginThreshold,
		contextWindow,
		providerID,
		modelName,
		step,
		emit,
		runAppendMessageInput{RunID: appendInput.RunID, Step: step, LogicalKey: fmt.Sprintf("system:context_compaction:%d", step), Principal: appendInput.Principal, ApplySessionMutation: appendInput.ApplySessionMutation},
	)
	if compactErr != nil {
		return nil, nil, nil, fmt.Errorf("threshold auto compact bookkeeping failed: %w", compactErr)
	}
	compactEvents = append(compactEvents, artifactEvents...)
	var activePlan *pebblestore.SessionPlanSnapshot
	plan, ok, planErr := s.sessions.GetActivePlan(sessionID)
	if planErr != nil {
		return nil, nil, nil, fmt.Errorf("threshold auto compact active plan lookup failed: %w", planErr)
	}
	if ok {
		activePlan = &plan
	}
	compactedInput := buildCompactedContinuationInput(runPrompt, compactedSummary, activePlan, contextCompactionOriginThreshold)
	if len(compactedInput) == 0 {
		return nil, nil, nil, errors.New("threshold auto compact produced empty input")
	}
	return compactedInput, resetSummary, compactEvents, nil
}
