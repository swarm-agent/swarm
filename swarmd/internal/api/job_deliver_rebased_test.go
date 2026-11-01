package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
)

// Requirement: the daemon's merge/publish deliverer (toBranch) lands a
// delivery head Swarm rebased once the job's branch names it, and refuses it,
// naming the branch, while the branch is still at the old head. It reads the
// branch through the daemon's routed rebase Git. Threat: every rebased
// delivery refused at its merge ("an earlier step moved the delivery
// head"), or a merge of a branch head the checks never saw. Owners:
// jobAutomationDeliverer.toBranch, jobautomation.BranchCarriesHead,
// JobAutomationRebaseGit over a real repository. Past the head rule the
// deliverer needs a project task to integrate, which this test does not
// build: the moved-branch case only proves the head rule let it through.
func TestToBranchDeliversARebasedHeadOnlyOnItsBranch(t *testing.T) {
	folder, base, head, tip := rebaseRouteRepo(t)
	g := jobautomation.GitRebaser{Git: JobAutomationRebaseGit}
	res, err := g.Replay(context.Background(), folder, head, tip)
	if err != nil || res.Status != jobautomation.RebaseRebased {
		t.Fatalf("replay: %+v %v", res, err)
	}
	req := jobautomation.DeliverRequest{Folder: folder, To: jobautomation.DeliverMerge, Head: res.To, RebasedFrom: head,
		Result: jobautomation.Result{JobID: "worker/wkr-1/run-1", Git: &jobautomation.ResultGit{Branch: "agent/run-1", BaseBranch: "main", BaseCommit: base, Head: head}}}
	d := jobAutomationDeliverer{s: &Server{}}

	_, err = d.toBranch(context.Background(), req)
	var refused *jobautomation.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "Swarm rebased the delivery head") || !strings.Contains(refused.Reason, "agent/run-1 is at "+head) {
		t.Fatalf("branch not moved: %v", err)
	}
	if update := g.UpdateBranch(context.Background(), folder, "agent/run-1", head, res.To); !update.Updated {
		t.Fatalf("branch update: %+v", update)
	}
	if _, err = d.toBranch(context.Background(), req); err != nil && strings.Contains(err.Error(), "delivery head") {
		t.Fatalf("branch moved, still refused for its head: %v", err)
	}
}
