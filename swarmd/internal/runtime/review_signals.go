package runtime

import (
	"strings"

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
		refs := map[string]string{"worker_id": worker, "run_id": run, "job_id": "worker/" + worker + "/" + run, "reviewer_job_id": review.ReviewerJobID}
		if project, ok := strings.CutPrefix(worker, "task:"); ok {
			// A review of one of Swarm's own agents' tasks (store.TaskReviewWorker).
			refs = map[string]string{"project_id": project, "task_id": run, "job_id": "task/" + project + "/" + run, "reviewer_job_id": review.ReviewerJobID}
		}
		emitter.Emit(store.Signal{Kind: "job.reviewed", Severity: severity, Account: account, Summary: "Job review recorded", DedupKey: "job.reviewed:" + review.ReviewerJobID, Refs: refs, Attrs: attrs})
	}
}
