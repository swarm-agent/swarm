package pebblestore

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	AgentRuntimeModePlanAuto  = "plan_auto"
	AgentRuntimeModeRead      = "read"
	AgentRuntimeModeReadWrite = "readwrite"

	AgentExecutionSettingRead      = AgentRuntimeModeRead
	AgentExecutionSettingReadWrite = AgentRuntimeModeReadWrite

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
	RuntimeMode         string             `json:"runtime_mode,omitempty"`
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

func NormalizeAgentRuntimeMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AgentRuntimeModePlanAuto, "plan-auto", "plan -> auto", "plan - auto", "plan -auto", "planauto", "auto":
		return AgentRuntimeModePlanAuto
	case AgentRuntimeModeRead:
		return AgentRuntimeModeRead
	case AgentRuntimeModeReadWrite, "read_write", "read-write":
		return AgentRuntimeModeReadWrite
	default:
		return ""
	}
}

func NormalizeAgentExecutionSetting(value string) string {
	switch NormalizeAgentRuntimeMode(value) {
	case AgentRuntimeModeRead:
		return AgentExecutionSettingRead
	case AgentRuntimeModeReadWrite:
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

func AgentProfileRuntimeMode(profile AgentProfile) string {
	if mode := NormalizeAgentRuntimeMode(profile.RuntimeMode); mode != "" {
		return mode
	}
	if AgentExitPlanModeEnabled(profile) {
		return AgentRuntimeModePlanAuto
	}
	if setting, ok := AgentExecutionSetting(profile); ok {
		return setting
	}
	return AgentRuntimeModeForToolContract(profile.ToolContract)
}

func AgentRuntimeModeForToolContract(contract *AgentToolContract) string {
	return AgentRuntimeModeForToolContractWithDefault(contract, true)
}

func AgentRuntimeModeForToolContractWithDefault(contract *AgentToolContract, defaultRead bool) string {
	contract = NormalizeAgentToolContract(contract)
	if contract == nil {
		return ""
	}
	if agentToolContractEnablesMutatingTools(contract) {
		return AgentRuntimeModeReadWrite
	}
	if defaultRead {
		return AgentRuntimeModeRead
	}
	return ""
}

func agentToolContractEnablesMutatingTools(contract *AgentToolContract) bool {
	if contract == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(contract.Preset)) {
	case "read_write", "bash_git_only", "background_commit":
		return true
	}
	readOnlyTools := map[string]struct{}{
		"ask_user":            {},
		"exit_plan_mode":      {},
		"list":                {},
		"manage_agent":        {},
		"manage_flow":         {},
		"manage_integrations": {},
		"manage_skill":        {},
		"manage_theme":        {},
		"manage_todos":        {},
		"manage_worktree":     {},
		"plan_manage":         {},
		"read":                {},
		"search":              {},
		"skill_use":           {},
		"webfetch":            {},
		"websearch":           {},
	}
	mutatingTools := map[string]struct{}{
		"bash":       {},
		"edit":       {},
		"git_add":    {},
		"git_commit": {},
		"task":       {},
		"write":      {},
	}
	for rawName, cfg := range contract.Tools {
		name := normalizeAgentToolScopeKey(rawName)
		if name == "" {
			continue
		}
		enabled := cfg.Enabled != nil && *cfg.Enabled
		if len(cfg.BashPrefixes) > 0 {
			enabled = true
		}
		if !enabled {
			continue
		}
		if _, ok := mutatingTools[name]; ok {
			return true
		}
		if _, ok := readOnlyTools[name]; !ok {
			return true
		}
	}
	return false
}

func NormalizeAgentProfile(profile AgentProfile) AgentProfile {
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Mode = strings.ToLower(strings.TrimSpace(profile.Mode))
	profile.Description = strings.TrimSpace(profile.Description)
	profile.Provider = strings.ToLower(strings.TrimSpace(profile.Provider))
	profile.Model = strings.TrimSpace(profile.Model)
	profile.Thinking = strings.ToLower(strings.TrimSpace(profile.Thinking))
	profile.Prompt = strings.TrimSpace(profile.Prompt)
	profile.RuntimeMode = NormalizeAgentRuntimeMode(profile.RuntimeMode)
	profile.ExecutionSetting = NormalizeAgentExecutionSetting(profile.ExecutionSetting)
	profile.ToolScope = NormalizeAgentToolScope(profile.ToolScope)
	profile.ToolContract = NormalizeAgentToolContract(profile.ToolContract)

	if profile.ExitPlanModeEnabled == nil {
		profile.ExitPlanModeEnabled = BoolPtr(strings.EqualFold(profile.Name, "swarm"))
	} else {
		profile.ExitPlanModeEnabled = CloneBoolPtr(profile.ExitPlanModeEnabled)
	}
	runtimeMode := AgentProfileRuntimeMode(profile)
	if toolRuntimeMode := AgentRuntimeModeForToolContract(profile.ToolContract); toolRuntimeMode != "" && runtimeMode == "" {
		runtimeMode = toolRuntimeMode
	}
	switch runtimeMode {
	case AgentRuntimeModePlanAuto:
		profile.RuntimeMode = AgentRuntimeModePlanAuto
		profile.ExitPlanModeEnabled = BoolPtr(true)
		profile.ExecutionSetting = ""
	case AgentRuntimeModeRead, AgentRuntimeModeReadWrite:
		profile.RuntimeMode = runtimeMode
		profile.ExitPlanModeEnabled = BoolPtr(false)
		profile.ExecutionSetting = runtimeMode
	default:
		if profile.ExitPlanModeEnabled != nil && !AgentExitPlanModeEnabled(profile) {
			profile.RuntimeMode = ""
			profile.ExecutionSetting = ""
		} else {
			profile.RuntimeMode = AgentRuntimeModePlanAuto
			profile.ExitPlanModeEnabled = BoolPtr(true)
			profile.ExecutionSetting = ""
		}
	}
	profile.Protected = strings.EqualFold(profile.Name, "memory")
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
	case "manage-integrations", "manage_integrations":
		return "manage_integrations"
	case "manage-flow", "manage_flow":
		return "manage_flow"
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
	return s.getProfileForAccount("", name)
}

func (s *AgentStore) GetProfileForAccount(accountScopeID, name string) (AgentProfile, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return AgentProfile{}, false, fmt.Errorf("account scope ID is required")
	}
	return s.getProfileForAccount(accountScopeID, name)
}

func (s *AgentStore) getProfileForAccount(accountScopeID, name string) (AgentProfile, bool, error) {
	profile := AgentProfile{}
	key := KeyAgentProfile(name)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyAgentProfileForAccount(accountScopeID, name)
	}
	ok, err := s.store.GetJSON(key, &profile)
	if err != nil || !ok {
		return profile, ok, err
	}
	profile = NormalizeAgentProfile(profile)
	return profile, true, nil
}

func (s *AgentStore) PutProfile(profile AgentProfile) error {
	return s.putProfileForAccount("", profile)
}

func (s *AgentStore) PutProfileForAccount(accountScopeID string, profile AgentProfile) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("account scope ID is required")
	}
	return s.putProfileForAccount(accountScopeID, profile)
}

func (s *AgentStore) putProfileForAccount(accountScopeID string, profile AgentProfile) error {
	profile = NormalizeAgentProfile(profile)
	key := KeyAgentProfile(profile.Name)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyAgentProfileForAccount(accountScopeID, profile.Name)
	}
	return s.store.PutJSON(key, profile)
}

func (s *AgentStore) DeleteProfile(name string) error {
	return s.deleteProfileForAccount("", name)
}

func (s *AgentStore) DeleteProfileForAccount(accountScopeID, name string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("account scope ID is required")
	}
	return s.deleteProfileForAccount(accountScopeID, name)
}

func (s *AgentStore) deleteProfileForAccount(accountScopeID, name string) error {
	key := KeyAgentProfile(name)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyAgentProfileForAccount(accountScopeID, name)
	}
	return s.store.Delete(key)
}

func (s *AgentStore) ListProfiles(limit int) ([]AgentProfile, error) {
	return s.listProfilesForAccount("", limit)
}

func (s *AgentStore) ListProfilesForAccount(accountScopeID string, limit int) ([]AgentProfile, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, fmt.Errorf("account scope ID is required")
	}
	return s.listProfilesForAccount(accountScopeID, limit)
}

func (s *AgentStore) listProfilesForAccount(accountScopeID string, limit int) ([]AgentProfile, error) {
	if limit <= 0 {
		limit = 200
	}
	prefix := AgentProfilePrefix()
	if strings.TrimSpace(accountScopeID) != "" {
		prefix = AgentProfilePrefixForAccount(accountScopeID)
	}
	out := make([]AgentProfile, 0, limit)
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
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
	return s.setActivePrimaryForAccount("", name)
}

func (s *AgentStore) SetActivePrimaryForAccount(accountScopeID, name string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("account scope ID is required")
	}
	return s.setActivePrimaryForAccount(accountScopeID, name)
}

func (s *AgentStore) setActivePrimaryForAccount(accountScopeID, name string) error {
	key := KeyAgentActivePrimary
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyAgentActivePrimaryForAccount(accountScopeID)
	}
	return s.store.PutJSON(key, map[string]string{"name": strings.TrimSpace(name)})
}

func (s *AgentStore) GetActivePrimary() (string, bool, error) {
	return s.getActivePrimaryForAccount("")
}

func (s *AgentStore) GetActivePrimaryForAccount(accountScopeID string) (string, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return "", false, fmt.Errorf("account scope ID is required")
	}
	return s.getActivePrimaryForAccount(accountScopeID)
}

func (s *AgentStore) getActivePrimaryForAccount(accountScopeID string) (string, bool, error) {
	var payload struct {
		Name string `json:"name"`
	}
	key := KeyAgentActivePrimary
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyAgentActivePrimaryForAccount(accountScopeID)
	}
	ok, err := s.store.GetJSON(key, &payload)
	if err != nil || !ok {
		return "", ok, err
	}
	return strings.TrimSpace(payload.Name), true, nil
}

func (s *AgentStore) SetActiveSubagent(purpose, name string) error {
	return s.setActiveSubagentForAccount("", purpose, name)
}

func (s *AgentStore) SetActiveSubagentForAccount(accountScopeID, purpose, name string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("account scope ID is required")
	}
	return s.setActiveSubagentForAccount(accountScopeID, purpose, name)
}

func (s *AgentStore) setActiveSubagentForAccount(accountScopeID, purpose, name string) error {
	payload := map[string]string{
		"purpose": strings.TrimSpace(purpose),
		"name":    strings.TrimSpace(name),
	}
	key := KeyAgentActiveSubagent(purpose)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyAgentActiveSubagentForAccount(accountScopeID, purpose)
	}
	return s.store.PutJSON(key, payload)
}

func (s *AgentStore) DeleteActiveSubagent(purpose string) error {
	return s.deleteActiveSubagentForAccount("", purpose)
}

func (s *AgentStore) DeleteActiveSubagentForAccount(accountScopeID, purpose string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("account scope ID is required")
	}
	return s.deleteActiveSubagentForAccount(accountScopeID, purpose)
}

func (s *AgentStore) deleteActiveSubagentForAccount(accountScopeID, purpose string) error {
	key := KeyAgentActiveSubagent(purpose)
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyAgentActiveSubagentForAccount(accountScopeID, purpose)
	}
	return s.store.Delete(key)
}

func (s *AgentStore) GetActiveSubagents(limit int) (map[string]string, error) {
	return s.getActiveSubagentsForAccount("", limit)
}

func (s *AgentStore) GetActiveSubagentsForAccount(accountScopeID string, limit int) (map[string]string, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, fmt.Errorf("account scope ID is required")
	}
	return s.getActiveSubagentsForAccount(accountScopeID, limit)
}

func (s *AgentStore) getActiveSubagentsForAccount(accountScopeID string, limit int) (map[string]string, error) {
	if limit <= 0 {
		limit = 200
	}
	prefix := AgentActiveSubagentPrefix()
	if strings.TrimSpace(accountScopeID) != "" {
		prefix = AgentActiveSubagentPrefixForAccount(accountScopeID)
	}
	out := make(map[string]string, 8)
	err := s.store.IteratePrefix(prefix, limit, func(key string, value []byte) error {
		var payload struct {
			Purpose string `json:"purpose"`
			Name    string `json:"name"`
		}
		if err := json.Unmarshal(value, &payload); err != nil {
			return fmt.Errorf("decode active subagent: %w", err)
		}
		purpose := strings.TrimSpace(payload.Purpose)
		if purpose == "" {
			purpose = decodeKeyTail(key, prefix)
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
	return s.getVersionForAccount("")
}

func (s *AgentStore) GetVersionForAccount(accountScopeID string) (int64, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return 0, false, fmt.Errorf("account scope ID is required")
	}
	return s.getVersionForAccount(accountScopeID)
}

func (s *AgentStore) getVersionForAccount(accountScopeID string) (int64, bool, error) {
	var payload struct {
		Version int64 `json:"version"`
	}
	key := KeyAgentVersion
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyAgentVersionForAccount(accountScopeID)
	}
	ok, err := s.store.GetJSON(key, &payload)
	if err != nil || !ok {
		return 0, ok, err
	}
	return payload.Version, true, nil
}

func (s *AgentStore) SetVersion(version int64) error {
	return s.setVersionForAccount("", version)
}

func (s *AgentStore) SetVersionForAccount(accountScopeID string, version int64) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("account scope ID is required")
	}
	return s.setVersionForAccount(accountScopeID, version)
}

func (s *AgentStore) setVersionForAccount(accountScopeID string, version int64) error {
	key := KeyAgentVersion
	if strings.TrimSpace(accountScopeID) != "" {
		key = KeyAgentVersionForAccount(accountScopeID)
	}
	return s.store.PutJSON(key, map[string]int64{"version": version})
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

func (s *AgentStore) GetCustomToolForAccount(accountScopeID, name string) (AgentCustomToolDefinition, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return AgentCustomToolDefinition{}, false, fmt.Errorf("account scope ID is required")
	}
	definition := AgentCustomToolDefinition{}
	ok, err := s.store.GetJSON(KeyAgentCustomToolForAccount(accountScopeID, name), &definition)
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

func (s *AgentStore) PutCustomToolForAccount(accountScopeID string, definition AgentCustomToolDefinition) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("account scope ID is required")
	}
	definition = NormalizeAgentCustomToolDefinition(definition)
	return s.store.PutJSON(KeyAgentCustomToolForAccount(accountScopeID, definition.Name), definition)
}

func (s *AgentStore) DeleteCustomTool(name string) error {
	return s.store.Delete(KeyAgentCustomTool(name))
}

func (s *AgentStore) DeleteCustomToolForAccount(accountScopeID, name string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("account scope ID is required")
	}
	return s.store.Delete(KeyAgentCustomToolForAccount(accountScopeID, name))
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

func (s *AgentStore) ListCustomToolsForAccount(accountScopeID string, limit int) ([]AgentCustomToolDefinition, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, fmt.Errorf("account scope ID is required")
	}
	if limit <= 0 {
		limit = 200
	}
	prefix := AgentCustomToolPrefixForAccount(accountScopeID)
	out := make([]AgentCustomToolDefinition, 0, limit)
	err := s.store.IteratePrefix(prefix, limit, func(key string, value []byte) error {
		var definition AgentCustomToolDefinition
		if err := json.Unmarshal(value, &definition); err != nil {
			return fmt.Errorf("decode agent custom tool: %w", err)
		}
		definition = NormalizeAgentCustomToolDefinition(definition)
		if definition.Name == "" {
			definition.Name = decodeKeyTail(key, prefix)
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
