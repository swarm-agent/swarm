package pebblestore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const legacyDefaultWorktreeBaseBranch = "main"

type WorktreeConfigRecord struct {
	Enabled          bool   `json:"enabled"`
	UseCurrentBranch *bool  `json:"use_current_branch,omitempty"`
	BaseBranch       string `json:"base_branch,omitempty"`
	BranchName       string `json:"branch_name,omitempty"`
	UpdatedAt        int64  `json:"updated_at"`
}

type WorktreeStore struct {
	store *Store
}

func NewWorktreeStore(store *Store) *WorktreeStore {
	return &WorktreeStore{store: store}
}

func (s *WorktreeStore) GetConfig(workspacePath string) (WorktreeConfigRecord, bool, error) {
	if s == nil || s.store == nil {
		return WorktreeConfigRecord{}, false, errors.New("worktree store is not configured")
	}
	workspacePath = normalizeWorktreeWorkspacePath(workspacePath)
	if workspacePath == "" {
		return WorktreeConfigRecord{}, false, errors.New("workspace path is required")
	}
	var record WorktreeConfigRecord
	ok, err := s.store.GetJSON(KeyWorktreeConfig(workspacePath), &record)
	if err != nil {
		return WorktreeConfigRecord{}, false, err
	}
	if !ok {
		return defaultWorktreeConfigRecord(), false, nil
	}
	return normalizeWorktreeConfigRecord(record), true, nil
}

func (s *WorktreeStore) SetConfig(workspacePath string, enabled, useCurrentBranch bool, baseBranch, branchName string) (WorktreeConfigRecord, error) {
	if s == nil || s.store == nil {
		return WorktreeConfigRecord{}, errors.New("worktree store is not configured")
	}
	workspacePath = normalizeWorktreeWorkspacePath(workspacePath)
	if workspacePath == "" {
		return WorktreeConfigRecord{}, errors.New("workspace path is required")
	}
	record := WorktreeConfigRecord{
		Enabled:          enabled,
		UseCurrentBranch: boolPtr(useCurrentBranch),
		BaseBranch:       normalizeWorktreeBaseBranch(baseBranch, useCurrentBranch),
		BranchName:       normalizeStoredWorktreeBranchName(branchName),
		UpdatedAt:        time.Now().UnixMilli(),
	}
	if err := s.store.PutJSON(KeyWorktreeConfig(workspacePath), record); err != nil {
		return WorktreeConfigRecord{}, err
	}
	return record, nil
}

func (s *WorktreeStore) GetLegacyGlobalConfig() (WorktreeConfigRecord, bool, error) {
	if s == nil || s.store == nil {
		return WorktreeConfigRecord{}, false, errors.New("worktree store is not configured")
	}
	var record WorktreeConfigRecord
	ok, err := s.store.GetJSON(KeyWorktreeGlobalConfig, &record)
	if err != nil {
		return WorktreeConfigRecord{}, false, err
	}
	if !ok {
		return WorktreeConfigRecord{}, false, nil
	}
	return normalizeWorktreeConfigRecord(record), true, nil
}

func (s *WorktreeStore) MigrateLegacyGlobalConfig(workspacePaths []string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("worktree store is not configured")
	}
	record, ok, err := s.GetLegacyGlobalConfig()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	unique := make([]string, 0, len(workspacePaths))
	seen := make(map[string]struct{}, len(workspacePaths))
	for _, workspacePath := range workspacePaths {
		normalized := normalizeWorktreeWorkspacePath(workspacePath)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		unique = append(unique, normalized)
	}
	if len(unique) == 0 {
		return false, nil
	}

	payload, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, workspacePath := range unique {
		if err := batch.Set([]byte(KeyWorktreeConfig(workspacePath)), payload, nil); err != nil {
			return false, err
		}
	}
	if err := batch.Delete([]byte(KeyWorktreeGlobalConfig), nil); err != nil {
		return false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return false, err
	}
	return true, nil
}

func defaultWorktreeConfigRecord() WorktreeConfigRecord {
	return WorktreeConfigRecord{
		Enabled:          false,
		UseCurrentBranch: boolPtr(true),
		BaseBranch:       "",
		BranchName:       "",
		UpdatedAt:        0,
	}
}

func normalizeWorktreeConfigRecord(record WorktreeConfigRecord) WorktreeConfigRecord {
	useCurrentBranch := resolveWorktreeUseCurrentBranch(record.UseCurrentBranch, record.BaseBranch)
	record.UseCurrentBranch = boolPtr(useCurrentBranch)
	record.BaseBranch = normalizeWorktreeBaseBranch(record.BaseBranch, useCurrentBranch)
	record.BranchName = normalizeStoredWorktreeBranchName(record.BranchName)
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func resolveWorktreeUseCurrentBranch(flag *bool, baseBranch string) bool {
	if flag != nil {
		return *flag
	}
	baseBranch = strings.TrimSpace(baseBranch)
	return baseBranch == "" || strings.EqualFold(baseBranch, legacyDefaultWorktreeBaseBranch)
}

func normalizeWorktreeBaseBranch(value string, useCurrentBranch bool) string {
	if useCurrentBranch {
		return ""
	}
	return strings.TrimSpace(value)
}

func normalizeStoredWorktreeBranchName(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	if strings.EqualFold(trimmed, "agent/<id>") {
		return "agent"
	}
	if strings.HasSuffix(trimmed, "/<id>") {
		trimmed = strings.TrimSuffix(trimmed, "/<id>")
		trimmed = strings.Trim(trimmed, "/")
	}
	if trimmed == "" || strings.EqualFold(trimmed, "agent") {
		return ""
	}
	return trimmed
}

func normalizeWorktreeWorkspacePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

func boolPtr(value bool) *bool {
	return &value
}
