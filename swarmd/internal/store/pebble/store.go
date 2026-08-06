package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble"
)

type Store struct {
	db                   *pebble.DB
	path                 string
	sessionMutations     *sessionMutationCoordinator
	modelProfilesMu      sync.Mutex
	swarmProfilesMu      sync.Mutex
	agentModelSettingsMu sync.Mutex
	tailscaleAllowlistMu sync.Mutex
}

func Open(path string) (*Store, error) {
	return openWithOptions(path, &pebble.Options{})
}

func OpenReadOnly(path string) (*Store, error) {
	return openWithOptions(path, &pebble.Options{ReadOnly: true})
}

func openWithOptions(path string, opts *pebble.Options) (*Store, error) {
	if err := secureDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("secure db parent directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("pebble db path %q is not a directory", path)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return nil, fmt.Errorf("secure pebble db directory: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect pebble db directory: %w", err)
	}
	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("open pebble db: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure pebble db directory: %w", err)
	}
	return &Store{db: db, path: path, sessionMutations: newSessionMutationCoordinator()}, nil
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is not a directory", path)
	}
	return os.Chmod(path, 0o700)
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.path)
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
	return iteratePrefixFromReader(s.db, prefix, limit, visit)
}

func getBytesFromReader(reader pebble.Reader, key string) ([]byte, bool, error) {
	value, closer, err := reader.Get([]byte(key))
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

func getJSONFromReader(reader pebble.Reader, key string, out any) (bool, error) {
	payload, ok, err := getBytesFromReader(reader, key)
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

type scanRangeOptions struct {
	Context    context.Context
	Prefix     string
	StartKey   string
	LowerBound string
	UpperBound string
	Limit      int
	Reverse    bool
}

func iteratePrefixFromReader(reader pebble.Reader, prefix string, limit int, visit func(key string, value []byte) error) error {
	return scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, Limit: limit}, func(key string, value []byte) (bool, error) {
		return true, visit(key, value)
	})
}

func scanRangeFromReader(reader pebble.Reader, opts scanRangeOptions, visit func(key string, value []byte) (bool, error)) error {
	if err := contextError(opts.Context); err != nil {
		return err
	}
	prefix := strings.TrimSpace(opts.Prefix)
	lower := strings.TrimSpace(opts.LowerBound)
	upper := strings.TrimSpace(opts.UpperBound)
	if prefix != "" {
		if lower == "" {
			lower = prefix
		}
		if upper == "" {
			upper = prefix + "\xff"
		}
	}
	if lower == "" || upper == "" {
		return errors.New("scan range bounds are required")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}

	iter, err := reader.NewIter(&pebble.IterOptions{
		LowerBound: []byte(lower),
		UpperBound: []byte(upper),
	})
	if err != nil {
		return fmt.Errorf("create range iterator: %w", err)
	}
	defer iter.Close()

	startKey := strings.TrimSpace(opts.StartKey)
	var valid bool
	if opts.Reverse {
		if startKey != "" {
			valid = iter.SeekLT([]byte(startKey))
		} else {
			valid = iter.Last()
		}
	} else {
		if startKey != "" {
			valid = iter.SeekGE([]byte(startKey))
		} else {
			valid = iter.First()
		}
	}

	count := 0
	for ; valid; count++ {
		if err := contextError(opts.Context); err != nil {
			return err
		}
		if count >= limit {
			break
		}
		key := string(append([]byte(nil), iter.Key()...))
		value := append([]byte(nil), iter.Value()...)
		cont, err := visit(key, value)
		if err != nil {
			return err
		}
		if !cont {
			break
		}
		if opts.Reverse {
			valid = iter.Prev()
		} else {
			valid = iter.Next()
		}
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("scan range %q..%q: %w", lower, upper, err)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
