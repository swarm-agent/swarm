package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	IntegrationAdapterTypeCLIWrapper       = "cli_wrapper"
	IntegrationAdapterTypeHostHTTPBridge   = "host_http_bridge"
	IntegrationAdapterTypeUnixSocketBridge = "unix_socket_bridge"
	IntegrationAdapterTypeHostedAPI        = "hosted_api"
	IntegrationPermissionModeAllow         = "allow"
	IntegrationPermissionModeAskBlocking   = "ask_blocking"
	IntegrationPermissionModeAskAsync      = "ask_async"
	IntegrationPermissionModeDeny          = "deny"
	IntegrationVersionStatusDraft          = "draft"
	IntegrationVersionStatusPublished      = "published"
	IntegrationAssignmentStatusActive      = "active"
	IntegrationAssignmentStatusDisabled    = "disabled"
)

type IntegrationPackRecord struct {
	AccountScopeID  string            `json:"account_scope_id,omitempty"`
	PackID          string            `json:"pack_id"`
	Slug            string            `json:"slug,omitempty"`
	DisplayName     string            `json:"display_name"`
	Description     string            `json:"description,omitempty"`
	LatestVersionID string            `json:"latest_version_id,omitempty"`
	DraftVersionID  string            `json:"draft_version_id,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type IntegrationPackVersionRecord struct {
	AccountScopeID string            `json:"account_scope_id,omitempty"`
	PackID         string            `json:"pack_id"`
	VersionID      string            `json:"version_id"`
	Version        string            `json:"version,omitempty"`
	Status         string            `json:"status"`
	DisplayName    string            `json:"display_name,omitempty"`
	Description    string            `json:"description,omitempty"`
	ToolIDs        []string          `json:"tool_ids,omitempty"`
	AdapterIDs     []string          `json:"adapter_ids,omitempty"`
	PromptIDs      []string          `json:"prompt_ids,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type IntegrationToolRecord struct {
	AccountScopeID string            `json:"account_scope_id,omitempty"`
	PackID         string            `json:"pack_id"`
	VersionID      string            `json:"version_id"`
	ToolID         string            `json:"tool_id"`
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	AdapterID      string            `json:"adapter_id"`
	PermissionMode string            `json:"permission_mode"`
	InputSchema    json.RawMessage   `json:"input_schema,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type IntegrationAdapterRecord struct {
	AccountScopeID string            `json:"account_scope_id,omitempty"`
	PackID         string            `json:"pack_id"`
	VersionID      string            `json:"version_id"`
	AdapterID      string            `json:"adapter_id"`
	Type           string            `json:"type"`
	DisplayName    string            `json:"display_name,omitempty"`
	Settings       map[string]string `json:"settings,omitempty"`
	CredentialRefs map[string]string `json:"credential_refs,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type IntegrationPromptFragmentRecord struct {
	AccountScopeID string            `json:"account_scope_id,omitempty"`
	PackID         string            `json:"pack_id"`
	VersionID      string            `json:"version_id"`
	PromptID       string            `json:"prompt_id"`
	Title          string            `json:"title,omitempty"`
	Content        string            `json:"content"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type IntegrationAssignmentRecord struct {
	AccountScopeID string            `json:"account_scope_id,omitempty"`
	AssignmentID   string            `json:"assignment_id"`
	AgentName      string            `json:"agent_name"`
	PackID         string            `json:"pack_id"`
	VersionID      string            `json:"version_id"`
	Status         string            `json:"status"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type IntegrationWorkspaceRecord struct {
	AccountScopeID       string            `json:"account_scope_id,omitempty"`
	WorkspaceID          string            `json:"workspace_id"`
	DisplayName          string            `json:"display_name"`
	PackID               string            `json:"pack_id,omitempty"`
	DraftVersionID       string            `json:"draft_version_id,omitempty"`
	LatestChildSessionID string            `json:"latest_child_session_id,omitempty"`
	LatestChildSessionAt time.Time         `json:"latest_child_session_at,omitempty"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
}

type IntegrationWorkspaceSessionRecord struct {
	AccountScopeID string            `json:"account_scope_id,omitempty"`
	WorkspaceID    string            `json:"workspace_id"`
	SessionID      string            `json:"session_id"`
	Title          string            `json:"title,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type IntegrationStore struct {
	store *Store
}

func NewIntegrationStore(store *Store) *IntegrationStore {
	return &IntegrationStore{store: store}
}

func (s *IntegrationStore) PutPack(record IntegrationPackRecord) (IntegrationPackRecord, error) {
	if err := s.configured(); err != nil {
		return IntegrationPackRecord{}, err
	}
	now := time.Now().UTC()
	record = normalizeIntegrationPack(record)
	if record.PackID == "" {
		return IntegrationPackRecord{}, errors.New("pack_id is required")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	key := KeyIntegrationPack(record.PackID)
	if record.AccountScopeID != "" {
		key = KeyIntegrationPackForAccount(record.AccountScopeID, record.PackID)
	}
	if err := s.store.PutJSON(key, record); err != nil {
		return IntegrationPackRecord{}, err
	}
	return record, nil
}

func (s *IntegrationStore) GetPack(packID string) (IntegrationPackRecord, bool, error) {
	return s.GetPackForAccount("", packID)
}

func (s *IntegrationStore) GetPackForAccount(accountScopeID, packID string) (IntegrationPackRecord, bool, error) {
	if err := s.configured(); err != nil {
		return IntegrationPackRecord{}, false, err
	}
	key := KeyIntegrationPack(packID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationPackForAccount(accountScopeID, packID)
	}
	var record IntegrationPackRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil || !ok {
		return IntegrationPackRecord{}, ok, err
	}
	return normalizeIntegrationPack(record), true, nil
}

func (s *IntegrationStore) DeletePack(packID string) error {
	return s.DeletePackForAccount("", packID)
}

func (s *IntegrationStore) DeletePackForAccount(accountScopeID, packID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	packID = normalizeIntegrationID(packID)
	if packID == "" {
		return errors.New("pack_id is required")
	}
	key := KeyIntegrationPack(packID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationPackForAccount(accountScopeID, packID)
	}
	return s.store.Delete(key)
}

func (s *IntegrationStore) ListPacks(limit int) ([]IntegrationPackRecord, error) {
	return s.ListPacksForAccount("", limit)
}

func (s *IntegrationStore) ListPacksForAccount(accountScopeID string, limit int) ([]IntegrationPackRecord, error) {
	out, err := listIntegrationRecords(s, IntegrationPackPrefixForAccount(accountScopeID), limit, func(value []byte) (IntegrationPackRecord, error) {
		var record IntegrationPackRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return IntegrationPackRecord{}, fmt.Errorf("decode integration pack: %w", err)
		}
		return normalizeIntegrationPack(record), nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName) })
	return out, nil
}

func (s *IntegrationStore) PutPackVersion(record IntegrationPackVersionRecord) (IntegrationPackVersionRecord, error) {
	if err := s.configured(); err != nil {
		return IntegrationPackVersionRecord{}, err
	}
	now := time.Now().UTC()
	record = normalizeIntegrationPackVersion(record)
	if record.PackID == "" || record.VersionID == "" {
		return IntegrationPackVersionRecord{}, errors.New("pack_id and version_id are required")
	}
	if record.Status == "" {
		record.Status = IntegrationVersionStatusDraft
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	key := KeyIntegrationPackVersion(record.PackID, record.VersionID)
	if record.AccountScopeID != "" {
		key = KeyIntegrationPackVersionForAccount(record.AccountScopeID, record.PackID, record.VersionID)
	}
	if err := s.store.PutJSON(key, record); err != nil {
		return IntegrationPackVersionRecord{}, err
	}
	return record, nil
}

func (s *IntegrationStore) GetPackVersion(packID, versionID string) (IntegrationPackVersionRecord, bool, error) {
	return s.GetPackVersionForAccount("", packID, versionID)
}

func (s *IntegrationStore) GetPackVersionForAccount(accountScopeID, packID, versionID string) (IntegrationPackVersionRecord, bool, error) {
	if err := s.configured(); err != nil {
		return IntegrationPackVersionRecord{}, false, err
	}
	key := KeyIntegrationPackVersion(packID, versionID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationPackVersionForAccount(accountScopeID, packID, versionID)
	}
	var record IntegrationPackVersionRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil || !ok {
		return IntegrationPackVersionRecord{}, ok, err
	}
	return normalizeIntegrationPackVersion(record), true, nil
}

func (s *IntegrationStore) DeletePackVersion(packID, versionID string) error {
	return s.DeletePackVersionForAccount("", packID, versionID)
}

func (s *IntegrationStore) DeletePackVersionForAccount(accountScopeID, packID, versionID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	packID = normalizeIntegrationID(packID)
	versionID = normalizeIntegrationID(versionID)
	if packID == "" || versionID == "" {
		return errors.New("pack_id and version_id are required")
	}
	key := KeyIntegrationPackVersion(packID, versionID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationPackVersionForAccount(accountScopeID, packID, versionID)
	}
	return s.store.Delete(key)
}

func (s *IntegrationStore) ListPackVersions(packID string, limit int) ([]IntegrationPackVersionRecord, error) {
	return s.ListPackVersionsForAccount("", packID, limit)
}

func (s *IntegrationStore) ListPackVersionsForAccount(accountScopeID, packID string, limit int) ([]IntegrationPackVersionRecord, error) {
	out, err := listIntegrationRecords(s, IntegrationPackVersionPrefixForAccount(accountScopeID, packID), limit, func(value []byte) (IntegrationPackVersionRecord, error) {
		var record IntegrationPackVersionRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return IntegrationPackVersionRecord{}, fmt.Errorf("decode integration pack version: %w", err)
		}
		return normalizeIntegrationPackVersion(record), nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *IntegrationStore) PutTool(record IntegrationToolRecord) (IntegrationToolRecord, error) {
	if err := s.configured(); err != nil {
		return IntegrationToolRecord{}, err
	}
	now := time.Now().UTC()
	record = normalizeIntegrationTool(record)
	if record.PackID == "" || record.VersionID == "" || record.ToolID == "" {
		return IntegrationToolRecord{}, errors.New("pack_id, version_id, and tool_id are required")
	}
	if record.AdapterID == "" {
		return IntegrationToolRecord{}, errors.New("adapter_id is required")
	}
	if record.PermissionMode == "" {
		record.PermissionMode = IntegrationPermissionModeAskBlocking
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	key := KeyIntegrationTool(record.PackID, record.VersionID, record.ToolID)
	if record.AccountScopeID != "" {
		key = KeyIntegrationToolForAccount(record.AccountScopeID, record.PackID, record.VersionID, record.ToolID)
	}
	if err := s.store.PutJSON(key, record); err != nil {
		return IntegrationToolRecord{}, err
	}
	return record, nil
}

func (s *IntegrationStore) GetTool(packID, versionID, toolID string) (IntegrationToolRecord, bool, error) {
	return s.GetToolForAccount("", packID, versionID, toolID)
}

func (s *IntegrationStore) GetToolForAccount(accountScopeID, packID, versionID, toolID string) (IntegrationToolRecord, bool, error) {
	if err := s.configured(); err != nil {
		return IntegrationToolRecord{}, false, err
	}
	key := KeyIntegrationTool(packID, versionID, toolID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationToolForAccount(accountScopeID, packID, versionID, toolID)
	}
	var record IntegrationToolRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil || !ok {
		return IntegrationToolRecord{}, ok, err
	}
	return normalizeIntegrationTool(record), true, nil
}

func (s *IntegrationStore) DeleteTool(packID, versionID, toolID string) error {
	return s.DeleteToolForAccount("", packID, versionID, toolID)
}

func (s *IntegrationStore) DeleteToolForAccount(accountScopeID, packID, versionID, toolID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	packID = normalizeIntegrationID(packID)
	versionID = normalizeIntegrationID(versionID)
	toolID = normalizeIntegrationID(toolID)
	if packID == "" || versionID == "" || toolID == "" {
		return errors.New("pack_id, version_id, and tool_id are required")
	}
	key := KeyIntegrationTool(packID, versionID, toolID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationToolForAccount(accountScopeID, packID, versionID, toolID)
	}
	return s.store.Delete(key)
}

func (s *IntegrationStore) ListTools(packID, versionID string, limit int) ([]IntegrationToolRecord, error) {
	return s.ListToolsForAccount("", packID, versionID, limit)
}

func (s *IntegrationStore) ListToolsForAccount(accountScopeID, packID, versionID string, limit int) ([]IntegrationToolRecord, error) {
	out, err := listIntegrationRecords(s, IntegrationToolPrefixForAccount(accountScopeID, packID, versionID), limit, func(value []byte) (IntegrationToolRecord, error) {
		var record IntegrationToolRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return IntegrationToolRecord{}, fmt.Errorf("decode integration tool: %w", err)
		}
		return normalizeIntegrationTool(record), nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ToolID < out[j].ToolID })
	return out, nil
}

func (s *IntegrationStore) PutAdapter(record IntegrationAdapterRecord) (IntegrationAdapterRecord, error) {
	if err := s.configured(); err != nil {
		return IntegrationAdapterRecord{}, err
	}
	now := time.Now().UTC()
	record = normalizeIntegrationAdapter(record)
	if record.PackID == "" || record.VersionID == "" || record.AdapterID == "" {
		return IntegrationAdapterRecord{}, errors.New("pack_id, version_id, and adapter_id are required")
	}
	if record.Type == "" {
		return IntegrationAdapterRecord{}, errors.New("adapter type is required")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	key := KeyIntegrationAdapter(record.PackID, record.VersionID, record.AdapterID)
	if record.AccountScopeID != "" {
		key = KeyIntegrationAdapterForAccount(record.AccountScopeID, record.PackID, record.VersionID, record.AdapterID)
	}
	if err := s.store.PutJSON(key, record); err != nil {
		return IntegrationAdapterRecord{}, err
	}
	return record, nil
}

func (s *IntegrationStore) GetAdapter(packID, versionID, adapterID string) (IntegrationAdapterRecord, bool, error) {
	return s.GetAdapterForAccount("", packID, versionID, adapterID)
}

func (s *IntegrationStore) GetAdapterForAccount(accountScopeID, packID, versionID, adapterID string) (IntegrationAdapterRecord, bool, error) {
	if err := s.configured(); err != nil {
		return IntegrationAdapterRecord{}, false, err
	}
	key := KeyIntegrationAdapter(packID, versionID, adapterID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationAdapterForAccount(accountScopeID, packID, versionID, adapterID)
	}
	var record IntegrationAdapterRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil || !ok {
		return IntegrationAdapterRecord{}, ok, err
	}
	return normalizeIntegrationAdapter(record), true, nil
}

func (s *IntegrationStore) DeleteAdapter(packID, versionID, adapterID string) error {
	return s.DeleteAdapterForAccount("", packID, versionID, adapterID)
}

func (s *IntegrationStore) DeleteAdapterForAccount(accountScopeID, packID, versionID, adapterID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	packID = normalizeIntegrationID(packID)
	versionID = normalizeIntegrationID(versionID)
	adapterID = normalizeIntegrationID(adapterID)
	if packID == "" || versionID == "" || adapterID == "" {
		return errors.New("pack_id, version_id, and adapter_id are required")
	}
	key := KeyIntegrationAdapter(packID, versionID, adapterID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationAdapterForAccount(accountScopeID, packID, versionID, adapterID)
	}
	return s.store.Delete(key)
}

func (s *IntegrationStore) ListAdapters(packID, versionID string, limit int) ([]IntegrationAdapterRecord, error) {
	return s.ListAdaptersForAccount("", packID, versionID, limit)
}

func (s *IntegrationStore) ListAdaptersForAccount(accountScopeID, packID, versionID string, limit int) ([]IntegrationAdapterRecord, error) {
	out, err := listIntegrationRecords(s, IntegrationAdapterPrefixForAccount(accountScopeID, packID, versionID), limit, func(value []byte) (IntegrationAdapterRecord, error) {
		var record IntegrationAdapterRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return IntegrationAdapterRecord{}, fmt.Errorf("decode integration adapter: %w", err)
		}
		return normalizeIntegrationAdapter(record), nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AdapterID < out[j].AdapterID })
	return out, nil
}

func (s *IntegrationStore) PutPromptFragment(record IntegrationPromptFragmentRecord) (IntegrationPromptFragmentRecord, error) {
	if err := s.configured(); err != nil {
		return IntegrationPromptFragmentRecord{}, err
	}
	now := time.Now().UTC()
	record = normalizeIntegrationPromptFragment(record)
	if record.PackID == "" || record.VersionID == "" || record.PromptID == "" {
		return IntegrationPromptFragmentRecord{}, errors.New("pack_id, version_id, and prompt_id are required")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	key := KeyIntegrationPromptFragment(record.PackID, record.VersionID, record.PromptID)
	if record.AccountScopeID != "" {
		key = KeyIntegrationPromptFragmentForAccount(record.AccountScopeID, record.PackID, record.VersionID, record.PromptID)
	}
	if err := s.store.PutJSON(key, record); err != nil {
		return IntegrationPromptFragmentRecord{}, err
	}
	return record, nil
}

func (s *IntegrationStore) GetPromptFragment(packID, versionID, promptID string) (IntegrationPromptFragmentRecord, bool, error) {
	return s.GetPromptFragmentForAccount("", packID, versionID, promptID)
}

func (s *IntegrationStore) GetPromptFragmentForAccount(accountScopeID, packID, versionID, promptID string) (IntegrationPromptFragmentRecord, bool, error) {
	if err := s.configured(); err != nil {
		return IntegrationPromptFragmentRecord{}, false, err
	}
	key := KeyIntegrationPromptFragment(packID, versionID, promptID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationPromptFragmentForAccount(accountScopeID, packID, versionID, promptID)
	}
	var record IntegrationPromptFragmentRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil || !ok {
		return IntegrationPromptFragmentRecord{}, ok, err
	}
	return normalizeIntegrationPromptFragment(record), true, nil
}

func (s *IntegrationStore) DeletePromptFragment(packID, versionID, promptID string) error {
	return s.DeletePromptFragmentForAccount("", packID, versionID, promptID)
}

func (s *IntegrationStore) DeletePromptFragmentForAccount(accountScopeID, packID, versionID, promptID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	packID = normalizeIntegrationID(packID)
	versionID = normalizeIntegrationID(versionID)
	promptID = normalizeIntegrationID(promptID)
	if packID == "" || versionID == "" || promptID == "" {
		return errors.New("pack_id, version_id, and prompt_id are required")
	}
	key := KeyIntegrationPromptFragment(packID, versionID, promptID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationPromptFragmentForAccount(accountScopeID, packID, versionID, promptID)
	}
	return s.store.Delete(key)
}

func (s *IntegrationStore) ListPromptFragments(packID, versionID string, limit int) ([]IntegrationPromptFragmentRecord, error) {
	return s.ListPromptFragmentsForAccount("", packID, versionID, limit)
}

func (s *IntegrationStore) ListPromptFragmentsForAccount(accountScopeID, packID, versionID string, limit int) ([]IntegrationPromptFragmentRecord, error) {
	out, err := listIntegrationRecords(s, IntegrationPromptFragmentPrefixForAccount(accountScopeID, packID, versionID), limit, func(value []byte) (IntegrationPromptFragmentRecord, error) {
		var record IntegrationPromptFragmentRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return IntegrationPromptFragmentRecord{}, fmt.Errorf("decode integration prompt fragment: %w", err)
		}
		return normalizeIntegrationPromptFragment(record), nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PromptID < out[j].PromptID })
	return out, nil
}

func (s *IntegrationStore) PutAssignment(record IntegrationAssignmentRecord) (IntegrationAssignmentRecord, error) {
	if err := s.configured(); err != nil {
		return IntegrationAssignmentRecord{}, err
	}
	now := time.Now().UTC()
	record = normalizeIntegrationAssignment(record)
	if record.AssignmentID == "" || record.AgentName == "" || record.PackID == "" || record.VersionID == "" {
		return IntegrationAssignmentRecord{}, errors.New("assignment_id, agent_name, pack_id, and version_id are required")
	}
	if record.Status == "" {
		record.Status = IntegrationAssignmentStatusActive
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	payload, err := json.Marshal(record)
	if err != nil {
		return IntegrationAssignmentRecord{}, fmt.Errorf("marshal integration assignment: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if previous, ok, err := s.GetAssignmentForAccount(record.AccountScopeID, record.AssignmentID); err != nil {
		return IntegrationAssignmentRecord{}, err
	} else if ok {
		if err := deleteAssignmentIndexes(batch, previous); err != nil {
			return IntegrationAssignmentRecord{}, err
		}
	}
	recordKey := KeyIntegrationAssignment(record.AssignmentID)
	agentKey := KeyIntegrationAssignmentByAgent(record.AgentName, record.AssignmentID)
	packKey := KeyIntegrationAssignmentByPack(record.PackID, record.VersionID, record.AssignmentID)
	if record.AccountScopeID != "" {
		recordKey = KeyIntegrationAssignmentForAccount(record.AccountScopeID, record.AssignmentID)
		agentKey = KeyIntegrationAssignmentByAgentForAccount(record.AccountScopeID, record.AgentName, record.AssignmentID)
		packKey = KeyIntegrationAssignmentByPackForAccount(record.AccountScopeID, record.PackID, record.VersionID, record.AssignmentID)
	}
	if err := batch.Set([]byte(recordKey), payload, nil); err != nil {
		return IntegrationAssignmentRecord{}, fmt.Errorf("set integration assignment: %w", err)
	}
	if err := batch.Set([]byte(agentKey), []byte(recordKey), nil); err != nil {
		return IntegrationAssignmentRecord{}, fmt.Errorf("set integration assignment agent index: %w", err)
	}
	if err := batch.Set([]byte(packKey), []byte(recordKey), nil); err != nil {
		return IntegrationAssignmentRecord{}, fmt.Errorf("set integration assignment pack index: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return IntegrationAssignmentRecord{}, fmt.Errorf("commit integration assignment: %w", err)
	}
	return record, nil
}

func (s *IntegrationStore) GetAssignment(assignmentID string) (IntegrationAssignmentRecord, bool, error) {
	return s.GetAssignmentForAccount("", assignmentID)
}

func (s *IntegrationStore) GetAssignmentForAccount(accountScopeID, assignmentID string) (IntegrationAssignmentRecord, bool, error) {
	if err := s.configured(); err != nil {
		return IntegrationAssignmentRecord{}, false, err
	}
	key := KeyIntegrationAssignment(assignmentID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationAssignmentForAccount(accountScopeID, assignmentID)
	}
	var record IntegrationAssignmentRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil || !ok {
		return IntegrationAssignmentRecord{}, ok, err
	}
	return normalizeIntegrationAssignment(record), true, nil
}

func (s *IntegrationStore) ListAssignments(limit int) ([]IntegrationAssignmentRecord, error) {
	return s.ListAssignmentsForAccount("", limit)
}

func (s *IntegrationStore) ListAssignmentsForAccount(accountScopeID string, limit int) ([]IntegrationAssignmentRecord, error) {
	return s.listAssignmentsByIndex(IntegrationAssignmentPrefixForAccount(accountScopeID), limit, false)
}

func (s *IntegrationStore) ListAssignmentsByAgent(agentName string, limit int) ([]IntegrationAssignmentRecord, error) {
	return s.ListAssignmentsByAgentForAccount("", agentName, limit)
}

func (s *IntegrationStore) ListAssignmentsByAgentForAccount(accountScopeID, agentName string, limit int) ([]IntegrationAssignmentRecord, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return s.listAssignmentsByIndex(IntegrationAssignmentByAgentPrefix(agentName), limit, true)
	}
	return s.listAssignmentsByIndex(IntegrationAssignmentByAgentPrefixForAccount(accountScopeID, agentName), limit, true)
}

func (s *IntegrationStore) ListAssignmentsByPack(packID, versionID string, limit int) ([]IntegrationAssignmentRecord, error) {
	return s.ListAssignmentsByPackForAccount("", packID, versionID, limit)
}

func (s *IntegrationStore) ListAssignmentsByPackForAccount(accountScopeID, packID, versionID string, limit int) ([]IntegrationAssignmentRecord, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return s.listAssignmentsByIndex(IntegrationAssignmentByPackPrefix(packID, versionID), limit, true)
	}
	return s.listAssignmentsByIndex(IntegrationAssignmentByPackPrefixForAccount(accountScopeID, packID, versionID), limit, true)
}

func (s *IntegrationStore) DeleteAssignment(assignmentID string) error {
	return s.DeleteAssignmentForAccount("", assignmentID)
}

func (s *IntegrationStore) DeleteAssignmentForAccount(accountScopeID, assignmentID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	assignment, ok, err := s.GetAssignmentForAccount(accountScopeID, assignmentID)
	if err != nil || !ok {
		return err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	assignmentKey := KeyIntegrationAssignment(assignmentID)
	if strings.TrimSpace(accountScopeID) != "" {
		assignmentKey = KeyIntegrationAssignmentForAccount(accountScopeID, assignmentID)
	}
	if err := batch.Delete([]byte(assignmentKey), nil); err != nil {
		return fmt.Errorf("delete integration assignment: %w", err)
	}
	if err := deleteAssignmentIndexes(batch, assignment); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

func (s *IntegrationStore) PutWorkspace(record IntegrationWorkspaceRecord) (IntegrationWorkspaceRecord, error) {
	if err := s.configured(); err != nil {
		return IntegrationWorkspaceRecord{}, err
	}
	now := time.Now().UTC()
	record = normalizeIntegrationWorkspace(record)
	if record.WorkspaceID == "" {
		return IntegrationWorkspaceRecord{}, errors.New("workspace_id is required")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	key := KeyIntegrationWorkspace(record.WorkspaceID)
	if record.AccountScopeID != "" {
		key = KeyIntegrationWorkspaceForAccount(record.AccountScopeID, record.WorkspaceID)
	}
	if err := s.store.PutJSON(key, record); err != nil {
		return IntegrationWorkspaceRecord{}, err
	}
	return record, nil
}

func (s *IntegrationStore) GetWorkspace(workspaceID string) (IntegrationWorkspaceRecord, bool, error) {
	return s.GetWorkspaceForAccount("", workspaceID)
}

func (s *IntegrationStore) GetWorkspaceForAccount(accountScopeID, workspaceID string) (IntegrationWorkspaceRecord, bool, error) {
	if err := s.configured(); err != nil {
		return IntegrationWorkspaceRecord{}, false, err
	}
	key := KeyIntegrationWorkspace(workspaceID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationWorkspaceForAccount(accountScopeID, workspaceID)
	}
	var record IntegrationWorkspaceRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil || !ok {
		return IntegrationWorkspaceRecord{}, ok, err
	}
	return normalizeIntegrationWorkspace(record), true, nil
}

func (s *IntegrationStore) DeleteWorkspace(workspaceID string) error {
	return s.DeleteWorkspaceForAccount("", workspaceID)
}

func (s *IntegrationStore) DeleteWorkspaceForAccount(accountScopeID, workspaceID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	workspaceID = normalizeIntegrationID(workspaceID)
	if workspaceID == "" {
		return errors.New("workspace_id is required")
	}
	key := KeyIntegrationWorkspace(workspaceID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationWorkspaceForAccount(accountScopeID, workspaceID)
	}
	return s.store.Delete(key)
}

func (s *IntegrationStore) ListWorkspaces(limit int) ([]IntegrationWorkspaceRecord, error) {
	return s.ListWorkspacesForAccount("", limit)
}

func (s *IntegrationStore) ListWorkspacesForAccount(accountScopeID string, limit int) ([]IntegrationWorkspaceRecord, error) {
	out, err := listIntegrationRecords(s, IntegrationWorkspacePrefixForAccount(accountScopeID), limit, func(value []byte) (IntegrationWorkspaceRecord, error) {
		var record IntegrationWorkspaceRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return IntegrationWorkspaceRecord{}, fmt.Errorf("decode integration workspace: %w", err)
		}
		return normalizeIntegrationWorkspace(record), nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *IntegrationStore) PutWorkspaceSession(record IntegrationWorkspaceSessionRecord) (IntegrationWorkspaceSessionRecord, error) {
	if err := s.configured(); err != nil {
		return IntegrationWorkspaceSessionRecord{}, err
	}
	now := time.Now().UTC()
	record = normalizeIntegrationWorkspaceSession(record)
	if record.WorkspaceID == "" || record.SessionID == "" {
		return IntegrationWorkspaceSessionRecord{}, errors.New("workspace_id and session_id are required")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return IntegrationWorkspaceSessionRecord{}, fmt.Errorf("marshal integration workspace session: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if previous, ok, err := s.GetWorkspaceSessionForAccount(record.AccountScopeID, record.WorkspaceID, record.SessionID); err != nil {
		return IntegrationWorkspaceSessionRecord{}, err
	} else if ok {
		previousUpdatedKey := KeyIntegrationWorkspaceSessionUpdated(previous.WorkspaceID, previous.UpdatedAt.UTC().UnixMilli(), previous.SessionID)
		if previous.AccountScopeID != "" {
			previousUpdatedKey = KeyIntegrationWorkspaceSessionUpdatedForAccount(previous.AccountScopeID, previous.WorkspaceID, previous.UpdatedAt.UTC().UnixMilli(), previous.SessionID)
		}
		if err := batch.Delete([]byte(previousUpdatedKey), nil); err != nil {
			return IntegrationWorkspaceSessionRecord{}, fmt.Errorf("delete stale workspace session index: %w", err)
		}
	}
	recordKey := KeyIntegrationWorkspaceSession(record.WorkspaceID, record.SessionID)
	updatedKey := KeyIntegrationWorkspaceSessionUpdated(record.WorkspaceID, record.UpdatedAt.UTC().UnixMilli(), record.SessionID)
	if record.AccountScopeID != "" {
		recordKey = KeyIntegrationWorkspaceSessionForAccount(record.AccountScopeID, record.WorkspaceID, record.SessionID)
		updatedKey = KeyIntegrationWorkspaceSessionUpdatedForAccount(record.AccountScopeID, record.WorkspaceID, record.UpdatedAt.UTC().UnixMilli(), record.SessionID)
	}
	if err := batch.Set([]byte(recordKey), payload, nil); err != nil {
		return IntegrationWorkspaceSessionRecord{}, fmt.Errorf("set integration workspace session: %w", err)
	}
	if err := batch.Set([]byte(updatedKey), []byte(recordKey), nil); err != nil {
		return IntegrationWorkspaceSessionRecord{}, fmt.Errorf("set integration workspace session updated index: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return IntegrationWorkspaceSessionRecord{}, fmt.Errorf("commit integration workspace session: %w", err)
	}
	if err := s.refreshWorkspaceLatestChild(record.AccountScopeID, record.WorkspaceID); err != nil {
		return IntegrationWorkspaceSessionRecord{}, err
	}
	return record, nil
}

func (s *IntegrationStore) GetWorkspaceSession(workspaceID, sessionID string) (IntegrationWorkspaceSessionRecord, bool, error) {
	return s.GetWorkspaceSessionForAccount("", workspaceID, sessionID)
}

func (s *IntegrationStore) GetWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID string) (IntegrationWorkspaceSessionRecord, bool, error) {
	if err := s.configured(); err != nil {
		return IntegrationWorkspaceSessionRecord{}, false, err
	}
	key := KeyIntegrationWorkspaceSession(workspaceID, sessionID)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyIntegrationWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID)
	}
	var record IntegrationWorkspaceSessionRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil || !ok {
		return IntegrationWorkspaceSessionRecord{}, ok, err
	}
	return normalizeIntegrationWorkspaceSession(record), true, nil
}

func (s *IntegrationStore) ListWorkspaceSessions(workspaceID string, limit int) ([]IntegrationWorkspaceSessionRecord, error) {
	return s.ListWorkspaceSessionsForAccount("", workspaceID, limit)
}

func (s *IntegrationStore) ListWorkspaceSessionsForAccount(accountScopeID, workspaceID string, limit int) ([]IntegrationWorkspaceSessionRecord, error) {
	return s.listWorkspaceSessionsByIndex(IntegrationWorkspaceSessionUpdatedPrefixForAccount(accountScopeID, workspaceID), limit)
}

func (s *IntegrationStore) DeleteWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID string) error {
	if err := s.configured(); err != nil {
		return err
	}
	join, ok, err := s.GetWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID)
	if err != nil || !ok {
		return err
	}
	recordKey := KeyIntegrationWorkspaceSession(workspaceID, sessionID)
	updatedKey := KeyIntegrationWorkspaceSessionUpdated(workspaceID, join.UpdatedAt.UTC().UnixMilli(), sessionID)
	if strings.TrimSpace(accountScopeID) != "" {
		recordKey = KeyIntegrationWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID)
		updatedKey = KeyIntegrationWorkspaceSessionUpdatedForAccount(accountScopeID, workspaceID, join.UpdatedAt.UTC().UnixMilli(), sessionID)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(recordKey), nil); err != nil {
		return fmt.Errorf("delete integration workspace session: %w", err)
	}
	if err := batch.Delete([]byte(updatedKey), nil); err != nil {
		return fmt.Errorf("delete integration workspace session updated index: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	return s.refreshWorkspaceLatestChild(accountScopeID, workspaceID)
}

func (s *IntegrationStore) LatestWorkspaceSession(workspaceID string) (IntegrationWorkspaceSessionRecord, bool, error) {
	return s.LatestWorkspaceSessionForAccount("", workspaceID)
}

func (s *IntegrationStore) LatestWorkspaceSessionForAccount(accountScopeID, workspaceID string) (IntegrationWorkspaceSessionRecord, bool, error) {
	sessions, err := s.ListWorkspaceSessionsForAccount(accountScopeID, workspaceID, 1)
	if err != nil || len(sessions) == 0 {
		return IntegrationWorkspaceSessionRecord{}, false, err
	}
	return sessions[0], true, nil
}

func (s *IntegrationStore) refreshWorkspaceLatestChild(accountScopeID, workspaceID string) error {
	workspace, ok, err := s.GetWorkspaceForAccount(accountScopeID, workspaceID)
	if err != nil || !ok {
		return err
	}
	latest, latestOK, err := s.LatestWorkspaceSessionForAccount(accountScopeID, workspaceID)
	if err != nil {
		return err
	}
	if latestOK {
		workspace.LatestChildSessionID = latest.SessionID
		workspace.LatestChildSessionAt = latest.UpdatedAt
	} else {
		workspace.LatestChildSessionID = ""
		workspace.LatestChildSessionAt = time.Time{}
	}
	workspace.UpdatedAt = time.Now().UTC()
	workspaceKey := KeyIntegrationWorkspace(workspace.WorkspaceID)
	if workspace.AccountScopeID != "" {
		workspaceKey = KeyIntegrationWorkspaceForAccount(workspace.AccountScopeID, workspace.WorkspaceID)
	}
	return s.store.PutJSON(workspaceKey, normalizeIntegrationWorkspace(workspace))
}

func (s *IntegrationStore) configured() error {
	if s == nil || s.store == nil {
		return errors.New("integration store is not configured")
	}
	return nil
}

func (s *IntegrationStore) listAssignmentsByIndex(prefix string, limit int, valuesAreKeys bool) ([]IntegrationAssignmentRecord, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]IntegrationAssignmentRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		payload := value
		if valuesAreKeys {
			loaded, ok, err := s.store.GetBytes(string(value))
			if err != nil || !ok {
				return err
			}
			payload = loaded
		}
		var record IntegrationAssignmentRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			return fmt.Errorf("decode integration assignment: %w", err)
		}
		out = append(out, normalizeIntegrationAssignment(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *IntegrationStore) listWorkspaceSessionsByIndex(prefix string, limit int) ([]IntegrationWorkspaceSessionRecord, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]IntegrationWorkspaceSessionRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		loaded, ok, err := s.store.GetBytes(string(value))
		if err != nil || !ok {
			return err
		}
		var record IntegrationWorkspaceSessionRecord
		if err := json.Unmarshal(loaded, &record); err != nil {
			return fmt.Errorf("decode integration workspace session: %w", err)
		}
		out = append(out, normalizeIntegrationWorkspaceSession(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func listIntegrationRecords[T any](s *IntegrationStore, prefix string, limit int, decode func([]byte) (T, error)) ([]T, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]T, 0, min(limit, 16))
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		record, err := decode(value)
		if err != nil {
			return err
		}
		out = append(out, record)
		return nil
	})
	return out, err
}

func deleteAssignmentIndexes(batch *pebble.Batch, assignment IntegrationAssignmentRecord) error {
	assignment = normalizeIntegrationAssignment(assignment)
	if assignment.AgentName != "" && assignment.AssignmentID != "" {
		key := KeyIntegrationAssignmentByAgent(assignment.AgentName, assignment.AssignmentID)
		if assignment.AccountScopeID != "" {
			key = KeyIntegrationAssignmentByAgentForAccount(assignment.AccountScopeID, assignment.AgentName, assignment.AssignmentID)
		}
		if err := batch.Delete([]byte(key), nil); err != nil {
			return fmt.Errorf("delete integration assignment agent index: %w", err)
		}
	}
	if assignment.PackID != "" && assignment.VersionID != "" && assignment.AssignmentID != "" {
		key := KeyIntegrationAssignmentByPack(assignment.PackID, assignment.VersionID, assignment.AssignmentID)
		if assignment.AccountScopeID != "" {
			key = KeyIntegrationAssignmentByPackForAccount(assignment.AccountScopeID, assignment.PackID, assignment.VersionID, assignment.AssignmentID)
		}
		if err := batch.Delete([]byte(key), nil); err != nil {
			return fmt.Errorf("delete integration assignment pack index: %w", err)
		}
	}
	return nil
}

func normalizeIntegrationPack(record IntegrationPackRecord) IntegrationPackRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.PackID = normalizeIntegrationID(record.PackID)
	record.Slug = normalizeIntegrationID(record.Slug)
	record.DisplayName = strings.TrimSpace(record.DisplayName)
	record.Description = strings.TrimSpace(record.Description)
	record.LatestVersionID = normalizeIntegrationID(record.LatestVersionID)
	record.DraftVersionID = normalizeIntegrationID(record.DraftVersionID)
	record.Metadata = normalizeIntegrationStringMap(record.Metadata)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record
}

func normalizeIntegrationPackVersion(record IntegrationPackVersionRecord) IntegrationPackVersionRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.PackID = normalizeIntegrationID(record.PackID)
	record.VersionID = normalizeIntegrationID(record.VersionID)
	record.Version = strings.TrimSpace(record.Version)
	record.Status = normalizeIntegrationVersionStatus(record.Status)
	record.DisplayName = strings.TrimSpace(record.DisplayName)
	record.Description = strings.TrimSpace(record.Description)
	record.ToolIDs = normalizeIntegrationIDSlice(record.ToolIDs)
	record.AdapterIDs = normalizeIntegrationIDSlice(record.AdapterIDs)
	record.PromptIDs = normalizeIntegrationIDSlice(record.PromptIDs)
	record.Metadata = normalizeIntegrationStringMap(record.Metadata)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record
}

func normalizeIntegrationTool(record IntegrationToolRecord) IntegrationToolRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.PackID = normalizeIntegrationID(record.PackID)
	record.VersionID = normalizeIntegrationID(record.VersionID)
	record.ToolID = normalizeIntegrationID(record.ToolID)
	record.Name = strings.TrimSpace(record.Name)
	record.Description = strings.TrimSpace(record.Description)
	record.AdapterID = normalizeIntegrationID(record.AdapterID)
	record.PermissionMode = normalizeIntegrationPermissionMode(record.PermissionMode)
	record.InputSchema = append(json.RawMessage(nil), record.InputSchema...)
	record.Metadata = normalizeIntegrationStringMap(record.Metadata)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record
}

func normalizeIntegrationAdapter(record IntegrationAdapterRecord) IntegrationAdapterRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.PackID = normalizeIntegrationID(record.PackID)
	record.VersionID = normalizeIntegrationID(record.VersionID)
	record.AdapterID = normalizeIntegrationID(record.AdapterID)
	record.Type = normalizeIntegrationAdapterType(record.Type)
	record.DisplayName = strings.TrimSpace(record.DisplayName)
	record.Settings = normalizeIntegrationStringMap(record.Settings)
	record.CredentialRefs = normalizeIntegrationStringMap(record.CredentialRefs)
	record.Metadata = normalizeIntegrationStringMap(record.Metadata)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record
}

func normalizeIntegrationPromptFragment(record IntegrationPromptFragmentRecord) IntegrationPromptFragmentRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.PackID = normalizeIntegrationID(record.PackID)
	record.VersionID = normalizeIntegrationID(record.VersionID)
	record.PromptID = normalizeIntegrationID(record.PromptID)
	record.Title = strings.TrimSpace(record.Title)
	record.Content = strings.TrimSpace(record.Content)
	record.Metadata = normalizeIntegrationStringMap(record.Metadata)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record
}

func normalizeIntegrationAssignment(record IntegrationAssignmentRecord) IntegrationAssignmentRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.AssignmentID = normalizeIntegrationID(record.AssignmentID)
	record.AgentName = strings.ToLower(strings.TrimSpace(record.AgentName))
	record.PackID = normalizeIntegrationID(record.PackID)
	record.VersionID = normalizeIntegrationID(record.VersionID)
	record.Status = normalizeIntegrationAssignmentStatus(record.Status)
	record.Metadata = normalizeIntegrationStringMap(record.Metadata)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record
}

func normalizeIntegrationWorkspace(record IntegrationWorkspaceRecord) IntegrationWorkspaceRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.WorkspaceID = normalizeIntegrationID(record.WorkspaceID)
	record.DisplayName = strings.TrimSpace(record.DisplayName)
	record.PackID = normalizeIntegrationID(record.PackID)
	record.DraftVersionID = normalizeIntegrationID(record.DraftVersionID)
	record.LatestChildSessionID = strings.TrimSpace(record.LatestChildSessionID)
	record.Metadata = normalizeIntegrationStringMap(record.Metadata)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	record.LatestChildSessionAt = record.LatestChildSessionAt.UTC()
	return record
}

func normalizeIntegrationWorkspaceSession(record IntegrationWorkspaceSessionRecord) IntegrationWorkspaceSessionRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.WorkspaceID = normalizeIntegrationID(record.WorkspaceID)
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.Title = strings.TrimSpace(record.Title)
	record.Metadata = normalizeIntegrationStringMap(record.Metadata)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record
}

func normalizeIntegrationID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeIntegrationStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeIntegrationIDSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeIntegrationID(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeIntegrationAdapterType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case IntegrationAdapterTypeCLIWrapper:
		return IntegrationAdapterTypeCLIWrapper
	case IntegrationAdapterTypeHostHTTPBridge, "primary_host_http", "local_api_bridge":
		return IntegrationAdapterTypeHostHTTPBridge
	case IntegrationAdapterTypeUnixSocketBridge, "unix_socket":
		return IntegrationAdapterTypeUnixSocketBridge
	case IntegrationAdapterTypeHostedAPI:
		return IntegrationAdapterTypeHostedAPI
	default:
		return ""
	}
}

func normalizeIntegrationPermissionMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case IntegrationPermissionModeAllow:
		return IntegrationPermissionModeAllow
	case IntegrationPermissionModeAskBlocking, "ask-blocking":
		return IntegrationPermissionModeAskBlocking
	case IntegrationPermissionModeAskAsync, "ask-async":
		return IntegrationPermissionModeAskAsync
	case IntegrationPermissionModeDeny:
		return IntegrationPermissionModeDeny
	default:
		return ""
	}
}

func normalizeIntegrationVersionStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case IntegrationVersionStatusPublished:
		return IntegrationVersionStatusPublished
	case IntegrationVersionStatusDraft:
		return IntegrationVersionStatusDraft
	default:
		return ""
	}
}

func normalizeIntegrationAssignmentStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case IntegrationAssignmentStatusDisabled:
		return IntegrationAssignmentStatusDisabled
	case IntegrationAssignmentStatusActive:
		return IntegrationAssignmentStatusActive
	default:
		return ""
	}
}
