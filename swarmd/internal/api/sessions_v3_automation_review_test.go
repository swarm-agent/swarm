package api

import (
	"context"

	"swarm/packages/swarmd/internal/automation"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type sidechatReviewFixture struct {
	store.AutomationRecord
	plan store.SessionPlanSnapshot
}

func (f *sidechatReviewFixture) Workspace(_ context.Context, p automation.Principal, scope store.AutomationScope, _ string) error {
	if scope != f.Scope || p.AccountID != f.Scope.AccountID {
		return automation.ErrDenied
	}
	return nil
}
