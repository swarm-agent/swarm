package tool

import (
	"errors"
	"github.com/google/uuid"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Media dimensions are resolved by the generation service, which exposes no
// pre-charge quote here. Unknown pricing is never treated as free: cost-capped
// workers reserve the unpriced ceiling for the media kind ("image", "audio",
// "video") and are refused when it would cross a cap. Token-only caps still apply.
func (r *Runtime) checkMediaWorkerBudget(account, session, kind string, operationIDs ...string) error {
	authority, ok := r.sessions.(interface{ Store() *store.SessionStore })
	if !ok {
		return errors.New("worker budget authority unavailable")
	}
	return authority.Store().CheckWorkerMediaAdmission(account, session, store.MediaCostEstimate{PriceStatus: "unknown", AdmissionKind: kind, AdmissionCount: 1}, operationIDs...)
}

func (r *Runtime) releaseMediaWorkerBudget(account, session string, operationIDs ...string) error {
	authority, ok := r.sessions.(interface{ Store() *store.SessionStore })
	if !ok {
		return errors.New("worker budget authority unavailable")
	}
	return authority.Store().ReleaseWorkerBudgetReservation(account, session, operationIDs...)
}

func newMediaBudgetOperation() string { return uuid.NewString() }
