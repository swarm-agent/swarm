package jobautomation

import (
	"sync"
	"testing"
)

// notifications records every change the store announces (SetNotify).
type notifications struct {
	mu   sync.Mutex
	jobs []string
}

func (n *notifications) record(account, projectID, jobID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.jobs = append(n.jobs, account+"|"+projectID+"|"+jobID)
}

func (n *notifications) count(entry string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, j := range n.jobs {
		if j == entry {
			c++
		}
	}
	return c
}

func (h *harness) evaluation(jobID string) (JobEvaluation, bool) {
	h.t.Helper()
	e, found, err := h.store.JobEvaluation(testAccount, jobID)
	if err != nil {
		h.t.Fatal(err)
	}
	return e, found
}

// Requirement: every task job end the runner reads records its evaluation,
// so a task's progress tells "no workflow applies" (none) from "not
// evaluated yet" (pending): with no workflow at all (the early return), with
// workflows that do not match, and with the deliveries it matched. A worker
// job end records none (only tasks have progress), and a task a workflow
// started (source automation) is never evaluated.
// Threat: "none" never recorded when there are no workflows, so a finished
// task looks pending forever, or an evaluation hidden behind the zero-
// candidate shortcut.
// Owners: Runner.consume, Store.RecordEvaluation/MarkJobEnded.
func TestTaskJobEndRecordsItsEvaluation(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	notes := &notifications{}
	h.store.SetNotify(notes.record)
	h.process()

	// No workflow at all.
	h.results.results["task-none"] = taskResult(folder, "task-none", "coder")
	h.taskEnd("task-none", "", "succeeded", "task")
	h.process()
	e, found := h.evaluation("task/proj-1/task-none")
	if !found || !e.Evaluated() || len(e.Deliveries) != 0 || e.Followup != "" || e.ProjectID != "proj-1" || e.Seq == 0 || e.EndedAt == 0 {
		t.Fatalf("zero candidates: evaluation %+v found=%v, want evaluated with no deliveries", e, found)
	}
	if notes.count(testAccount+"|proj-1|task/proj-1/task-none") != 1 {
		t.Fatalf("the evaluation was not announced: %v", notes.jobs)
	}

	// A workflow that does not match (another role).
	h.automation(Automation{Name: "finder notes", Scope: Scope{Folder: folder, Role: "finder"}, Enabled: true, Steps: []Step{{ID: "notes", Type: StepRun, Command: "push.sh"}}})
	h.results.results["task-other"] = taskResult(folder, "task-other", "coder")
	h.taskEnd("task-other", "", "succeeded", "task")
	h.process()
	if e, found := h.evaluation("task/proj-1/task-other"); !found || !e.Evaluated() || len(e.Deliveries) != 0 {
		t.Fatalf("no match: evaluation %+v found=%v, want evaluated with no deliveries", e, found)
	}

	// A workflow that matches records its delivery, and each of its writes
	// is announced for the task's project.
	h.automation(Automation{Name: "coder push", Scope: Scope{Folder: folder, Role: "coder"}, Enabled: true, Steps: []Step{{ID: "push", Type: StepRun, Command: "push.sh"}}})
	h.results.results["task-match"] = taskResult(folder, "task-match", "coder")
	h.taskEnd("task-match", "", "succeeded", "task")
	h.process()
	list := h.deliveries("task/proj-1/task-match")
	if len(list) != 1 || list[0].State != StateSucceeded {
		t.Fatalf("deliveries = %+v, want one succeeded", list)
	}
	e, found = h.evaluation("task/proj-1/task-match")
	if !found || !e.Evaluated() || len(e.Deliveries) != 1 || e.Deliveries[0] != list[0].ID {
		t.Fatalf("match: evaluation %+v, want delivery %s", e, list[0].ID)
	}
	// Created, step started, step finished, finished, and the evaluation.
	if n := notes.count(testAccount + "|proj-1|task/proj-1/task-match"); n < 4 {
		t.Fatalf("delivery transitions announced %d times, want every transition: %v", n, notes.jobs)
	}

	// A follow-up's end replaces the record; a later mark of the same end
	// (a replayed signal) never turns it back to pending.
	h.taskEnd("task-none", "run-2", "succeeded", "task")
	h.process()
	e, _ = h.evaluation("task/proj-1/task-none")
	if e.Followup != "run-2" || !e.Evaluated() {
		t.Fatalf("follow-up evaluation %+v", e)
	}
	if err := h.store.MarkJobEnded(testAccount, "proj-1", "task/proj-1/task-none", "run-2", e.Seq, e.EndedAt); err != nil {
		t.Fatal(err)
	}
	if again, _ := h.evaluation("task/proj-1/task-none"); !again.Evaluated() {
		t.Fatalf("a replayed mark made the evaluated end pending: %+v", again)
	}
	// A newer end marked before the runner reads it is pending, and an
	// older end's evaluation does not overwrite it.
	if err := h.store.MarkJobEnded(testAccount, "proj-1", "task/proj-1/task-none", "run-3", e.Seq+100, e.EndedAt+1); err != nil {
		t.Fatal(err)
	}
	if err := h.store.RecordEvaluation(testAccount, "proj-1", "task/proj-1/task-none", "run-2", e.Seq, e.EndedAt, nil); err != nil {
		t.Fatal(err)
	}
	if pending, _ := h.evaluation("task/proj-1/task-none"); pending.Evaluated() || pending.Followup != "run-3" {
		t.Fatalf("newer end not pending: %+v", pending)
	}

	// A worker job end and a workflow-started task keep no evaluation.
	h.results.results["run-w"] = workerResult(folder, "run-w")
	h.jobEnd("run-w", "", "succeeded", "direct")
	h.taskEnd("task-auto", "", "succeeded", RequestSourceAutomation)
	h.process()
	if _, found := h.evaluation("worker/coder-1/run-w"); found {
		t.Fatal("a worker job end recorded a task evaluation")
	}
	if _, found := h.evaluation("task/proj-1/task-auto"); found {
		t.Fatal("a workflow-started task was evaluated")
	}
}

// Requirement: with no workflow runner wired to results (r.results nil)
// and no candidates, a task job end still records "none".
// Owners: Runner.consume early return.
func TestTaskJobEndWithoutCandidatesRecordsNoneEvenWithoutResults(t *testing.T) {
	h := newHarness(t)
	h.runner.results = nil
	h.process()
	h.taskEnd("task-x", "", "failed", "task")
	h.process()
	e, found := h.evaluation("task/proj-1/task-x")
	if !found || !e.Evaluated() || len(e.Deliveries) != 0 {
		t.Fatalf("evaluation %+v found=%v, want none recorded", e, found)
	}
}
