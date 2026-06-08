package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// TerminalClassifier converts provider/runtime terminal evidence into the
// authoritative V3 run-intent transition that is later committed to Pebble.
//
// Provider stream ends, stop reasons, assistant messages, and tool events are
// executor inputs only. Clients must consume the durable V3SessionRunIntent
// status written by the executor instead of treating those provider facts as
// run truth.
type TerminalClassifier struct{}

type TerminalClassifierInput struct {
	ProviderID       string
	StopReason       string
	HasFinalContent  bool
	HasFunctionCalls bool
	RestartTurn      bool
	Err              error
}

type TerminalClassification struct {
	Status    string
	Reason    string
	EventType string
	Terminal  bool
}

func (TerminalClassifier) Classify(input TerminalClassifierInput) TerminalClassification {
	if input.Err != nil {
		return sessionV3TerminalFailure(sessionV3RuntimeErrorReason(input.Err))
	}
	if input.HasFunctionCalls || input.RestartTurn {
		return TerminalClassification{Status: sessionruntime.RunIntentRunning, Terminal: false}
	}
	provider := sessionV3TerminalProviderName(input.ProviderID)
	if !input.HasFinalContent {
		return sessionV3TerminalFailure(fmt.Sprintf("%s returned no final assistant content", provider))
	}
	stopReason := strings.TrimSpace(input.StopReason)
	if stopReason == "" {
		return sessionV3TerminalFailure(fmt.Sprintf("%s ended without a terminal stop reason", provider))
	}
	normalized := sessionV3NormalizeStopReason(stopReason)
	if sessionV3StopReasonIsSuccessful(normalized) {
		return TerminalClassification{Status: sessionruntime.RunIntentCompleted, EventType: "session.assistant.completed", Terminal: true}
	}
	if sessionV3StopReasonRequestsContinuation(normalized) {
		return sessionV3TerminalFailure(fmt.Sprintf("%s requested continuation with stop reason %q but returned no executable continuation", provider, stopReason))
	}
	if sessionV3StopReasonIsFailure(normalized) {
		return sessionV3TerminalFailure(fmt.Sprintf("%s ended with non-completion stop reason %q", provider, stopReason))
	}
	return sessionV3TerminalFailure(fmt.Sprintf("%s ended with unrecognized stop reason %q", provider, stopReason))
}

var sessionV3TerminalClassifier = TerminalClassifier{}

func sessionV3TerminalFailure(reason string) TerminalClassification {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "run failed"
	}
	return TerminalClassification{Status: sessionruntime.RunIntentFailed, Reason: reason, EventType: "session.run.failed", Terminal: true}
}

func sessionV3RuntimeErrorReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "executor context canceled"
	}
	return strings.TrimSpace(err.Error())
}

func sessionV3TerminalProviderName(providerID string) string {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return "provider"
	}
	return fmt.Sprintf("provider %s", providerID)
}

func sessionV3NormalizeStopReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range reason {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func sessionV3StopReasonIsSuccessful(normalized string) bool {
	switch normalized {
	case "stop", "end_turn", "stop_sequence", "complete", "completed", "success", "succeeded", "finish", "finished", "done", "turn_complete", "normal", "normal_completion":
		return true
	default:
		return false
	}
}

func sessionV3StopReasonRequestsContinuation(normalized string) bool {
	switch normalized {
	case "tool_use", "tool_call", "tool_calls", "function_call", "function_calls", "requires_action", "restart_turn", "pause_turn":
		return true
	default:
		return false
	}
}

func sessionV3StopReasonIsFailure(normalized string) bool {
	failureFragments := []string{
		"abort",
		"block",
		"cancel",
		"content_filter",
		"context",
		"error",
		"fail",
		"incomplete",
		"invalid",
		"length",
		"malformed",
		"max_output_token",
		"max_token",
		"other",
		"overload",
		"prohibited",
		"rate_limit",
		"recitation",
		"refusal",
		"refused",
		"safety",
		"spii",
		"timeout",
	}
	for _, fragment := range failureFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func sessionV3RunIntentStatusTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case sessionruntime.RunIntentCompleted, sessionruntime.RunIntentFailed, sessionruntime.RunIntentDispatchBlocked:
		return true
	default:
		return false
	}
}

func sessionV3RunIntentStatusActive(status string) bool {
	switch strings.TrimSpace(status) {
	case sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning:
		return true
	default:
		return false
	}
}
