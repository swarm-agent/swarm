package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/cockroachdb/pebble"
)

var ErrApplicationAgentConflict = errors.New("application agent revision conflict")

// ApplicationAgent is application context, never a model or tool-policy authority.
// Revisions are immutable so reopened conversations retain their original context.
type ApplicationAgent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Instructions string   `json:"instructions"`
	Context      string   `json:"context"`
	Revision     uint64   `json:"revision"`
	ProjectID    string   `json:"project_id,omitempty"`
	WorkerIDs    []string `json:"worker_ids,omitempty"`
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
	if errors.Is(err, pebble.ErrNotFound) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	defer closer.Close()
	err = json.Unmarshal(b, &record)
	return record, err == nil, err
}

// PutApplicationAgent atomically publishes an immutable revision and latest pointer.
// Identical retries of the immediately preceding write return the same revision.
func (s *SessionStore) PutApplicationAgent(account, user string, input ApplicationAgent, expected uint64) (ApplicationAgent, error) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(user) == "" || strings.TrimSpace(input.ID) == "" || len(input.ID) > 128 || strings.TrimSpace(input.Name) == "" || len(input.Name) > 256 || len(input.Instructions)+len(input.Context) > 64*1024 || len(input.WorkerIDs) > 100 {
		return ApplicationAgent{}, errors.New("invalid application agent: principal, id, name and bounded context required")
	}
	if len(input.WorkerIDs) == 0 {
		input.WorkerIDs = nil
	}
	s.store.projectsMu.Lock()
	defer s.store.projectsMu.Unlock()
	current, found, err := s.GetApplicationAgent(account, user, input.ID, 0)
	if err != nil {
		return ApplicationAgent{}, err
	}
	if expected == ^uint64(0) {
		return ApplicationAgent{}, ErrApplicationAgentConflict
	}
	if found && current.Revision == expected+1 && current.Name == input.Name && current.Instructions == input.Instructions && current.Context == input.Context && current.ProjectID == input.ProjectID && reflect.DeepEqual(current.WorkerIDs, input.WorkerIDs) {
		return current, nil
	}
	if (!found && expected != 0) || (found && current.Revision != expected) || expected == ^uint64(0) {
		return ApplicationAgent{}, ErrApplicationAgentConflict
	}
	input.Revision = expected + 1
	b, err := json.Marshal(input)
	if err != nil {
		return ApplicationAgent{}, err
	}
	batch := s.store.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(applicationAgentKey(account, user, input.ID, input.Revision), b, nil); err != nil {
		return ApplicationAgent{}, err
	}
	if err := batch.Set(applicationAgentKey(account, user, input.ID, 0), b, nil); err != nil {
		return ApplicationAgent{}, err
	}
	if err := batch.Set(append(applicationAgentListPrefix(account, user), []byte(hex.EncodeToString([]byte(input.ID)))...), []byte(input.ID), nil); err != nil {
		return ApplicationAgent{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return ApplicationAgent{}, err
	}
	return input, nil
}

func applicationAgentListPrefix(account, user string) []byte {
	b, _ := json.Marshal([]string{account, user})
	h := sha256.Sum256(b)
	return []byte("application_agent_index:" + hex.EncodeToString(h[:]) + ":")
}

// ListApplicationAgents uses an account/user index, never a cross-account scan.
func (s *SessionStore) ListApplicationAgents(account, user, after string, limit int) ([]ApplicationAgent, string, error) {
	if account == "" || user == "" || limit < 1 || limit > 100 {
		return nil, "", errors.New("principal and limit 1..100 required")
	}
	prefix := applicationAgentListPrefix(account, user)
	upper := append([]byte(nil), prefix...)
	upper[len(upper)-1]++
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, "", err
	}
	defer iter.Close()
	start := append(append([]byte(nil), prefix...), []byte(hex.EncodeToString([]byte(after)))...)
	records := make([]ApplicationAgent, 0)
	for valid := iter.SeekGE(start); valid; valid = iter.Next() {
		id := string(iter.Value())
		if id == after {
			continue
		}
		if len(records) == limit {
			return records, records[len(records)-1].ID, nil
		}
		record, found, err := s.GetApplicationAgent(account, user, id, 0)
		if err != nil {
			return nil, "", err
		}
		if found {
			records = append(records, record)
		}
	}
	return records, "", iter.Error()
}
