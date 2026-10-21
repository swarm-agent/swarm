package pebblestore

import (
	"fmt"
	"strings"
	"testing"
)

// boldReviewFindings is a FINDINGS body shaped like the forge reviewer's
// bold-heading report: three numbered findings whose titles are themselves
// bold, each with detail lines.
func boldReviewFindings() string {
	var b strings.Builder
	for i, severity := range []string{"Medium", "Medium", "High"} {
		fmt.Fprintf(&b, "%d. **%s: internal/upload/upload.go:%d the limit is not enforced on this path**\n", i+1, severity, 10*(i+1))
		for j := 0; j < 12; j++ {
			fmt.Fprintf(&b, "   Detail %d.%d: the change does not cover this case, and the report claims it does.\n", i+1, j)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// boldReviewMessage is the forge reviewer's final message: progress notes,
// then a report whose four headings are wrapped in Markdown bold at the
// start of their lines.
func boldReviewMessage(verdict, findings string) string {
	var b strings.Builder
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&b, "I'm checking whether step %d of the brief is covered by the diff.\n", i)
	}
	b.WriteString("\n**VERDICT: " + verdict + "**\n\n**SUMMARY:**\nThe change is reviewed against its brief; see the findings.\n\n**FINDINGS:**\n\n" + findings + "\n\n**TESTS RUN:** none (review is read-only)\n")
	return b.String()
}

// Purpose: ParseJobReview (the only reader of a reviewer's verdict; the
// review record stores its output) must read a report whose structural
// headings are wrapped in Markdown emphasis at the start of their lines, and
// keep the FINDINGS body verbatim without the heading's own markers.
// Threat (forge run): a 7,090-character BLOCK report with bold headings and
// three findings was recorded as "missing or malformed review verdict" with
// no findings, so the coder was sent nothing it could fix. A pure function
// test is the narrowest layer for the parser; the store/runner paths are in
// the run and api packages.
func TestParseJobReviewReadsEmphasisedHeadings(t *testing.T) {
	findings := boldReviewFindings()
	message := boldReviewMessage("BLOCK", findings)
	if len(message) < 4000 {
		t.Fatalf("fixture message is only %d bytes", len(message))
	}
	block := ParseJobReview(message, reviewTestHead, "worker/r/run", "m", 1)
	if block.Verdict != "block" || block.Reason == JobReviewMalformedReason {
		t.Fatalf("bold block review = verdict %q reason %.80q", block.Verdict, block.Reason)
	}
	if block.Findings != findings {
		t.Fatalf("findings not kept verbatim:\n got %q\nwant %q", block.Findings, findings)
	}
	if !strings.HasPrefix(block.Reason, "VERDICT: BLOCK") || strings.Contains(block.Reason, "I'm checking") {
		t.Fatalf("block reason = %.80q", block.Reason)
	}

	ok := ParseJobReview(boldReviewMessage("OK", "None"), reviewTestHead, "worker/r/run", "m", 1)
	if ok.Verdict != "ok" || ok.Reason != "" || ok.Findings != "None" {
		t.Fatalf("bold ok review = %+v", ok)
	}

	// Every accepted wrapper form records the same verdict as the plain one.
	for name, report := range map[string]string{
		"underscore verdict":          "__VERDICT: OK__\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"markers after colon":         "**VERDICT:** OK\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"underscore sections":         "VERDICT: OK\n__SUMMARY:__ fine\n__FINDINGS:__ None\n__TESTS RUN:__ none",
		"bold verdict plain sections": "**VERDICT: OK**\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
	} {
		if got := ParseJobReview(report, reviewTestHead, "worker/r/run", "m", 1); got.Verdict != "ok" || got.Findings != "None" {
			t.Errorf("%s: review = %+v", name, got)
		}
	}
	inline := ParseJobReview("**VERDICT: BLOCK**\n**SUMMARY:** bad\n**FINDINGS:** 1. high a.go:1 broken\n**TESTS RUN:** none", reviewTestHead, "worker/r/run", "m", 1)
	if inline.Verdict != "block" || inline.Findings != "1. high a.go:1 broken" {
		t.Fatalf("inline bold findings = %+v", inline)
	}
}

// Purpose: emphasis is accepted only as a wrapper around a heading at the
// start of its line; every fail-closed rule still holds for emphasised
// headings. Threat: a quoted, listed, fenced, duplicated or reordered bold
// heading, or one hidden in progress notes, being read as an approval.
func TestParseJobReviewEmphasisedHeadingsStillFailClosed(t *testing.T) {
	notes := "I'm checking whether the limit is enforced.\nI'm reading the tests.\n"
	report := "**VERDICT: OK**\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none"
	cases := map[string]string{
		"bold and plain verdict":         notes + "**VERDICT: OK**\nSUMMARY: fine\nFINDINGS: None\nVERDICT: OK\nTESTS RUN: none",
		"plain then bold verdict":        notes + "VERDICT: BLOCK\nSUMMARY: fine\nFINDINGS: None\n**VERDICT: OK**\nTESTS RUN: none",
		"bold verdict in the notes":      "**VERDICT: BLOCK**\n" + notes + "VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"bold summary in the notes":      notes + "**SUMMARY:** early\n" + report,
		"bold findings in the notes":     "**FINDINGS:**\n" + notes + report,
		"quoted bold verdict":            notes + "> **VERDICT: OK**\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"quoted bold section":            notes + "**VERDICT: OK**\n> **SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"bold verdict in a list item":    notes + "- **VERDICT: OK**\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"bold section in a list item":    notes + "**VERDICT: OK**\n**SUMMARY:** fine\n* **FINDINGS:** None\n**TESTS RUN:** none",
		"bold report in a fence":         notes + "```\n" + report + "\n```",
		"bold verdict indented":          notes + "  **VERDICT: OK**\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"bold sections out of order":     notes + "**VERDICT: OK**\n**FINDINGS:** None\n**SUMMARY:** fine\n**TESTS RUN:** none",
		"bold verdict in prose":          notes + "The reviewer said **VERDICT: OK**\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"unclosed bold verdict":          notes + "**VERDICT: OK\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"mismatched markers":             notes + "**VERDICT: OK__\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"text after the verdict markers": notes + "**VERDICT: OK** looks fine\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"bold malformed verdict value":   notes + "**VERDICT: APPROVE**\n**SUMMARY:** fine\n**FINDINGS:** None\n**TESTS RUN:** none",
		"empty bold section":             notes + "**VERDICT: OK**\n**SUMMARY:**\n**FINDINGS:** None\n**TESTS RUN:** none",
	}
	for name, report := range cases {
		got := ParseJobReview(report, reviewTestHead, "worker/r/run", "m", 1)
		if got.Verdict != "block" || got.Reason != JobReviewMalformedReason || got.Findings != "" {
			t.Errorf("%s: review = verdict %q reason %q findings %q", name, got.Verdict, got.Reason, got.Findings)
		}
	}
}
