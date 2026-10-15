package api

import (
	"errors"

	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: AutoPublisher.once consumes OK review signals, but never BLOCK or
// unrelated signals; the event predicate is the narrowest routing-layer proof.
func TestAutoPublishReviewSignal(t *testing.T) {
	for _, tc := range []struct {
		kind, verdict string
		want          bool
	}{
		{"job.reviewed", "ok", true}, {"job.reviewed", "block", false}, {"job.reviewed", "", false},
		{"worker.run.succeeded", "", true}, {"job.landed", "ok", false},
	} {
		if autoPublishSignal(tc.kind, tc.verdict) != tc.want {
			t.Fatal(tc)
		}
	}
	if reviewRequired("head").Error() != "needs an OK review of head: give the reviewer `review_job`" {
		t.Fatal("missing remediation")
	}
}

// Purpose: rejectBlockingPatch is the preparation policy boundary: exact-head
// and equivalent-head BLOCK both override an older OK, while distinct patches
// do not. Lookup failure must fail closed, never authorize landing.
func TestLandReviewBlockingPatch(t *testing.T) {
	for _, tc := range []struct {
		name, head, patch string
		fail, want        bool
	}{
		{"rebased", "current", "different", false, true},
		{"equivalent", "other", "same", false, true},
		{"different", "other", "different", false, false},
		{"lookup error", "other", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectBlockingPatch("current", "same", []store.JobReview{{Head: "original", Verdict: "ok"}, {Head: tc.head, Verdict: "block"}}, func(string) (string, error) {
				if tc.fail {
					return "", errors.New("unavailable")
				}
				return tc.patch, nil
			})
			if (err != nil) != tc.want {
				t.Fatalf("BLOCK decision: %v", err)
			}
		})
	}
}
