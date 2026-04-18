package todo

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const streamPrefix = "workspace_todo:"

type Service struct {
	store   *pebblestore.WorkspaceTodoStore
	events  *pebblestore.EventLog
	publish func(pebblestore.EventEnvelope)
}

type TodoItem = pebblestore.WorkspaceTodoItem

type TodoSummary = pebblestore.WorkspaceTodoSummary

type CreateInput struct {
	WorkspacePath string
	OwnerKind     string
	Text          string
	Priority      string
	Group         string
	Tags          []string
	InProgress    bool
	SessionID     string
	ParentID      string
}

type UpdateInput struct {
	WorkspacePath string
	ID            string
	Text          *string
	Done          *bool
	Priority      *string
	Group         *string
	Tags          []string
	InProgress    *bool
	SessionID     *string
	ParentID      *string
}

type ReorderInput struct {
	WorkspacePath string
	OwnerKind     string
	OrderedIDs    []string
}

type ListOptions struct {
	OwnerKind string
}

type BatchOperation struct {
	Action     string
	ID         string
	OwnerKind  string
	Text       *string
	Done       *bool
	Priority   *string
	Group      *string
	Tags       []string
	InProgress *bool
	SessionID  *string
	ParentID   *string
	OrderedIDs []string
}

type BatchResult struct {
	Index  int        `json:"index"`
	Action string     `json:"action"`
	ID     string     `json:"id,omitempty"`
	Item   TodoItem   `json:"item,omitempty"`
	Items  []TodoItem `json:"items,omitempty"`
}

func NewService(store *pebblestore.WorkspaceTodoStore, events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope)) *Service {
	return &Service{store: store, events: events, publish: publish}
}

func (s *Service) List(workspacePath string, options ...ListOptions) ([]TodoItem, TodoSummary, error) {
	items, err := s.store.List(strings.TrimSpace(workspacePath), 100000)
	if err != nil {
		return nil, TodoSummary{}, err
	}
	ownerKind := listOwnerKind(options)
	if ownerKind == "" {
		return items, pebblestore.SummarizeWorkspaceTodos(items), nil
	}
	filtered := filterItemsByOwnerKind(items, ownerKind)
	return filtered, summarizeForOwner(items, ownerKind), nil
}

func (s *Service) Summaries(workspacePaths []string) (map[string]TodoSummary, error) {
	return s.store.Summaries(workspacePaths)
}

func (s *Service) Create(input CreateInput) (TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	workspacePath := strings.TrimSpace(input.WorkspacePath)
	if workspacePath == "" {
		return TodoItem{}, TodoSummary{}, nil, fmt.Errorf("workspace path is required")
	}
	ownerKind := pebblestore.NormalizeWorkspaceTodoOwnerKind(input.OwnerKind)
	items, err := s.store.List(workspacePath, 100000)
	if err != nil {
		return TodoItem{}, TodoSummary{}, nil, err
	}
	if input.InProgress {
		clearInProgressForOwner(items, ownerKind, "")
	}
	now := time.Now().UnixMilli()
	item := TodoItem{
		ID:            fmt.Sprintf("todo_%d_%d", now, len(items)),
		WorkspacePath: workspacePath,
		OwnerKind:     ownerKind,
		Text:          strings.TrimSpace(input.Text),
		Priority:      strings.TrimSpace(input.Priority),
		Group:         strings.TrimSpace(input.Group),
		Tags:          append([]string(nil), input.Tags...),
		InProgress:    input.InProgress,
		SessionID:     strings.TrimSpace(input.SessionID),
		ParentID:      strings.TrimSpace(input.ParentID),
		SortIndex:     len(items),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	items = append(items, item)
	items = normalizeSortOrder(items)
	if err := s.persistItems(items); err != nil {
		return TodoItem{}, TodoSummary{}, nil, err
	}
	created, ok := findItem(items, item.ID)
	if !ok {
		return TodoItem{}, TodoSummary{}, nil, fmt.Errorf("created todo not found")
	}
	summary := pebblestore.SummarizeWorkspaceTodos(items)
	event, err := s.appendEvent(workspacePath, "workspace.todo.created", created.ID, map[string]any{
		"workspace_path": workspacePath,
		"item":           created,
		"summary":        summary,
	})
	if err != nil {
		return TodoItem{}, TodoSummary{}, nil, err
	}
	return created, summary, event, nil
}

func (s *Service) Update(input UpdateInput) (TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	workspacePath := strings.TrimSpace(input.WorkspacePath)
	itemID := strings.TrimSpace(input.ID)
	if workspacePath == "" {
		return TodoItem{}, TodoSummary{}, nil, fmt.Errorf("workspace path is required")
	}
	if itemID == "" {
		return TodoItem{}, TodoSummary{}, nil, fmt.Errorf("todo id is required")
	}
	items, err := s.store.List(workspacePath, 100000)
	if err != nil {
		return TodoItem{}, TodoSummary{}, nil, err
	}
	index := indexOfItem(items, itemID)
	if index < 0 {
		return TodoItem{}, TodoSummary{}, nil, fmt.Errorf("todo %q not found", itemID)
	}
	now := time.Now().UnixMilli()
	item := items[index]
	if input.Text != nil {
		item.Text = strings.TrimSpace(*input.Text)
	}
	if input.Done != nil {
		item.Done = *input.Done
		if item.Done {
			item.CompletedAt = now
			item.InProgress = false
		} else {
			item.CompletedAt = 0
		}
	}
	if input.Priority != nil {
		item.Priority = strings.TrimSpace(*input.Priority)
	}
	if input.Group != nil {
		item.Group = strings.TrimSpace(*input.Group)
	}
	if input.Tags != nil {
		item.Tags = append([]string(nil), input.Tags...)
	}
	if input.InProgress != nil {
		item.InProgress = *input.InProgress
		if item.InProgress {
			clearInProgressForOwner(items, item.OwnerKind, item.ID)
		}
	}
	if input.SessionID != nil {
		item.SessionID = strings.TrimSpace(*input.SessionID)
	}
	if input.ParentID != nil {
		item.ParentID = strings.TrimSpace(*input.ParentID)
	}
	item.UpdatedAt = now
	items[index] = item
	items = normalizeSortOrder(items)
	if err := s.persistItems(items); err != nil {
		return TodoItem{}, TodoSummary{}, nil, err
	}
	updated, ok := findItem(items, item.ID)
	if !ok {
		return TodoItem{}, TodoSummary{}, nil, fmt.Errorf("updated todo not found")
	}
	summary := pebblestore.SummarizeWorkspaceTodos(items)
	event, err := s.appendEvent(workspacePath, "workspace.todo.updated", updated.ID, map[string]any{
		"workspace_path": workspacePath,
		"item":           updated,
		"summary":        summary,
	})
	if err != nil {
		return TodoItem{}, TodoSummary{}, nil, err
	}
	return updated, summary, event, nil
}

func (s *Service) Delete(workspacePath, itemID string) (TodoSummary, *pebblestore.EventEnvelope, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	itemID = strings.TrimSpace(itemID)
	if workspacePath == "" {
		return TodoSummary{}, nil, fmt.Errorf("workspace path is required")
	}
	if itemID == "" {
		return TodoSummary{}, nil, fmt.Errorf("todo id is required")
	}
	items, err := s.store.List(workspacePath, 100000)
	if err != nil {
		return TodoSummary{}, nil, err
	}
	index := indexOfItem(items, itemID)
	if index < 0 {
		return TodoSummary{}, nil, fmt.Errorf("todo %q not found", itemID)
	}
	items = append(items[:index], items[index+1:]...)
	items = normalizeSortOrder(items)
	if err := s.persistItems(items); err != nil {
		return TodoSummary{}, nil, err
	}
	if err := s.store.Delete(workspacePath, itemID); err != nil {
		return TodoSummary{}, nil, err
	}
	summary := pebblestore.SummarizeWorkspaceTodos(items)
	event, err := s.appendEvent(workspacePath, "workspace.todo.deleted", itemID, map[string]any{
		"workspace_path": workspacePath,
		"item_id":        itemID,
		"summary":        summary,
	})
	if err != nil {
		return TodoSummary{}, nil, err
	}
	return summary, event, nil
}

func (s *Service) DeleteDone(workspacePath string, options ...ListOptions) ([]TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	ownerKind := listOwnerKind(options)
	return s.deleteMatching(workspacePath, "workspace.todo.deleted_done", func(item TodoItem) bool {
		return item.Done && (ownerKind == "" || item.OwnerKind == ownerKind)
	}, options...)
}

func (s *Service) DeleteAll(workspacePath string, options ...ListOptions) ([]TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	ownerKind := listOwnerKind(options)
	return s.deleteMatching(workspacePath, "workspace.todo.deleted_all", func(item TodoItem) bool {
		return ownerKind == "" || item.OwnerKind == ownerKind
	}, options...)
}

func (s *Service) deleteMatching(workspacePath, eventType string, shouldDelete func(TodoItem) bool, options ...ListOptions) ([]TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, TodoSummary{}, nil, fmt.Errorf("workspace path is required")
	}
	items, err := s.store.List(workspacePath, 100000)
	if err != nil {
		return nil, TodoSummary{}, nil, err
	}
	ownerKind := listOwnerKind(options)
	remaining := make([]TodoItem, 0, len(items))
	deletedIDs := make([]string, 0)
	for _, item := range items {
		if shouldDelete != nil && shouldDelete(item) {
			deletedIDs = append(deletedIDs, strings.TrimSpace(item.ID))
			continue
		}
		remaining = append(remaining, item)
	}
	remaining = normalizeSortOrder(remaining)
	summary := summarizeForOwner(remaining, ownerKind)
	fullSummary := pebblestore.SummarizeWorkspaceTodos(remaining)
	if len(deletedIDs) == 0 {
		return filteredItemsForOwner(remaining, ownerKind), summary, nil, nil
	}
	if err := s.store.ReplaceWorkspaceItems(workspacePath, remaining); err != nil {
		return nil, TodoSummary{}, nil, err
	}
	event, err := s.appendEvent(workspacePath, eventType, workspacePath, map[string]any{
		"workspace_path":   workspacePath,
		"owner_kind":       ownerKind,
		"deleted_ids":      deletedIDs,
		"deleted_count":    len(deletedIDs),
		"items":            filteredItemsForOwner(remaining, ownerKind),
		"summary":          fullSummary,
		"filtered_summary": summary,
	})
	if err != nil {
		return nil, TodoSummary{}, nil, err
	}
	return filteredItemsForOwner(remaining, ownerKind), summary, event, nil
}

func (s *Service) Reorder(input ReorderInput) ([]TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	workspacePath := strings.TrimSpace(input.WorkspacePath)
	if workspacePath == "" {
		return nil, TodoSummary{}, nil, fmt.Errorf("workspace path is required")
	}
	items, err := s.store.List(workspacePath, 100000)
	if err != nil {
		return nil, TodoSummary{}, nil, err
	}
	ownerKind := normalizeOptionalOwnerKind(input.OwnerKind)
	ordered := reorderItemsForOwner(items, ownerKind, input.OrderedIDs)
	ordered = normalizeSortOrder(ordered)
	if err := s.persistItems(ordered); err != nil {
		return nil, TodoSummary{}, nil, err
	}
	summary := summarizeForOwner(ordered, ownerKind)
	fullSummary := pebblestore.SummarizeWorkspaceTodos(ordered)
	event, err := s.appendEvent(workspacePath, "workspace.todo.reordered", workspacePath, map[string]any{
		"workspace_path":   workspacePath,
		"owner_kind":       ownerKind,
		"items":            filteredItemsForOwner(ordered, ownerKind),
		"summary":          fullSummary,
		"filtered_summary": summary,
	})
	if err != nil {
		return nil, TodoSummary{}, nil, err
	}
	return filteredItemsForOwner(ordered, ownerKind), summary, event, nil
}

func (s *Service) SetInProgress(workspacePath, itemID string) (TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	inProgress := true
	return s.Update(UpdateInput{WorkspacePath: workspacePath, ID: itemID, InProgress: &inProgress})
}

func (s *Service) ApplyBatch(workspacePath string, operations []BatchOperation) ([]BatchResult, []TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, nil, TodoSummary{}, nil, fmt.Errorf("workspace path is required")
	}
	if len(operations) == 0 {
		return nil, nil, TodoSummary{}, nil, fmt.Errorf("operations is required")
	}
	items, err := s.store.List(workspacePath, 100000)
	if err != nil {
		return nil, nil, TodoSummary{}, nil, err
	}
	working := append([]TodoItem(nil), items...)
	results := make([]BatchResult, 0, len(operations))
	ownerKindsUsed := make(map[string]struct{})
	for idx, op := range operations {
		action := strings.ToLower(strings.TrimSpace(op.Action))
		if action == "" {
			return nil, nil, TodoSummary{}, nil, fmt.Errorf("operation %d action is required", idx)
		}
		result := BatchResult{Index: idx, Action: action}
		normalizedOwnerKind := normalizeOptionalOwnerKind(op.OwnerKind)
		if normalizedOwnerKind != "" {
			ownerKindsUsed[normalizedOwnerKind] = struct{}{}
		}
		switch action {
		case "create":
			text := ""
			if op.Text != nil {
				text = strings.TrimSpace(*op.Text)
			}
			if text == "" {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("operation %d text is required", idx)
			}
			now := time.Now().UnixMilli()
			ownerKind := pebblestore.NormalizeWorkspaceTodoOwnerKind(op.OwnerKind)
			item := TodoItem{
				ID:            fmt.Sprintf("todo_%d_%d", now, idx),
				WorkspacePath: workspacePath,
				OwnerKind:     ownerKind,
				Text:          text,
				Priority:      derefString(op.Priority),
				Group:         derefString(op.Group),
				Tags:          append([]string(nil), op.Tags...),
				InProgress:    op.InProgress != nil && *op.InProgress,
				SessionID:     derefString(op.SessionID),
				ParentID:      derefString(op.ParentID),
				SortIndex:     len(working),
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if item.InProgress {
				clearInProgressForOwner(working, ownerKind, "")
			}
			working = append(working, item)
			working = normalizeSortOrder(working)
			created, ok := findItem(working, item.ID)
			if !ok {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("created todo not found")
			}
			result.ID = created.ID
			result.Item = created
		case "update":
			itemID := strings.TrimSpace(op.ID)
			if itemID == "" {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("operation %d id is required", idx)
			}
			itemIndex := indexOfItem(working, itemID)
			if itemIndex < 0 {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("todo %q not found", itemID)
			}
			now := time.Now().UnixMilli()
			item := working[itemIndex]
			if op.Text != nil {
				item.Text = strings.TrimSpace(*op.Text)
			}
			if op.Done != nil {
				item.Done = *op.Done
				if item.Done {
					item.CompletedAt = now
					item.InProgress = false
				} else {
					item.CompletedAt = 0
				}
			}
			if op.Priority != nil {
				item.Priority = strings.TrimSpace(*op.Priority)
			}
			if op.Group != nil {
				item.Group = strings.TrimSpace(*op.Group)
			}
			if op.Tags != nil {
				item.Tags = append([]string(nil), op.Tags...)
			}
			if op.InProgress != nil {
				item.InProgress = *op.InProgress
				if item.InProgress {
					clearInProgressForOwner(working, item.OwnerKind, item.ID)
				}
			}
			if op.SessionID != nil {
				item.SessionID = strings.TrimSpace(*op.SessionID)
			}
			if op.ParentID != nil {
				item.ParentID = strings.TrimSpace(*op.ParentID)
			}
			item.UpdatedAt = now
			working[itemIndex] = item
			working = normalizeSortOrder(working)
			updated, ok := findItem(working, item.ID)
			if !ok {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("updated todo not found")
			}
			result.ID = updated.ID
			result.Item = updated
		case "delete_done":
			ownerKind := normalizedOwnerKind
			remaining := make([]TodoItem, 0, len(working))
			deletedCount := 0
			for _, item := range working {
				if item.Done && (ownerKind == "" || item.OwnerKind == ownerKind) {
					deletedCount++
					continue
				}
				remaining = append(remaining, item)
			}
			working = normalizeSortOrder(remaining)
			result.ID = fmt.Sprintf("deleted:%d", deletedCount)
			result.Items = filteredItemsForOwner(working, ownerKind)
		case "delete_all":
			ownerKind := normalizedOwnerKind
			remaining := make([]TodoItem, 0, len(working))
			deletedCount := 0
			for _, item := range working {
				if ownerKind == "" || item.OwnerKind == ownerKind {
					deletedCount++
					continue
				}
				remaining = append(remaining, item)
			}
			working = normalizeSortOrder(remaining)
			result.ID = fmt.Sprintf("deleted:%d", deletedCount)
			result.Items = filteredItemsForOwner(working, ownerKind)
		case "delete":
			itemID := strings.TrimSpace(op.ID)
			if itemID == "" {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("operation %d id is required", idx)
			}
			itemIndex := indexOfItem(working, itemID)
			if itemIndex < 0 {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("todo %q not found", itemID)
			}
			working = append(working[:itemIndex], working[itemIndex+1:]...)
			working = normalizeSortOrder(working)
			result.ID = itemID
		case "reorder":
			if len(op.OrderedIDs) == 0 {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("operation %d ordered_ids is required", idx)
			}
			working = reorderItemsForOwner(working, normalizedOwnerKind, op.OrderedIDs)
			working = normalizeSortOrder(working)
			result.Items = filteredItemsForOwner(working, normalizedOwnerKind)
		case "in_progress":
			itemID := strings.TrimSpace(op.ID)
			if itemID == "" {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("operation %d id is required", idx)
			}
			itemIndex := indexOfItem(working, itemID)
			if itemIndex < 0 {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("todo %q not found", itemID)
			}
			clearInProgressForOwner(working, working[itemIndex].OwnerKind, itemID)
			working[itemIndex].InProgress = true
			working[itemIndex].UpdatedAt = time.Now().UnixMilli()
			working = normalizeSortOrder(working)
			inProgress, ok := findItem(working, itemID)
			if !ok {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("in-progress todo not found")
			}
			result.ID = inProgress.ID
			result.Item = inProgress
		default:
			return nil, nil, TodoSummary{}, nil, fmt.Errorf("unsupported todo action %q", action)
		}
		results = append(results, result)
	}
	if err := s.store.ReplaceWorkspaceItems(workspacePath, working); err != nil {
		return nil, nil, TodoSummary{}, nil, err
	}
	summary := pebblestore.SummarizeWorkspaceTodos(working)
	event, err := s.appendEvent(workspacePath, "workspace.todo.batch_applied", workspacePath, map[string]any{
		"workspace_path": workspacePath,
		"operations":     operations,
		"results":        results,
		"items":          working,
		"summary":        summary,
	})
	if err != nil {
		return nil, nil, TodoSummary{}, nil, err
	}
	returnOwnerKind := batchReturnOwnerKind(ownerKindsUsed)
	return results, filteredItemsForOwner(working, returnOwnerKind), summarizeForOwner(working, returnOwnerKind), event, nil
}

func (s *Service) persistItems(items []TodoItem) error {
	for _, item := range items {
		if _, err := s.store.Save(item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) appendEvent(workspacePath, eventType, entityID string, payload any) (*pebblestore.EventEnvelope, error) {
	if s == nil || s.events == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	env, err := s.events.Append(streamID(workspacePath), strings.TrimSpace(eventType), strings.TrimSpace(entityID), raw, "", "")
	if err != nil {
		return nil, err
	}
	if s.publish != nil {
		s.publish(env)
	}
	return &env, nil
}

func streamID(workspacePath string) string {
	return streamPrefix + strings.TrimSpace(workspacePath)
}

func indexOfItem(items []TodoItem, itemID string) int {
	for i, item := range items {
		if strings.TrimSpace(item.ID) == strings.TrimSpace(itemID) {
			return i
		}
	}
	return -1
}

func findItem(items []TodoItem, itemID string) (TodoItem, bool) {
	index := indexOfItem(items, itemID)
	if index < 0 {
		return TodoItem{}, false
	}
	return items[index], true
}

func normalizeSortOrder(items []TodoItem) []TodoItem {
	for i := range items {
		items[i].SortIndex = i
	}
	return items
}

func reorderItemsForOwner(items []TodoItem, ownerKind string, orderedIDs []string) []TodoItem {
	ownerKind = normalizeOptionalOwnerKind(ownerKind)
	if len(items) == 0 || len(orderedIDs) == 0 {
		return items
	}
	if ownerKind == "" {
		return reorderItems(items, orderedIDs)
	}
	selected := make([]TodoItem, 0)
	for _, item := range items {
		if item.OwnerKind == ownerKind {
			selected = append(selected, item)
		}
	}
	reorderedSelected := reorderItems(selected, orderedIDs)
	merged := make([]TodoItem, 0, len(items))
	selectedIndex := 0
	for _, item := range items {
		if item.OwnerKind == ownerKind {
			if selectedIndex < len(reorderedSelected) {
				merged = append(merged, reorderedSelected[selectedIndex])
				selectedIndex++
			}
			continue
		}
		merged = append(merged, item)
	}
	return merged
}

func reorderItems(items []TodoItem, orderedIDs []string) []TodoItem {
	if len(items) == 0 {
		return items
	}
	byID := make(map[string]TodoItem, len(items))
	for _, item := range items {
		byID[strings.TrimSpace(item.ID)] = item
	}
	out := make([]TodoItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, rawID := range orderedIDs {
		id := strings.TrimSpace(rawID)
		item, ok := byID[id]
		if !ok {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, item)
	}
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if _, exists := seen[id]; exists {
			continue
		}
		out = append(out, item)
	}
	return out
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func listOwnerKind(options []ListOptions) string {
	if len(options) == 0 {
		return ""
	}
	return normalizeOptionalOwnerKind(options[0].OwnerKind)
}

func normalizeOptionalOwnerKind(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return pebblestore.NormalizeWorkspaceTodoOwnerKind(raw)
}

func filterItemsByOwnerKind(items []TodoItem, ownerKind string) []TodoItem {
	ownerKind = normalizeOptionalOwnerKind(ownerKind)
	if ownerKind == "" {
		return append([]TodoItem(nil), items...)
	}
	filtered := make([]TodoItem, 0, len(items))
	for _, item := range items {
		if item.OwnerKind == ownerKind {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filteredItemsForOwner(items []TodoItem, ownerKind string) []TodoItem {
	return filterItemsByOwnerKind(items, ownerKind)
}

func summarizeForOwner(items []TodoItem, ownerKind string) TodoSummary {
	ownerKind = normalizeOptionalOwnerKind(ownerKind)
	summary := pebblestore.SummarizeWorkspaceTodos(items)
	if ownerKind == "" {
		return summary
	}
	ownerSummary := pebblestore.WorkspaceTodoSummaryForOwner(summary, ownerKind)
	return TodoSummary{
		TaskCount:       ownerSummary.TaskCount,
		OpenCount:       ownerSummary.OpenCount,
		InProgressCount: ownerSummary.InProgressCount,
		User:            summary.User,
		Agent:           summary.Agent,
	}
}

func clearInProgressForOwner(items []TodoItem, ownerKind, exceptID string) {
	ownerKind = pebblestore.NormalizeWorkspaceTodoOwnerKind(ownerKind)
	if ownerKind != pebblestore.WorkspaceTodoOwnerKindAgent {
		return
	}
	exceptID = strings.TrimSpace(exceptID)
	for i := range items {
		if items[i].OwnerKind != ownerKind {
			continue
		}
		if exceptID != "" && strings.TrimSpace(items[i].ID) == exceptID {
			continue
		}
		if !items[i].InProgress {
			continue
		}
		items[i].InProgress = false
		items[i].UpdatedAt = time.Now().UnixMilli()
	}
}

func batchReturnOwnerKind(ownerKinds map[string]struct{}) string {
	if len(ownerKinds) != 1 {
		return ""
	}
	for ownerKind := range ownerKinds {
		return ownerKind
	}
	return ""
}
