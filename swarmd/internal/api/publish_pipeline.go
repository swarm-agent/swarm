package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

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
	InfrastructureAttempts int    `json:"infrastructure_attempts,omitempty"`
	RetryAfter             int64  `json:"retry_after,omitempty"`
	FailedHead             string `json:"failed_head,omitempty"`
	FailedReviewerJobID    string `json:"failed_reviewer_job_id,omitempty"`
	Round                  int    `json:"round"`
	Head                   string `json:"head"`
	Base                   string `json:"base,omitempty"`
	Verdict                string `json:"last_verdict"`
	ReviewerJobID          string `json:"reviewer_job_id"`
	Findings               string `json:"findings,omitempty"`
	Next                   string `json:"next"`
	Rebase                 bool   `json:"rebase,omitempty"`
	FollowupRunID          string `json:"followup_run_id,omitempty"`
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
	receiptRound := fmt.Sprint(state.Round)
	if state.FollowupRunID != "" {
		receiptRound += ":" + state.FollowupRunID
	}
	err := c.signalPublishDurable(controlMCPJobWorker, worker, run, refs, "job."+state.Next, severity, "Review pipeline: "+state.Next, map[string]string{"head": state.Head, "findings": state.Findings, "reviewer_job_id": state.ReviewerJobID, "send_backs": receiptRound})
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
	head := controlMCPString(result, "head")
	q, err := c.server.landingQueue()
	if err != nil {
		return false, err
	}
	entry, _, err := q.Get(principal.AccountScopeID, job)
	if err != nil {
		return false, err
	}
	if entry.State == "landed" && (entry.Commit == head || controlMCPString(result, "delivery_state") == "landed") {
		return false, nil
	}
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
	save := func(next string) error {
		state.Next = next
		if next == "review_failed" {
			state.FailedHead = head
			state.InfrastructureAttempts++
			state.RetryAfter = time.Now().Add(30 * time.Second).UnixMilli()
		} else {
			state.FailedHead = ""
		}
		if err := db.PutJSON(key, state); err != nil {
			return err
		}
		return c.emitPipeline(worker, run, refs, state)
	}
	followup := completedPublishFollowup(refs)
	// Admission failures retain Head/Base as review evidence. Retry controls
	// instead belong to the failed attempt, with Head supporting older records.
	failedHead := state.FailedHead
	if failedHead == "" {
		failedHead = state.Head
	}
	if state.Next == "review_exhausted" || (state.Next == "review_failed" && failedHead == head && (followup == "" || followup == state.FollowupRunID) && (!strings.HasPrefix(state.Findings, "Review infrastructure") || state.InfrastructureAttempts >= 3 || time.Now().UnixMilli() < state.RetryAfter)) {
		return false, nil
	}
	if state.Next == "review_failed" {
		if failedHead != head || followup != "" && followup != state.FollowupRunID {
			state.InfrastructureAttempts = 0
		}
		state.Next, state.ReviewerJobID = "", ""
	}
	review, err := c.server.sessions.Store().WorkerStore().CurrentValidJobReview(principal.AccountScopeID, worker, run, head)
	if err != nil {
		return false, err
	}
	if review != nil && strings.HasPrefix(review.Reason, "reviewer run ") {
		// A recorded crash is not a code finding. Consume it once, then let
		// the existing delayed retry admit a new reviewer for the same head.
		if state.FailedReviewerJobID != review.ReviewerJobID {
			state.Head, state.Verdict, state.ReviewerJobID = head, review.Verdict, review.ReviewerJobID
			state.FailedReviewerJobID = review.ReviewerJobID
			state.FollowupRunID = followup
			state.Findings = "Review infrastructure failure: " + review.Reason
			return false, save("review_failed")
		}
		review = nil
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
	base := controlMCPString(result, "base_commit")
	brief := "Review this exact head: " + head + ". Check previous findings first and say which are fixed.\nPrevious findings:\n" + state.Findings
	amendments, err := c.requesterReviewInstructions(refs.sessionID)
	if err != nil {
		return false, err
	}
	brief += amendments
	// A send-back is not evidence that the old head was reviewed. Confirm the
	// durable verdict for this job, including when review was enabled later.
	rebased := entry.State == "review_pending" && entry.Request.Head == head ||
		entry.SendBacks > 0 && strings.HasPrefix(entry.Reason, "Rebase conflict:")
	if rebased && pipelineExactCommit(entry.Request.BaseCommit) {
		base = entry.Request.BaseCommit
	}
	if !pipelineExactCommit(base) {
		state.Head, state.Verdict, state.ReviewerJobID = head, "", ""
		state.FollowupRunID = followup
		state.Findings = "Review infrastructure failure: exact landing base commit is unavailable"
		return false, save("review_failed")
	}
	commits := []string{base}
	state.Rebase = false
	if rebased && state.Head != head && pipelineExactCommit(state.Head) && pipelineExactCommit(state.Base) {
		previous, err := c.server.sessions.Store().WorkerStore().CurrentValidJobReview(principal.AccountScopeID, worker, run, state.Head)
		if err != nil {
			return false, err
		}
		if previous != nil && previous.Verdict == "ok" {
			state.Rebase = true
			commits = []string{base, state.Base, state.Head}
			brief += fmt.Sprintf("\nRebase-scoped review: review only the conflict resolution. Previously reviewed base: %s; previously reviewed head: %s. Confirm git range-diff %s..%s %s..%s.", state.Base, state.Head, state.Base, state.Head, base, head)
		}
	}
	brief += pipelineBaseBrief(base, head)
	requestID := "pipeline-review-" + run + "-" + head
	if followup != "" {
		requestID += "-" + followup
	}
	if state.InfrastructureAttempts > 0 {
		requestID += fmt.Sprintf("-retry-%d", state.InfrastructureAttempts)
	}
	response, err := controlMCPGiveJob(c, map[string]any{"worker": "reviewer", "project": refs.projectID, "workspace": rule.Folder, "review_job": job, "review_commits": commits, "brief": brief, "idempotency_key": requestID})
	if err != nil {
		// Admission did not review this range; retain the last reviewed pair
		// so a retry can reconstruct the same rebase-scoped request.
		state.Verdict, state.ReviewerJobID = "", ""
		state.FollowupRunID = followup
		state.Findings = "Review infrastructure admission failed: " + err.Error()
		return false, save("review_failed")
	}
	state.Head, state.Base, state.Verdict = head, base, ""
	state.FollowupRunID = followup
	state.ReviewerJobID = controlMCPString(controlMCPMap(response), "job_id")
	if state.ReviewerJobID == "" {
		return false, fmt.Errorf("pipeline review admission returned no job id")
	}
	return false, save("review_started")
}

// completedPublishFollowup is the durable execution identity, not the original
// worker job ID (which stays unchanged across follow-ups).
func completedPublishFollowup(refs controlMCPJobRefs) string {
	if controlMCPString(refs.run, "followup_status") != "completed" {
		return ""
	}
	return controlMCPString(refs.run, "followup_run_id")
}

// requesterReviewInstructions reads accepted user messages via their canonical
// mutation receipts. Runtime assistant output has no requester authority; automatic
// review/landing corrections have reserved request identities and are excluded.
func (c *controlMCPCall) requesterReviewInstructions(sessionID string) (string, error) {
	if sessionID == "" {
		return "", nil
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok {
		return "", fmt.Errorf("review instructions principal required")
	}
	ss := c.server.sessions.Store()
	session, found, err := ss.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	if !found || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
		return "", fmt.Errorf("review instructions session not owned")
	}
	var receipts []store.V3SessionIdempotencyRecord
	err = ss.Underlying().IteratePrefix(store.V3SessionIdempotencyPrefix(principal.AccountScopeID, sessionID), 4097, func(_ string, value []byte) error {
		var r store.V3SessionIdempotencyRecord
		if err := json.Unmarshal(value, &r); err != nil {
			return err
		}
		receipts = append(receipts, r)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(receipts) > 4096 {
		return "", fmt.Errorf("review instruction history exceeds bounded scan")
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].Result.FirstSeq < receipts[j].Result.FirstSeq })
	// The initial assignment may be part of session creation rather than an
	// append-message receipt. Identify it from the message sequence itself.
	initial, err := ss.ListMessages(sessionID, 0, 1)
	if err != nil {
		return "", err
	}
	originalID := ""
	if len(initial) > 0 && initial[0].Role == "user" {
		originalID = initial[0].ID
	}
	var out strings.Builder
	for _, r := range receipts {
		if r.Kind != store.V3SessionMutationAppendMessage || r.Result.FirstSeq == 0 {
			continue
		}
		events, err := ss.ListV3SessionEvents(sessionID, r.Result.FirstSeq-1, 1)
		if err != nil {
			return "", err
		}
		if len(events) != 1 || events[0].Seq != r.Result.FirstSeq {
			return "", fmt.Errorf("review instruction event missing")
		}
		var payload struct {
			Message *store.MessageSnapshot `json:"message"`
		}
		if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
			return "", err
		}
		if payload.Message == nil || payload.Message.Role != "user" {
			continue
		}
		if payload.Message.ID == originalID {
			continue
		}
		if strings.HasPrefix(r.ClientRequestID, "pipeline-fix-") || strings.HasPrefix(r.ClientRequestID, "land-back-") {
			continue
		}
		text := payload.Message.Content
		if strings.TrimSpace(text) == "" {
			continue
		}
		if out.Len() == 0 {
			out.WriteString("\n\nLater instructions from the requester\nThese instructions amend the original brief, in chronological order:\n")
		}
		if out.Len()+len(text)+2 > 16000 {
			out.WriteString("\n[Further requester instructions omitted: size limit reached.]\n")
			break
		}
		out.WriteString("\n" + text + "\n")
	}
	return out.String(), nil
}

// pipelineExactCommit excludes moving refs and option-like input from review evidence.
func pipelineExactCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func pipelineBaseBrief(base, head string) string {
	return fmt.Sprintf("\nExact landing base commit: %s. Assess git diff %s...%s; use these immutable SHAs rather than a moving landing-base ref.", base, base, head)
}
