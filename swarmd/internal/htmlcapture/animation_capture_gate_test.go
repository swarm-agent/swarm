package htmlcapture

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Requirement: captureAnimationFrame must exclusively own shared-browser
// activation, seek/paint and screenshot until stability readback completes.
// Threat: sibling tabs contend for compositor ownership; cancellation must not
// leak a permit or cancel the page owner. This unit boundary proves exclusion,
// job isolation and cancellation without timing-dependent browser scheduling.
func TestAnimationCaptureGateOwnershipAndCancellation(t *testing.T) {
	owner := withAnimationCaptureGate(context.Background())
	release, err := acquireAnimationCapture(owner)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(owner, 20*time.Millisecond)
	defer cancel()
	if stop, err := acquireAnimationCapture(waiting); !errors.Is(err, context.DeadlineExceeded) || stop != nil {
		t.Fatalf("contended acquisition: %v", err)
	}
	independent := withAnimationCaptureGate(context.Background())
	other, err := acquireAnimationCapture(independent)
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	next, err := acquireAnimationCapture(owner)
	if err != nil {
		t.Fatal(err)
	}
	next()
	cancelled, stop := context.WithCancel(owner)
	stop()
	if release, err := acquireAnimationCapture(cancelled); !errors.Is(err, context.Canceled) || release != nil {
		t.Fatalf("cancelled acquisition: %v", err)
	}
	if owner.Err() != nil {
		t.Fatal("page owner cancelled")
	}
}
