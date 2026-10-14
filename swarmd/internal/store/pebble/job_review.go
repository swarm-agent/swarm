package pebblestore

import "strings"

// JobReview is independent review evidence for one immutable head. Landing
// policy may consume CurrentJobReview; this does not impose a landing gate.
type JobReview struct {
	Verdict       string `json:"verdict"`
	Head          string `json:"head"`
	ReviewerJobID string `json:"reviewer_job_id"`
	Model         string `json:"model"`
	RecordedAt    int64  `json:"recorded_at"`
	Reason        string `json:"reason,omitempty"`
}

// ParseJobReview fails closed on absent, duplicate or malformed verdicts and
// missing report sections. It never interprets quoted prose as an approval.
func ParseJobReview(report, head, reviewerJobID, model string, now int64) JobReview {
	r := JobReview{Verdict: "block", Head: head, ReviewerJobID: reviewerJobID, Model: model, RecordedAt: now, Reason: "missing or malformed review verdict"}
	lines := strings.Split(strings.TrimSpace(report), "\n")
	if len(lines) == 0 || (lines[0] != "VERDICT: OK" && lines[0] != "VERDICT: BLOCK") {
		return r
	}
	verdicts := 0
	sections := []string{"SUMMARY:", "FINDINGS:", "TESTS RUN:"}
	next := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "VERDICT:") {
			verdicts++
		}
		if next < len(sections) && strings.HasPrefix(line, sections[next]) {
			next++
		}
	}
	if verdicts != 1 || next != len(sections) || head == "" {
		return r
	}
	if lines[0] == "VERDICT: BLOCK" {
		r.Reason = strings.TrimSpace(report)
		return r
	}
	r.Verdict, r.Reason = "ok", ""
	return r
}

// CurrentJobReview returns evidence only for the current exact head. New
// commits make prior evidence stale regardless of its stored verdict.
func CurrentJobReview(review *JobReview, head string) *JobReview {
	if review == nil || head == "" || review.Head != head {
		return nil
	}
	copy := *review
	return &copy
}
