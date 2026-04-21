package todo

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	streamPrefix                = "workspace_todo:"
	agentTodoSummaryMetadataKey = "agent_todo_summary"
)

type SessionMetadataStore interface {
	GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error)
	UpdateMetadata(sessionID string, metadata map[string]any) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error)
}

type Service struct {
	store    *pebblestore.WorkspaceTodoStore
	events   *pebblestore.EventLog
	publish  func(pebblestore.EventEnvelope)
	sessions SessionMetadataStore
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
	SessionID string
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

func NewService(store *pebblestore.WorkspaceTodoStore, events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope), sessions SessionMetadataStore) *Service {
	return &Service{store: store, events: events, publish: publish, sessions: sessions}
}

func (s *Service) List(workspacePath string, options ...ListOptions) ([]TodoItem, TodoSummary, error) {
	items, err := s.store.List(strings.TrimSpace(workspacePath), 100000)
	if err != nil {
		return nil, TodoSummary{}, err
	}
	resolved := firstListOptions(options)
	filtered := filterItemsByOptions(items, resolved)
	if hasListOptionsFilter(resolved) {
		return filtered, summarizeForOptions(items, resolved), nil
	}
	return filtered, pebblestore.SummarizeWorkspaceTodos(items), nil
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
	originalItems := append([]TodoItem(nil), items...)
	if input.InProgress {
		clearInProgressForScope(items, ownerKind, strings.TrimSpace(input.SessionID), "")
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
	if err := s.syncAgentTodoSessionMetadata(originalItems, items); err != nil {
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

func (s *Service) Update(input UpdateInput, options ...ListOptions) (TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
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
	originalItems := append([]TodoItem(nil), items...)
	resolved := firstListOptions(options)
	index := indexOfItemWithOptions(items, itemID, resolved)
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
			clearInProgressForScope(items, item.OwnerKind, item.SessionID, item.ID)
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
	if err := s.syncAgentTodoSessionMetadata(originalItems, items); err != nil {
		return TodoItem{}, TodoSummary{}, nil, err
	}
	updated, ok := findItem(items, item.ID)
	if !ok {
		return TodoItem{}, TodoSummary{}, nil, fmt.Errorf("updated todo not found")
	}
	summary := summarizeForOptions(items, resolved)
	fullSummary := pebblestore.SummarizeWorkspaceTodos(items)
	eventPayload := map[string]any{
		"workspace_path": workspacePath,
		"item":           updated,
		"summary":        fullSummary,
	}
	if hasListOptionsFilter(resolved) {
		eventPayload["owner_kind"] = resolved.OwnerKind
		eventPayload["session_id"] = resolved.SessionID
		eventPayload["filtered_summary"] = summary
	}
	event, err := s.appendEvent(workspacePath, "workspace.todo.updated", updated.ID, eventPayload)
	if err != nil {
		return TodoItem{}, TodoSummary{}, nil, err
	}
	return updated, summary, event, nil
}

func (s *Service) Delete(workspacePath, itemID string, options ...ListOptions) (TodoSummary, *pebblestore.EventEnvelope, error) {
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
	originalItems := append([]TodoItem(nil), items...)
	resolved := firstListOptions(options)
	index := indexOfItemWithOptions(items, itemID, resolved)
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
	if err := s.syncAgentTodoSessionMetadata(originalItems, items); err != nil {
		return TodoSummary{}, nil, err
	}
	summary := summarizeForOptions(items, resolved)
	fullSummary := pebblestore.SummarizeWorkspaceTodos(items)
	eventPayload := map[string]any{
		"workspace_path": workspacePath,
		"item_id":        itemID,
		"summary":        fullSummary,
	}
	if hasListOptionsFilter(resolved) {
		eventPayload["owner_kind"] = resolved.OwnerKind
		eventPayload["session_id"] = resolved.SessionID
		eventPayload["filtered_summary"] = summary
	}
	event, err := s.appendEvent(workspacePath, "workspace.todo.deleted", itemID, eventPayload)
	if err != nil {
		return TodoSummary{}, nil, err
	}
	return summary, event, nil
}

func (s *Service) DeleteDone(workspacePath string, options ...ListOptions) ([]TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	resolved := firstListOptions(options)
	return s.deleteMatching(workspacePath, "workspace.todo.deleted_done", func(item TodoItem) bool {
		return item.Done && itemMatchesListOptions(item, resolved)
	}, options...)
}

func (s *Service) DeleteAll(workspacePath string, options ...ListOptions) ([]TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	resolved := firstListOptions(options)
	return s.deleteMatching(workspacePath, "workspace.todo.deleted_all", func(item TodoItem) bool {
		return itemMatchesListOptions(item, resolved)
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
	originalItems := append([]TodoItem(nil), items...)
	resolved := firstListOptions(options)
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
	summary := summarizeForOptions(remaining, resolved)
	fullSummary := pebblestore.SummarizeWorkspaceTodos(remaining)
	if len(deletedIDs) == 0 {
		return filterItemsByOptions(remaining, resolved), summary, nil, nil
	}
	if err := s.store.ReplaceWorkspaceItems(workspacePath, remaining); err != nil {
		return nil, TodoSummary{}, nil, err
	}
	if err := s.syncAgentTodoSessionMetadata(originalItems, remaining); err != nil {
		return nil, TodoSummary{}, nil, err
	}
	eventPayload := map[string]any{
		"workspace_path":   workspacePath,
		"owner_kind":       resolved.OwnerKind,
		"deleted_ids":      deletedIDs,
		"deleted_count":    len(deletedIDs),
		"items":            filterItemsByOptions(remaining, resolved),
		"summary":          fullSummary,
		"filtered_summary": summary,
	}
	if strings.TrimSpace(resolved.SessionID) != "" {
		eventPayload["session_id"] = resolved.SessionID
	}
	event, err := s.appendEvent(workspacePath, eventType, workspacePath, eventPayload)
	if err != nil {
		return nil, TodoSummary{}, nil, err
	}
	return filterItemsByOptions(remaining, resolved), summary, event, nil
}

func (s *Service) Reorder(input ReorderInput, options ...ListOptions) ([]TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	workspacePath := strings.TrimSpace(input.WorkspacePath)
	if workspacePath == "" {
		return nil, TodoSummary{}, nil, fmt.Errorf("workspace path is required")
	}
	items, err := s.store.List(workspacePath, 100000)
	if err != nil {
		return nil, TodoSummary{}, nil, err
	}
	originalItems := append([]TodoItem(nil), items...)
	resolved := mergeListOptions(firstListOptions(options), ListOptions{OwnerKind: input.OwnerKind})
	ordered := reorderItemsForOptions(items, resolved, input.OrderedIDs)
	ordered = normalizeSortOrder(ordered)
	if err := s.persistItems(ordered); err != nil {
		return nil, TodoSummary{}, nil, err
	}
	if err := s.syncAgentTodoSessionMetadata(originalItems, ordered); err != nil {
		return nil, TodoSummary{}, nil, err
	}
	summary := summarizeForOptions(ordered, resolved)
	fullSummary := pebblestore.SummarizeWorkspaceTodos(ordered)
	eventPayload := map[string]any{
		"workspace_path":   workspacePath,
		"owner_kind":       resolved.OwnerKind,
		"items":            filterItemsByOptions(ordered, resolved),
		"summary":          fullSummary,
		"filtered_summary": summary,
	}
	if strings.TrimSpace(resolved.SessionID) != "" {
		eventPayload["session_id"] = resolved.SessionID
	}
	event, err := s.appendEvent(workspacePath, "workspace.todo.reordered", workspacePath, eventPayload)
	if err != nil {
		return nil, TodoSummary{}, nil, err
	}
	return filterItemsByOptions(ordered, resolved), summary, event, nil
}

func (s *Service) SetInProgress(workspacePath, itemID string, options ...ListOptions) (TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
	inProgress := true
	return s.Update(UpdateInput{WorkspacePath: workspacePath, ID: itemID, InProgress: &inProgress}, options...)
}

func (s *Service) ApplyBatch(workspacePath string, operations []BatchOperation, options ...ListOptions) ([]BatchResult, []TodoItem, TodoSummary, *pebblestore.EventEnvelope, error) {
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
	originalItems := append([]TodoItem(nil), items...)
	resolved := firstListOptions(options)
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
			effectiveOptions := mergeListOptions(resolved, ListOptions{OwnerKind: op.OwnerKind, SessionID: derefString(op.SessionID)})
			candidate := TodoItem{OwnerKind: pebblestore.NormalizeWorkspaceTodoOwnerKind(op.OwnerKind), SessionID: derefString(op.SessionID)}
			if hasListOptionsFilter(effectiveOptions) && !itemMatchesListOptions(candidate, effectiveOptions) {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("operation %d does not match list scope", idx)
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
				clearInProgressForScope(working, ownerKind, item.SessionID, "")
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
			effectiveOptions := mergeListOptions(resolved, ListOptions{OwnerKind: op.OwnerKind, SessionID: derefString(op.SessionID)})
			itemIndex := indexOfItemWithOptions(working, itemID, effectiveOptions)
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
					clearInProgressForScope(working, item.OwnerKind, item.SessionID, item.ID)
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
			effectiveOptions := mergeListOptions(resolved, ListOptions{OwnerKind: normalizedOwnerKind, SessionID: derefString(op.SessionID)})
			remaining := make([]TodoItem, 0, len(working))
			deletedCount := 0
			for _, item := range working {
				if item.Done && itemMatchesListOptions(item, effectiveOptions) {
					deletedCount++
					continue
				}
				remaining = append(remaining, item)
			}
			working = normalizeSortOrder(remaining)
			result.ID = fmt.Sprintf("deleted:%d", deletedCount)
			result.Items = filterItemsByOptions(working, effectiveOptions)
		case "delete_all":
			effectiveOptions := mergeListOptions(resolved, ListOptions{OwnerKind: normalizedOwnerKind, SessionID: derefString(op.SessionID)})
			remaining := make([]TodoItem, 0, len(working))
			deletedCount := 0
			for _, item := range working {
				if itemMatchesListOptions(item, effectiveOptions) {
					deletedCount++
					continue
				}
				remaining = append(remaining, item)
			}
			working = normalizeSortOrder(remaining)
			result.ID = fmt.Sprintf("deleted:%d", deletedCount)
			result.Items = filterItemsByOptions(working, effectiveOptions)
		case "delete":
			itemID := strings.TrimSpace(op.ID)
			if itemID == "" {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("operation %d id is required", idx)
			}
			effectiveOptions := mergeListOptions(resolved, ListOptions{OwnerKind: op.OwnerKind, SessionID: derefString(op.SessionID)})
			itemIndex := indexOfItemWithOptions(working, itemID, effectiveOptions)
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
			effectiveOptions := mergeListOptions(resolved, ListOptions{OwnerKind: normalizedOwnerKind, SessionID: derefString(op.SessionID)})
			working = reorderItemsForOptions(working, effectiveOptions, op.OrderedIDs)
			working = normalizeSortOrder(working)
			result.Items = filterItemsByOptions(working, effectiveOptions)
		case "in_progress":
			itemID := strings.TrimSpace(op.ID)
			if itemID == "" {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("operation %d id is required", idx)
			}
			effectiveOptions := mergeListOptions(resolved, ListOptions{OwnerKind: op.OwnerKind, SessionID: derefString(op.SessionID)})
			itemIndex := indexOfItemWithOptions(working, itemID, effectiveOptions)
			if itemIndex < 0 {
				return nil, nil, TodoSummary{}, nil, fmt.Errorf("todo %q not found", itemID)
			}
			clearInProgressForScope(working, working[itemIndex].OwnerKind, working[itemIndex].SessionID, itemID)
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
	if err := s.syncAgentTodoSessionMetadata(originalItems, working); err != nil {
		return nil, nil, TodoSummary{}, nil, err
	}
	summary := summarizeForOptions(working, resolved)
	fullSummary := pebblestore.SummarizeWorkspaceTodos(working)
	eventPayload := map[string]any{
		"workspace_path": workspacePath,
		"operations":     operations,
		"results":        results,
		"items":          filterItemsByOptions(working, resolved),
		"summary":        fullSummary,
	}
	if hasListOptionsFilter(resolved) {
		eventPayload["owner_kind"] = resolved.OwnerKind
		eventPayload["session_id"] = resolved.SessionID
		eventPayload["filtered_summary"] = summary
	}
	event, err := s.appendEvent(workspacePath, "workspace.todo.batch_applied", workspacePath, eventPayload)
	if err != nil {
		return nil, nil, TodoSummary{}, nil, err
	}
	returnOptions := resolved
	if strings.TrimSpace(returnOptions.OwnerKind) == "" {
		returnOptions.OwnerKind = batchReturnOwnerKind(ownerKindsUsed)
	}
	return results, filterItemsByOptions(working, returnOptions), summarizeForOptions(working, returnOptions), event, nil
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

func indexOfItemWithOptions(items []TodoItem, itemID string, options ListOptions) int {
	for i, item := range items {
		if strings.TrimSpace(item.ID) != strings.TrimSpace(itemID) {
			continue
		}
		if !itemMatchesListOptions(item, options) {
			continue
		}
		return i
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

func reorderItemsForOptions(items []TodoItem, options ListOptions, orderedIDs []string) []TodoItem {
	if len(items) == 0 || len(orderedIDs) == 0 {
		return items
	}
	if !hasListOptionsFilter(options) {
		return reorderItems(items, orderedIDs)
	}
	selected := make([]TodoItem, 0)
	for _, item := range items {
		if itemMatchesListOptions(item, options) {
			selected = append(selected, item)
		}
	}
	reorderedSelected := reorderItems(selected, orderedIDs)
	merged := make([]TodoItem, 0, len(items))
	selectedIndex := 0
	for _, item := range items {
		if itemMatchesListOptions(item, options) {
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

func firstListOptions(options []ListOptions) ListOptions {
	if len(options) == 0 {
		return ListOptions{}
	}
	return ListOptions{
		OwnerKind: normalizeOptionalOwnerKind(options[0].OwnerKind),
		SessionID: strings.TrimSpace(options[0].SessionID),
	}
}

func mergeListOptions(base, override ListOptions) ListOptions {
	merged := base
	if ownerKind := normalizeOptionalOwnerKind(override.OwnerKind); ownerKind != "" {
		merged.OwnerKind = ownerKind
	}
	if sessionID := strings.TrimSpace(override.SessionID); sessionID != "" {
		merged.SessionID = sessionID
	}
	return merged
}

func hasListOptionsFilter(options ListOptions) bool {
	return strings.TrimSpace(options.OwnerKind) != "" || strings.TrimSpace(options.SessionID) != ""
}

func itemMatchesListOptions(item TodoItem, options ListOptions) bool {
	if ownerKind := normalizeOptionalOwnerKind(options.OwnerKind); ownerKind != "" && item.OwnerKind != ownerKind {
		return false
	}
	if sessionID := strings.TrimSpace(options.SessionID); sessionID != "" && strings.TrimSpace(item.SessionID) != sessionID {
		return false
	}
	return true
}

func normalizeOptionalOwnerKind(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return pebblestore.NormalizeWorkspaceTodoOwnerKind(raw)
}

func filterItemsByOptions(items []TodoItem, options ListOptions) []TodoItem {
	filtered := make([]TodoItem, 0, len(items))
	for _, item := range items {
		if itemMatchesListOptions(item, options) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filteredItemsForOwner(items []TodoItem, ownerKind string) []TodoItem {
	return filterItemsByOptions(items, ListOptions{OwnerKind: ownerKind})
}

func summarizeForOptions(items []TodoItem, options ListOptions) TodoSummary {
	filtered := filterItemsByOptions(items, options)
	summary := pebblestore.SummarizeWorkspaceTodos(filtered)
	ownerKind := normalizeOptionalOwnerKind(options.OwnerKind)
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

func summarizeForOwner(items []TodoItem, ownerKind string) TodoSummary {
	return summarizeForOptions(items, ListOptions{OwnerKind: ownerKind})
}

func clearInProgressForScope(items []TodoItem, ownerKind, sessionID, exceptID string) {
	ownerKind = pebblestore.NormalizeWorkspaceTodoOwnerKind(ownerKind)
	if ownerKind != pebblestore.WorkspaceTodoOwnerKindAgent {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	exceptID = strings.TrimSpace(exceptID)
	for i := range items {
		if items[i].OwnerKind != ownerKind {
			continue
		}
		if sessionID != "" && strings.TrimSpace(items[i].SessionID) != sessionID {
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

func syncAgentSummaryMetadataMap(current map[string]any, summary TodoSummary) (map[string]any, bool) {
	next := cloneMetadataMap(current)
	if next == nil {
		next = make(map[string]any, 1)
	}
	serialized := map[string]any{
		"task_count":        summary.TaskCount,
		"open_count":        summary.OpenCount,
		"in_progress_count": summary.InProgressCount,
		"user": map[string]any{
			"task_count":        summary.User.TaskCount,
			"open_count":        summary.User.OpenCount,
			"in_progress_count": summary.User.InProgressCount,
		},
		"agent": map[string]any{
			"task_count":        summary.Agent.TaskCount,
			"open_count":        summary.Agent.OpenCount,
			"in_progress_count": summary.Agent.InProgressCount,
		},
	}
	if metadataEqual(next[agentTodoSummaryMetadataKey], serialized) {
		return next, false
	}
	next[agentTodoSummaryMetadataKey] = serialized
	return next, true
}

func clearAgentSummaryMetadataMap(current map[string]any) (map[string]any, bool) {
	if current == nil {
		return nil, false
	}
	if _, ok := current[agentTodoSummaryMetadataKey]; !ok {
		return cloneMetadataMap(current), false
	}
	next := cloneMetadataMap(current)
	delete(next, agentTodoSummaryMetadataKey)
	return next, true
}

func cloneMetadataMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = cloneMetadataValue(value)
	}
	return cloned
}

func cloneMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMetadataMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, entry := range typed {
			cloned[i] = cloneMetadataValue(entry)
		}
		return cloned
	default:
		return typed
	}
}

func metadataEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

func affectedAgentSessionIDs(before, after []TodoItem) []string {
	seen := make(map[string]struct{})
	for _, item := range before {
		if item.OwnerKind != pebblestore.WorkspaceTodoOwnerKindAgent {
			continue
		}
		sessionID := strings.TrimSpace(item.SessionID)
		if sessionID != "" {
			seen[sessionID] = struct{}{}
		}
	}
	for _, item := range after {
		if item.OwnerKind != pebblestore.WorkspaceTodoOwnerKindAgent {
			continue
		}
		sessionID := strings.TrimSpace(item.SessionID)
		if sessionID != "" {
			seen[sessionID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for sessionID := range seen {
		ids = append(ids, sessionID)
	}
	return ids
}

func (s *Service) syncAgentTodoSessionMetadata(before, after []TodoItem) error {
	if s == nil || s.sessions == nil {
		return nil
	}
	for _, sessionID := range affectedAgentSessionIDs(before, after) {
		summary := summarizeForOptions(after, ListOptions{OwnerKind: pebblestore.WorkspaceTodoOwnerKindAgent, SessionID: sessionID})
		session, ok, err := s.sessions.GetSession(sessionID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		metadata := cloneMetadataMap(session.Metadata)
		var nextMetadata map[string]any
		var changed bool
		if summary.Agent.TaskCount > 0 {
			nextMetadata, changed = syncAgentSummaryMetadataMap(metadata, summary)
		} else {
			nextMetadata, changed = clearAgentSummaryMetadataMap(metadata)
		}
		if !changed {
			continue
		}
		_, event, err := s.sessions.UpdateMetadata(sessionID, nextMetadata)
		if err != nil {
			return err
		}
		if event != nil && s.publish != nil {
			s.publish(*event)
		}
	}
	return nil
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
