package htmlcapture

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var errAnimationRenderDeadline = errors.New("animation render deadline")

// Keep the page's CDP executor while propagating pipeline cancellation into
// in-flight browser operations, not merely checking it before starting a frame.
func animationCaptureContext(page, pipeline context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(page)
	stop := context.AfterFunc(pipeline, func() { cancel(context.Cause(pipeline)) })
	if pipeline.Err() != nil {
		cancel(context.Cause(pipeline))
	}
	return ctx, func() { stop(); cancel(context.Canceled) }
}

// Diagnostics contain only renderer-owned stages, numeric timing and a closed
// cancellation scope. Never include browser exceptions, paths or author bytes.
func animationFrameFailure(parent, frame context.Context, err error, stage string, timestamp int, budget, elapsed time.Duration) error {
	scope := ""
	cause := frame.Err()
	if parent.Err() != nil {
		cause = context.Cause(parent)
		switch {
		case errors.Is(cause, errAnimationRenderDeadline):
			scope = "render_deadline"
		case errors.Is(cause, context.DeadlineExceeded):
			scope = "parent_deadline"
		default:
			scope = "parent_cancelled"
		}
	} else if errors.Is(frame.Err(), context.DeadlineExceeded) {
		scope = "frame_deadline"
	}
	if scope == "" {
		return err
	}
	switch stage {
	case "seek_and_paint", "viewport_audit", "screenshot", "stability_wait", "stability_screenshot":
	default:
		stage = "frame"
	}
	return newErrorWithCause("animation_timeout", fmt.Sprintf("animation capture interrupted: scope=%s stage=%s timestamp_ms=%d frame_budget_ms=%d frame_elapsed_ms=%d; frame budget is shared by seek, paint and screenshot; inspect parent/tool deadline or renderer load according to scope", scope, stage, timestamp, budget.Milliseconds(), elapsed.Milliseconds()), cause)
}
