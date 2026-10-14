package api

import "testing"

// A job with a head and commits can be published even while Swarm's delivery
// assessment reads "unavailable" (it may never settle for a folder Swarm only
// reaches through its sandbox); a job without commits cannot.
func TestControlMCPJobHasCommits(t *testing.T) {
	for _, tc := range []struct {
		result map[string]any
		want   bool
	}{
		{map[string]any{"head": "c26b8ca2434e0a7d7f6ca81f0a95fe0b81830d8f", "commits": []any{"c26b8ca models: refresh google"}, "delivery_state": "unavailable"}, true},
		{map[string]any{"head": "c26b8ca2434e0a7d7f6ca81f0a95fe0b81830d8f", "commits": []any{}}, false},
		{map[string]any{"commits": []any{"c26b8ca x"}}, false},
	} {
		if got := controlMCPJobHasCommits(tc.result); got != tc.want {
			t.Fatalf("%v: got %v", tc.result, got)
		}
	}
}
