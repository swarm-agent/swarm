package api

import (
	"fmt"
	"net/url"
	"strings"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func unchangedRangeDiff(output string) bool {
	if len(output) >= 4096 || strings.TrimSpace(output) == "" {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[2] != "=" {
			return false
		}
	}
	return true
}

// ReviewPipeline is durable per-job progress, not a second execution queue.
// Side effects use stable request identities so replay after a crash is safe.
type ReviewPipeline struct {
	Round         int    `json:"round"`
	Head          string `json:"head"`
	Verdict       string `json:"last_verdict"`
	ReviewerJobID string `json:"reviewer_job_id"`
	Findings      string `json:"findings,omitempty"`
	Next          string `json:"next"`
	Rebase        bool   `json:"rebase,omitempty"`
}

func pipelineKey(account, job string) string {
	return "publish_pipeline/" + url.PathEscape(account) + "/" + url.PathEscape(job)
}

// pipelineReceiptError leaves the durable transition intact for cursor replay.
type pipelineReceiptError struct{ error }

func (c *controlMCPCall) emitPipeline(worker, run string, refs controlMCPJobRefs, state ReviewPipeline) error {
	if state.Next == "" {
		return nil
	}
	severity := store.SignalSeverityInfo
	if state.Next == "review_failed" {
		severity = store.SignalSeverityWarning
	}
	err := c.signalPublishDurable(controlMCPJobWorker, worker, run, refs, "job."+state.Next, severity, "Review pipeline: "+state.Next, map[string]string{"head": state.Head, "findings": state.Findings, "reviewer_job_id": state.ReviewerJobID, "send_backs": fmt.Sprint(state.Round)})
	if err != nil {
		return pipelineReceiptError{err}
	}
	return nil
}

func (c *controlMCPCall) advancePipeline(worker, run string, refs controlMCPJobRefs, rule publish.Rule, result map[string]any) (bool, error) {
	principal, ok := PrincipalFromRequest(c.request)
	if !ok {
		return false, fmt.Errorf("pipeline principal required")
	}
	db := c.server.sessions.Store().Underlying()
	job := controlMCPJobID(controlMCPJobWorker, worker, run)
	key := pipelineKey(principal.AccountScopeID, job)
	var state ReviewPipeline
	if _, err := db.GetJSON(key, &state); err != nil {
		return false, err
	}
	// Repair a state-before-receipt crash before any early return or new transition.
	// Activity and signal writers both deduplicate durable receipts.
	if err := c.emitPipeline(worker, run, refs, state); err != nil {
		return false, err
	}
	head := controlMCPString(result, "head")
	save := func(next string) error {
		state.Next = next
		if err := db.PutJSON(key, state); err != nil {
			return err
		}
		return c.emitPipeline(worker, run, refs, state)
	}
	if state.Next == "review_exhausted" || state.Next == "review_failed" {
		return false, save(state.Next)
	}
	review, err := c.server.sessions.Store().WorkerStore().CurrentValidJobReview(principal.AccountScopeID, worker, run, head)
	if err != nil {
		return false, err
	}
	if review != nil {
		if state.Head != head {
			state.Next = ""
		}
		state.Head, state.Verdict, state.ReviewerJobID = head, review.Verdict, review.ReviewerJobID
		if review.Verdict == "ok" {
			return true, save("review_approved")
		}
		state.Findings = review.Reason
		if state.Next == "review_fix_sent" {
			return false, nil
		}
		limits, err := db.GetLimits(principal.AccountScopeID)
		if err != nil {
			return false, err
		}
		if state.Next != "review_fix_pending" {
			if !state.Rebase {
				state.Round++
			}
			if !state.Rebase && state.Round >= limits.FixRounds {
				return false, save("review_exhausted")
			}
			if err := save("review_fix_pending"); err != nil {
				return false, err
			}
		}
		_, err = controlMCPMessageJob(c, map[string]any{"job_id": job, "client_request_id": "pipeline-fix-" + run + "-" + head, "message": "Fix only these findings and commit.\n\n" + state.Findings})
		if err != nil {
			return false, err
		}
		return false, save("review_fix_sent")
	}
	if state.Head == head && state.ReviewerJobID != "" {
		return false, nil
	}
	brief := "Review this exact head: " + head + ". Check previous findings first and say which are fixed.\nPrevious findings:\n" + state.Findings
	q, err := c.server.landingQueue()
	if err != nil {
		return false, err
	}
	entry, _, err := q.Get(principal.AccountScopeID, job)
	if err != nil {
		return false, err
	}
	state.Rebase = false
	if entry.SendBacks > 0 && strings.HasPrefix(entry.Reason, "Rebase conflict:") {
		state.Rebase = true
		brief += "\nRebase-scoped review: review only the conflict resolution and confirm the range-diff against the previously reviewed commits."
	}
	response, err := controlMCPGiveJob(c, map[string]any{"worker": "reviewer", "project": refs.projectID, "workspace": rule.Folder, "review_job": job, "brief": brief, "idempotency_key": "pipeline-review-" + run + "-" + head})
	if err != nil {
		return false, err
	}
	state.Head, state.Verdict = head, ""
	state.ReviewerJobID = controlMCPString(controlMCPMap(response), "job_id")
	if state.ReviewerJobID == "" {
		return false, fmt.Errorf("pipeline review admission returned no job id")
	}
	return false, save("review_started")
}
