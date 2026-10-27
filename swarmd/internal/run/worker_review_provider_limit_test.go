package run

import (
	"strings"
	"testing"
)

// Requirement: a reviewer job whose provider turn ended on a usage, rate or
// session limit records no review on the reviewed job (it gave no verdict),
// while a reviewer that failed for any other reason still records BLOCK.
// Threat (socials daily check): a session-limit failure was recorded as the
// reviewer's BLOCK verdict ("missing or malformed review verdict"), so the
// workflow stopped as if the work had been rejected.
// Owners: WorkerExecutionService.recordReviewResult over providerlimit.Is,
// reviewFromResult and WorkerStore.RecordJobReview on a real Pebble store and
// session; the model provider is the only boundary replaced.
func TestRecordReviewResultSkipsAProviderLimit(t *testing.T) {
	limited := newReviewRecordFixture(t, "I'm checking the plan against its brief.")
	limited.reviewer.Status = "failed"
	limited.reviewer.Error = "claude code turn failed (success): You've hit your session limit · resets 4pm (UTC)"
	if err := limited.service.recordReviewResult(limited.reviewer); err != nil {
		t.Fatal(err)
	}
	review, err := limited.sessions.Store().WorkerStore().GetJobReview("account-a", limited.coder, "run-coder")
	if err != nil || review != nil {
		t.Fatalf("a provider limit was recorded as a review: %+v %v", review, err)
	}

	failed := newReviewRecordFixture(t, "I'm checking the plan against its brief.")
	failed.reviewer.Status = "failed"
	failed.reviewer.Error = "the reviewer's checkout could not be read"
	block := failed.record(t)
	if block.Verdict != "block" || !strings.HasPrefix(block.Reason, "reviewer run failed: ") {
		t.Fatalf("a failed reviewer did not record block: %+v", block)
	}
}
