package api

import (
	"errors"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

func TestTerminalClassifierProviderStopReasons(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		stopReason string
		wantStatus string
		wantReason string
	}{
		{name: "anthropic end turn", provider: "anthropic", stopReason: "end_turn", wantStatus: sessionruntime.RunIntentCompleted},
		{name: "anthropic max tokens", provider: "anthropic", stopReason: "max_tokens", wantStatus: sessionruntime.RunIntentFailed, wantReason: "max_tokens"},
		{name: "anthropic refusal", provider: "anthropic", stopReason: "refusal", wantStatus: sessionruntime.RunIntentFailed, wantReason: "refusal"},
		{name: "codex completed", provider: "codex", stopReason: "completed", wantStatus: sessionruntime.RunIntentCompleted},
		{name: "codex incomplete max output", provider: "codex", stopReason: "incomplete: max_output_tokens", wantStatus: sessionruntime.RunIntentFailed, wantReason: "max_output_tokens"},
		{name: "fireworks stop", provider: "fireworks", stopReason: "stop", wantStatus: sessionruntime.RunIntentCompleted},
		{name: "fireworks length", provider: "fireworks", stopReason: "length", wantStatus: sessionruntime.RunIntentFailed, wantReason: "length"},
		{name: "google stop", provider: "google", stopReason: "STOP", wantStatus: sessionruntime.RunIntentCompleted},
		{name: "google safety", provider: "google", stopReason: "SAFETY", wantStatus: sessionruntime.RunIntentFailed, wantReason: "SAFETY"},
		{name: "google recitation", provider: "google", stopReason: "RECITATION", wantStatus: sessionruntime.RunIntentFailed, wantReason: "RECITATION"},
		{name: "openrouter stop", provider: "openrouter", stopReason: "stop", wantStatus: sessionruntime.RunIntentCompleted},
		{name: "openrouter content filter", provider: "openrouter", stopReason: "content_filter", wantStatus: sessionruntime.RunIntentFailed, wantReason: "content_filter"},
	}

	classifier := TerminalClassifier{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifier.Classify(TerminalClassifierInput{ProviderID: tt.provider, StopReason: tt.stopReason, HasFinalContent: true})
			if got.Status != tt.wantStatus || !got.Terminal {
				t.Fatalf("classification = %+v, want terminal status %q", got, tt.wantStatus)
			}
			if tt.wantStatus == sessionruntime.RunIntentCompleted {
				if got.EventType != "session.assistant.completed" || got.Reason != "" {
					t.Fatalf("completed classification = %+v", got)
				}
				return
			}
			if got.EventType != "session.run.failed" || !strings.Contains(got.Reason, tt.wantReason) {
				t.Fatalf("failed classification = %+v, want reason containing %q", got, tt.wantReason)
			}
		})
	}
}

func TestTerminalClassifierContinuationAndMalformedTerminals(t *testing.T) {
	classifier := TerminalClassifier{}
	if got := classifier.Classify(TerminalClassifierInput{ProviderID: "anthropic", StopReason: "tool_use", HasFinalContent: true, HasFunctionCalls: true}); got.Status != sessionruntime.RunIntentRunning || got.Terminal {
		t.Fatalf("tool-use with function calls = %+v, want running", got)
	}
	if got := classifier.Classify(TerminalClassifierInput{ProviderID: "anthropic", StopReason: "tool_use", HasFinalContent: true}); got.Status != sessionruntime.RunIntentFailed || !strings.Contains(got.Reason, "tool_use") {
		t.Fatalf("tool-use without calls = %+v, want failed", got)
	}
	if got := classifier.Classify(TerminalClassifierInput{ProviderID: "codex", StopReason: "", HasFinalContent: true}); got.Status != sessionruntime.RunIntentFailed || !strings.Contains(got.Reason, "without a terminal stop reason") {
		t.Fatalf("missing stop reason = %+v, want failed", got)
	}
	if got := classifier.Classify(TerminalClassifierInput{ProviderID: "codex", StopReason: "stop", HasFinalContent: false}); got.Status != sessionruntime.RunIntentFailed || !strings.Contains(got.Reason, "no final assistant content") {
		t.Fatalf("missing final content = %+v, want failed", got)
	}
	if got := classifier.Classify(TerminalClassifierInput{ProviderID: "codex", Err: errors.New("provider exploded")}); got.Status != sessionruntime.RunIntentFailed || !strings.Contains(got.Reason, "provider exploded") {
		t.Fatalf("runtime error = %+v, want failed", got)
	}
}

func TestSessionV3RunIntentStatusHelpers(t *testing.T) {
	for _, status := range []string{sessionruntime.RunIntentCompleted, sessionruntime.RunIntentFailed, sessionruntime.RunIntentCancelled, sessionruntime.RunIntentExpired, sessionruntime.RunIntentInterrupted, sessionruntime.RunIntentDispatchBlocked} {
		if !sessionV3RunIntentStatusTerminal(status) || sessionV3RunIntentStatusActive(status) {
			t.Fatalf("status %q terminal/active helpers disagree", status)
		}
	}
	for _, status := range []string{sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning} {
		if !sessionV3RunIntentStatusActive(status) || sessionV3RunIntentStatusTerminal(status) {
			t.Fatalf("status %q active/terminal helpers disagree", status)
		}
	}
}
