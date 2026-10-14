package pebblestore

import "testing"

// Purpose: ParseJobReview must never approve empty or ambiguous structures.
// Pure parser tests prove this boundary without provider or persistence fixtures.
func TestJobReviewRejectsAmbiguousStructure(t *testing.T) {
	for _, body := range []string{
		"SUMMARY:\nFINDINGS:\nTESTS RUN:",
		"SUMMARY: good\nSUMMARY: duplicate\nFINDINGS: None\nTESTS RUN: test",
		"SUMMARY: good\nFINDINGS: None\nTESTS RUN: test\nFINDINGS: duplicate",
		"FINDINGS: None\nSUMMARY: good\nTESTS RUN: test",
		"SUMMARY: good\n> FINDINGS: None\nTESTS RUN: test",
		"SUMMARY: good\nFINDINGS:\nTESTS RUN: test",
		"SUMMARY: good\nFINDINGS: None\nTESTS RUN:   ",
		"SUMMARY: good\n```\nFINDINGS: None\nTESTS RUN: test\n```",
	} {
		r := ParseJobReview("VERDICT: OK\n"+body, "head", "job", "model", 1)
		if r.Verdict != "block" || r.Reason == "" {
			t.Fatalf("approved %q", body)
		}
	}
	r := ParseJobReview("VERDICT: OK\nSUMMARY:\ngood\nFINDINGS:\nNone\nTESTS RUN:\ngo test passed", "head", "job", "model", 1)
	if r.Verdict != "ok" {
		t.Fatal(r)
	}
}
