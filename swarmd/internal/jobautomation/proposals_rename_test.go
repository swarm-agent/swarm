package jobautomation

import (
	"errors"
	"testing"
)

// Purpose: a proposal that renames a workflow (its base named by id) keeps
// the workflow's id on acceptance and leaves the old name free; a rename to
// a name another workflow holds is refused when proposed, and when that
// name is taken after the proposal, refused at acceptance with nothing
// changed. Threat: a rename accepted as a second workflow, or two workflows
// with one name (names resolve workflows everywhere). Boundary:
// Store.Propose and Store.AcceptProposal over a real store.
func TestProposeRenameKeepsIDAndRefusesTakenName(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	steps := func() []Step { return []Step{{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "review"}} }
	base, _, err := h.store.Put(testAccount, Automation{Name: "old", Scope: Scope{Folder: folder}, Enabled: true, Steps: steps()})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := h.store.Put(testAccount, Automation{Name: "other", Scope: Scope{Folder: folder}, Enabled: true, Steps: steps()})
	if err != nil {
		t.Fatal(err)
	}
	authorize := func(Automation) error { return nil }
	if _, err := h.store.Propose(testAccount, "session", Automation{Name: "other", BaseID: base.ID, Scope: Scope{Folder: folder}, Enabled: true, Steps: steps()}); !IsValidationError(err) {
		t.Fatalf("a rename onto a taken name: %v", err)
	}
	if _, err := h.store.Propose(testAccount, "session", Automation{Name: "new", BaseID: "jauto_missing", Scope: Scope{Folder: folder}, Enabled: true, Steps: steps()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a rename of a missing workflow: %v", err)
	}
	proposal, err := h.store.Propose(testAccount, "session", Automation{Name: "new", BaseID: base.ID, Scope: Scope{Folder: folder}, Enabled: true, Steps: steps()})
	if err != nil || proposal.BaseID != base.ID || proposal.BaseRevision != base.Revision {
		t.Fatalf("rename proposal: %+v %v", proposal, err)
	}
	accepted, err := h.store.AcceptProposal(testAccount, proposal.ID, proposal.Revision, "user", authorize)
	if err != nil || accepted.ID != base.ID || accepted.Name != "new" {
		t.Fatalf("accepted rename: %+v %v", accepted, err)
	}
	if _, err := h.store.FindByName(testAccount, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the old name still names a workflow: %v", err)
	}
	// A name taken after the proposal is refused at acceptance.
	late, err := h.store.Propose(testAccount, "session", Automation{Name: "later", BaseID: other.ID, Scope: Scope{Folder: folder}, Enabled: true, Steps: steps()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.Put(testAccount, Automation{Name: "later", Scope: Scope{Folder: folder}, Enabled: true, Steps: steps()}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.AcceptProposal(testAccount, late.ID, late.Revision, "user", authorize); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("a rename onto a name taken since: %v", err)
	}
	if kept, _ := h.store.Get(testAccount, other.ID); kept.Name != "other" || kept.Revision != other.Revision {
		t.Fatalf("a refused acceptance changed the workflow: %+v", kept)
	}
}
