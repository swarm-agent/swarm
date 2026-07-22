package app

import "testing"

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
