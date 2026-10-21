package artifact

import (
	"sync"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// These tests use actual Pebble mutations and bare Git, not mock chain state.
// Requirement: Import publishes an independent editable root in one canonical
// mutation. Threats: cross-principal reads, partial publication, forged reference
// replay, byte/part loss and accidentally accepting a source iteration.
// Authority.Import/prepareV3ArtifactMutation/attachGitProjection own the contract;
// this is the narrowest layer that proves durable projections and exact bytes.
type importTestResolver struct{ *pebblestore.SessionStore }

func (s importTestResolver) ListPendingSessionArtifactCleanups(limit int) ([]pebblestore.V3SessionTombstone, error) {
	return s.ListPendingV3SessionArtifactCleanups(limit)
}
func (s importTestResolver) MarkSessionArtifactCleanupComplete(id string) error {
	return s.MarkV3SessionArtifactCleanupComplete(id)
}
func (s importTestResolver) GetSessionTombstone(id string) (pebblestore.V3SessionTombstone, bool, error) {
	return s.GetV3SessionTombstone(id)
}

type importTestStore struct {
	*pebblestore.SessionStore
	last   pebblestore.V3SessionMutationResult
	mu     sync.Mutex
	db     *pebblestore.Store
	dbPath string
}

func (s *importTestStore) ApplySessionMutation(in pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	result, err := s.SessionStore.ApplyV3SessionMutation(in)
	if err == nil {
		s.mu.Lock()
		s.last = result
		s.mu.Unlock()
	}
	return result, err
}
