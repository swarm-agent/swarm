package run

import (
	"context"
	"errors"
	"strings"
)

// Who can end a run that run.Service owns. A run is cancelled only by its own
// stop (a stop request for its session and run, or worker job control acting
// on its receipt) or by the context its caller started it with (the request,
// executor or daemon context).
const (
	RunCancelledByStopRequest   = "a stop request"
	RunCancelledByWorkerControl = "worker job control"
	RunCancelledByCaller        = "its caller's context"
)

// RunCancelledError names what cancelled a run. It still matches
// context.Canceled, so lifecycle classification and failure persistence keep
// treating the run as stopped, not failed.
type RunCancelledError struct {
	By     string
	Reason string
	Err    error
}

func (e *RunCancelledError) Error() string {
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "no reason given"
	}
	return "run cancelled by " + e.By + ": " + reason
}

func (e *RunCancelledError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return context.Canceled
}

func (e *RunCancelledError) Is(target error) bool { return target == context.Canceled }

func runCancelCause(by, reason string) error {
	return &RunCancelledError{By: by, Reason: strings.TrimSpace(reason)}
}

// newRunContext is the context a run executes under. Its cancel function
// records the cause, so a stop says by what it was made.
func newRunContext(parent context.Context) (context.Context, context.CancelCauseFunc) {
	return context.WithCancelCause(parent)
}

// namedRunCancellation returns err with the cause of the run's cancellation
// when err is that cancellation, and err unchanged otherwise. caller is the
// context the run was started with; runCtx is the run's own context.
func namedRunCancellation(caller, runCtx context.Context, err error) error {
	if err == nil || runCtx == nil || !errors.Is(err, context.Canceled) {
		return err
	}
	var named *RunCancelledError
	if errors.As(err, &named) {
		return err
	}
	// The run context keeps the first cause it was cancelled with, so a stop
	// that came before its caller ended is still named as the stop.
	if runCtx.Err() != nil && errors.As(context.Cause(runCtx), &named) {
		return &RunCancelledError{By: named.By, Reason: named.Reason, Err: err}
	}
	if caller != nil && caller.Err() != nil {
		return &RunCancelledError{By: RunCancelledByCaller, Reason: context.Cause(caller).Error(), Err: err}
	}
	return err
}
