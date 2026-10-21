package run

import (
	store "swarm/packages/swarmd/internal/store/pebble"
)

type workerScopeSessions struct{}

func (workerScopeSessions) GetSession(id string) (store.SessionSnapshot, bool, error) {
	return store.SessionSnapshot{ID: id, AccountScopeID: "account"}, true, nil
}
