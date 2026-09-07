package session

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: read-only service wrappers must fail explicitly without a configured
// durable store rather than claim an empty inventory. This is the narrowest
// service boundary check; principal and persisted pagination proofs live in the
// store tests for ListSessionRepositoryHistory/ListTaskProgramRepositoryHistory.
func TestRepositoryHistoryUnavailable(t *testing.T) {
	for _, service := range []*Service{nil, {}} {
		if _, err := service.RepositoryHistory(pebblestore.RepositoryHistoryQuery{}); err == nil {
			t.Fatal("missing repository store accepted")
		}
		if _, err := service.TaskProgramRepositoryHistory(pebblestore.RepositoryHistoryQuery{}); err == nil {
			t.Fatal("missing program store accepted")
		}
		if _, err := service.BackfillRepositoryHistory(1); err == nil {
			t.Fatal("missing migration store accepted")
		}
	}
}
