package pebblestore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	WorkspaceTodoOwnerKindUser  = "user"
	WorkspaceTodoOwnerKindAgent = "agent"

	WorkspaceTodoAIStateQueued     = "queued"
	WorkspaceTodoAIStatePreparing  = "preparing"
	WorkspaceTodoAIStateInProgress = "in_progress"
	WorkspaceTodoAIStateFailed     = "failed"
)

type WorkspaceTodoOwnerSummary struct {
	TaskCount       int `json:"task_count"`
	OpenCount       int `json:"open_count"`
	InProgressCount int `json:"in_progress_count"`
}

type WorkspaceTodoItem struct {
	ID                   string   `json:"id"`
	WorkspacePath        string   `json:"workspace_path"`
	OwnerKind            string   `json:"owner_kind"`
	Text                 string   `json:"text"`
	Done                 bool     `json:"done"`
	Priority             string   `json:"priority,omitempty"`
	Group                string   `json:"group,omitempty"`
	Tags                 []string `json:"tags,omitempty"`
	InProgress           bool     `json:"in_progress,omitempty"`
	SessionID            string   `json:"session_id,omitempty"`
	ParentID             string   `json:"parent_id,omitempty"`
	AIState              string   `json:"ai_state,omitempty"`
	AIMode               string   `json:"ai_mode,omitempty"`
	AIWorktree           bool     `json:"ai_worktree,omitempty"`
	AIRequest            string   `json:"ai_request,omitempty"`
	AIError              string   `json:"ai_error,omitempty"`
	ManagedSessionID     string   `json:"managed_session_id,omitempty"`
	AccountScopeID       string   `json:"account_scope_id,omitempty"`
	UserID               string   `json:"user_id,omitempty"`
	WorkspaceID          string   `json:"workspace_id,omitempty"`
	OriginSessionID      string   `json:"origin_session_id,omitempty"`
	PreparationSessionID string   `json:"preparation_session_id,omitempty"`
	PreparationRunID     string   `json:"preparation_run_id,omitempty"`
	PreparationAttemptID string   `json:"preparation_attempt_id,omitempty"`
	FinalRunID           string   `json:"final_run_id,omitempty"`
	AIIdempotencyKeyHash string   `json:"ai_idempotency_key_hash,omitempty"`
	AIRequestHash        string   `json:"ai_request_hash,omitempty"`
	AIStateVersion       uint64   `json:"ai_state_version,omitempty"`
	AIClaimedAt          int64    `json:"ai_claimed_at,omitempty"`
	SortIndex            int      `json:"sort_index"`
	CreatedAt            int64    `json:"created_at"`
	UpdatedAt            int64    `json:"updated_at"`
	CompletedAt          int64    `json:"completed_at,omitempty"`
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
	mu    sync.Mutex
}

type AITaskAuditRecord struct {
	AccountScopeID       string `json:"account_scope_id"`
	TaskID               string `json:"task_id"`
	StageKey             string `json:"stage_key"`
	Stage                string `json:"stage"`
	State                string `json:"state"`
	StateVersion         uint64 `json:"state_version"`
	AttemptID            string `json:"attempt_id,omitempty"`
	PreparationSessionID string `json:"preparation_session_id,omitempty"`
	PreparationRunID     string `json:"preparation_run_id,omitempty"`
	FinalSessionID       string `json:"final_session_id,omitempty"`
	FinalRunID           string `json:"final_run_id,omitempty"`
	Disposition          string `json:"disposition,omitempty"`
	Provider             string `json:"provider,omitempty"`
	Model                string `json:"model,omitempty"`
	Thinking             string `json:"thinking,omitempty"`
	ServiceTier          string `json:"service_tier,omitempty"`
	Error                string `json:"error,omitempty"`
	CreatedAt            int64  `json:"created_at"`
}

type CreateAITaskStoreInput struct {
	Item            WorkspaceTodoItem
	IdempotencyHash string
	RequestHash     string
	Audit           AITaskAuditRecord
}

type AITaskTransitionStoreInput struct {
	AccountScopeID       string
	WorkspacePath        string
	TaskID               string
	ExpectedState        string
	ExpectedVersion      uint64
	NextState            string
	Mode                 string
	Worktree             bool
	ManagedSessionID     string
	FinalRunID           string
	PreparationSessionID string
	PreparationRunID     string
	PreparationAttemptID string
	Error                string
	ClaimedAt            int64
	Audit                AITaskAuditRecord
}

func NewWorkspaceTodoStore(store *Store) *WorkspaceTodoStore {
	return &WorkspaceTodoStore{store: store}
}

func (s *WorkspaceTodoStore) GetForAccount(accountScopeID, workspacePath, itemID string) (WorkspaceTodoItem, bool, error) {
	var item WorkspaceTodoItem
	ok, err := s.store.GetJSON(KeyWorkspaceTodoItemForAccount(accountScopeID, workspacePath, itemID), &item)
	if err != nil || !ok {
		return WorkspaceTodoItem{}, ok, err
	}
	item = normalizeWorkspaceTodoItem(item)
	if item.AccountScopeID != strings.TrimSpace(accountScopeID) {
		return WorkspaceTodoItem{}, false, nil
	}
	return item, true, nil
}

func (s *WorkspaceTodoStore) SaveForAccount(accountScopeID string, item WorkspaceTodoItem) (WorkspaceTodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveForAccountLocked(accountScopeID, item)
}

func (s *WorkspaceTodoStore) saveForAccountLocked(accountScopeID string, item WorkspaceTodoItem) (WorkspaceTodoItem, error) {
	item.AccountScopeID = strings.TrimSpace(accountScopeID)
	item = normalizeWorkspaceTodoItem(item)
	if item.AccountScopeID == "" {
		return WorkspaceTodoItem{}, fmt.Errorf("account scope id is required")
	}
	if item.ID == "" || item.WorkspacePath == "" || item.Text == "" {
		return WorkspaceTodoItem{}, fmt.Errorf("todo id, workspace path, and text are required")
	}
	var current WorkspaceTodoItem
	if ok, err := s.store.GetJSON(KeyWorkspaceTodoItemForAccount(item.AccountScopeID, item.WorkspacePath, item.ID), &current); err != nil {
		return WorkspaceTodoItem{}, err
	} else if ok {
		item = mergeAITaskAuthority(item, normalizeWorkspaceTodoItem(current))
	}
	if err := s.store.PutJSON(KeyWorkspaceTodoItemForAccount(item.AccountScopeID, item.WorkspacePath, item.ID), item); err != nil {
		return WorkspaceTodoItem{}, err
	}
	return item, nil
}

func (s *WorkspaceTodoStore) ListForAccount(accountScopeID, workspacePath string, limit int) ([]WorkspaceTodoItem, error) {
	accountScopeID, workspacePath = strings.TrimSpace(accountScopeID), strings.TrimSpace(workspacePath)
	if accountScopeID == "" || workspacePath == "" {
		return nil, fmt.Errorf("account scope id and workspace path are required")
	}
	if limit <= 0 {
		limit = 100000
	}
	items := make([]WorkspaceTodoItem, 0, minWorkspaceTodoInt(limit, 256))
	err := s.store.IteratePrefix(WorkspaceTodoPrefixForAccount(accountScopeID, workspacePath), limit, func(_ string, value []byte) error {
		var item WorkspaceTodoItem
		if err := json.Unmarshal(value, &item); err != nil {
			return err
		}
		item = normalizeWorkspaceTodoItem(item)
		if item.ID != "" && item.AccountScopeID == accountScopeID {
			items = append(items, item)
		}
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

func (s *WorkspaceTodoStore) CreateAITask(input CreateAITaskStoreInput) (WorkspaceTodoItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := normalizeWorkspaceTodoItem(input.Item)
	accountScopeID := strings.TrimSpace(item.AccountScopeID)
	if accountScopeID == "" || item.WorkspacePath == "" || item.ID == "" || input.IdempotencyHash == "" || input.RequestHash == "" {
		return WorkspaceTodoItem{}, false, fmt.Errorf("complete AI task authority and idempotency data are required")
	}
	indexKey := KeyAITaskIdempotencyForAccount(accountScopeID, item.WorkspacePath, input.IdempotencyHash)
	var reservation struct {
		TaskID      string `json:"task_id"`
		RequestHash string `json:"request_hash"`
	}
	if ok, err := s.store.GetJSON(indexKey, &reservation); err != nil {
		return WorkspaceTodoItem{}, false, err
	} else if ok {
		if reservation.RequestHash != input.RequestHash {
			return WorkspaceTodoItem{}, false, fmt.Errorf("idempotency key conflicts with a different AI task request")
		}
		existing, found, err := s.GetForAccount(accountScopeID, item.WorkspacePath, reservation.TaskID)
		if err != nil {
			return WorkspaceTodoItem{}, false, err
		}
		if !found {
			return WorkspaceTodoItem{}, false, fmt.Errorf("AI task idempotency reservation is missing task %q", reservation.TaskID)
		}
		replay := input.Audit
		replay.AccountScopeID, replay.TaskID, replay.State, replay.StateVersion, replay.StageKey, replay.Stage, replay.Disposition = accountScopeID, existing.ID, existing.AIState, existing.AIStateVersion, "000001_replayed", "replayed", "replayed"
		raw, err := json.Marshal(replay)
		if err != nil {
			return WorkspaceTodoItem{}, false, err
		}
		if err := s.store.PutBytes(KeyAITaskAuditForAccount(accountScopeID, existing.ID, replay.StageKey), raw); err != nil {
			return WorkspaceTodoItem{}, false, err
		}
		return existing, true, nil
	}
	item.AIIdempotencyKeyHash, item.AIRequestHash, item.AIStateVersion = input.IdempotencyHash, input.RequestHash, 1
	input.Audit.AccountScopeID, input.Audit.TaskID = accountScopeID, item.ID
	input.Audit.State, input.Audit.StateVersion = item.AIState, item.AIStateVersion
	payload, err := json.Marshal(item)
	if err != nil {
		return WorkspaceTodoItem{}, false, err
	}
	reservationPayload, err := json.Marshal(struct {
		TaskID      string `json:"task_id"`
		RequestHash string `json:"request_hash"`
	}{item.ID, input.RequestHash})
	if err != nil {
		return WorkspaceTodoItem{}, false, err
	}
	auditPayload, err := json.Marshal(input.Audit)
	if err != nil {
		return WorkspaceTodoItem{}, false, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyWorkspaceTodoItemForAccount(accountScopeID, item.WorkspacePath, item.ID)), payload, nil); err != nil {
		return WorkspaceTodoItem{}, false, err
	}
	if err := batch.Set([]byte(indexKey), reservationPayload, nil); err != nil {
		return WorkspaceTodoItem{}, false, err
	}
	if err := batch.Set([]byte(KeyAITaskAuditForAccount(accountScopeID, item.ID, input.Audit.StageKey)), auditPayload, nil); err != nil {
		return WorkspaceTodoItem{}, false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return WorkspaceTodoItem{}, false, err
	}
	return item, false, nil
}

func (s *WorkspaceTodoStore) TransitionAITask(input AITaskTransitionStoreInput) (WorkspaceTodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok, err := s.GetForAccount(input.AccountScopeID, input.WorkspacePath, input.TaskID)
	if err != nil {
		return WorkspaceTodoItem{}, err
	}
	if !ok {
		return WorkspaceTodoItem{}, fmt.Errorf("AI task %q not found", input.TaskID)
	}
	if item.AIState != NormalizeWorkspaceTodoAIState(input.ExpectedState) {
		return WorkspaceTodoItem{}, fmt.Errorf("AI task %q state is %q, expected %q", item.ID, item.AIState, input.ExpectedState)
	}
	if input.ExpectedVersion != 0 && item.AIStateVersion != input.ExpectedVersion {
		return WorkspaceTodoItem{}, fmt.Errorf("AI task %q version is %d, expected %d", item.ID, item.AIStateVersion, input.ExpectedVersion)
	}
	item.AIState = NormalizeWorkspaceTodoAIState(input.NextState)
	item.AIStateVersion++
	item.AIMode, item.AIWorktree = strings.ToLower(strings.TrimSpace(input.Mode)), input.Worktree
	if value := strings.TrimSpace(input.ManagedSessionID); value != "" {
		item.ManagedSessionID = value
	}
	if value := strings.TrimSpace(input.FinalRunID); value != "" {
		item.FinalRunID = value
	}
	if value := strings.TrimSpace(input.PreparationSessionID); value != "" {
		item.PreparationSessionID = value
	}
	if value := strings.TrimSpace(input.PreparationRunID); value != "" {
		item.PreparationRunID = value
	}
	if value := strings.TrimSpace(input.PreparationAttemptID); value != "" {
		item.PreparationAttemptID = value
	}
	item.AIError, item.AIClaimedAt, item.UpdatedAt = strings.TrimSpace(input.Error), input.ClaimedAt, time.Now().UnixMilli()
	item.InProgress = item.AIState == WorkspaceTodoAIStateInProgress
	input.Audit.AccountScopeID, input.Audit.TaskID = item.AccountScopeID, item.ID
	input.Audit.State, input.Audit.StateVersion = item.AIState, item.AIStateVersion
	input.Audit.AttemptID, input.Audit.PreparationSessionID, input.Audit.PreparationRunID = item.PreparationAttemptID, item.PreparationSessionID, item.PreparationRunID
	input.Audit.FinalSessionID, input.Audit.FinalRunID, input.Audit.Error = item.ManagedSessionID, item.FinalRunID, item.AIError
	payload, err := json.Marshal(item)
	if err != nil {
		return WorkspaceTodoItem{}, err
	}
	auditPayload, err := json.Marshal(input.Audit)
	if err != nil {
		return WorkspaceTodoItem{}, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyWorkspaceTodoItemForAccount(item.AccountScopeID, item.WorkspacePath, item.ID)), payload, nil); err != nil {
		return WorkspaceTodoItem{}, err
	}
	if err := batch.Set([]byte(KeyAITaskAuditForAccount(item.AccountScopeID, item.ID, input.Audit.StageKey)), auditPayload, nil); err != nil {
		return WorkspaceTodoItem{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return WorkspaceTodoItem{}, err
	}
	return item, nil
}

func (s *WorkspaceTodoStore) ListAITaskAccounts(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1000
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	err := s.store.IteratePrefix(KeyWorkspaceTodoItemAccountPrefix, limit*100, func(_ string, value []byte) error {
		var item WorkspaceTodoItem
		if err := json.Unmarshal(value, &item); err != nil {
			return err
		}
		account := strings.TrimSpace(item.AccountScopeID)
		if account != "" && item.AIState != "" {
			if _, ok := seen[account]; !ok && len(out) < limit {
				seen[account] = struct{}{}
				out = append(out, account)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func (s *WorkspaceTodoStore) ListActiveAITasksForAccount(accountScopeID string, limit int) ([]WorkspaceTodoItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	prefix := fmt.Sprintf("%s%s/", KeyWorkspaceTodoItemAccountPrefix, keyPart(accountScopeID))
	out := make([]WorkspaceTodoItem, 0)
	err := s.store.IteratePrefix(prefix, limit*10, func(_ string, value []byte) error {
		var item WorkspaceTodoItem
		if err := json.Unmarshal(value, &item); err != nil {
			return err
		}
		item = normalizeWorkspaceTodoItem(item)
		if item.AccountScopeID == strings.TrimSpace(accountScopeID) && (item.AIState == WorkspaceTodoAIStateQueued || item.AIState == WorkspaceTodoAIStatePreparing) && len(out) < limit {
			out = append(out, item)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt < out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, err
}

func (s *WorkspaceTodoStore) TerminalizeLegacyActiveAITasks(limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	count := 0
	err := s.store.IteratePrefix(KeyWorkspaceTodoItemPrefix, limit, func(key string, value []byte) error {
		var item WorkspaceTodoItem
		if err := json.Unmarshal(value, &item); err != nil {
			return err
		}
		item = normalizeWorkspaceTodoItem(item)
		if item.AccountScopeID != "" || (item.AIState != WorkspaceTodoAIStateQueued && item.AIState != WorkspaceTodoAIStatePreparing) {
			return nil
		}
		item.AIState, item.AIError, item.InProgress, item.UpdatedAt = WorkspaceTodoAIStateFailed, "legacy AI task lacks trusted account/user/workspace ownership; resubmit the task", false, time.Now().UnixMilli()
		item.AIStateVersion++
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		audit := AITaskAuditRecord{AccountScopeID: "legacy-untrusted", TaskID: item.ID, StageKey: fmt.Sprintf("%06d_legacy_terminalized", item.AIStateVersion), Stage: "legacy_terminalized", State: item.AIState, StateVersion: item.AIStateVersion, Disposition: "terminalized", Error: item.AIError, CreatedAt: item.UpdatedAt}
		auditRaw, err := json.Marshal(audit)
		if err != nil {
			return err
		}
		batch := s.store.NewBatch()
		defer batch.Close()
		if err := batch.Set([]byte(key), raw, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyAITaskAuditForAccount(audit.AccountScopeID, item.ID, audit.StageKey)), auditRaw, nil); err != nil {
			return err
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

func (s *WorkspaceTodoStore) AppendAITaskAudit(accountScopeID, workspacePath, taskID string, record AITaskAuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok, err := s.GetForAccount(accountScopeID, workspacePath, taskID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("AI task %q not found", taskID)
	}
	record.AccountScopeID, record.TaskID = item.AccountScopeID, item.ID
	record.State, record.StateVersion = item.AIState, item.AIStateVersion
	record.AttemptID, record.PreparationSessionID, record.PreparationRunID = item.PreparationAttemptID, item.PreparationSessionID, item.PreparationRunID
	record.FinalSessionID, record.FinalRunID = item.ManagedSessionID, item.FinalRunID
	record.StageKey, record.Stage = strings.TrimSpace(record.StageKey), strings.TrimSpace(record.Stage)
	if record.StageKey == "" || record.Stage == "" {
		return fmt.Errorf("AI task audit stage key and stage are required")
	}
	if record.CreatedAt == 0 {
		record.CreatedAt = time.Now().UnixMilli()
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.store.PutBytes(KeyAITaskAuditForAccount(item.AccountScopeID, item.ID, record.StageKey), raw)
}

func (s *WorkspaceTodoStore) ListAITaskAudit(accountScopeID, taskID string, limit int) ([]AITaskAuditRecord, error) {
	if limit <= 0 {
		limit = 1000
	}
	out := make([]AITaskAuditRecord, 0)
	err := s.store.IteratePrefix(AITaskAuditPrefixForAccount(accountScopeID, taskID), limit, func(_ string, value []byte) error {
		var record AITaskAuditRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, record)
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].StageKey < out[j].StageKey
	})
	return out, err
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

func (s *WorkspaceTodoStore) ReplaceWorkspaceItemsForAccount(accountScopeID, workspacePath string, items []WorkspaceTodoItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.ListForAccount(accountScopeID, workspacePath, 100000)
	if err != nil {
		return err
	}
	incoming := make(map[string]WorkspaceTodoItem, len(items))
	for _, item := range items {
		item.AccountScopeID = strings.TrimSpace(accountScopeID)
		incoming[strings.TrimSpace(item.ID)] = item
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, existing := range current {
		if _, ok := incoming[existing.ID]; !ok {
			if existing.AIState == WorkspaceTodoAIStateQueued || existing.AIState == WorkspaceTodoAIStatePreparing || existing.AIState == WorkspaceTodoAIStateInProgress {
				incoming[existing.ID] = existing
				continue
			}
			if err := batch.Delete([]byte(KeyWorkspaceTodoItemForAccount(accountScopeID, workspacePath, existing.ID)), nil); err != nil {
				return err
			}
		}
	}
	for _, item := range incoming {
		var currentItem WorkspaceTodoItem
		if ok, err := getJSONFromReader(s.store.db, KeyWorkspaceTodoItemForAccount(accountScopeID, workspacePath, item.ID), &currentItem); err != nil {
			return err
		} else if ok {
			item = mergeAITaskAuthority(item, normalizeWorkspaceTodoItem(currentItem))
		}
		item = normalizeWorkspaceTodoItem(item)
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(KeyWorkspaceTodoItemForAccount(accountScopeID, workspacePath, item.ID)), raw, nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
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

func mergeAITaskAuthority(candidate, current WorkspaceTodoItem) WorkspaceTodoItem {
	if current.AIState == "" {
		return candidate
	}
	candidate.AccountScopeID, candidate.UserID, candidate.WorkspaceID, candidate.WorkspacePath = current.AccountScopeID, current.UserID, current.WorkspaceID, current.WorkspacePath
	candidate.OriginSessionID, candidate.AIState, candidate.AIMode, candidate.AIWorktree = current.OriginSessionID, current.AIState, current.AIMode, current.AIWorktree
	candidate.AIRequest, candidate.AIError, candidate.ManagedSessionID, candidate.FinalRunID = current.AIRequest, current.AIError, current.ManagedSessionID, current.FinalRunID
	candidate.PreparationSessionID, candidate.PreparationRunID, candidate.PreparationAttemptID = current.PreparationSessionID, current.PreparationRunID, current.PreparationAttemptID
	candidate.AIIdempotencyKeyHash, candidate.AIRequestHash, candidate.AIStateVersion, candidate.AIClaimedAt = current.AIIdempotencyKeyHash, current.AIRequestHash, current.AIStateVersion, current.AIClaimedAt
	candidate.InProgress = current.InProgress
	if current.UpdatedAt > candidate.UpdatedAt {
		candidate.UpdatedAt = current.UpdatedAt
	}
	return candidate
}

func normalizeWorkspaceTodoItem(item WorkspaceTodoItem) WorkspaceTodoItem {
	item.ID = strings.TrimSpace(item.ID)
	item.WorkspacePath = strings.TrimSpace(item.WorkspacePath)
	item.AccountScopeID = strings.TrimSpace(item.AccountScopeID)
	item.UserID = strings.TrimSpace(item.UserID)
	item.WorkspaceID = strings.TrimSpace(item.WorkspaceID)
	item.OriginSessionID = strings.TrimSpace(item.OriginSessionID)
	item.PreparationSessionID = strings.TrimSpace(item.PreparationSessionID)
	item.PreparationRunID = strings.TrimSpace(item.PreparationRunID)
	item.PreparationAttemptID = strings.TrimSpace(item.PreparationAttemptID)
	item.FinalRunID = strings.TrimSpace(item.FinalRunID)
	item.AIIdempotencyKeyHash = strings.TrimSpace(item.AIIdempotencyKeyHash)
	item.AIRequestHash = strings.TrimSpace(item.AIRequestHash)
	item.OwnerKind = NormalizeWorkspaceTodoOwnerKind(item.OwnerKind)
	item.Text = strings.TrimSpace(item.Text)
	item.Priority = normalizeWorkspaceTodoPriority(item.Priority)
	item.Group = strings.TrimSpace(item.Group)
	item.Tags = normalizeWorkspaceTodoTags(item.Tags)
	item.SessionID = strings.TrimSpace(item.SessionID)
	item.ParentID = strings.TrimSpace(item.ParentID)
	item.AIState = NormalizeWorkspaceTodoAIState(item.AIState)
	item.AIMode = strings.ToLower(strings.TrimSpace(item.AIMode))
	item.AIRequest = strings.TrimSpace(item.AIRequest)
	item.AIError = strings.TrimSpace(item.AIError)
	item.ManagedSessionID = strings.TrimSpace(item.ManagedSessionID)
	if item.OwnerKind == WorkspaceTodoOwnerKindAgent {
		item.Priority = "medium"
		item.AIState, item.AIMode, item.AIRequest, item.AIError, item.ManagedSessionID = "", "", "", "", ""
		item.AIWorktree = false
	} else {
		item.SessionID = ""
		item.ParentID = ""
		if item.AIState == "" {
			item.AIMode, item.AIRequest, item.AIError, item.ManagedSessionID = "", "", "", ""
			item.AIWorktree = false
		}
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

func NormalizeWorkspaceTodoAIState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case WorkspaceTodoAIStateQueued, WorkspaceTodoAIStatePreparing, WorkspaceTodoAIStateInProgress, WorkspaceTodoAIStateFailed:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
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
