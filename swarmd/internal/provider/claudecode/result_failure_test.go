package claudecode

import (
	"strings"
	"testing"
)

// Purpose: turnState.resultFailure builds the user-visible error for a failed
// Claude Code turn. Claude Code reports some failures (for example a session
// limit) as is_error with subtype "success"; the message must not contradict
// itself with "(success)" while keeping the detail. Real error subtypes must
// still be named. A direct unit test of resultFailure is the narrowest layer
// that owns this text.
func TestResultFailureOmitsSuccessSubtype(t *testing.T) {
	const detail = "You've hit your session limit · resets 1pm"
	for _, subtype := range []string{"success", ""} {
		state := &turnState{result: &streamResult{Subtype: subtype, IsError: true, Result: detail}}
		got := state.resultFailure()
		want := "claude code turn failed: " + detail
		if got != want {
			t.Fatalf("subtype %q: resultFailure() = %q, want %q", subtype, got, want)
		}
		if strings.Contains(got, "(success)") || strings.Contains(got, "()") {
			t.Fatalf("subtype %q: message names an empty or success subtype: %q", subtype, got)
		}
	}
}

func TestResultFailureKeepsRealErrorSubtype(t *testing.T) {
	state := &turnState{result: &streamResult{Subtype: "error_max_turns", IsError: true, Result: "too many turns"}}
	if got, want := state.resultFailure(), "claude code turn failed (error_max_turns): too many turns"; got != want {
		t.Fatalf("resultFailure() = %q, want %q", got, want)
	}
	// A non-error subtype without is_error still fails, naming the subtype.
	state = &turnState{result: &streamResult{Subtype: "error_during_execution"}}
	if got, want := state.resultFailure(), "claude code turn failed (error_during_execution)"; got != want {
		t.Fatalf("resultFailure() = %q, want %q", got, want)
	}
	// A successful result is not a failure.
	state = &turnState{result: &streamResult{Subtype: "success", Result: "done"}}
	if got := state.resultFailure(); got != "" {
		t.Fatalf("resultFailure() = %q, want empty for success", got)
	}
}
