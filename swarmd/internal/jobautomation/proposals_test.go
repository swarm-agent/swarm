package jobautomation

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// Purpose: Runner.ProcessOnce must never start a proposal, including after
// restart; accepting the exact revision unlocks delivery, while stale and
// cross-account acceptance leave state untouched. The real consumer/store is
// the narrowest layer proving job-end delivery eligibility (not provider work).
func TestWorkflowProposalRunner(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	a := Automation{Name: "review", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{{Type: StepAgent, Worker: "reviewer", Brief: "review"}}}
	proposal, err := h.store.Propose(testAccount, "session", a)
	if err != nil {
		t.Fatal(err)
	}
	h.restart()
	heads := 0
	emit := func(id string) {
		// Each job end has commits of its own to review: a reviewer step
		// with no head is refused ("no head to review"), and a second end at
		// a head a delivery covers creates no delivery.
		heads++
		head := strings.Repeat(strconv.Itoa(heads), 40)
		h.results.results[id] = &Result{V: 1, JobID: "worker/coder-1/" + id, Kind: KindWorker, RunID: id, Folder: folder, Status: StatusSucceeded,
			Git: &ResultGit{Branch: "agent/" + id, BaseCommit: strings.Repeat("a", 40), Head: head, Commits: []ResultCommit{{SHA: head, Subject: id}}}}
		h.jobEnd(id, "", StatusSucceeded, "")
		h.process()
	}
	emit("before")
	if len(h.agents.started) != 0 || len(h.deliveries("worker/coder-1/before")) != 0 {
		t.Fatal("proposal ran")
	}
	authorize := func(Automation) error { return nil }
	if _, err = h.store.AcceptProposal("other", proposal.ID, proposal.Revision, "user", authorize); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = h.store.AcceptProposal(testAccount, proposal.ID, proposal.Revision+1, "user", authorize); !errors.Is(err, ErrProposalConflict) {
		t.Fatal(err)
	}
	active, err := h.store.AcceptProposal(testAccount, proposal.ID, proposal.Revision, "user", authorize)
	if err != nil {
		t.Fatal(err)
	}
	emit("after")
	if len(h.agents.started) != 1 || len(h.deliveries("worker/coder-1/after")) != 1 {
		t.Fatal("accepted workflow did not run")
	}
	a.Steps[0].Brief = "changed"
	pending, err := h.store.Propose(testAccount, "session", a)
	if err != nil {
		t.Fatal(err)
	}
	emit("unchanged")
	if len(h.agents.started) != 2 || h.agents.started[1].Brief != "review" {
		t.Fatal("pending change replaced active")
	}
	if _, err = h.store.SetEnabled(testAccount, active.ID, false, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err = h.store.AcceptProposal(testAccount, pending.ID, pending.Revision, "user", authorize); !errors.Is(err, ErrProposalConflict) {
		t.Fatal("stale base accepted", err)
	}
	current, _ := h.store.Get(testAccount, active.ID)
	if current.Enabled || current.Steps[0].Brief != "review" {
		t.Fatal("stale acceptance mutated active")
	}
}
