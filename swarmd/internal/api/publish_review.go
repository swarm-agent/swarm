package api

import (
	"errors"
	"fmt"

	"swarm/packages/swarmd/internal/publish"
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
