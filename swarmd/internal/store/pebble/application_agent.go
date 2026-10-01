package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

var ErrApplicationAgentConflict = errors.New("application agent revision conflict")

// ApplicationAgent is application context, never a model or tool-policy authority.
// Revisions are immutable so reopened conversations retain their original context.
type ApplicationAgent struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Instructions string `json:"instructions"`
	Context string `json:"context"`
	Revision uint64 `json:"revision"`
}

func applicationAgentKey(account, user, id string, revision uint64) []byte {
	b, _ := json.Marshal([]string{account, user, id})
	h := sha256.Sum256(b)
	return []byte(fmt.Sprintf("application_agent:%s:%020d", hex.EncodeToString(h[:]), revision))
}

func (s *SessionStore) GetApplicationAgent(account, user, id string, revision uint64) (ApplicationAgent, bool, error) {
	var record ApplicationAgent
	if strings.TrimSpace(account) == "" || strings.TrimSpace(user) == "" || strings.TrimSpace(id) == "" {
		return record, false, errors.New("application agent principal and id required")
	}
	b, closer, err := s.store.db.Get(applicationAgentKey(account, user, id, revision))
	if errors.Is(err, pebble.ErrNotFound) { return record, false, nil }
	if err != nil { return record, false, err }
	defer closer.Close()
	err = json.Unmarshal(b, &record)
	return record, err == nil, err
}

// PutApplicationAgent atomically publishes an immutable revision and latest pointer.
// Identical retries of the immediately preceding write return the same revision.
func (s *SessionStore) PutApplicationAgent(account, user string, input ApplicationAgent, expected uint64) (ApplicationAgent, error) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(user) == "" || strings.TrimSpace(input.ID) == "" || len(input.ID) > 128 || strings.TrimSpace(input.Name) == "" || len(input.Name) > 256 || len(input.Instructions)+len(input.Context) > 64*1024 {
		return ApplicationAgent{}, errors.New("invalid application agent: principal, id, name and bounded context required")
	}
	s.store.projectsMu.Lock()
	defer s.store.projectsMu.Unlock()
	current, found, err := s.GetApplicationAgent(account, user, input.ID, 0)
	if err != nil { return ApplicationAgent{}, err }
	if expected == ^uint64(0) { return ApplicationAgent{}, ErrApplicationAgentConflict }
	if found && current.Revision == expected+1 && current.Name == input.Name && current.Instructions == input.Instructions && current.Context == input.Context {
		return current, nil
	}
	if (!found && expected != 0) || (found && current.Revision != expected) || expected == ^uint64(0) {
		return ApplicationAgent{}, ErrApplicationAgentConflict
	}
	input.Revision = expected+1
	b, err := json.Marshal(input)
	if err != nil { return ApplicationAgent{}, err }
	batch := s.store.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(applicationAgentKey(account, user, input.ID, input.Revision), b, nil); err != nil { return ApplicationAgent{}, err }
	if err := batch.Set(applicationAgentKey(account, user, input.ID, 0), b, nil); err != nil { return ApplicationAgent{}, err }
	if err := batch.Commit(pebble.Sync); err != nil { return ApplicationAgent{}, err }
	return input, nil
}
