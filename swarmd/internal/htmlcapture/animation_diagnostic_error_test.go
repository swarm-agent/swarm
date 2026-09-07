package htmlcapture

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// Requirement: WithAnimationDiagnostics must bound untrusted numeric/selector
// evidence and preserve failure identity; it must never turn success into an
// error. Unit tests isolate this error-only conversion boundary from Chromium.
func TestWithAnimationDiagnosticsBounds(t *testing.T) {
	failure := NewError("animation_viewport_overflow", "viewport rejected")
	timeMS := 0
	d := AnimationDiagnostic{Stage: "viewport", Outcome: "bounds_overflow", TimestampMS: &timeMS, Selector: "\x00" + strings.Repeat("x", 1000), Bounds: &AnimationBounds{Left: -2, Right: 20, Bottom: 20}}
	list := make([]AnimationDiagnostic, 100)
	for i := range list {
		list[i] = d
	}
	err := WithAnimationDiagnostics(failure, list)
	if !errors.Is(err, failure) || strings.Count(err.Error(), "selector=") != 4 || len(err.Error()) > 2000 || strings.ContainsRune(err.Error(), '\x00') {
		t.Fatalf("unbounded diagnostics: %v", err)
	}
	d.Bounds = &AnimationBounds{Left: math.Inf(1)}
	if WithAnimationDiagnostics(failure, []AnimationDiagnostic{d}) != failure {
		t.Fatal("invalid numeric bounds propagated")
	}
	if WithAnimationDiagnostics(nil, list) != nil {
		t.Fatal("success became failure")
	}
	other := errors.New("unrelated")
	if WithAnimationDiagnostics(other, list) != other {
		t.Fatal("unrelated failure changed")
	}
}
