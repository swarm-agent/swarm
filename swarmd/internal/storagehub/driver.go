package storagehub

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Driver provides an abstract key-value / object storage interface.
type Driver interface {
	List(ctx context.Context, prefix string) ([]string, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// MockDriver is an in-memory storage driver for tests.
type MockDriver struct {
	mu    sync.RWMutex
	files map[string][]byte
}

func NewMockDriver() *MockDriver {
	return &MockDriver{
		files: make(map[string][]byte),
	}
}

func (m *MockDriver) List(ctx context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cleanPrefix := strings.TrimPrefix(prefix, "/")
	var keys []string
	for k := range m.files {
		cleanK := strings.TrimPrefix(k, "/")
		if strings.HasPrefix(cleanK, cleanPrefix) {
			keys = append(keys, cleanK)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (m *MockDriver) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cleanKey := strings.TrimPrefix(key, "/")
	data, ok := m.files[cleanKey]
	if !ok {
		return nil, os.ErrNotExist
	}
	res := make([]byte, len(data))
	copy(res, data)
	return res, nil
}

func (m *MockDriver) Put(ctx context.Context, key string, data []byte, contentType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanKey := strings.TrimPrefix(key, "/")
	buf := make([]byte, len(data))
	copy(buf, data)
	m.files[cleanKey] = buf
	return nil
}

func (m *MockDriver) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanKey := strings.TrimPrefix(key, "/")
	delete(m.files, cleanKey)
	return nil
}

func (m *MockDriver) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cleanKey := strings.TrimPrefix(key, "/")
	_, ok := m.files[cleanKey]
	return ok, nil
}

// LocalDirDriver adapts a local directory as an object storage bucket.
type LocalDirDriver struct {
	rootDir string
}

func NewLocalDirDriver(rootDir string) (*LocalDirDriver, error) {
	clean := filepath.Clean(rootDir)
	if err := os.MkdirAll(clean, 0755); err != nil {
		return nil, err
	}
	return &LocalDirDriver{rootDir: clean}, nil
}

func (l *LocalDirDriver) fullPath(key string) string {
	clean := strings.TrimPrefix(filepath.Clean(key), "/")
	return filepath.Join(l.rootDir, clean)
}

func (l *LocalDirDriver) List(ctx context.Context, prefix string) ([]string, error) {
	cleanPrefix := strings.TrimPrefix(prefix, "/")
	var keys []string

	err := filepath.WalkDir(l.rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(l.rootDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, cleanPrefix) {
			keys = append(keys, rel)
		}
		return nil
	})

	sort.Strings(keys)
	return keys, err
}

func (l *LocalDirDriver) Get(ctx context.Context, key string) ([]byte, error) {
	fp := l.fullPath(key)
	return os.ReadFile(fp)
}

func (l *LocalDirDriver) Put(ctx context.Context, key string, data []byte, contentType string) error {
	fp := l.fullPath(key)
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return err
	}
	return os.WriteFile(fp, data, 0644)
}

func (l *LocalDirDriver) Delete(ctx context.Context, key string) error {
	fp := l.fullPath(key)
	err := os.Remove(fp)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *LocalDirDriver) Exists(ctx context.Context, key string) (bool, error) {
	fp := l.fullPath(key)
	_, err := os.Stat(fp)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
