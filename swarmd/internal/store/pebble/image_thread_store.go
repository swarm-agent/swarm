package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ImageAssetSnapshot struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Extension  string `json:"extension"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt int64  `json:"modified_at"`
}

type ImageThreadSnapshot struct {
	ID              string               `json:"id"`
	WorkspacePath   string               `json:"workspace_path"`
	WorkspaceName   string               `json:"workspace_name"`
	Title           string               `json:"title"`
	ImageFolders    []string             `json:"image_folders"`
	ImageAssets     []ImageAssetSnapshot `json:"image_assets"`
	ImageAssetOrder []string             `json:"image_asset_order"`
	Metadata        map[string]any       `json:"metadata,omitempty"`
	CreatedAt       int64                `json:"created_at"`
	UpdatedAt       int64                `json:"updated_at"`
}

type ImageThreadStore struct {
	store *Store
}

func NewImageThreadStore(store *Store) *ImageThreadStore {
	return &ImageThreadStore{store: store}
}

func (s *ImageThreadStore) Create(thread ImageThreadSnapshot) (ImageThreadSnapshot, error) {
	if s == nil || s.store == nil {
		return ImageThreadSnapshot{}, errors.New("image thread store is not configured")
	}
	thread = normalizeImageThread(thread)
	if thread.ID == "" {
		return ImageThreadSnapshot{}, errors.New("image thread id is required")
	}
	if thread.WorkspacePath == "" {
		return ImageThreadSnapshot{}, errors.New("workspace path is required")
	}
	if thread.Title == "" {
		thread.Title = "Image Thread"
	}
	now := time.Now().UnixMilli()
	if thread.CreatedAt == 0 {
		thread.CreatedAt = now
	}
	if thread.UpdatedAt == 0 {
		thread.UpdatedAt = thread.CreatedAt
	}
	if err := s.store.PutJSON(KeyImageThread(thread.ID), thread); err != nil {
		return ImageThreadSnapshot{}, err
	}
	return thread, nil
}

func (s *ImageThreadStore) Get(threadID string) (ImageThreadSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return ImageThreadSnapshot{}, false, errors.New("image thread store is not configured")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ImageThreadSnapshot{}, false, errors.New("image thread id is required")
	}
	var thread ImageThreadSnapshot
	ok, err := s.store.GetJSON(KeyImageThread(threadID), &thread)
	if err != nil || !ok {
		return ImageThreadSnapshot{}, ok, err
	}
	return normalizeImageThread(thread), true, nil
}

func (s *ImageThreadStore) Update(thread ImageThreadSnapshot) (ImageThreadSnapshot, error) {
	if s == nil || s.store == nil {
		return ImageThreadSnapshot{}, errors.New("image thread store is not configured")
	}
	thread = normalizeImageThread(thread)
	if thread.ID == "" {
		return ImageThreadSnapshot{}, errors.New("image thread id is required")
	}
	thread.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(KeyImageThread(thread.ID), thread); err != nil {
		return ImageThreadSnapshot{}, err
	}
	return thread, nil
}

func (s *ImageThreadStore) ListForWorkspace(workspacePath string, limit int) ([]ImageThreadSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("image thread store is not configured")
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, errors.New("workspace path is required")
	}
	if limit <= 0 {
		limit = 200
	}
	const iterateAll = int(^uint(0) >> 1)
	threads := make([]ImageThreadSnapshot, 0)
	err := s.store.IteratePrefix(ImageThreadPrefix(), iterateAll, func(_ string, value []byte) error {
		var thread ImageThreadSnapshot
		if err := json.Unmarshal(value, &thread); err != nil {
			return fmt.Errorf("unmarshal image thread: %w", err)
		}
		thread = normalizeImageThread(thread)
		if thread.WorkspacePath == workspacePath {
			threads = append(threads, thread)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(threads, func(i, j int) bool {
		if threads[i].UpdatedAt == threads[j].UpdatedAt {
			return threads[i].ID < threads[j].ID
		}
		return threads[i].UpdatedAt > threads[j].UpdatedAt
	})
	if len(threads) > limit {
		threads = threads[:limit]
	}
	return threads, nil
}

func normalizeImageThread(thread ImageThreadSnapshot) ImageThreadSnapshot {
	thread.ID = strings.TrimSpace(thread.ID)
	thread.WorkspacePath = strings.TrimSpace(thread.WorkspacePath)
	thread.WorkspaceName = strings.TrimSpace(thread.WorkspaceName)
	thread.Title = strings.TrimSpace(thread.Title)
	thread.ImageFolders = normalizeStringSlice(thread.ImageFolders)
	thread.ImageAssetOrder = normalizeStringSlice(thread.ImageAssetOrder)
	if thread.Metadata == nil {
		thread.Metadata = nil
	}
	assets := make([]ImageAssetSnapshot, 0, len(thread.ImageAssets))
	for _, asset := range thread.ImageAssets {
		asset.ID = strings.TrimSpace(asset.ID)
		asset.Name = strings.TrimSpace(asset.Name)
		asset.Path = strings.TrimSpace(asset.Path)
		asset.Extension = strings.TrimSpace(asset.Extension)
		if asset.ID == "" || asset.Name == "" || asset.Path == "" {
			continue
		}
		assets = append(assets, asset)
	}
	thread.ImageAssets = assets
	return thread
}
