package pebblestore

import (
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/pebble"
	"net/url"
	"swarm/packages/swarmd/internal/sandbox"
)

func reviewCleanupKey(account, session string) string {
	return "review_cleanup/" + url.PathEscape(account) + "/" + url.PathEscape(session)
}
func (ss *SessionStore) GetReviewCleanup(account, session string) (sandbox.ReviewCleanupState, bool, error) {
	s := ss.store
	var state sandbox.ReviewCleanupState
	found, err := s.GetJSON(reviewCleanupKey(account, session), &state)
	return state, found, err
}
func (ss *SessionStore) PutReviewCleanup(account string, state sandbox.ReviewCleanupState) error {
	s := ss.store
	s.workersMu.Lock()
	defer s.workersMu.Unlock()
	old, found, err := ss.GetReviewCleanup(account, state.Session)
	if err != nil {
		return err
	}
	phases := map[string]int{"active": 0, "closing": 1, "container_removed": 2, "checkout_removed": 3, "volume_removed": 4}
	rank, ok := phases[state.Phase]
	if !ok || account == "" || state.Session == "" {
		return fmt.Errorf("invalid review cleanup state")
	}
	if found {
		phase := old.Phase
		old.Phase = state.Phase
		if old != state || rank < phases[phase] {
			return fmt.Errorf("review cleanup identity or phase changed")
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.db.Set([]byte(reviewCleanupKey(account, state.Session)), data, pebble.Sync)
}
