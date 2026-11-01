package pebblestore

import (
	"strings"
	"testing"
)

// Purpose: a woken Orchestrator learns from its task's wake row that Swarm
// rebased the work (old -> new head, onto which branch), that a rebase
// conflict is being resolved, or that one was not resolved (files): the
// milestone names it, a conflict before blocked/failed, and a rebase
// arriving from a linked machine is bounded and its unknown outcomes
// dropped. Threat: an unresolved conflict reported as a plain failure (or
// not at all), or a linked machine's oversized rebase view flooding a row.
// Boundary: projectTaskWatchMilestone and ClipProjectTaskRebase, the
// functions every local and linked wake row goes through.
func TestProjectTaskWatchMilestoneNamesRebaseAndConflict(t *testing.T) {
	from, to := strings.Repeat("a", 40), strings.Repeat("b", 40)
	rebased := &ProjectTaskRebase{Outcome: ProjectTaskRebaseRebased, Target: "dev", From: from, To: to}
	cases := []struct {
		row  map[string]any
		want string
	}{
		{map[string]any{"workflow_state": "running", "workflow_step_type": "run", "rebase": rebased}, "rebased onto dev (aaaaaaaaaaaa → bbbbbbbbbbbb)"},
		// Decoded from JSON (a linked machine's row) reads the same.
		{map[string]any{"workflow_state": "running", "workflow_step_type": "deliver", "rebase": map[string]any{"outcome": "rebased", "target": "dev", "from": from, "to": to}}, "rebased onto dev (aaaaaaaaaaaa → bbbbbbbbbbbb)"},
		{map[string]any{"workflow_state": "running", "workflow_step_type": "run", "rebase": &ProjectTaskRebase{Outcome: ProjectTaskRebaseResolving, Target: "dev", Files: []string{"a.go"}}}, "rebase conflict on dev: a.go (resolver working)"},
		{map[string]any{"workflow_state": ProjectTaskWorkflowConflict, "status": "failed", "rebase": &ProjectTaskRebase{Outcome: ProjectTaskRebaseConflict, Target: "dev", Files: []string{"a.go", "b.go"}}}, "conflict on dev: a.go, b.go (not resolved)"},
		{map[string]any{"workflow_state": ProjectTaskWorkflowConflict}, "conflict"},
		// A review running stays the review milestone.
		{map[string]any{"workflow_state": "running", "workflow_step_type": "agent", "workflow_round": 1, "rebase": rebased}, "review started (round 1)"},
	}
	for _, tc := range cases {
		if got := projectTaskWatchMilestone(tc.row); got != tc.want {
			t.Fatalf("milestone of %v = %q, want %q", tc.row, got, tc.want)
		}
	}

	files := make([]string, 30)
	for i := range files {
		files[i] = strings.Repeat("f", 300)
	}
	clipped := ClipProjectTaskRebase(&ProjectTaskRebase{Outcome: ProjectTaskRebaseConflict, Target: strings.Repeat("t", 500), Files: files, ResolverJobs: files, Rebases: -3})
	if clipped == nil || len(clipped.Files) != maxProjectTaskRebaseFiles || len(clipped.ResolverJobs) != maxProjectTaskRebaseJobs || len([]rune(clipped.Target)) > 120 ||
		len([]rune(clipped.Files[0])) > 120 || clipped.Rebases != 0 {
		t.Fatalf("clipped = %+v", clipped)
	}
	if ClipProjectTaskRebase(&ProjectTaskRebase{Outcome: "merged"}) != nil || ClipProjectTaskRebase(nil) != nil {
		t.Fatal("an unknown outcome was kept")
	}
}
