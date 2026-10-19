package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

type recordingAttemptObserver struct {
	mu     sync.Mutex
	events []string
}

func (o *recordingAttemptObserver) add(event string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}
func (o *recordingAttemptObserver) AttemptTimedOut(attempt int) {
	o.add(fmt.Sprint("timed_out:", attempt))
}
func (o *recordingAttemptObserver) AttemptStreamIncomplete(attempt int) {
	o.add(fmt.Sprint("stream_incomplete:", attempt))
}
func (o *recordingAttemptObserver) AttemptCancelled(attempt int) {
	o.add(fmt.Sprint("cancelled:", attempt))
}
func (o *recordingAttemptObserver) AttemptRetrying(attempt int) {
	o.add(fmt.Sprint("retrying:", attempt))
}
func (o *recordingAttemptObserver) AttemptFailed(attempt int, _ error) {
	o.add(fmt.Sprint("failed:", attempt))
}
func (o *recordingAttemptObserver) list() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Join(o.events, " ")
}

func TestProviderAttemptReplaysAStreamWithoutFinishReasonOnceAndRecordsIt(t *testing.T) {
	// Purpose:
	// - Requirement: when a provider attempt fails with
	//   provideriface.ErrStreamIncomplete (the stream closed without a finish
	//   reason), runProviderAttempt replays the exact same request once,
	//   reports stream_incomplete then retrying to the attempt observer (which
	//   the run records durably as session.run.recovery.* events), and returns
	//   only the replacement's response; a second incomplete stream is
	//   terminal. Any other provider error is returned at once, never replayed.
	// - Threat/regression: one truncated Vertex stream killed a whole Coder job
	//   (1 of 10 in two qualification runs); an unbounded or silent replay would
	//   hide a failing provider or spend without limit, and replaying ordinary
	//   errors (refusals, 4xx) would double cost for nothing.
	// - Boundary/authority: runProviderAttempt (provider_attempt_watchdog.go)
	//   and providerAttemptRetryLimit; tools run only after it returns success
	//   (runTurn), so a replayed attempt has executed none.
	// - Narrowest test layer: runProviderAttempt with a scripted runner and a
	//   recording observer; no store, network or daemon.
	request := provideriface.Request{Model: "test-model"}
	incomplete := fmt.Errorf("vertex %w", provideriface.ErrStreamIncomplete)

	t.Run("one replay succeeds", func(t *testing.T) {
		observer := &recordingAttemptObserver{}
		runner := &watchdogTestRunner{run: func(_ context.Context, attempt int, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
			if attempt == 1 {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "partial"})
				return provideriface.Response{Text: "partial", FunctionCalls: []provideriface.FunctionCall{{CallID: "c1", Name: "edit"}}}, incomplete
			}
			return provideriface.Response{Text: "done"}, nil
		}}
		ctx := withProviderAttemptObserver(context.Background(), observer)
		resp, err := runProviderAttempt(ctx, runner, request, time.Minute, func(provideriface.StreamEvent) {})
		if err != nil || resp.Text != "done" || len(resp.FunctionCalls) != 0 {
			t.Fatalf("resp = %+v, err = %v; want only the replacement's response", resp, err)
		}
		if runner.attempts != 2 {
			t.Fatalf("attempts = %d, want 2", runner.attempts)
		}
		if got := observer.list(); got != "stream_incomplete:1 retrying:2" {
			t.Fatalf("observer = %q", got)
		}
	})

	t.Run("second incomplete stream is terminal", func(t *testing.T) {
		observer := &recordingAttemptObserver{}
		runner := &watchdogTestRunner{run: func(context.Context, int, func(provideriface.StreamEvent)) (provideriface.Response, error) {
			return provideriface.Response{}, incomplete
		}}
		ctx := withProviderAttemptObserver(context.Background(), observer)
		_, err := runProviderAttempt(ctx, runner, request, time.Minute, nil)
		if !errors.Is(err, provideriface.ErrStreamIncomplete) || err.Error() != "vertex stream ended without a finish reason after 2 attempts" {
			t.Fatalf("err = %v", err)
		}
		if runner.attempts != 2 {
			t.Fatalf("attempts = %d, want the bounded 2", runner.attempts)
		}
		if got := observer.list(); got != "stream_incomplete:1 retrying:2 stream_incomplete:2 failed:2" {
			t.Fatalf("observer = %q", got)
		}
	})

	t.Run("other errors are not replayed", func(t *testing.T) {
		for _, providerErr := range []error{
			errors.New("vertex streamGenerateContent failed status=400"),
			// Same words without the sentinel: only adapters that ran no tools tag it.
			errors.New("copilot stream ended without a finish reason"),
		} {
			observer := &recordingAttemptObserver{}
			runner := &watchdogTestRunner{run: func(context.Context, int, func(provideriface.StreamEvent)) (provideriface.Response, error) {
				return provideriface.Response{}, providerErr
			}}
			ctx := withProviderAttemptObserver(context.Background(), observer)
			if _, err := runProviderAttempt(ctx, runner, request, time.Minute, nil); err != providerErr {
				t.Fatalf("err = %v, want %v unchanged", err, providerErr)
			}
			if runner.attempts != 1 || observer.list() != "" {
				t.Fatalf("attempts = %d, observer = %q; want one attempt and no recovery", runner.attempts, observer.list())
			}
		}
	})
}

func TestProviderAttemptRecoveryRecordsAreDistinctPerStep(t *testing.T) {
	// Purpose:
	// - Requirement: every recovery record the durable observer writes is a
	//   session.run.recovery.<state> diagnostic whose idempotency key names the
	//   run, step and attempt, so a replay at a later step of the same run is
	//   recorded instead of colliding with an earlier step's key.
	// - Threat/regression: the key used to omit the step; the second
	//   stream_incomplete in one run reused it with a different payload, the
	//   store refused it as an idempotency conflict and the observer dropped
	//   the error, making that replay invisible in session events.
	// - Boundary/authority: durableProviderAttemptObserver.record
	//   (provider_attempt_observability.go), ApplySessionMutation idempotency.
	// - Narrowest test layer: the observer with a capturing mutation function.
	var inputs []sessionruntime.SessionMutationInput
	apply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		inputs = append(inputs, input)
		return sessionruntime.SessionMutationResult{}, nil
	}
	principal := identity.Principal{UserID: "user-1", AccountScopeID: "account-1"}
	for _, step := range []int{1, 2} {
		observer := &durableProviderAttemptObserver{sessionID: "session-1", runID: "run-1", step: step, principal: principal, apply: apply}
		observer.AttemptStreamIncomplete(1)
	}
	if len(inputs) != 2 {
		t.Fatalf("records = %d, want 2", len(inputs))
	}
	if inputs[0].IdempotencyKey == inputs[1].IdempotencyKey {
		t.Fatalf("both steps share idempotency key %q", inputs[0].IdempotencyKey)
	}
	for i, input := range inputs {
		if input.EventType != "session.run.recovery.stream_incomplete" || input.Kind != sessionruntime.SessionMutationRecordDiagnostic {
			t.Fatalf("record %d = %s/%s", i, input.Kind, input.EventType)
		}
		var payload struct {
			Step   int    `json:"step"`
			State  string `json:"recovery_state"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(input.EventPayload, &payload); err != nil || payload.Step != i+1 || payload.State != "stream_incomplete" || payload.Reason != "provider_stream_ended_without_finish" {
			t.Fatalf("record %d payload = %s, err = %v", i, input.EventPayload, err)
		}
	}
}
