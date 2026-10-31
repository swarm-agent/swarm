package jobautomation

import "errors"

var ErrProposalConflict = errors.New("workflow proposal or active revision changed; review it again")

// Propose keeps a session's pending revision separate from the running definition.
// The proposal replaces the accepted workflow of its name, or with in.BaseID
// set, that workflow (a change that renames it); a name another workflow
// already has is refused.
func (s *Store) Propose(account, session string, in Automation) (Automation, error) {
	if account == "" || session == "" {
		return Automation{}, errors.New("account and proposing session are required")
	}
	if err := in.Normalize(); err != nil {
		return Automation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.listLocked(account)
	if err != nil {
		return Automation{}, err
	}
	baseID := in.BaseID
	in.BaseID, in.BaseRevision = "", 0
	in.ID, err = newID("jauto_")
	if err != nil {
		return Automation{}, err
	}
	in.Account, in.Proposed, in.ProposedBy = account, true, session
	in.Revision, in.CreatedAt, in.UpdatedAt = 1, s.now().UnixMilli(), s.now().UnixMilli()
	if baseID != "" {
		for _, a := range list {
			if a.ID == baseID && !a.Proposed {
				in.BaseID, in.BaseRevision = a.ID, a.Revision
			}
		}
		if in.BaseID == "" {
			return Automation{}, ErrNotFound
		}
	}
	var previous *Automation
	for _, a := range list {
		if a.Name != in.Name && (baseID == "" || a.BaseID != baseID) {
			continue
		}
		if !a.Proposed && a.Name == in.Name {
			if baseID != "" && a.ID != baseID {
				return Automation{}, invalid("another workflow is already named %q (%s)", in.Name, a.ID)
			}
			in.BaseID, in.BaseRevision = a.ID, a.Revision
		}
		if a.Proposed && a.ProposedBy == session && previous == nil {
			copy := a
			previous = &copy
			in.ID, in.CreatedAt, in.Revision = a.ID, a.CreatedAt, a.Revision+1
		}
	}
	if previous == nil && len(list) >= maxDefinitions {
		return Automation{}, invalid("too many workflows")
	}
	return in, s.saveHistoryLocked(in, previous, "proposed", Change{Source: "propose", Who: session})
}

// AcceptProposal atomically replaces only the reviewed base and removes the proposal.
// authorize is evaluated under the revision lock before any durable change.
func (s *Store) AcceptProposal(account, id string, revision uint64, who string, authorize func(Automation) error) (Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.Get(account, id)
	if err != nil {
		return Automation{}, err
	}
	if !a.Proposed || revision == 0 || a.Revision != revision {
		return Automation{}, ErrProposalConflict
	}
	if authorize == nil {
		return Automation{}, errors.New("proposal acceptance requires authorization")
	}
	if err := authorize(a); err != nil {
		return Automation{}, err
	}
	list, err := s.listLocked(account)
	if err != nil {
		return Automation{}, err
	}
	var previous *Automation
	for _, current := range list {
		if current.Proposed {
			continue
		}
		// The reviewed base, by id (a proposal may rename it); with no base,
		// the workflow of the proposal's name.
		if (a.BaseID != "" && current.ID == a.BaseID) || (a.BaseID == "" && current.Name == a.Name) {
			copy := current
			previous = &copy
		}
	}
	if (previous == nil && a.BaseID != "") || (previous != nil && (previous.ID != a.BaseID || previous.Revision != a.BaseRevision)) {
		return Automation{}, ErrProposalConflict
	}
	// A rename must not take another workflow's name.
	for _, current := range list {
		if !current.Proposed && current.Name == a.Name && (previous == nil || current.ID != previous.ID) {
			return Automation{}, ErrProposalConflict
		}
	}
	proposalID := a.ID
	if previous != nil {
		a.ID, a.CreatedAt, a.Revision = previous.ID, previous.CreatedAt, previous.Revision
	}
	a.Revision++
	a.UpdatedAt = s.now().UnixMilli()
	a.Proposed, a.ProposedBy, a.BaseID, a.BaseRevision = false, "", "", 0
	return a, s.saveHistoryRemovingProposalLocked(a, previous, "accepted", Change{Source: "accept", Who: who}, proposalID)
}

// SetEnabled changes only the switch, never publishes a pending revision.
func (s *Store) SetEnabled(account, id string, enabled bool, who string) (Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.Get(account, id)
	if err != nil {
		return Automation{}, err
	}
	if a.Proposed {
		return Automation{}, ErrProposalConflict
	}
	if a.Enabled == enabled {
		return a, nil
	}
	previous := a
	a.Enabled = enabled
	a.Revision++
	a.UpdatedAt = s.now().UnixMilli()
	return a, s.saveHistoryLocked(a, &previous, "updated", Change{Source: "toggle", Who: who})
}
