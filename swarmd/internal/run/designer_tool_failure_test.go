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

// Requirement: only a managed Designer with a trusted destination may consume
// one author-correctable publication failure as a refinement round. The exact
// failure code must drive that decision; infrastructure/authority errors and all
// non-Designer agents remain on the existing fail-fast path.
func TestManagedDesignerRefinementCandidateIsBoundedAndDesignerOnly(t *testing.T) {
	run := &tool.ArtifactRunContext{CollectionID: "collection", VariantID: "variant"}
	calls := []tool.Call{{Name: "manage_artifact", Arguments: `{"action":"create"}`}}
	correctable := []tool.Result{{Error: "manage_artifact HTML animation failed (code=animation_viewport_overflow): animation content escapes the canonical viewport"}}

	index, code, ok := managedDesignerRefinementCandidate("designer", run, 0, calls, correctable)
	if !ok || index != 0 || code != "animation_viewport_overflow" {
		t.Fatalf("eligible refinement = index %d code %q ok=%t", index, code, ok)
	}
	if _, _, ok := managedDesignerRefinementCandidate("designer", run, 1, calls, correctable); ok {
		t.Fatal("second managed Designer refinement was allowed")
	}
	if _, _, ok := managedDesignerRefinementCandidate("coder", run, 0, calls, correctable); ok {
		t.Fatal("non-Designer agent received managed refinement behavior")
	}
	if _, _, ok := managedDesignerRefinementCandidate("designer", run, 0, calls, []tool.Result{{Error: "artifact storage quota exceeded"}}); ok {
		t.Fatal("infrastructure failure was treated as author-correctable")
	}
	if _, _, ok := managedDesignerRefinementCandidate("designer", run, 0, calls, []tool.Result{{Error: "manage_artifact HTML animation failed (code=animation_renderer_unavailable): renderer unavailable"}}); ok {
		t.Fatal("renderer unavailability was treated as author-correctable")
	}
}

func TestDesignerToolFailureStateSkipsOnlyChosenRefinementFailure(t *testing.T) {
	state := designerToolFailureState{}
	calls := []tool.Call{
		{Name: "manage_artifact", Arguments: `{"action":"create"}`},
		{Name: "read", Arguments: `{}`},
	}
	results := []tool.Result{
		{Error: "manage_artifact HTML animation failed (code=animation_viewport_overflow): overflow"},
		{Error: "independent read failure"},
	}
	message, stop := state.ObserveSkipping(calls, results, 0)
	if stop || message != "" || state.attempts != 1 {
		t.Fatalf("skipped refinement failure state: stop=%v message=%q attempts=%d", stop, message, state.attempts)
	}
	message, stop = state.Observe(calls[:1], results[:1])
	if !stop || !strings.Contains(message, "failed to publish") || state.attempts != 2 {
		t.Fatalf("second publication failure did not stop: stop=%v message=%q attempts=%d", stop, message, state.attempts)
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
