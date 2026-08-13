package run

import (
	"fmt"
	"strings"
	"sync"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const taskContextRotationThreshold = 0.80

type taskContextUsageAuthority interface {
	GetUsageSummary(sessionID string) (pebblestore.SessionUsageSummary, bool, error)
	GetTurnUsage(sessionID, runID string) (pebblestore.SessionTurnUsageSnapshot, bool, error)
	GetDelegatedChildLineage(accountScopeID, logicalTaskID string) (pebblestore.DelegatedChildLineageRecord, bool, error)
	GetDelegatedChildGeneration(accountScopeID, logicalTaskID string, generation int) (pebblestore.DelegatedChildGenerationRecord, bool, error)
}

type taskContextWatcher struct {
	authority      taskContextUsageAuthority
	accountScopeID string
	logicalTaskID  string
	sessionID      string
	provider       string
	model          string
	generation     int
	mu             sync.Mutex
	armed          bool
	observed       pebblestore.SessionUsageSummary
}

func newTaskContextWatcher(authority taskContextUsageAuthority, launch taskLaunchPrepared) *taskContextWatcher {
	generation := 1
	if raw := launch.ChildSession.Metadata["context_generation"]; raw != nil {
		switch value := raw.(type) {
		case int:
			if value > 0 {
				generation = value
			}
		case float64:
			if value >= 1 && value == float64(int(value)) {
				generation = int(value)
			}
		}
	}
	return &taskContextWatcher{
		authority: authority, accountScopeID: strings.TrimSpace(launch.ChildSession.AccountScopeID),
		logicalTaskID: strings.TrimSpace(launch.LogicalTaskID), sessionID: strings.TrimSpace(launch.ChildSession.ID),
		provider: strings.ToLower(strings.TrimSpace(launch.SubagentProvider)), model: strings.TrimSpace(launch.SubagentModel), generation: generation,
	}
}

func (w *taskContextWatcher) Observe(summary *pebblestore.SessionUsageSummary) bool {
	if w == nil || summary == nil || !w.acceptsSummary(*summary, "", false) {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.observed = *summary
	if taskUsageReachesRotationThreshold(*summary) {
		w.armed = true
	}
	return w.armed
}

func (w *taskContextWatcher) Boundary(input RunContinuationBoundaryInput) (RunContinuationBoundaryDecision, error) {
	if w == nil || w.authority == nil {
		return RunContinuationBoundaryDecision{}, nil
	}
	current, err := w.ensureCurrentRunOwnership(input.RunID)
	if err != nil {
		return RunContinuationBoundaryDecision{}, err
	}
	if !current {
		return RunContinuationBoundaryDecision{}, nil
	}
	if strings.ToLower(strings.TrimSpace(input.Provider)) != w.provider || strings.TrimSpace(input.Model) != w.model {
		return RunContinuationBoundaryDecision{}, nil
	}
	if input.UsageSummary != nil {
		w.Observe(input.UsageSummary)
	}
	summary, ok, err := w.authority.GetUsageSummary(w.sessionID)
	if err != nil {
		return RunContinuationBoundaryDecision{}, fmt.Errorf("re-read delegated child usage summary: %w", err)
	}
	if !ok || !w.acceptsSummary(summary, input.RunID, true) {
		return RunContinuationBoundaryDecision{}, nil
	}
	turn, ok, err := w.authority.GetTurnUsage(w.sessionID, input.RunID)
	if err != nil {
		return RunContinuationBoundaryDecision{}, fmt.Errorf("re-read delegated child provider usage fact: %w", err)
	}
	if !ok || !taskProviderUsagePathAccepted(turn.Source, turn.APIUsageRawPath) || turn.SessionID != w.sessionID || turn.RunID != input.RunID || turn.UpdatedAt != summary.UpdatedAt {
		return RunContinuationBoundaryDecision{}, nil
	}
	w.mu.Lock()
	w.observed = summary
	if taskUsageReachesRotationThreshold(summary) {
		w.armed = true
	}
	armed := w.armed
	w.mu.Unlock()
	if !armed {
		return RunContinuationBoundaryDecision{}, nil
	}
	copy := summary
	return RunContinuationBoundaryDecision{
		Kind:         RunContinuationBoundaryTaskRotation,
		Reason:       fmt.Sprintf("committed provider usage reached %.0f%% (%d/%d)", taskContextRotationThreshold*100, summary.TotalTokens, summary.ContextWindow),
		UsageSummary: &copy,
	}, nil
}

func (w *taskContextWatcher) acceptsSummary(summary pebblestore.SessionUsageSummary, runID string, requireRun bool) bool {
	if w == nil || summary.SessionID != w.sessionID || !taskProviderUsageSourceAccepted(summary.Provider, summary.Source) {
		return false
	}
	if strings.ToLower(strings.TrimSpace(summary.Provider)) != w.provider || strings.TrimSpace(summary.Model) != w.model {
		return false
	}
	if summary.ContextWindow <= 0 || summary.TotalTokens <= 0 || summary.UpdatedAt <= 0 {
		return false
	}
	if requireRun {
		runID = strings.TrimSpace(runID)
		if runID == "" || strings.TrimSpace(summary.LastRunID) != runID {
			return false
		}
	}
	return true
}

func (w *taskContextWatcher) ensureCurrentRunOwnership(runID string) (bool, error) {
	lineage, ok, err := w.authority.GetDelegatedChildLineage(w.accountScopeID, w.logicalTaskID)
	if err != nil {
		return false, fmt.Errorf("revalidate delegated child lineage: %w", err)
	}
	if !ok || lineage.CurrentGeneration != w.generation || lineage.CurrentSessionID != w.sessionID {
		return false, nil
	}
	generation, ok, err := w.authority.GetDelegatedChildGeneration(w.accountScopeID, w.logicalTaskID, w.generation)
	if err != nil {
		return false, fmt.Errorf("revalidate delegated child generation: %w", err)
	}
	if !ok || generation.State != pebblestore.DelegatedChildGenerationActive || generation.SessionID != w.sessionID {
		return false, nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false, nil
	}
	if strings.TrimSpace(lineage.CurrentRunID) == runID && strings.TrimSpace(generation.RunID) == runID {
		return true, nil
	}
	if strings.TrimSpace(lineage.CurrentRunID) != "" || strings.TrimSpace(generation.RunID) != "" {
		return false, nil
	}
	updater, ok := w.authority.(interface {
		UpdateDelegatedChildRun(pebblestore.UpdateDelegatedChildRunInput) (pebblestore.DelegatedChildLineageRecord, bool, error)
	})
	if !ok {
		return false, nil
	}
	updated, _, err := updater.UpdateDelegatedChildRun(pebblestore.UpdateDelegatedChildRunInput{
		AccountScopeID: w.accountScopeID, LogicalTaskID: w.logicalTaskID,
		ExpectedLineageRevision: lineage.Revision, ExpectedGenerationRevision: generation.Revision,
		Generation: w.generation, SessionID: w.sessionID, RunID: runID,
		MutationID: "delegated-child-run:" + w.logicalTaskID + ":" + runID,
	})
	if err != nil {
		return false, fmt.Errorf("bind delegated child run ownership: %w", err)
	}
	return updated.CurrentGeneration == w.generation && updated.CurrentSessionID == w.sessionID && updated.CurrentRunID == runID, nil
}

func taskUsageReachesRotationThreshold(summary pebblestore.SessionUsageSummary) bool {
	if summary.ContextWindow <= 0 || summary.TotalTokens <= 0 {
		return false
	}
	return float64(summary.TotalTokens)/float64(summary.ContextWindow) >= taskContextRotationThreshold
}

func taskProviderUsagePathAccepted(source, path string) bool {
	source, path = strings.ToLower(strings.TrimSpace(source)), strings.TrimSpace(path)
	switch source {
	case "codex_api_usage":
		return path == "response.usage" || path == "usage"
	case "google_api_usage":
		return path == "usageMetadata"
	case "copilot_session_usage":
		return path == "session.usage_info"
	case "anthropic_api_usage", "fireworks_api_usage", "openrouter_api_usage":
		return path == "usage"
	default:
		return false
	}
}

func taskProviderUsageSourceAccepted(provider, source string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	source = strings.ToLower(strings.TrimSpace(source))
	accepted := map[string]string{
		"codex": "codex_api_usage", "google": "google_api_usage", "copilot": "copilot_session_usage",
		"anthropic": "anthropic_api_usage", "fireworks": "fireworks_api_usage", "openrouter": "openrouter_api_usage",
	}
	return accepted[provider] != "" && source == accepted[provider]
}
