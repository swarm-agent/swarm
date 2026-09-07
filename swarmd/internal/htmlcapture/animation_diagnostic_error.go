package htmlcapture

import (
	"errors"
	"fmt"
	"strings"
)

// WithAnimationDiagnostics preserves the typed failure while adding bounded,
// sanitized viewport evidence at adapters that cannot return AnimationResult.
// It never includes HTML, console output, URLs or renderer process errors.
func WithAnimationDiagnostics(err error, diagnostics []AnimationDiagnostic) error {
	if err == nil {
		return nil
	}
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "animation_viewport_overflow" {
		return err
	}
	var details []string
	for _, d := range boundedAnimationDiagnostics(diagnostics) {
		if d.Stage != "viewport" || d.Outcome != "bounds_overflow" || d.Bounds == nil || d.TimestampMS == nil || *d.TimestampMS < 0 || *d.TimestampMS > MaxAnimationDurationMS {
			continue
		}
		details = append(details, fmt.Sprintf("selector=%q%s time_ms=%d bounds=[%.2f,%.2f,%.2f,%.2f]", d.Selector, d.Pseudo, *d.TimestampMS, d.Bounds.Left, d.Bounds.Top, d.Bounds.Right, d.Bounds.Bottom))
		if len(details) == 4 {
			break
		}
	}
	if len(details) == 0 {
		return err
	}
	return fmt.Errorf("%w; viewport diagnostics: %s", err, strings.Join(details, "; "))
}
