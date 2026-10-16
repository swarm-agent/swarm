package api

import (
	"testing"
)

// Requirement: the auto publisher opens a pull request only for a settled job
// with commits; a job that committed nothing is skipped (no retries, no
// "not settled" warning), including one whose delivery lane was unreadable.
// autoPublishNext owns this gate; this pure decision test is the narrowest
// layer proving terminal failures cannot consume retry budgets.
func TestAutoPublishNext(t *testing.T) {
	const base = "5de45360ed909a695e28468f38a786d2a3f3edbc"
	commit := map[string]any{"sha": "1111111111111111111111111111111111111111"}
	for _, c := range []struct {
		name   string
		status string
		result map[string]any
		want   int
	}{
		{"empty delivery", "needs_review", map[string]any{"delivery_state": "empty", "head": base, "base_commit": base}, autoPublishSkip},
		{"no commits, lane unavailable", "needs_review", map[string]any{"delivery_state": "unavailable", "head": base, "base_commit": base, "commits": []any{}}, autoPublishSkip},
		{"blocked", "blocked", nil, autoPublishSkip},
		{"failed", "failed", nil, autoPublishSkip},
		{"cancelled", "cancelled", nil, autoPublishSkip},
		{"completed without commits", "completed", nil, autoPublishSkip},
		{"active", "in_progress", nil, autoPublishWait},
		{"still running", "running", map[string]any{"head": base, "base_commit": base}, autoPublishWait},
		{"commits not assessed yet", "needs_review", map[string]any{"delivery_state": "unavailable", "head": "2222222222222222222222222222222222222222", "base_commit": base}, autoPublishWait},
		{"candidate after restart", "needs_review", map[string]any{"delivery_state": "candidate_work", "action_needed": "Bounded changes since captured base", "head": "2222222222222222222222222222222222222222", "base_commit": base, "commits": []any{commit, commit}}, autoPublishDeliver},
		{"ready", "needs_review", map[string]any{"delivery_state": "ready", "head": "2222222222222222222222222222222222222222", "base_commit": base, "commits": []any{commit}}, autoPublishDeliver},
	} {
		if got := autoPublishNext(c.status, c.result); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
