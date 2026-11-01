package jobautomation

import (
	"strings"
	"testing"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a step's failure reason is recorded whole up to MaxStepReasonRunes
// and, past that, with its head and tail and a marker, never cut at its head
// alone; its failure history keeps the wake's reason bound the same way.
// Threat (review finding): reasons were cut to their first 300 bytes with no
// marker, so a step's error lost its end (often what failed) before task
// progress or a wake could show it. Boundary: stepReason, which every step
// failure reason in runner.go, rebase.go and deliver.go goes through, and
// StepRecord.recordFailure; the narrowest layer that records the reason.
func TestStepReasonKeepsWholeReasonsAndHeadAndTail(t *testing.T) {
	mid := "rebase not accepted: " + strings.Repeat("conflict in file.go ", 60) + "END-OF-REASON"
	if got := stepReason(mid); got != mid {
		t.Fatalf("a %d-rune reason was cut: %d runes", utf8.RuneCountInString(mid), utf8.RuneCountInString(got))
	}
	long := "HEAD " + strings.Repeat("é detail ", 2000) + " TAIL"
	got := stepReason(long)
	if utf8.RuneCountInString(got) > MaxStepReasonRunes || !strings.HasPrefix(got, "HEAD ") || !strings.HasSuffix(got, " TAIL") || !strings.Contains(got, "chars cut") || !utf8.ValidString(got) {
		t.Fatalf("long reason not head+tail within %d runes: %d runes", MaxStepReasonRunes, utf8.RuneCountInString(got))
	}
	var record StepRecord
	record.recordFailure(0, 1, long)
	history := record.Failures[0].Reason
	if utf8.RuneCountInString(history) > pebblestore.ProjectTaskWakeReasonRunes || !strings.HasPrefix(history, "HEAD ") || !strings.HasSuffix(history, " TAIL") {
		t.Fatalf("failure history not bounded head+tail: %d runes", utf8.RuneCountInString(history))
	}
}
