package pebblestore

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cockroachdb/pebble"
	"swarm-refactor/swarmtui/pkg/environments"
)

// ReviewPage bounds work, including non-review records, and resumes by opaque key.
// Restart starts from the beginning; no in-memory deadline is correctness state.
func (s *DeploymentStore) ReviewPage(ctx context.Context, after string) ([]environments.Deployment, string, error) {
	prefix := KeyDeploymentAccountPrefix
	it, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil { return nil, "", err }
	defer it.Close()
	start := prefix
	if strings.HasPrefix(after, prefix) { start = after + "\x00" }
	rows := make([]environments.Deployment, 0, 64)
	next := ""
	for ok := it.SeekGE([]byte(start)); ok && len(rows) < 64; ok = it.Next() {
		if err := ctx.Err(); err != nil { return nil, after, err }
		var dep environments.Deployment
		if err := json.Unmarshal(it.Value(), &dep); err != nil { return nil, after, err }
		rows = append(rows, dep)
		next = string(it.Key())
	}
	if len(rows) < 64 { next = "" }
	return rows, next, it.Error()
}
