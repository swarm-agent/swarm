package pebblestore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	WorkspaceTodoOwnerKindUser  = "user"
	WorkspaceTodoOwnerKindAgent = "agent"
)

type WorkspaceTodoOwnerSummary struct {
	TaskCount       int `json:"task_count"`
	OpenCount       int `json:"open_count"`
	InProgressCount int `json:"in_progress_count"`
}

type WorkspaceTodoItem struct {
	ID            string   `json:"id"`
	WorkspacePath string   `json:"workspace_path"`
	OwnerKind     string   `json:"owner_kind"`
	Text          string   `json:"text"`
	Done          bool     `json:"done"`
	Priority      string   `json:"priority,omitempty"`
	Group         string   `json:"group,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	InProgress    bool     `json:"in_progress,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
	ParentID      string   `json:"parent_id,omitempty"`
	SortIndex     int      `json:"sort_index"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
	CompletedAt   int64    `json:"completed_at,omitempty"`
}

type WorkspaceTodoSummary struct {
	TaskCount       int                       `json:"task_count"`
	OpenCount       int                       `json:"open_count"`
	InProgressCount int                       `json:"in_progress_count"`
	User            WorkspaceTodoOwnerSummary `json:"user"`
	Agent           WorkspaceTodoOwnerSummary `json:"agent"`
}

type WorkspaceTodoStore struct {
	store *Store
}

func NewWorkspaceTodoStore(store *Store) *WorkspaceTodoStore {
	return &WorkspaceTodoStore{store: store}
}

func (s *WorkspaceTodoStore) Get(workspacePath, itemID string) (WorkspaceTodoItem, bool, error) {
	var item WorkspaceTodoItem
	ok, err := s.store.GetJSON(KeyWorkspaceTodoItem(workspacePath, itemID), &item)
	if err != nil {
		return WorkspaceTodoItem{}, false, err
	}
	if !ok {
		return WorkspaceTodoItem{}, false, nil
	}
	return normalizeWorkspaceTodoItem(item), true, nil
}

func (s *WorkspaceTodoStore) Save(item WorkspaceTodoItem) (WorkspaceTodoItem, error) {
	item = normalizeWorkspaceTodoItem(item)
	if strings.TrimSpace(item.ID) == "" {
		return WorkspaceTodoItem{}, fmt.Errorf("todo id is required")
	}
	if strings.TrimSpace(item.WorkspacePath) == "" {
		return WorkspaceTodoItem{}, fmt.Errorf("workspace path is required")
	}
	if strings.TrimSpace(item.Text) == "" {
		return WorkspaceTodoItem{}, fmt.Errorf("todo text is required")
	}
	if err := s.store.PutJSON(KeyWorkspaceTodoItem(item.WorkspacePath, item.ID), item); err != nil {
		return WorkspaceTodoItem{}, err
	}
	return item, nil
}

func (s *WorkspaceTodoStore) Delete(workspacePath, itemID string) error {
	workspacePath = strings.TrimSpace(workspacePath)
	itemID = strings.TrimSpace(itemID)
	if workspacePath == "" {
		return fmt.Errorf("workspace path is required")
	}
	if itemID == "" {
		return fmt.Errorf("todo id is required")
	}
	return s.store.Delete(KeyWorkspaceTodoItem(workspacePath, itemID))
}

func (s *WorkspaceTodoStore) ReplaceWorkspaceItems(workspacePath string, items []WorkspaceTodoItem) error {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return fmt.Errorf("workspace path is required")
	}
	for i := range items {
		items[i].WorkspacePath = workspacePath
		items[i] = normalizeWorkspaceTodoItem(items[i])
	}

	batch := s.store.NewBatch()
	defer batch.Close()

	iter, err := s.store.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(WorkspaceTodoPrefix(workspacePath)),
		UpperBound: []byte(WorkspaceTodoPrefix(workspacePath) + "\xff"),
	})
	if err != nil {
		return fmt.Errorf("create workspace todo iterator: %w", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := append([]byte(nil), iter.Key()...)
		if err := batch.Delete(key, nil); err != nil {
			return fmt.Errorf("delete stale todo key %q: %w", string(key), err)
		}
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("iterate workspace todo keys: %w", err)
	}

	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			return fmt.Errorf("todo id is required")
		}
		if strings.TrimSpace(item.Text) == "" {
			return fmt.Errorf("todo text is required")
		}
		payload, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal todo %q: %w", item.ID, err)
		}
		if err := batch.Set([]byte(KeyWorkspaceTodoItem(workspacePath, item.ID)), payload, nil); err != nil {
			return fmt.Errorf("set todo %q: %w", item.ID, err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit workspace todo batch: %w", err)
	}
	return nil
}

func (s *WorkspaceTodoStore) List(workspacePath string, limit int) ([]WorkspaceTodoItem, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, fmt.Errorf("workspace path is required")
	}
	if limit <= 0 {
		limit = 100000
	}
	items := make([]WorkspaceTodoItem, 0, minWorkspaceTodoInt(limit, 256))
	err := s.store.IteratePrefix(WorkspaceTodoPrefix(workspacePath), limit, func(_ string, value []byte) error {
		var item WorkspaceTodoItem
		if err := json.Unmarshal(value, &item); err != nil {
			return err
		}
		if strings.TrimSpace(item.ID) == "" {
			return nil
		}
		items = append(items, normalizeWorkspaceTodoItem(item))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SortIndex != items[j].SortIndex {
			return items[i].SortIndex < items[j].SortIndex
		}
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		return items[i].ID < items[j].ID
	})
	for i := range items {
		items[i].SortIndex = i
	}
	return items, nil
}

func (s *WorkspaceTodoStore) Summary(workspacePath string) (WorkspaceTodoSummary, error) {
	items, err := s.List(workspacePath, 100000)
	if err != nil {
		return WorkspaceTodoSummary{}, err
	}
	return summarizeWorkspaceTodos(items), nil
}

func (s *WorkspaceTodoStore) Summaries(workspacePaths []string) (map[string]WorkspaceTodoSummary, error) {
	out := make(map[string]WorkspaceTodoSummary, len(workspacePaths))
	seen := make(map[string]struct{}, len(workspacePaths))
	for _, raw := range workspacePaths {
		workspacePath := strings.TrimSpace(raw)
		if workspacePath == "" {
			continue
		}
		if _, exists := seen[workspacePath]; exists {
			continue
		}
		seen[workspacePath] = struct{}{}
		summary, err := s.Summary(workspacePath)
		if err != nil {
			return nil, err
		}
		out[workspacePath] = summary
	}
	return out, nil
}

func normalizeWorkspaceTodoItem(item WorkspaceTodoItem) WorkspaceTodoItem {
	item.ID = strings.TrimSpace(item.ID)
	item.WorkspacePath = strings.TrimSpace(item.WorkspacePath)
	item.OwnerKind = NormalizeWorkspaceTodoOwnerKind(item.OwnerKind)
	item.Text = strings.TrimSpace(item.Text)
	item.Priority = normalizeWorkspaceTodoPriority(item.Priority)
	item.Group = strings.TrimSpace(item.Group)
	item.Tags = normalizeWorkspaceTodoTags(item.Tags)
	item.SessionID = strings.TrimSpace(item.SessionID)
	item.ParentID = strings.TrimSpace(item.ParentID)
	if item.OwnerKind == WorkspaceTodoOwnerKindAgent {
		item.Priority = "medium"
	} else {
		item.SessionID = ""
		item.ParentID = ""
	}
	if item.ParentID != "" && item.ParentID == item.ID {
		item.ParentID = ""
	}
	if item.CreatedAt <= 0 {
		item.CreatedAt = time.Now().UnixMilli()
	}
	if item.UpdatedAt < item.CreatedAt {
		item.UpdatedAt = item.CreatedAt
	}
	if item.Done {
		if item.CompletedAt <= 0 {
			item.CompletedAt = item.UpdatedAt
		}
	} else {
		item.CompletedAt = 0
	}
	if item.SortIndex < 0 {
		item.SortIndex = 0
	}
	return item
}

func ParseWorkspaceTodoOwnerKind(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case WorkspaceTodoOwnerKindUser:
		return WorkspaceTodoOwnerKindUser, true
	case WorkspaceTodoOwnerKindAgent:
		return WorkspaceTodoOwnerKindAgent, true
	default:
		return "", false
	}
}

func NormalizeWorkspaceTodoOwnerKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case WorkspaceTodoOwnerKindAgent:
		return WorkspaceTodoOwnerKindAgent
	case "", WorkspaceTodoOwnerKindUser:
		return WorkspaceTodoOwnerKindUser
	default:
		return WorkspaceTodoOwnerKindUser
	}
}

func WorkspaceTodoSummaryForOwner(summary WorkspaceTodoSummary, ownerKind string) WorkspaceTodoOwnerSummary {
	switch strings.ToLower(strings.TrimSpace(ownerKind)) {
	case WorkspaceTodoOwnerKindUser:
		return summary.User
	case WorkspaceTodoOwnerKindAgent:
		return summary.Agent
	default:
		return WorkspaceTodoOwnerSummary{}
	}
}

func normalizeWorkspaceTodoPriority(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low", "medium", "high", "urgent":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "medium"
	}
}

func normalizeWorkspaceTodoTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func summarizeWorkspaceTodos(items []WorkspaceTodoItem) WorkspaceTodoSummary {
	summary := WorkspaceTodoSummary{TaskCount: len(items)}
	for _, item := range items {
		if !item.Done {
			summary.OpenCount++
		}
		if item.InProgress {
			summary.InProgressCount++
		}
		ownerSummary := &summary.User
		if item.OwnerKind == WorkspaceTodoOwnerKindAgent {
			ownerSummary = &summary.Agent
		}
		ownerSummary.TaskCount++
		if !item.Done {
			ownerSummary.OpenCount++
		}
		if item.InProgress {
			ownerSummary.InProgressCount++
		}
	}
	return summary
}

func SummarizeWorkspaceTodos(items []WorkspaceTodoItem) WorkspaceTodoSummary {
	return summarizeWorkspaceTodos(items)
}

func minWorkspaceTodoInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
