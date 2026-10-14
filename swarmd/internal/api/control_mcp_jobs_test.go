package api

import (
	"strings"
	"testing"
)

// Requirement: when a job settles for review with nothing committed (a
// finder's report, for example), get_job and give_job's wait say there is
// nothing to merge and the report is the result. Threat: the hint told the
// delegating AI to call swarm_deliver_job, which can only fail on a job with
// no commits. An unknown result keeps the merge hint so committed work is
// never hidden. controlMCPResultCommitted and controlMCPJobNext own the hint
// that jobView returns; a pure-function test is the narrowest layer.
func TestControlMCPJobNextForReportOnlyJobs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    map[string]any
		committed bool
	}{
		{"no commits by delivery", map[string]any{"delivery_state": "empty", "commits": []string{"abc fix"}}, false},
		{"no net change by delivery", map[string]any{"delivery_state": "empty"}, false},
		{"candidate work", map[string]any{"delivery_state": "candidate_work", "commits": []string{}}, true},
		{"already merged", map[string]any{"delivery_state": "integrated"}, true},
		{"empty branch log", map[string]any{"commits": []string{}}, false},
		{"branch log with commits", map[string]any{"commits": []string{"abc add script"}}, true},
		{"unknown", map[string]any{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			committed := controlMCPResultCommitted(tc.result)
			if committed != tc.committed {
				t.Fatalf("committed = %v, want %v", committed, tc.committed)
			}
			next := controlMCPJobNext("needs_review", committed)
			if merges := strings.Contains(next, "swarm_deliver_job"); merges != tc.committed {
				t.Fatalf("next %q suggests merging = %v, want %v", next, merges, tc.committed)
			}
			if !tc.committed && (!strings.Contains(next, "nothing to merge") || !strings.Contains(next, "swarm_get_job")) {
				t.Fatalf("report-only next %q does not say there is nothing to merge", next)
			}
		})
	}
	if next := controlMCPJobNext("completed", false); next != "the job is finished and merged" {
		t.Fatalf("other statuses changed: %q", next)
	}
}

func TestControlMCPFillWorktreeHead(t *testing.T) {
	head := strings.Repeat("c", 40)
	assessed := strings.Repeat("d", 40)
	cases := []struct {
		name    string
		out     map[string]any
		changes map[string]any
		want    any
	}{
		{"assessment head wins", map[string]any{"head": assessed}, map[string]any{"head": head}, assessed},
		{"worktree head when the assessment has none (sync)", map[string]any{}, map[string]any{"head": head, "base": strings.Repeat("e", 40)}, head},
		{"no worktree: no head", map[string]any{}, map[string]any{"head": head, "note": "works directly in its folder"}, nil},
		{"not a commit id", map[string]any{}, map[string]any{"head": "HEAD"}, nil},
	}
	for _, tc := range cases {
		controlMCPFillWorktreeHead(tc.out, tc.changes)
		if tc.out["head"] != tc.want {
			t.Errorf("%s: head = %v, want %v", tc.name, tc.out["head"], tc.want)
		}
	}
}
