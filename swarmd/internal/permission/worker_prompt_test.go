package permission

import (
	store "swarm/packages/swarmd/internal/store/pebble"
)

type workerPromptSessions struct{ worker bool }

func (s workerPromptSessions) GetSession(id string) (store.SessionSnapshot, bool, error) {
	snapshot := store.SessionSnapshot{ID: id, AccountScopeID: "account", UserID: "owner"}
	if s.worker {
		snapshot.Metadata = map[string]any{"worker_id": "worker", "worker_execution_run_id": "job-run"}
	}
	return snapshot, true, nil
}
