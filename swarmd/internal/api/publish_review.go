package api

import (
	"errors"
	"fmt"

	"swarm/packages/swarmd/internal/publish"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type missingLandReview struct{ head string }

func (e *missingLandReview) Error() string {
	return fmt.Sprintf("needs an OK review of %s: give the reviewer `review_job`", e.head)
}

func reviewRequired(head string) error { return &missingLandReview{head: head} }

// requireLandReview consumes durable, account-scoped evidence, never caller
// supplied review fields. Non-worker jobs have no independent review contract.
func (c *controlMCPCall) requireLandReview(kind, worker, run, head string) error {
	principal, ok := PrincipalFromRequest(c.request)
	if !ok || c.server == nil || c.server.sessions == nil || c.server.sessions.Store() == nil {
		return errors.New("review store unavailable")
	}
	if kind != controlMCPJobWorker {
		return reviewRequired(head)
	}
	review, err := c.server.sessions.Store().WorkerStore().CurrentValidJobReview(principal.AccountScopeID, worker, run, head)
	if err != nil {
		return err
	}
	if review == nil || review.Verdict != "ok" {
		return reviewRequired(head)
	}
	return nil
}

func autoPublishSignal(kind, verdict string) bool {
	return kind == "worker.run.succeeded" || (kind == "job.reviewed" && verdict == "ok")
}

func reviewGated(rule publish.Rule) bool { return rule.Mode == "land" && rule.RequireReview }

func rejectBlockingPatch(head, patch string, reviews []store.JobReview, patchID func(string) (string, error)) error {
	for _, r := range reviews {
		if r.Verdict != "block" {
			continue
		}
		if r.Head == head {
			return reviewRequired(head)
		}
		blockedPatch, err := patchID(r.Head)
		if err != nil {
			return err
		}
		if blockedPatch == patch {
			return reviewRequired(head)
		}
	}
	return nil
}

// Admission may reuse only durable queue-owned evidence for this exact retry.
// It is not landing authority: preparation rechecks patch identity against the
// newly fetched base and rejects BLOCK on every equivalent head before landing.
func (c *controlMCPCall) requireLandAdmission(kind, worker, run string, rule publish.Rule, result map[string]any) error {
	head, _ := result["head"].(string)
	err := c.requireLandReview(kind, worker, run, head)
	if err == nil {
		return nil
	}
	var missing *missingLandReview
	if !errors.As(err, &missing) {
		return err
	}
	principal, ok := PrincipalFromRequest(c.request)
	if !ok {
		return err
	}
	q, loadErr := c.server.landingQueue()
	if loadErr != nil {
		return loadErr
	}
	entry, _, loadErr := q.Get(principal.AccountScopeID, controlMCPJobID(kind, worker, run))
	if loadErr != nil {
		return loadErr
	}
	evidence := entry.Review
	if evidence == nil || evidence.ReviewID == "" || evidence.RebasedHead != head || entry.Request.Folder != rule.Folder || entry.Request.Branch != result["worktree_branch"] || entry.Request.BaseBranch != result["base_branch"] {
		return err
	}
	ws := c.server.sessions.Store().WorkerStore()
	current, loadErr := ws.CurrentValidJobReview(principal.AccountScopeID, worker, run, head)
	if loadErr != nil {
		return loadErr
	}
	if current != nil && current.Verdict == "block" {
		return err
	}
	reviewed, loadErr := ws.CurrentValidJobReview(principal.AccountScopeID, worker, run, evidence.ReviewedHead)
	if loadErr != nil {
		return loadErr
	}
	if reviewed == nil || reviewed.Verdict != "ok" || reviewed.ReviewerJobID != evidence.ReviewID {
		return err
	}
	return nil
}
