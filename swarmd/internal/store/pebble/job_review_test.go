package pebblestore

import "testing"

// Purpose: ParseJobReview and CurrentJobReview must fail closed on malformed
// reports and stale heads. Pure unit tests are the narrowest layer for parsing
// and exact-head validity; they do not prove durable result recording.
func TestJobReviewVerdicts(t *testing.T) {
	for _, tc := range []struct{ report, verdict string }{
		{"VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: go test", "ok"},
		{"VERDICT: BLOCK\nSUMMARY: broken\nFINDINGS: 1. high src.go:1\nTESTS RUN: failed", "block"},
		{"", "block"}, {"VERDICT: OK", "block"},
		{"VERDICT: OK\nSUMMARY: fine\nVERDICT: BLOCK\nFINDINGS: None\nTESTS RUN: none", "block"},
	} {
		r := ParseJobReview(tc.report, "head", "review-job", "model", 123)
		if r.Verdict != tc.verdict {
			t.Fatalf("%q: %+v", tc.report, r)
		}
		if CurrentJobReview(&r, "head") == nil || CurrentJobReview(&r, "new-head") != nil {
			t.Fatal("head validity")
		}
		if r.Verdict == "block" && r.Reason == "" {
			t.Fatal("block without reason")
		}
	}
	if role, err := NormalizeWorkerRole("Reviewer"); err != nil || role != WorkerRoleReviewer {
		t.Fatal(role, err)
	}
}
