package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble"
)

type Store struct {
	db *pebble.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db parent directory: %w", err)
	}
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("open pebble db: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) PutBytes(key string, value []byte) error {
	return s.db.Set([]byte(key), value, pebble.Sync)
}

func (s *Store) GetBytes(key string) ([]byte, bool, error) {
	value, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer closer.Close()

	copyValue := append([]byte(nil), value...)
	return copyValue, true, nil
}

func (s *Store) Delete(key string) error {
	return s.db.Delete([]byte(key), pebble.Sync)
}

func (s *Store) PutJSON(key string, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal json for key %q: %w", key, err)
	}
	if err := s.PutBytes(key, payload); err != nil {
		return fmt.Errorf("put json key %q: %w", key, err)
	}
	return nil
}

func (s *Store) GetJSON(key string, out any) (bool, error) {
	payload, ok, err := s.GetBytes(key)
	if err != nil {
		return false, fmt.Errorf("get json key %q: %w", key, err)
	}
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return false, fmt.Errorf("unmarshal json key %q: %w", key, err)
	}
	return true, nil
}

func (s *Store) NewBatch() *pebble.Batch {
	return s.db.NewBatch()
}

func (s *Store) IteratePrefix(prefix string, limit int, visit func(key string, value []byte) error) error {
	if strings.TrimSpace(prefix) == "" {
		return errors.New("iterate prefix must not be empty")
	}
	if limit <= 0 {
		limit = 1000
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return fmt.Errorf("create prefix iterator: %w", err)
	}
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		if count >= limit {
			break
		}
		key := string(append([]byte(nil), iter.Key()...))
		value := append([]byte(nil), iter.Value()...)
		if err := visit(key, value); err != nil {
			return err
		}
		count++
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("iterate prefix %q: %w", prefix, err)
	}
	return nil
}
