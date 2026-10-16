package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

// Serialize join consumption across all auth-store wrappers of the same database.
var runnerJoinMu sync.Mutex
var runnerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type RunnerJoinToken struct {
	Name      string `json:"name"`
	Account   string `json:"account"`
	User      string `json:"user"`
	ExpiresAt int64  `json:"expires_at"`
}

func runnerHash(raw string) string { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }

func (s *ClientAuthStore) CreateRunnerJoinToken(account, user, name string, now time.Time) (string, error) {
	if s == nil || s.secretStore == nil {
		return "", errors.New("auth store unavailable")
	}
	if account == "" || user == "" || !runnerNamePattern.MatchString(name) {
		return "", errors.New("account, user and a valid runner name required")
	}
	raw, err := GenerateToken(32)
	if err != nil {
		return "", err
	}
	raw = "swj_" + raw
	data, err := json.Marshal(RunnerJoinToken{Name: name, Account: account, User: user, ExpiresAt: now.Add(15 * time.Minute).UnixMilli()})
	if err != nil {
		return "", err
	}
	err = s.secretStore.db.Set([]byte("runner-join:v1:"+runnerHash(raw)), data, pebble.Sync)
	return raw, err
}

// ConsumeRunnerJoinToken atomically spends the one-time token and publishes both
// credential indexes. A crash cannot consume a token without creating its key.
func (s *ClientAuthStore) ConsumeRunnerJoinToken(raw string, now time.Time) (string, ScopedTokenRecord, error) {
	runnerJoinMu.Lock()
	defer runnerJoinMu.Unlock()
	var zero ScopedTokenRecord
	if s == nil || s.secretStore == nil {
		return "", zero, errors.New("auth store unavailable")
	}
	key := "runner-join:v1:" + runnerHash(raw)
	var join RunnerJoinToken
	found, err := s.secretStore.GetJSON(key, &join)
	if err != nil {
		return "", zero, err
	}
	if !found || now.UnixMilli() >= join.ExpiresAt {
		return "", zero, errors.New("invalid or expired join token")
	}
	records, err := s.ListScopedTokens(join.Account)
	if err != nil {
		return "", zero, err
	}
	for _, rec := range records {
		if rec.Kind == "runner" && rec.Name == join.Name && !rec.Revoked {
			return "", zero, errors.New("runner name already registered")
		}
	}
	secret, err := GenerateToken(32)
	if err != nil {
		return "", zero, err
	}
	id, err := GenerateToken(16)
	if err != nil {
		return "", zero, err
	}
	token := "swk_" + secret
	rec := ScopedTokenRecord{ID: "tok_" + id, Kind: "runner", Name: join.Name, TokenHash: runnerHash(token), Scopes: []string{"runners:claim", "runners:report"}, AccountScopeID: join.Account, UserID: join.User, CreatedAt: now.UnixMilli()}
	data, err := json.Marshal(rec)
	if err != nil {
		return "", zero, err
	}
	batch := s.secretStore.NewBatch()
	defer batch.Close()
	if err = batch.Set([]byte(KeyAuthScopedToken(rec.AccountScopeID, rec.ID)), data, nil); err != nil {
		return "", zero, err
	}
	if err = batch.Set([]byte(KeyAuthScopedTokenByHash(rec.TokenHash)), data, nil); err != nil {
		return "", zero, err
	}
	if err = batch.Delete([]byte(key), nil); err != nil {
		return "", zero, err
	}
	if err = batch.Commit(pebble.Sync); err != nil {
		return "", zero, err
	}
	return token, rec, nil
}
