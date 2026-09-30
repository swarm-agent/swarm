package tool

import (
	"errors"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Media dimensions are resolved by the generation service. Until that service
// exposes a pre-charge verified quote here, cost-capped workers fail closed:
// unknown pricing must never be treated as free. Token-only caps still apply.
func (r *Runtime) checkMediaWorkerBudget(account, session string) error {
	authority, ok := r.sessions.(interface{ Store() *store.SessionStore })
	if !ok {
		return errors.New("worker budget authority unavailable")
	}
	return authority.Store().CheckWorkerSessionBudgetWithPrice(account, session, "unknown")
}

func (r *Runtime) releaseMediaWorkerBudget(account, session string) error {
	authority, ok := r.sessions.(interface{ Store() *store.SessionStore })
	if !ok {
		return errors.New("worker budget authority unavailable")
	}
	return authority.Store().ReleaseWorkerBudgetReservation(account, session)
}
