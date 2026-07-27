package app

import "testing"

func TestPlanSuggestionOnlyShowsExistingSessionPlan(t *testing.T) {
	for _, suggestion := range buildHomeCommandSuggestions(false) {
		if suggestion.Command != "/plan" {
			continue
		}
		if len(suggestion.QuickTips) != 0 {
			t.Fatalf("/plan quick tips = %v, want none", suggestion.QuickTips)
		}
		if suggestion.Hint != "Show or close the existing session plan" {
			t.Fatalf("/plan hint = %q", suggestion.Hint)
		}
		return
	}
	t.Fatal("/plan suggestion not found")
}

func TestRetiredCommandsAreNotSuggested(t *testing.T) {
	retired := map[string]struct{}{
		"/voice":   {},
		"/swarm":   {},
		"/output":  {},
		"/rebuild": {},
		"/reload":  {},
		"/vault":   {},
	}

	for _, suggestion := range buildHomeCommandSuggestions(false) {
		if _, ok := retired[suggestion.Command]; ok {
			t.Fatalf("retired command %q remains in TUI suggestions", suggestion.Command)
		}
	}
}
