package pebblestore

import (
	"github.com/cockroachdb/pebble"
	"strings"
)

// Scan positions are inclusive index keys, so an unfinished session is revisited.
func beforeMemorySession(account, session string) string {
	return KeySessionByAccount(account, session)
}

// memorySessionPage bounds metadata reads and durably continues through the whole
// account index instead of repeatedly selecting only the most recent sessions.
func (s *MemoryStore) memorySessionPage(account, user, after string) ([]SessionSnapshot, string, error) {
	prefix := SessionByAccountPrefix(account)
	if after != "" && !strings.HasPrefix(after, prefix) {
		return nil, "", ErrMemoryPolicy
	}
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, "", err
	}
	defer iter.Close()
	start := after
	if start == "" {
		start = prefix
	}
	out := []SessionSnapshot{}
	n := 0
	for valid := iter.SeekGE([]byte(start)); valid; valid = iter.Next() {
		if n == 256 {
			return out, string(iter.Key()), iter.Error()
		}
		n++
		session, ok, err := NewSessionStore(s.store).GetSession(string(iter.Value()))
		if err != nil {
			return nil, "", err
		}
		if ok && session.AccountScopeID == account && session.UserID == user {
			out = append(out, session)
		}
	}
	return out, "", iter.Error()
}
