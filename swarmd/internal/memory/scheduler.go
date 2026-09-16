package memory

import (
	"context"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

// RunScheduler owns one serial worker and one minute timer, not session polling.
// Only metadata for explicitly opted-in owners is scanned. Errors are surfaced
// through a redacted callback; job failures remain durable in MemoryDocument.
func (s *Service) RunScheduler(ctx context.Context, identities *store.IdentityStore, report func(error)) {
	recovered := map[string]bool{}
	step := func() {
		owners, err := s.Store.MemoryAutomationOwners()
		if err != nil {
			report(err)
			return
		}
		for _, owner := range owners {
			if ctx.Err() != nil {
				return
			}
			member, ok, err := identities.GetAccountUser(owner.Account, owner.User)
			if err != nil {
				report(err)
				continue
			}
			if !ok || member.Status != "active" {
				continue
			}
			p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: owner.Account, UserID: owner.User}
			jobCtx := identity.ContextWithPrincipal(ctx, p)
			key := owner.Account + "\x00" + owner.User
			if !recovered[key] {
				if err = s.Recover(jobCtx); err != nil {
					report(err)
					continue
				}
				recovered[key] = true
			}
			if _, err = s.Tick(jobCtx, time.Now()); err != nil {
				report(err)
			}
		}
	}
	step()
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			step()
		}
	}
}
