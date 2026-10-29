package run

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the Orchestrator's per-turn "Recent Project Tasks & Deliverables"
// block shows each task's state (pebblestore.ProjectTaskState over its
// progress outcome), never the raw run status, says it is a snapshot to be
// re-read live, and never prints a code deliverable's creation placeholder
// (pending) for finished work, while asset deliverables keep their lines and
// stored status for "picture 2" references. Threat: every turn told the
// Orchestrator that pushed tasks were [needs_review] with a pending
// deliverable, which it reported to the person as current state. Owner:
// recentProjectTasksPromptBlock (pure; projectContextPromptBlock passes it
// the store's ListRecentProjectTasks and ProjectTaskProgress).
func TestRecentProjectTasksPromptBlockShowsTheTaskState(t *testing.T) {
	tasks := []pebblestore.ProjectTaskRecord{
		{ID: "task-pushed", Agent: "coder", Status: "needs_review", Title: "Fix the parser",
			Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "deliv_code", Title: "Code PR & Verified Tests", Kind: "pr", Status: "pending"}}},
		{ID: "task-branch", Agent: "coder", Status: "needs_review", Title: "Rename flags", WorktreeBranch: "agent/rename-flags",
			Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "deliv_code", Title: "Rename flags", Kind: "code_pr", Status: "pending"}}},
		{ID: "task-image", Agent: "designer", Status: "needs_review", Title: "Logo",
			Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "img_1", Title: "Logo variant", Kind: "image", Status: "ready"}}},
	}
	outcomes := map[string]string{"task-pushed": "pushed abc123", "task-branch": "ready to integrate", "task-image": "delivered files ready"}
	block := recentProjectTasksPromptBlock(tasks, func(task pebblestore.ProjectTaskRecord) (string, bool, bool) {
		return outcomes[task.ID], false, false
	})
	for _, want := range []string{
		"Snapshot when this turn started; read live with manage_projects list_tasks before reporting status.",
		"- Task `task-pushed` (coder) [landed: pushed abc123]: Fix the parser",
		"Code PR & Verified Tests (status: delivered, kind: pr, id: deliv_code)",
		"[not_integrated: commits on branch agent/rename-flags not handed over]",
		"Rename flags (status: on branch, kind: code_pr",
		"Logo variant (status: ready, kind: image, id: img_1)",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("prompt block misses %q:\n%s", want, block)
		}
	}
	for _, leaked := range []string{"[needs_review]", "status: pending"} {
		if strings.Contains(block, leaked) {
			t.Fatalf("prompt block carries %q:\n%s", leaked, block)
		}
	}
	// Without progress (no hook) the block still never claims a status the
	// record does not support: a run that ended reads as needing review of
	// its outcome, not as pending work.
	bare := recentProjectTasksPromptBlock(tasks[:1], nil)
	if !strings.Contains(bare, "[needs_you: run ended") || strings.Contains(bare, "status: pending") {
		t.Fatalf("prompt block without progress:\n%s", bare)
	}
}
