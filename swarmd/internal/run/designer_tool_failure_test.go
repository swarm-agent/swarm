package run

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

func TestDesignerToolFailureStateStopsManagedPublicationImmediatelyWithExactReason(t *testing.T) {
	state := designerToolFailureState{}
	message, stop := state.Observe(
		[]tool.Call{{Name: "manage_artifact", Arguments: `{"action":"create"}`}},
		[]tool.Result{{Error: "artifact storage quota exceeded"}},
	)
	if !stop {
		t.Fatal("managed publication failure did not stop Designer")
	}
	if state.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", state.attempts)
	}
	for _, want := range []string{"failed to publish", "artifact storage quota exceeded"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want %q", message, want)
		}
	}
}

func TestDesignerToolFailureStateAllowsCorrectionOfRejectedManagedPublicationPreflight(t *testing.T) {
	state := designerToolFailureState{}
	message, stop := state.Observe(
		[]tool.Call{{Name: "manage_artifact", Arguments: `{"action":"create"}`}},
		[]tool.Result{{Error: "create requires filename"}},
	)
	if stop || message != "" {
		t.Fatalf("correctable preflight rejection stopped Designer: stop=%v message=%q", stop, message)
	}
	if state.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", state.attempts)
	}

	message, stop = state.Observe(
		[]tool.Call{{Name: "manage_artifact", Arguments: `{"action":"create","filename":"concept.html"}`}},
		[]tool.Result{{Output: "ready"}},
	)
	if stop || message != "" || state.attempts != 1 {
		t.Fatalf("corrected publication changed failure state: stop=%v message=%q attempts=%d", stop, message, state.attempts)
	}
}

func TestDesignerToolFailureStateStopsAfterThreeFailuresWithLatestExactReason(t *testing.T) {
	state := designerToolFailureState{}
	for attempt, reason := range []string{"first exact error", "second exact error"} {
		message, stop := state.Observe(
			[]tool.Call{{Name: "read", Arguments: `{}`}},
			[]tool.Result{{Error: reason}},
		)
		if stop || message != "" {
			t.Fatalf("attempt %d stopped early: stop=%v message=%q", attempt+1, stop, message)
		}
	}

	message, stop := state.Observe(
		[]tool.Call{{Name: "search", Arguments: `{}`}},
		[]tool.Result{{Error: "third exact error"}},
	)
	if !stop {
		t.Fatal("third failed tool attempt did not stop Designer")
	}
	for _, want := range []string{"after 3 failed tool attempts", "third exact error"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want %q", message, want)
		}
	}
}

func TestDesignerToolFailureStateIgnoresSuccessfulResults(t *testing.T) {
	state := designerToolFailureState{}
	message, stop := state.Observe(
		[]tool.Call{{Name: "read", Arguments: `{}`}, {Name: "search", Arguments: `{}`}},
		[]tool.Result{{Output: "ok"}, {Output: "also ok"}},
	)
	if stop || message != "" || state.attempts != 0 {
		t.Fatalf("successful tools changed failure state: stop=%v message=%q attempts=%d", stop, message, state.attempts)
	}
}
