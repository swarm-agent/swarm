package session

import (
	"errors"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// RepositoryHistory returns retained contexts, not filesystem access grants.
// The caller supplies its authenticated principal and must authorize every
// historical path independently before performing Git or filesystem operations.
func (s *Service) RepositoryHistory(query pebblestore.RepositoryHistoryQuery) (pebblestore.RepositoryHistoryPage, error) {
	if s == nil || s.store == nil {
		return pebblestore.RepositoryHistoryPage{}, errors.New("session store unavailable")
	}
	return s.store.ListSessionRepositoryHistory(query)
}

// TaskProgramRepositoryHistory is a read-only alternative to the mutation-
// oriented TaskProgramRepositoryLanes. Running programs are reported unchanged.
func (s *Service) TaskProgramRepositoryHistory(query pebblestore.RepositoryHistoryQuery) (pebblestore.RepositoryHistoryPage, error) {
	if s == nil || s.store == nil {
		return pebblestore.RepositoryHistoryPage{}, errors.New("session store unavailable")
	}
	return s.store.ListTaskProgramRepositoryHistory(query)
}

// BackfillRepositoryHistory is an explicit maintenance operation, never called
// by either read wrapper. Repeat bounded steps until ready before serving pages.
func (s *Service) BackfillRepositoryHistory(limit int) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("session store unavailable")
	}
	return s.store.BackfillRepositoryHistory(limit)
}

func (s *Service) RepositoryContinuation(query pebblestore.RepositoryHistoryQuery, token string, payload []byte) ([]byte, string, error) {
	if s == nil || s.store == nil { return nil, "", errors.New("session store unavailable") }
	return s.store.RepositoryContinuation(query, token, payload)
}

func (s *Service) ExactRepositoryHistory(query pebblestore.RepositoryHistoryQuery, path string) (pebblestore.RepositoryHistoryPage, error) {
	if s == nil || s.store == nil { return pebblestore.RepositoryHistoryPage{}, errors.New("session store unavailable") }
	return s.store.ExactRepositoryHistory(query, path)
}
