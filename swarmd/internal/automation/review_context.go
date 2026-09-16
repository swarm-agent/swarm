package automation

import (
	"context"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// ReviewContext resolves only canonical definition and plan bytes. The caller
// must authenticate access to parent; this method additionally checks workspace,
// exact revision, parent binding and every immutable executable pin.
func (s *Service) ReviewContext(ctx context.Context, p Principal, scope store.AutomationScope, id string, revision uint64, parent string) (store.AutomationRecord, error) {
	if revision == 0 || parent == "" || p.Role != "user" {
		return store.AutomationRecord{}, ErrDenied
	}
	if err := s.authorize(ctx, p, scope, "read"); err != nil {
		return store.AutomationRecord{}, err
	}
	if err := s.access.PlanSession(ctx, p, scope, parent); err != nil {
		return store.AutomationRecord{}, err
	}
	r, found, err := s.repo.GetAutomationRecord(scope, id, "definition", id, 0)
	if err != nil {
		return store.AutomationRecord{}, err
	}
	if !found || r.Definition == nil || r.Revision != revision || r.Scope != scope || r.AutomationID != id || r.Definition.SessionID != parent {
		return store.AutomationRecord{}, ErrDenied
	}
	if err := store.ValidateAutomationBindings(r.Definition.Plans); err != nil {
		return store.AutomationRecord{}, err
	}
	for _, binding := range r.Definition.Plans {
		ref := binding.Plan
		if ref.DocumentSHA256 == "" {
			return store.AutomationRecord{}, ErrDenied
		}
		if err := s.plan(ctx, p, scope, &ref); err != nil {
			return store.AutomationRecord{}, err
		}
	}
	return r, nil
}
