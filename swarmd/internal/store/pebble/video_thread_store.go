package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type VideoClipSnapshot struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Extension  string `json:"extension"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt int64  `json:"modified_at"`
}

type VideoThreadSnapshot struct {
	ID             string              `json:"id"`
	WorkspacePath  string              `json:"workspace_path"`
	WorkspaceName  string              `json:"workspace_name"`
	Title          string              `json:"title"`
	VideoFolders   []string            `json:"video_folders"`
	VideoClips     []VideoClipSnapshot `json:"video_clips"`
	VideoClipOrder []string            `json:"video_clip_order"`
	Metadata       map[string]any      `json:"metadata,omitempty"`
	CreatedAt      int64               `json:"created_at"`
	UpdatedAt      int64               `json:"updated_at"`
}

type VideoThreadStore struct {
	store *Store
}

func NewVideoThreadStore(store *Store) *VideoThreadStore {
	return &VideoThreadStore{store: store}
}

func (s *VideoThreadStore) Create(thread VideoThreadSnapshot) (VideoThreadSnapshot, error) {
	if s == nil || s.store == nil {
		return VideoThreadSnapshot{}, errors.New("video thread store is not configured")
	}
	thread = normalizeVideoThread(thread)
	if thread.ID == "" {
		return VideoThreadSnapshot{}, errors.New("video thread id is required")
	}
	if thread.WorkspacePath == "" {
		return VideoThreadSnapshot{}, errors.New("workspace path is required")
	}
	if thread.Title == "" {
		thread.Title = "Video Thread"
	}
	now := time.Now().UnixMilli()
	if thread.CreatedAt == 0 {
		thread.CreatedAt = now
	}
	if thread.UpdatedAt == 0 {
		thread.UpdatedAt = thread.CreatedAt
	}
	if err := s.store.PutJSON(KeyVideoThread(thread.ID), thread); err != nil {
		return VideoThreadSnapshot{}, err
	}
	return thread, nil
}

func (s *VideoThreadStore) Get(threadID string) (VideoThreadSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return VideoThreadSnapshot{}, false, errors.New("video thread store is not configured")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return VideoThreadSnapshot{}, false, errors.New("video thread id is required")
	}
	var thread VideoThreadSnapshot
	ok, err := s.store.GetJSON(KeyVideoThread(threadID), &thread)
	if err != nil || !ok {
		return VideoThreadSnapshot{}, ok, err
	}
	return normalizeVideoThread(thread), true, nil
}

func (s *VideoThreadStore) Update(thread VideoThreadSnapshot) (VideoThreadSnapshot, error) {
	if s == nil || s.store == nil {
		return VideoThreadSnapshot{}, errors.New("video thread store is not configured")
	}
	thread = normalizeVideoThread(thread)
	if thread.ID == "" {
		return VideoThreadSnapshot{}, errors.New("video thread id is required")
	}
	thread.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(KeyVideoThread(thread.ID), thread); err != nil {
		return VideoThreadSnapshot{}, err
	}
	return thread, nil
}

func (s *VideoThreadStore) ListForWorkspace(workspacePath string, limit int) ([]VideoThreadSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("video thread store is not configured")
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, errors.New("workspace path is required")
	}
	if limit <= 0 {
		limit = 200
	}
	const iterateAll = int(^uint(0) >> 1)
	threads := make([]VideoThreadSnapshot, 0)
	err := s.store.IteratePrefix(VideoThreadPrefix(), iterateAll, func(_ string, value []byte) error {
		var thread VideoThreadSnapshot
		if err := json.Unmarshal(value, &thread); err != nil {
			return fmt.Errorf("unmarshal video thread: %w", err)
		}
		thread = normalizeVideoThread(thread)
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

func normalizeVideoThread(thread VideoThreadSnapshot) VideoThreadSnapshot {
	thread.ID = strings.TrimSpace(thread.ID)
	thread.WorkspacePath = strings.TrimSpace(thread.WorkspacePath)
	thread.WorkspaceName = strings.TrimSpace(thread.WorkspaceName)
	thread.Title = strings.TrimSpace(thread.Title)
	thread.VideoFolders = normalizeStringSlice(thread.VideoFolders)
	thread.VideoClipOrder = normalizeStringSlice(thread.VideoClipOrder)
	if thread.Metadata == nil {
		thread.Metadata = nil
	}
	clips := make([]VideoClipSnapshot, 0, len(thread.VideoClips))
	for _, clip := range thread.VideoClips {
		clip.ID = strings.TrimSpace(clip.ID)
		clip.Name = strings.TrimSpace(clip.Name)
		clip.Path = strings.TrimSpace(clip.Path)
		clip.Extension = strings.TrimSpace(clip.Extension)
		if clip.ID == "" || clip.Name == "" || clip.Path == "" {
			continue
		}
		clips = append(clips, clip)
	}
	thread.VideoClips = clips
	return thread
}

func normalizeStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
