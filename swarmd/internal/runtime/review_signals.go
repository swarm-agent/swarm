package runtime

import (
	"swarm/packages/swarmd/internal/signals"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func jobReviewSignal(emitter *signals.Emitter) func(string, string, string, store.JobReview) {
	return func(account, worker, run string, review store.JobReview) {
		severity := store.SignalSeverityInfo
		if review.Verdict == "block" {
			severity = store.SignalSeverityWarning
		}
		attrs := map[string]string{"verdict": review.Verdict, "head": review.Head}
		if review.FixHead != "" {
			attrs["fix_head"] = review.FixHead
		}
		emitter.Emit(store.Signal{Kind: "job.reviewed", Severity: severity, Account: account, Summary: "Job review recorded", DedupKey: "job.reviewed:" + review.ReviewerJobID, Refs: map[string]string{"worker_id": worker, "run_id": run, "job_id": "worker/" + worker + "/" + run, "reviewer_job_id": review.ReviewerJobID}, Attrs: attrs})
	}
}
