package pebblestore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const reviewTestHead = "0123456789abcdef0123456789abcdef01234567"

// forgeReviewFindings is a FINDINGS section shaped like a real reviewer's:
// five numbered findings, the fifth a security finding, together far longer
// than the 4,000-character task summary.
func forgeReviewFindings() string {
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		severity := "medium"
		if i == 5 {
			severity = "high security"
		}
		fmt.Fprintf(&b, "%d. %s internal/upload/upload.go:%d finding %d starts here.\n", i, severity, 10*i, i)
		for j := 0; j < 16; j++ {
			fmt.Fprintf(&b, "   detail %d.%d: the change does not cover this case and the report claims it does.\n", i, j)
		}
		fmt.Fprintf(&b, "   end of finding %d\n", i)
	}
	return strings.TrimSpace(b.String())
}

// forgeReviewMessage is a reviewer's final assistant message as the forge
// run produced it: sixteen lines of progress notes, then the structured report.
func forgeReviewMessage(verdict, findings string) string {
	var b strings.Builder
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&b, "I'm checking whether step %d of the brief is covered by the diff.\n", i)
	}
	b.WriteString("VERDICT: " + verdict + "\n")
	b.WriteString("SUMMARY: the change is reviewed against its brief.\n")
	b.WriteString("FINDINGS: " + findings + "\n")
	b.WriteString("TESTS RUN: none (review is read-only)\n")
	return b.String()
}

// Purpose: ParseJobReview is the only reader of a reviewer's verdict
// (recordReviewResult stores its output). Requirement: the structured report
// is read from the line that starts with VERDICT: OK|BLOCK, wherever it
// starts, and its verbatim FINDINGS section is kept on the record.
// Threat (forge run): a reviewer that writes progress notes before its report
// was always recorded as BLOCK, so a loop waiting for an ok could never pass,
// and its findings were lost. A pure function test is the narrowest layer;
// the store round trip is covered below.
func TestParseJobReviewReadsTheReportAfterProgressNotes(t *testing.T) {
	findings := forgeReviewFindings()
	ok := ParseJobReview(forgeReviewMessage("OK", "None"), reviewTestHead, "worker/r/run", "m", 1)
	if ok.Verdict != "ok" || ok.Reason != "" {
		t.Fatalf("ok review = %+v", ok)
	}
	if ok.Findings != "None" {
		t.Fatalf("ok findings = %q", ok.Findings)
	}

	message := forgeReviewMessage("BLOCK", findings)
	if len(message) < 7000 {
		t.Fatalf("fixture message is %d bytes; it must reproduce the >7,000 character report", len(message))
	}
	block := ParseJobReview(message, reviewTestHead, "worker/r/run", "m", 1)
	if block.Verdict != "block" || block.Reason == "missing or malformed review verdict" {
		t.Fatalf("block review = verdict %q reason %q", block.Verdict, block.Reason)
	}
	if block.Findings != findings {
		t.Fatalf("findings were not kept verbatim:\n got %d bytes\nwant %d bytes", len(block.Findings), len(findings))
	}
	if !strings.Contains(block.Findings, "5. high security") || !strings.HasSuffix(block.Findings, "end of finding 5") {
		t.Fatal("the fifth (security) finding is missing")
	}
	if strings.Contains(block.Reason, "I'm checking whether") || !strings.HasPrefix(block.Reason, "VERDICT: BLOCK") {
		t.Fatalf("block reason must be the structured report only, got prefix %q", block.Reason[:40])
	}

	// A report with no progress notes still parses as before.
	plain := ParseJobReview("VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none", reviewTestHead, "worker/r/run", "m", 1)
	if plain.Verdict != "ok" {
		t.Fatalf("plain ok review = %+v", plain)
	}
}

// Purpose: every fail-closed rule still holds from the verdict line on, and
// the progress notes before it may not carry a heading. Threat: quoted,
// duplicated, fenced or reordered reports, or a heading hidden in progress
// notes, being read as an approval.
func TestParseJobReviewStillFailsClosed(t *testing.T) {
	notes := "I'm checking whether the limit is enforced.\nI'm reading the tests.\n"
	cases := map[string]string{
		"two verdict lines":             notes + "VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nVERDICT: OK\nTESTS RUN: none",
		"verdict in the notes":          "VERDICT: BLOCK\n" + notes + "VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"summary heading in the notes":  "SUMMARY: early\n" + notes + "VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"findings heading in the notes": notes + "FINDINGS: None\nVERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"indented heading in the notes": notes + "  TESTS RUN: none\nVERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"quoted heading in the notes":   notes + "> SUMMARY: quoted\nVERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"sections out of order":         notes + "VERDICT: OK\nFINDINGS: None\nSUMMARY: fine\nTESTS RUN: none",
		"missing section":               notes + "VERDICT: OK\nSUMMARY: fine\nTESTS RUN: none",
		"empty section":                 notes + "VERDICT: OK\nSUMMARY:\nFINDINGS: None\nTESTS RUN: none",
		"quoted verdict":                notes + "> VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"verdict in prose":              notes + "The reviewer said VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"fenced report":                 notes + "```\nVERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none\n```",
		"fence opened in the notes":     notes + "```text\nVERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"heading embedded after":        notes + "VERDICT: OK\nSUMMARY: fine, see FINDINGS: below\nFINDINGS: None\nTESTS RUN: none",
		"malformed verdict value":       notes + "VERDICT: APPROVE\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none",
		"no verdict":                    notes,
	}
	for name, report := range cases {
		got := ParseJobReview(report, reviewTestHead, "worker/r/run", "m", 1)
		if got.Verdict != "block" || got.Reason != "missing or malformed review verdict" || got.Findings != "" {
			t.Errorf("%s: review = verdict %q reason %q findings %q", name, got.Verdict, got.Reason, got.Findings)
		}
	}
	// No head: never ok.
	if got := ParseJobReview(notes+"VERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nTESTS RUN: none", "", "worker/r/run", "m", 1); got.Verdict != "block" {
		t.Errorf("review without a head = %+v", got)
	}
}

// Purpose: findings are bounded at MaxJobReviewFindingsBytes (the automation
// findings bound), cut at a line boundary, and say so. Threat: an unbounded
// record, or a cut in the middle of a finding that silently drops the rest.
func TestParseJobReviewBoundsFindingsAtALineBoundary(t *testing.T) {
	var b strings.Builder
	for i := 1; b.Len() < 3*MaxJobReviewFindingsBytes; i++ {
		fmt.Fprintf(&b, "%d. medium a.go:%d a long finding line that repeats to exceed the bound\n", i, i)
	}
	findings := strings.TrimSpace(b.String())
	got := ParseJobReview(forgeReviewMessage("BLOCK", findings), reviewTestHead, "worker/r/run", "m", 1)
	if got.Verdict != "block" {
		t.Fatalf("verdict = %q (%s)", got.Verdict, got.Reason)
	}
	if len(got.Findings) > MaxJobReviewFindingsBytes {
		t.Fatalf("findings are %d bytes, over %d", len(got.Findings), MaxJobReviewFindingsBytes)
	}
	if !strings.HasSuffix(got.Findings, JobReviewFindingsCutNote) {
		tail := got.Findings
		if len(tail) > 80 {
			tail = tail[len(tail)-80:]
		}
		t.Fatalf("cut findings do not say so: %q", tail)
	}
	kept := strings.TrimSuffix(got.Findings, "\n"+JobReviewFindingsCutNote)
	if !strings.HasPrefix(findings, kept+"\n") {
		t.Fatal("findings were not cut at a line boundary")
	}
}

// Purpose: the full findings survive the real store (RecordJobReview /
// GetJobReviewByReviewer), and a record written before the field existed
// still decodes. Threat: findings dropped on persistence, or old records
// becoming unreadable.
func TestJobReviewFindingsPersistAndOldRecordsDecode(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "review.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ws := NewWorkerStore(db)
	coder, err := ws.CreateWorker("acct", "user", CreateWorkerRequest{Role: WorkerRoleCoder, Name: "Coder", IdempotencyKey: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := ws.CreateWorker("acct", "user", CreateWorkerRequest{Role: WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.RecordWorkerRun("acct", WorkerRunRecord{ID: "run-coder", WorkerID: coder.ID, UserID: "user", Role: WorkerRoleCoder, Status: "succeeded", RequestSource: "direct", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.RecordWorkerRun("acct", WorkerRunRecord{ID: "run-review", WorkerID: reviewer.ID, UserID: "user", Role: WorkerRoleReviewer, Status: "succeeded", RequestSource: "direct", CreatedAt: 1,
		Input: map[string]any{"review_job": "worker/" + coder.ID + "/run-coder"}}); err != nil {
		t.Fatal(err)
	}
	reviewerJob := "worker/" + reviewer.ID + "/run-review"
	findings := forgeReviewFindings()
	review := ParseJobReview(forgeReviewMessage("BLOCK", findings), reviewTestHead, reviewerJob, "m", 5)
	if err := ws.RecordJobReview("acct", coder.ID, "run-coder", review); err != nil {
		t.Fatal(err)
	}
	got, err := ws.GetJobReviewByReviewer("acct", coder.ID, "run-coder", reviewerJob)
	if err != nil || got == nil {
		t.Fatalf("recorded review = %v %v", got, err)
	}
	if got.Verdict != "block" || got.Findings != findings {
		t.Fatalf("stored review = verdict %q, %d findings bytes (want %d)", got.Verdict, len(got.Findings), len(findings))
	}

	var old JobReview
	if err := json.Unmarshal([]byte(`{"verdict":"block","head":"h","reviewer_job_id":"worker/r/x","model":"m","at":1,"reason":"VERDICT: BLOCK"}`), &old); err != nil || old.Findings != "" || old.Verdict != "block" {
		t.Fatalf("old record = %+v %v", old, err)
	}
	encoded, _ := json.Marshal(JobReview{Verdict: "ok", Head: "h", ReviewerJobID: "worker/r/x", RecordedAt: 1})
	if strings.Contains(string(encoded), "findings") {
		t.Fatalf("an empty findings field must be omitted: %s", encoded)
	}
}
