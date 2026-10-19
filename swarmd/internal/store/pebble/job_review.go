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
	// Findings is the verbatim FINDINGS section of the parsed report, read
	// from the reviewer's full final message and bounded at
	// MaxJobReviewFindingsBytes. Empty for a malformed report and for
	// records written before this field existed.
	Findings string `json:"findings,omitempty"`
	// FixHead is set when a reviewer allowed to commit security fixes
	// committed on top of Head: its verdict covers Head only together with
	// those fix commits (up to FixHead), never Head alone.
	FixHead string `json:"fix_head,omitempty"`
}

// MaxJobReviewFindingsBytes bounds the findings a review record keeps; the
// same bound applies to an automation agent step's findings and to what a
// reopen step sends.
const MaxJobReviewFindingsBytes = 16000

// JobReviewFindingsCutNote ends findings cut at MaxJobReviewFindingsBytes.
const JobReviewFindingsCutNote = "[findings cut at 16000 bytes; the reviewer job's session has the full report]"

// jobReviewHeadings are the report's structural headings, in order.
var jobReviewHeadings = []string{"VERDICT:", "SUMMARY:", "FINDINGS:", "TESTS RUN:"}

// ParseJobReview reads the structured report (VERDICT, SUMMARY, FINDINGS,
// TESTS RUN) from the line that starts with "VERDICT: OK" or
// "VERDICT: BLOCK"; text before it (a reviewer's progress notes) is ignored.
// It fails closed on absent, duplicate or malformed verdicts, missing, empty
// or reordered sections, and headings that are quoted, fenced, indented or
// embedded in prose, including a heading at the start of a line in the text
// before the verdict. It never interprets quoted prose as an approval.
func ParseJobReview(report, head, reviewerJobID, model string, now int64) JobReview {
	r := JobReview{Verdict: "block", Head: head, ReviewerJobID: reviewerJobID, Model: model, RecordedAt: now, Reason: "missing or malformed review verdict"}
	lines := strings.Split(strings.TrimSpace(report), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "VERDICT:") {
			if start >= 0 {
				return r // exactly one line in the whole message starts with VERDICT:
			}
			start = i
		}
	}
	if start < 0 || (lines[start] != "VERDICT: OK" && lines[start] != "VERDICT: BLOCK") {
		return r
	}
	fences := 0
	for _, line := range lines[:start] {
		// The notes before the report may not carry a heading at the start
		// of a line, plain or behind quote, list, emphasis or fence markup,
		// and may not leave a fence open around the verdict.
		bare := strings.TrimLeft(line, " \t>*-_#`")
		for _, heading := range jobReviewHeadings {
			if strings.HasPrefix(bare, heading) {
				return r
			}
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fences++
		}
	}
	if fences%2 != 0 {
		return r
	}
	sections := jobReviewHeadings[1:]
	next := 0
	nonempty := false
	var findings []string
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		// Structural headings may not appear quoted, fenced, indented,
		// duplicated or embedded in prose: ambiguous reports fail closed.
		for _, heading := range jobReviewHeadings {
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
				rest := strings.TrimSpace(strings.TrimPrefix(line, heading))
				nonempty = rest != ""
				if heading == "FINDINGS:" && rest != "" {
					findings = append(findings, strings.TrimLeft(strings.TrimPrefix(line, heading), " \t"))
				}
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
		if !structural && next == 2 {
			findings = append(findings, line)
		}
	}
	if next != len(sections) || !nonempty || head == "" {
		return r
	}
	r.Findings = boundJobReviewFindings(strings.TrimSpace(strings.Join(findings, "\n")))
	if lines[start] == "VERDICT: BLOCK" {
		r.Reason = strings.TrimSpace(strings.Join(lines[start:], "\n"))
		return r
	}
	r.Verdict, r.Reason = "ok", ""
	return r
}

// boundJobReviewFindings keeps findings within MaxJobReviewFindingsBytes: a
// longer section is cut at the last line boundary that leaves room for
// JobReviewFindingsCutNote, which then ends it.
func boundJobReviewFindings(findings string) string {
	if len(findings) <= MaxJobReviewFindingsBytes {
		return findings
	}
	limit := MaxJobReviewFindingsBytes - len(JobReviewFindingsCutNote) - 1
	kept := findings[:limit+1]
	if i := strings.LastIndex(kept, "\n"); i >= 0 {
		kept = kept[:i]
	} else {
		kept = ""
	}
	if strings.TrimSpace(kept) == "" {
		return JobReviewFindingsCutNote
	}
	return kept + "\n" + JobReviewFindingsCutNote
}
