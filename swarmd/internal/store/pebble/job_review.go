package pebblestore

import "strings"

// JobReview is independent review evidence for one immutable head.
type JobReview struct {
	Verdict       string `json:"verdict"`
	Head          string `json:"head"`
	ReviewerJobID string `json:"reviewer_job_id"`
	Model         string `json:"model"`
	RecordedAt    int64  `json:"at"`
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
	sections := []string{"SUMMARY:", "FINDINGS:", "TESTS RUN:"}
	next := 0
	nonempty := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		// Structural headings may not appear quoted, fenced, indented,
		// duplicated or embedded in prose: ambiguous reports fail closed.
		for _, heading := range append([]string{"VERDICT:"}, sections...) {
			if strings.Contains(line, heading) && !strings.HasPrefix(line, heading) {
				return r
			}
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, ">") {
			return r
		}
		if strings.HasPrefix(line, "VERDICT:") {
			return r
		}
		structural := false
		for _, heading := range sections {
			if strings.HasPrefix(line, heading) {
				if next >= len(sections) || heading != sections[next] || (next > 0 && !nonempty) {
					return r
				}
				next++
				nonempty = strings.TrimSpace(strings.TrimPrefix(line, heading)) != ""
				structural = true
				break
			}
		}
		if !structural && trimmed != "" {
			if next == 0 {
				return r
			}
			nonempty = true
		}
	}
	if next != len(sections) || !nonempty || head == "" {
		return r
	}
	if lines[0] == "VERDICT: BLOCK" {
		r.Reason = strings.TrimSpace(report)
		return r
	}
	r.Verdict, r.Reason = "ok", ""
	return r
}
