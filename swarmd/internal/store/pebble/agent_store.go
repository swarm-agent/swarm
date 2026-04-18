package pebblestore

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	AgentExecutionSettingRead      = "read"
	AgentExecutionSettingReadWrite = "readwrite"

	AgentCustomToolKindFixedBash = "fixed_bash"
)

type AgentToolScope struct {
	Preset        string   `json:"preset,omitempty"`
	AllowTools    []string `json:"allow_tools,omitempty"`
	DenyTools     []string `json:"deny_tools,omitempty"`
	BashPrefixes  []string `json:"bash_prefixes,omitempty"`
	InheritPolicy bool     `json:"inherit_policy,omitempty"`
}

type AgentToolConfig struct {
	Enabled      *bool    `json:"enabled,omitempty"`
	BashPrefixes []string `json:"bash_prefixes,omitempty"`
}

type AgentToolContract struct {
	Preset        string                     `json:"preset,omitempty"`
	Tools         map[string]AgentToolConfig `json:"tools,omitempty"`
	InheritPolicy bool                       `json:"inherit_policy,omitempty"`
}

type AgentProfile struct {
	Name                string             `json:"name"`
	Mode                string             `json:"mode"`
	Description         string             `json:"description"`
	Provider            string             `json:"provider"`
	Model               string             `json:"model"`
	Thinking            string             `json:"thinking"`
	Prompt              string             `json:"prompt"`
	ExecutionSetting    string             `json:"execution_setting,omitempty"`
	ExitPlanModeEnabled *bool              `json:"exit_plan_mode_enabled,omitempty"`
	ToolScope           *AgentToolScope    `json:"tool_scope,omitempty"`
	ToolContract        *AgentToolContract `json:"tool_contract,omitempty"`
	Enabled             bool               `json:"enabled"`
	Protected           bool               `json:"protected,omitempty"`
	UpdatedAt           int64              `json:"updated_at"`
}

type AgentCustomToolDefinition struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
	UpdatedAt   int64  `json:"updated_at"`
}

func BoolPtr(value bool) *bool {
	v := value
	return &v
}

func CloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	return BoolPtr(*value)
}

func NormalizeAgentExecutionSetting(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AgentExecutionSettingRead:
		return AgentExecutionSettingRead
	case AgentExecutionSettingReadWrite, "read_write", "read-write":
		return AgentExecutionSettingReadWrite
	default:
		return ""
	}
}

func CloneAgentToolScope(scope *AgentToolScope) *AgentToolScope {
	if scope == nil {
		return nil
	}
	return &AgentToolScope{
		Preset:        strings.TrimSpace(scope.Preset),
		AllowTools:    append([]string(nil), scope.AllowTools...),
		DenyTools:     append([]string(nil), scope.DenyTools...),
		BashPrefixes:  append([]string(nil), scope.BashPrefixes...),
		InheritPolicy: scope.InheritPolicy,
	}
}

func CloneAgentToolContract(contract *AgentToolContract) *AgentToolContract {
	if contract == nil {
		return nil
	}
	out := &AgentToolContract{
		Preset:        strings.TrimSpace(contract.Preset),
		InheritPolicy: contract.InheritPolicy,
	}
	if len(contract.Tools) > 0 {
		out.Tools = make(map[string]AgentToolConfig, len(contract.Tools))
		for name, cfg := range contract.Tools {
			out.Tools[name] = AgentToolConfig{
				Enabled:      CloneBoolPtr(cfg.Enabled),
				BashPrefixes: append([]string(nil), cfg.BashPrefixes...),
			}
		}
	}
	return out
}

func NormalizeAgentToolScope(scope *AgentToolScope) *AgentToolScope {
	if scope == nil {
		return nil
	}
	out := &AgentToolScope{
		Preset:        strings.ToLower(strings.TrimSpace(scope.Preset)),
		AllowTools:    normalizeAgentToolScopeStringSlice(scope.AllowTools),
		DenyTools:     normalizeAgentToolScopeStringSlice(scope.DenyTools),
		BashPrefixes:  normalizeAgentToolScopeStringSlice(scope.BashPrefixes),
		InheritPolicy: scope.InheritPolicy,
	}
	if strings.TrimSpace(out.Preset) == "" && len(out.AllowTools) == 0 && len(out.DenyTools) == 0 && len(out.BashPrefixes) == 0 && !out.InheritPolicy {
		return nil
	}
	return out
}

func NormalizeAgentToolContract(contract *AgentToolContract) *AgentToolContract {
	if contract == nil {
		return nil
	}
	out := &AgentToolContract{
		Preset:        strings.ToLower(strings.TrimSpace(contract.Preset)),
		InheritPolicy: contract.InheritPolicy,
	}
	if len(contract.Tools) > 0 {
		out.Tools = make(map[string]AgentToolConfig, len(contract.Tools))
		for rawName, rawCfg := range contract.Tools {
			name := normalizeAgentToolScopeKey(rawName)
			if name == "" {
				continue
			}
			cfg := AgentToolConfig{
				Enabled:      CloneBoolPtr(rawCfg.Enabled),
				BashPrefixes: normalizeAgentToolScopeStringSlice(rawCfg.BashPrefixes),
			}
			if cfg.Enabled == nil && len(cfg.BashPrefixes) == 0 {
				continue
			}
			out.Tools[name] = cfg
		}
	}
	if strings.TrimSpace(out.Preset) == "" && len(out.Tools) == 0 && !out.InheritPolicy {
		return nil
	}
	return out
}

func NormalizeAgentCustomToolName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func NormalizeAgentCustomToolKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AgentCustomToolKindFixedBash:
		return AgentCustomToolKindFixedBash
	default:
		return ""
	}
}

func CloneAgentCustomToolDefinition(definition AgentCustomToolDefinition) AgentCustomToolDefinition {
	return AgentCustomToolDefinition{
		Name:        strings.TrimSpace(definition.Name),
		Kind:        strings.TrimSpace(definition.Kind),
		Description: strings.TrimSpace(definition.Description),
		Command:     strings.TrimSpace(definition.Command),
		UpdatedAt:   definition.UpdatedAt,
	}
}

func NormalizeAgentCustomToolDefinition(definition AgentCustomToolDefinition) AgentCustomToolDefinition {
	definition = CloneAgentCustomToolDefinition(definition)
	definition.Name = NormalizeAgentCustomToolName(definition.Name)
	definition.Kind = NormalizeAgentCustomToolKind(definition.Kind)
	definition.Description = strings.TrimSpace(definition.Description)
	definition.Command = strings.TrimSpace(definition.Command)
	if definition.UpdatedAt < 0 {
		definition.UpdatedAt = 0
	}
	return definition
}

func AgentExitPlanModeEnabled(profile AgentProfile) bool {
	if profile.ExitPlanModeEnabled != nil {
		return *profile.ExitPlanModeEnabled
	}
	return strings.EqualFold(strings.TrimSpace(profile.Name), "swarm")
}

func AgentExecutionSetting(profile AgentProfile) (string, bool) {
	setting := NormalizeAgentExecutionSetting(profile.ExecutionSetting)
	return setting, setting != ""
}

func NormalizeAgentProfile(profile AgentProfile) AgentProfile {
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Mode = strings.ToLower(strings.TrimSpace(profile.Mode))
	profile.Description = strings.TrimSpace(profile.Description)
	profile.Provider = strings.ToLower(strings.TrimSpace(profile.Provider))
	profile.Model = strings.TrimSpace(profile.Model)
	profile.Thinking = strings.ToLower(strings.TrimSpace(profile.Thinking))
	profile.Prompt = strings.TrimSpace(profile.Prompt)
	profile.ExecutionSetting = NormalizeAgentExecutionSetting(profile.ExecutionSetting)
	profile.ToolScope = NormalizeAgentToolScope(profile.ToolScope)
	profile.ToolContract = NormalizeAgentToolContract(profile.ToolContract)

	if profile.ExitPlanModeEnabled == nil {
		profile.ExitPlanModeEnabled = BoolPtr(strings.EqualFold(profile.Name, "swarm"))
	} else {
		profile.ExitPlanModeEnabled = CloneBoolPtr(profile.ExitPlanModeEnabled)
	}
	if AgentExitPlanModeEnabled(profile) {
		profile.ExecutionSetting = ""
	}
	profile.Protected = strings.EqualFold(profile.Name, "swarm") || strings.EqualFold(profile.Name, "memory")
	return profile
}

func normalizeAgentToolScopeKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "":
		return ""
	case "ask-user", "ask_user":
		return "ask_user"
	case "exit-plan-mode", "exit_plan_mode":
		return "exit_plan_mode"
	case "plan-manage", "plan_manage":
		return "plan_manage"
	case "skill-use", "skill_use":
		return "skill_use"
	case "manage-skill", "manage_skill":
		return "manage_skill"
	case "manage-agent", "manage_agent":
		return "manage_agent"
	case "manage-theme", "manage_theme":
		return "manage_theme"
	case "manage-worktree", "manage_worktree":
		return "manage_worktree"
	case "manage-todos", "manage_todos":
		return "manage_todos"
	default:
		return value
	}
}

func normalizeAgentToolScopeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type AgentStore struct {
	store *Store
}

func NewAgentStore(store *Store) *AgentStore {
	return &AgentStore{store: store}
}

func (s *AgentStore) GetProfile(name string) (AgentProfile, bool, error) {
	profile := AgentProfile{}
	ok, err := s.store.GetJSON(KeyAgentProfile(name), &profile)
	if err != nil || !ok {
		return profile, ok, err
	}
	profile = NormalizeAgentProfile(profile)
	return profile, true, nil
}

func (s *AgentStore) PutProfile(profile AgentProfile) error {
	profile = NormalizeAgentProfile(profile)
	return s.store.PutJSON(KeyAgentProfile(profile.Name), profile)
}

func (s *AgentStore) DeleteProfile(name string) error {
	return s.store.Delete(KeyAgentProfile(name))
}

func (s *AgentStore) ListProfiles(limit int) ([]AgentProfile, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make([]AgentProfile, 0, limit)
	err := s.store.IteratePrefix(AgentProfilePrefix(), limit, func(_ string, value []byte) error {
		var profile AgentProfile
		if err := json.Unmarshal(value, &profile); err != nil {
			return fmt.Errorf("decode agent profile: %w", err)
		}
		out = append(out, NormalizeAgentProfile(profile))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(out[i].Name)) < strings.ToLower(strings.TrimSpace(out[j].Name))
	})
	return out, nil
}

func (s *AgentStore) SetActivePrimary(name string) error {
	return s.store.PutJSON(KeyAgentActivePrimary, map[string]string{"name": strings.TrimSpace(name)})
}

func (s *AgentStore) GetActivePrimary() (string, bool, error) {
	var payload struct {
		Name string `json:"name"`
	}
	ok, err := s.store.GetJSON(KeyAgentActivePrimary, &payload)
	if err != nil || !ok {
		return "", ok, err
	}
	return strings.TrimSpace(payload.Name), true, nil
}

func (s *AgentStore) SetActiveSubagent(purpose, name string) error {
	payload := map[string]string{
		"purpose": strings.TrimSpace(purpose),
		"name":    strings.TrimSpace(name),
	}
	return s.store.PutJSON(KeyAgentActiveSubagent(purpose), payload)
}

func (s *AgentStore) DeleteActiveSubagent(purpose string) error {
	return s.store.Delete(KeyAgentActiveSubagent(purpose))
}

func (s *AgentStore) GetActiveSubagents(limit int) (map[string]string, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make(map[string]string, 8)
	err := s.store.IteratePrefix(AgentActiveSubagentPrefix(), limit, func(key string, value []byte) error {
		var payload struct {
			Purpose string `json:"purpose"`
			Name    string `json:"name"`
		}
		if err := json.Unmarshal(value, &payload); err != nil {
			return fmt.Errorf("decode active subagent: %w", err)
		}
		purpose := strings.TrimSpace(payload.Purpose)
		if purpose == "" {
			purpose = decodeKeyTail(key, AgentActiveSubagentPrefix())
		}
		if purpose == "" {
			return nil
		}
		out[purpose] = strings.TrimSpace(payload.Name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *AgentStore) GetVersion() (int64, bool, error) {
	var payload struct {
		Version int64 `json:"version"`
	}
	ok, err := s.store.GetJSON(KeyAgentVersion, &payload)
	if err != nil || !ok {
		return 0, ok, err
	}
	return payload.Version, true, nil
}

func (s *AgentStore) SetVersion(version int64) error {
	return s.store.PutJSON(KeyAgentVersion, map[string]int64{"version": version})
}

func (s *AgentStore) GetCustomTool(name string) (AgentCustomToolDefinition, bool, error) {
	definition := AgentCustomToolDefinition{}
	ok, err := s.store.GetJSON(KeyAgentCustomTool(name), &definition)
	if err != nil || !ok {
		return definition, ok, err
	}
	definition = NormalizeAgentCustomToolDefinition(definition)
	if definition.Name == "" {
		definition.Name = NormalizeAgentCustomToolName(name)
	}
	return definition, true, nil
}

func (s *AgentStore) PutCustomTool(definition AgentCustomToolDefinition) error {
	definition = NormalizeAgentCustomToolDefinition(definition)
	return s.store.PutJSON(KeyAgentCustomTool(definition.Name), definition)
}

func (s *AgentStore) DeleteCustomTool(name string) error {
	return s.store.Delete(KeyAgentCustomTool(name))
}

func (s *AgentStore) ListCustomTools(limit int) ([]AgentCustomToolDefinition, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make([]AgentCustomToolDefinition, 0, limit)
	err := s.store.IteratePrefix(AgentCustomToolPrefix(), limit, func(key string, value []byte) error {
		var definition AgentCustomToolDefinition
		if err := json.Unmarshal(value, &definition); err != nil {
			return fmt.Errorf("decode agent custom tool: %w", err)
		}
		definition = NormalizeAgentCustomToolDefinition(definition)
		if definition.Name == "" {
			definition.Name = decodeKeyTail(key, AgentCustomToolPrefix())
		}
		if definition.Name == "" {
			return nil
		}
		out = append(out, definition)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(out[i].Name)) < strings.ToLower(strings.TrimSpace(out[j].Name))
	})
	return out, nil
}

func decodeKeyTail(key, prefix string) string {
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	raw := strings.TrimPrefix(key, prefix)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return strings.TrimSpace(decoded)
}
