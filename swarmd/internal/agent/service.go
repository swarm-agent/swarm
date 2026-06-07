package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	ModePrimary    = "primary"
	ModeSubagent   = "subagent"
	ModeBackground = "background"

	IntegrationBuilderAgentID   = "integration-builder"
	IntegrationBuilderAgentName = "Integration Builder"
)

func IntegrationBuilderPrompt() string {
	return strings.TrimSpace("You are the Integration Builder agent. Help the user design, inspect, and refine scoped Swarm Integration Packs. Prefer CLI-first or local-API-first designs, keep secrets outside Swarm, and only use the `manage-integrations` tool plus web/repository read tools. Do not run shell commands, write files directly, or expose hidden tools. When an integration is selected, use its workspace context automatically and explain concise next steps, risks, and validation status.")
}

func IsIntegrationBuilderAgentName(name string) bool {
	switch normalizeName(name) {
	case IntegrationBuilderAgentID, "integration_builder", "integration builder":
		return true
	default:
		return false
	}
}

func IntegrationBuilderToolContract() *pebblestore.AgentToolContract {
	return &pebblestore.AgentToolContract{
		Tools: map[string]pebblestore.AgentToolConfig{
			"read":                {Enabled: pebblestore.BoolPtr(true)},
			"search":              {Enabled: pebblestore.BoolPtr(true)},
			"list":                {Enabled: pebblestore.BoolPtr(true)},
			"websearch":           {Enabled: pebblestore.BoolPtr(true)},
			"webfetch":            {Enabled: pebblestore.BoolPtr(true)},
			"manage-integrations": {Enabled: pebblestore.BoolPtr(true)},
			"write":               {Enabled: pebblestore.BoolPtr(false)},
			"edit":                {Enabled: pebblestore.BoolPtr(false)},
			"bash":                {Enabled: pebblestore.BoolPtr(false)},
			"task":                {Enabled: pebblestore.BoolPtr(false)},
			"skill_use":           {Enabled: pebblestore.BoolPtr(false)},
			"plan_manage":         {Enabled: pebblestore.BoolPtr(false)},
			"ask_user":            {Enabled: pebblestore.BoolPtr(false)},
			"exit_plan_mode":      {Enabled: pebblestore.BoolPtr(false)},
		},
	}
}

func IntegrationBuilderProfile() pebblestore.AgentProfile {
	return pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name:                IntegrationBuilderAgentID,
		Mode:                ModeSubagent,
		Description:         "Hidden transient Integration Pack builder",
		Prompt:              IntegrationBuilderPrompt(),
		ExecutionSetting:    pebblestore.AgentExecutionSettingRead,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
		ToolContract:        IntegrationBuilderToolContract(),
		Enabled:             true,
	})
}

type Service struct {
	store   *pebblestore.AgentStore
	events  *pebblestore.EventLog
	publish func(pebblestore.EventEnvelope)
	mu      sync.Mutex
}

type State struct {
	Profiles       []pebblestore.AgentProfile              `json:"profiles"`
	CustomTools    []pebblestore.AgentCustomToolDefinition `json:"custom_tools,omitempty"`
	ActivePrimary  string                                  `json:"active_primary"`
	ActiveSubagent map[string]string                       `json:"active_subagent"`
	Version        int64                                   `json:"version"`
}

type UpsertInput struct {
	Name                string                         `json:"name"`
	Mode                string                         `json:"mode"`
	Description         string                         `json:"description"`
	Provider            string                         `json:"provider"`
	Model               string                         `json:"model"`
	Thinking            string                         `json:"thinking"`
	ProviderSet         bool                           `json:"-"`
	ModelSet            bool                           `json:"-"`
	ThinkingSet         bool                           `json:"-"`
	Prompt              string                         `json:"prompt"`
	RuntimeMode         string                         `json:"runtime_mode"`
	ExecutionSetting    string                         `json:"execution_setting"`
	ExitPlanModeEnabled *bool                          `json:"exit_plan_mode_enabled"`
	ToolScope           *pebblestore.AgentToolScope    `json:"tool_scope"`
	ToolContract        *pebblestore.AgentToolContract `json:"tool_contract"`
	Enabled             *bool                          `json:"enabled"`
}

type DeleteResult struct {
	Deleted       string `json:"deleted"`
	ActivePrimary string `json:"active_primary"`
}

type PreviewUpsertResult struct {
	Before *pebblestore.AgentProfile `json:"before,omitempty"`
	After  pebblestore.AgentProfile  `json:"after"`
	Exists bool                      `json:"exists"`
}

func NewService(store *pebblestore.AgentStore, events *pebblestore.EventLog) *Service {
	return &Service{
		store:  store,
		events: events,
	}
}

func (s *Service) SetEventPublisher(publish func(pebblestore.EventEnvelope)) {
	if s == nil {
		return
	}
	s.publish = publish
}

func (s *Service) EnsureDefaults() error {
	return s.ensureDefaultsForAccount("")
}

func (s *Service) EnsureDefaultsForAccount(accountScopeID string) error {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	return s.ensureDefaultsForAccount(accountScopeID)
}

func (s *Service) ensureDefaultsForAccount(accountScopeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	version, hasVersion, err := s.getVersionForAccountLocked(accountScopeID)
	if err != nil {
		return err
	}
	profiles, err := s.listProfilesForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return err
	}
	if !hasVersion && len(profiles) == 0 {
		now := time.Now().UnixMilli()
		for _, profile := range defaultProfiles(now) {
			if err := s.putProfileForAccountLocked(accountScopeID, profile); err != nil {
				return err
			}
		}
		if err := s.setActivePrimaryForAccountLocked(accountScopeID, "swarm"); err != nil {
			return err
		}
		for purpose, profileName := range defaultSubagentAssignments() {
			if err := s.setActiveSubagentForAccountLocked(accountScopeID, purpose, profileName); err != nil {
				return err
			}
		}
		return s.setVersionForAccountLocked(accountScopeID, 1)
	}

	now := time.Now().UnixMilli()
	if current, ok, err := s.getProfileForAccountLocked(accountScopeID, "memory"); err != nil {
		return err
	} else if ok && shouldReconcileBuiltInMemory(current) {
		profile := reconcileBuiltInMemory(current, now)
		if err := s.putProfileForAccountLocked(accountScopeID, profile); err != nil {
			return err
		}
	}
	if current, ok, err := s.getProfileForAccountLocked(accountScopeID, "commit"); err != nil {
		return err
	} else if ok && shouldRemoveBuiltInCommit(current) {
		if err := s.deleteProfileForAccountLocked(accountScopeID, "commit"); err != nil {
			return err
		}
	}
	if current, ok, err := s.getProfileForAccountLocked(accountScopeID, "parallel"); err != nil {
		return err
	} else if ok && shouldReconcileBuiltInParallel(current) {
		current.ExecutionSetting = pebblestore.AgentExecutionSettingReadWrite
		current.UpdatedAt = now
		if err := s.putProfileForAccountLocked(accountScopeID, current); err != nil {
			return err
		}
	}

	if !hasVersion {
		version = 1
		if err := s.setVersionForAccountLocked(accountScopeID, version); err != nil {
			return err
		}
	}

	activePrimary, ok, err := s.getActivePrimaryForAccountLocked(accountScopeID)
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(activePrimary) == "" {
		fallback, err := s.nextPrimaryForAccountLocked(accountScopeID, "")
		if err != nil {
			return err
		}
		if fallback != "" {
			if err := s.setActivePrimaryForAccountLocked(accountScopeID, fallback); err != nil {
				return err
			}
		}
		return nil
	}
	valid, err := s.activePrimaryValidForAccountLocked(accountScopeID, activePrimary)
	if err != nil {
		return err
	}
	if valid {
		return nil
	}
	fallback, err := s.nextPrimaryForAccountLocked(accountScopeID, activePrimary)
	if err != nil {
		return err
	}
	if fallback == "" {
		return nil
	}
	return s.setActivePrimaryForAccountLocked(accountScopeID, fallback)
}

func defaultMemoryPrompt() string {
	return strings.TrimSpace("" +
		"You are Memory, the durable artifacts agent.\n" +
		"Produce commit messages, session titles, compact summaries, and durable run artifacts that are accurate and traceable.\n" +
		"When handling a background commit, inspect git status and diffs, stage the correct files, and create exactly one accurate commit.\n" +
		"Do not push unless the user explicitly requested push.")
}

func defaultSwarmToolContract() *pebblestore.AgentToolContract {
	return &pebblestore.AgentToolContract{
		Preset: "custom",
		Tools: map[string]pebblestore.AgentToolConfig{
			"read":                {Enabled: pebblestore.BoolPtr(true)},
			"search":              {Enabled: pebblestore.BoolPtr(true)},
			"list":                {Enabled: pebblestore.BoolPtr(true)},
			"write":               {Enabled: pebblestore.BoolPtr(true)},
			"edit":                {Enabled: pebblestore.BoolPtr(true)},
			"bash":                {Enabled: pebblestore.BoolPtr(true)},
			"websearch":           {Enabled: pebblestore.BoolPtr(true)},
			"webfetch":            {Enabled: pebblestore.BoolPtr(true)},
			"webdownload":         {Enabled: pebblestore.BoolPtr(true)},
			"task":                {Enabled: pebblestore.BoolPtr(true)},
			"skill_use":           {Enabled: pebblestore.BoolPtr(true)},
			"manage_skill":        {Enabled: pebblestore.BoolPtr(true)},
			"manage_agent":        {Enabled: pebblestore.BoolPtr(true)},
			"manage_flow":         {Enabled: pebblestore.BoolPtr(true)},
			"manage_integrations": {Enabled: pebblestore.BoolPtr(true)},
			"manage_image":        {Enabled: pebblestore.BoolPtr(true)},
			"manage_theme":        {Enabled: pebblestore.BoolPtr(true)},
			"manage_worktree":     {Enabled: pebblestore.BoolPtr(true)},
			"manage_todos":        {Enabled: pebblestore.BoolPtr(true)},
			"plan_manage":         {Enabled: pebblestore.BoolPtr(true)},
			"ask_user":            {Enabled: pebblestore.BoolPtr(true)},
			"exit_plan_mode":      {Enabled: pebblestore.BoolPtr(true)},
		},
	}
}

func defaultExplorerToolContract() *pebblestore.AgentToolContract {
	return &pebblestore.AgentToolContract{Preset: "read_only"}
}

func defaultReadWriteSubagentToolContract() *pebblestore.AgentToolContract {
	return &pebblestore.AgentToolContract{Preset: "read_write"}
}

func defaultMemoryToolContract() *pebblestore.AgentToolContract {
	return &pebblestore.AgentToolContract{
		Preset: "background_commit",
		Tools: map[string]pebblestore.AgentToolConfig{
			"git_status":     {Enabled: pebblestore.BoolPtr(true)},
			"git_diff":       {Enabled: pebblestore.BoolPtr(true)},
			"git_add":        {Enabled: pebblestore.BoolPtr(true)},
			"git_commit":     {Enabled: pebblestore.BoolPtr(true)},
			"bash":           {Enabled: pebblestore.BoolPtr(false)},
			"write":          {Enabled: pebblestore.BoolPtr(false)},
			"edit":           {Enabled: pebblestore.BoolPtr(false)},
			"websearch":      {Enabled: pebblestore.BoolPtr(false)},
			"webfetch":       {Enabled: pebblestore.BoolPtr(false)},
			"skill_use":      {Enabled: pebblestore.BoolPtr(false)},
			"plan_manage":    {Enabled: pebblestore.BoolPtr(false)},
			"ask_user":       {Enabled: pebblestore.BoolPtr(false)},
			"exit_plan_mode": {Enabled: pebblestore.BoolPtr(false)},
			"task":           {Enabled: pebblestore.BoolPtr(false)},
		},
	}
}

func shouldReconcileBuiltInMemory(profile pebblestore.AgentProfile) bool {
	if strings.TrimSpace(profile.Name) != "memory" {
		return false
	}
	if profile.Mode != ModeSubagent || !profile.Enabled {
		return true
	}
	if pebblestore.AgentProfileRuntimeMode(profile) != pebblestore.AgentRuntimeModeReadWrite {
		return true
	}
	if profile.ToolContract == nil || strings.TrimSpace(profile.ToolContract.Preset) != "background_commit" {
		return true
	}
	for _, toolName := range []string{"git_status", "git_diff", "git_add", "git_commit"} {
		state, ok := profile.ToolContract.Tools[toolName]
		if !ok || state.Enabled == nil || !*state.Enabled {
			return true
		}
	}
	for _, toolName := range []string{"websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "task", "bash", "write", "edit", "exit_plan_mode"} {
		state, ok := profile.ToolContract.Tools[toolName]
		if !ok || state.Enabled == nil || *state.Enabled {
			return true
		}
	}
	return false
}

func reconcileBuiltInMemory(profile pebblestore.AgentProfile, now int64) pebblestore.AgentProfile {
	profile.Name = "memory"
	profile.Mode = ModeSubagent
	profile.Description = "Durable artifacts and commits"
	profile.RuntimeMode = pebblestore.AgentRuntimeModeReadWrite
	profile.ExecutionSetting = pebblestore.AgentExecutionSettingReadWrite
	profile.ExitPlanModeEnabled = pebblestore.BoolPtr(false)
	profile.ToolContract = defaultMemoryToolContract()
	profile.Enabled = true
	profile.UpdatedAt = now
	if prompt := strings.TrimSpace(profile.Prompt); prompt == "" || strings.EqualFold(prompt, oldDefaultMemoryPrompt()) {
		profile.Prompt = defaultMemoryPrompt()
	}
	return pebblestore.NormalizeAgentProfile(profile)
}

func oldDefaultMemoryPrompt() string {
	return strings.TrimSpace("" +
		"You are Memory, a subagent for durable artifacts.\n" +
		"Produce commit messages, session titles, and compact summaries that are accurate and traceable.")
}

func shouldRemoveBuiltInCommit(profile pebblestore.AgentProfile) bool {
	if strings.TrimSpace(profile.Name) != "commit" {
		return false
	}
	if strings.TrimSpace(profile.Description) == "Commit specialist" {
		return true
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(profile.Prompt)), "you are commit") {
		return true
	}
	return profile.Mode == ModeBackground && profile.ToolContract != nil && strings.TrimSpace(profile.ToolContract.Preset) == "background_commit"
}

func shouldReconcileBuiltInParallel(profile pebblestore.AgentProfile) bool {
	if strings.TrimSpace(profile.Name) != "parallel" {
		return false
	}
	if profile.Mode != ModeSubagent {
		return true
	}
	return pebblestore.NormalizeAgentExecutionSetting(profile.ExecutionSetting) != pebblestore.AgentExecutionSettingReadWrite
}

func (s *Service) ListState(limit int) (State, error) {
	return s.listStateForAccount("", limit)
}

func (s *Service) ListStateForAccount(accountScopeID string, limit int) (State, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return State{}, err
	}
	return s.listStateForAccount(accountScopeID, limit)
}

func (s *Service) listStateForAccount(accountScopeID string, limit int) (State, error) {
	return s.currentStateForAccountLocked(accountScopeID, limit)
}

func (s *Service) ReplaceManagedState(state State, syncProfiles, syncCustomTools bool) (State, int64, *pebblestore.EventEnvelope, error) {
	return s.replaceManagedStateForAccount("", state, syncProfiles, syncCustomTools)
}

func (s *Service) ReplaceManagedStateForAccount(accountScopeID string, state State, syncProfiles, syncCustomTools bool) (State, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return State{}, 0, nil, errors.New("account scope ID is required")
	}
	return s.replaceManagedStateForAccount(accountScopeID, state, syncProfiles, syncCustomTools)
}

func (s *Service) replaceManagedStateForAccount(accountScopeID string, state State, syncProfiles, syncCustomTools bool) (State, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s == nil || s.store == nil {
		return State{}, 0, nil, errors.New("agent store is not configured")
	}
	if syncCustomTools {
		currentTools, err := s.listCustomToolsForManagedAccountLocked(accountScopeID, 2000)
		if err != nil {
			return State{}, 0, nil, err
		}
		desiredTools := make(map[string]pebblestore.AgentCustomToolDefinition, len(state.CustomTools))
		toolNames := make([]string, 0, len(state.CustomTools))
		for _, raw := range state.CustomTools {
			definition := pebblestore.NormalizeAgentCustomToolDefinition(raw)
			if definition.Name == "" {
				continue
			}
			definition.UpdatedAt = time.Now().UnixMilli()
			if _, ok := desiredTools[definition.Name]; ok {
				continue
			}
			desiredTools[definition.Name] = definition
			toolNames = append(toolNames, definition.Name)
		}
		sort.Strings(toolNames)
		for _, current := range currentTools {
			name := pebblestore.NormalizeAgentCustomToolName(current.Name)
			if name == "" {
				continue
			}
			if _, ok := desiredTools[name]; ok {
				continue
			}
			if err := s.deleteCustomToolForManagedAccountLocked(accountScopeID, name); err != nil {
				return State{}, 0, nil, err
			}
		}
		for _, name := range toolNames {
			if err := s.putCustomToolForManagedAccountLocked(accountScopeID, desiredTools[name]); err != nil {
				return State{}, 0, nil, err
			}
		}
	}
	if syncProfiles {
		currentProfiles, err := s.listProfilesForManagedAccountLocked(accountScopeID, 2000)
		if err != nil {
			return State{}, 0, nil, err
		}
		desiredProfiles := make(map[string]pebblestore.AgentProfile, len(state.Profiles))
		profileNames := make([]string, 0, len(state.Profiles))
		for _, raw := range state.Profiles {
			profile := pebblestore.NormalizeAgentProfile(raw)
			name := normalizeName(profile.Name)
			if name == "" {
				continue
			}
			profile.UpdatedAt = time.Now().UnixMilli()
			if _, ok := desiredProfiles[name]; ok {
				continue
			}
			desiredProfiles[name] = profile
			profileNames = append(profileNames, name)
		}
		sort.Strings(profileNames)
		for _, current := range currentProfiles {
			name := normalizeName(current.Name)
			if name == "" || current.Protected {
				continue
			}
			if _, ok := desiredProfiles[name]; ok {
				continue
			}
			if err := s.deleteProfileForManagedAccountLocked(accountScopeID, name); err != nil {
				return State{}, 0, nil, err
			}
		}
		for _, name := range profileNames {
			if err := s.putProfileForManagedAccountLocked(accountScopeID, desiredProfiles[name]); err != nil {
				return State{}, 0, nil, err
			}
		}
		currentAssignments, err := s.getActiveSubagentsForManagedAccountLocked(accountScopeID, 200)
		if err != nil {
			return State{}, 0, nil, err
		}
		for purpose := range currentAssignments {
			if _, ok := state.ActiveSubagent[purpose]; ok {
				continue
			}
			if err := s.deleteActiveSubagentForManagedAccountLocked(accountScopeID, purpose); err != nil {
				return State{}, 0, nil, err
			}
		}
		assignmentKeys := make([]string, 0, len(state.ActiveSubagent))
		for purpose := range state.ActiveSubagent {
			purpose = normalizeName(purpose)
			if purpose == "" {
				continue
			}
			assignmentKeys = append(assignmentKeys, purpose)
		}
		sort.Strings(assignmentKeys)
		for _, purpose := range assignmentKeys {
			name := normalizeName(state.ActiveSubagent[purpose])
			if name == "" {
				continue
			}
			if err := s.setActiveSubagentForManagedAccountLocked(accountScopeID, purpose, name); err != nil {
				return State{}, 0, nil, err
			}
		}
		activePrimary := normalizeName(state.ActivePrimary)
		if activePrimary != "" {
			if err := s.setActivePrimaryForManagedAccountLocked(accountScopeID, activePrimary); err != nil {
				return State{}, 0, nil, err
			}
		}
	}
	version, err := s.bumpVersionForManagedAccountLocked(accountScopeID)
	if err != nil {
		return State{}, 0, nil, err
	}
	current, err := s.currentStateForManagedAccountLocked(accountScopeID, 2000)
	if err != nil {
		return State{}, 0, nil, err
	}
	entityID := ""
	if strings.TrimSpace(accountScopeID) != "" {
		entityID = "account:" + strings.TrimSpace(accountScopeID)
	}
	env, err := s.appendEventLocked("agent.state.synced", entityID, map[string]any{
		"sync_profiles":     syncProfiles,
		"sync_custom_tools": syncCustomTools,
		"state":             current,
		"version":           version,
	})
	if err != nil {
		return State{}, 0, nil, err
	}
	return current, version, &env, nil
}

func (s *Service) listCustomToolsForManagedAccountLocked(accountScopeID string, limit int) ([]pebblestore.AgentCustomToolDefinition, error) {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.ListCustomToolsForAccount(accountScopeID, limit)
	}
	return s.store.ListCustomTools(limit)
}

func (s *Service) putCustomToolForManagedAccountLocked(accountScopeID string, definition pebblestore.AgentCustomToolDefinition) error {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.PutCustomToolForAccount(accountScopeID, definition)
	}
	return s.store.PutCustomTool(definition)
}

func (s *Service) deleteCustomToolForManagedAccountLocked(accountScopeID, name string) error {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.DeleteCustomToolForAccount(accountScopeID, name)
	}
	return s.store.DeleteCustomTool(name)
}

func (s *Service) listProfilesForManagedAccountLocked(accountScopeID string, limit int) ([]pebblestore.AgentProfile, error) {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.ListProfilesForAccount(accountScopeID, limit)
	}
	return s.store.ListProfiles(limit)
}

func (s *Service) putProfileForManagedAccountLocked(accountScopeID string, profile pebblestore.AgentProfile) error {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.PutProfileForAccount(accountScopeID, profile)
	}
	return s.store.PutProfile(profile)
}

func (s *Service) deleteProfileForManagedAccountLocked(accountScopeID, name string) error {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.DeleteProfileForAccount(accountScopeID, name)
	}
	return s.store.DeleteProfile(name)
}

func (s *Service) getActiveSubagentsForManagedAccountLocked(accountScopeID string, limit int) (map[string]string, error) {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.GetActiveSubagentsForAccount(accountScopeID, limit)
	}
	return s.store.GetActiveSubagents(limit)
}

func (s *Service) setActiveSubagentForManagedAccountLocked(accountScopeID, purpose, name string) error {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.SetActiveSubagentForAccount(accountScopeID, purpose, name)
	}
	return s.store.SetActiveSubagent(purpose, name)
}

func (s *Service) deleteActiveSubagentForManagedAccountLocked(accountScopeID, purpose string) error {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.DeleteActiveSubagentForAccount(accountScopeID, purpose)
	}
	return s.store.DeleteActiveSubagent(purpose)
}

func (s *Service) setActivePrimaryForManagedAccountLocked(accountScopeID, name string) error {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.store.SetActivePrimaryForAccount(accountScopeID, name)
	}
	return s.store.SetActivePrimary(name)
}

func (s *Service) bumpVersionForManagedAccountLocked(accountScopeID string) (int64, error) {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.bumpVersionForAccountLocked(accountScopeID)
	}
	return s.bumpVersionLocked()
}

func (s *Service) currentStateForManagedAccountLocked(accountScopeID string, limit int) (State, error) {
	if strings.TrimSpace(accountScopeID) != "" {
		return s.currentStateForAccountLocked(accountScopeID, limit)
	}
	return s.currentStateLocked(limit)
}

func (s *Service) GetCustomTool(name string) (pebblestore.AgentCustomToolDefinition, bool, error) {
	name = pebblestore.NormalizeAgentCustomToolName(name)
	if name == "" {
		return pebblestore.AgentCustomToolDefinition{}, false, errors.New("custom tool name is required")
	}
	return s.store.GetCustomTool(name)
}

func (s *Service) GetCustomToolForAccount(accountScopeID, name string) (pebblestore.AgentCustomToolDefinition, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	name = pebblestore.NormalizeAgentCustomToolName(name)
	if accountScopeID == "" {
		return pebblestore.AgentCustomToolDefinition{}, false, errors.New("account scope ID is required")
	}
	if name == "" {
		return pebblestore.AgentCustomToolDefinition{}, false, errors.New("custom tool name is required")
	}
	return s.store.GetCustomToolForAccount(accountScopeID, name)
}

func (s *Service) ListCustomTools(limit int) ([]pebblestore.AgentCustomToolDefinition, error) {
	return s.store.ListCustomTools(limit)
}

func (s *Service) ListCustomToolsForAccount(accountScopeID string, limit int) ([]pebblestore.AgentCustomToolDefinition, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope ID is required")
	}
	return s.store.ListCustomToolsForAccount(accountScopeID, limit)
}

func (s *Service) PutCustomTool(definition pebblestore.AgentCustomToolDefinition) (pebblestore.AgentCustomToolDefinition, error) {
	return s.putCustomToolForAccount("", definition)
}

func (s *Service) PutCustomToolForAccount(accountScopeID string, definition pebblestore.AgentCustomToolDefinition) (pebblestore.AgentCustomToolDefinition, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return pebblestore.AgentCustomToolDefinition{}, errors.New("account scope ID is required")
	}
	return s.putCustomToolForAccount(accountScopeID, definition)
}

func (s *Service) putCustomToolForAccount(accountScopeID string, definition pebblestore.AgentCustomToolDefinition) (pebblestore.AgentCustomToolDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	definition = pebblestore.NormalizeAgentCustomToolDefinition(definition)
	if definition.Name == "" {
		return pebblestore.AgentCustomToolDefinition{}, errors.New("custom tool name is required")
	}
	if definition.Kind == "" {
		return pebblestore.AgentCustomToolDefinition{}, errors.New("custom tool kind is required")
	}
	if definition.Command == "" {
		return pebblestore.AgentCustomToolDefinition{}, errors.New("custom tool command is required")
	}
	eventType := "agent.custom_tool.created"
	var exists bool
	var err error
	if strings.TrimSpace(accountScopeID) == "" {
		_, exists, err = s.store.GetCustomTool(definition.Name)
	} else {
		_, exists, err = s.store.GetCustomToolForAccount(accountScopeID, definition.Name)
	}
	if err != nil {
		return pebblestore.AgentCustomToolDefinition{}, err
	}
	if exists {
		eventType = "agent.custom_tool.updated"
	}
	definition.UpdatedAt = time.Now().UnixMilli()
	if strings.TrimSpace(accountScopeID) == "" {
		err = s.store.PutCustomTool(definition)
	} else {
		err = s.store.PutCustomToolForAccount(accountScopeID, definition)
	}
	if err != nil {
		return pebblestore.AgentCustomToolDefinition{}, err
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return pebblestore.AgentCustomToolDefinition{}, err
	}
	state, err := s.currentStateLocked(2000)
	if err != nil {
		return pebblestore.AgentCustomToolDefinition{}, err
	}
	if _, err := s.appendEventLocked(eventType, definition.Name, map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"custom_tool":      definition,
		"state":            state,
		"version":          version,
	}); err != nil {
		return pebblestore.AgentCustomToolDefinition{}, err
	}
	return definition, nil
}

func (s *Service) DeleteCustomTool(name string) (bool, error) {
	return s.deleteCustomToolForAccount("", name)
}

func (s *Service) DeleteCustomToolForAccount(accountScopeID, name string) (bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return false, errors.New("account scope ID is required")
	}
	return s.deleteCustomToolForAccount(accountScopeID, name)
}

func (s *Service) deleteCustomToolForAccount(accountScopeID, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name = pebblestore.NormalizeAgentCustomToolName(name)
	if name == "" {
		return false, errors.New("custom tool name is required")
	}
	var definition pebblestore.AgentCustomToolDefinition
	var ok bool
	var err error
	if strings.TrimSpace(accountScopeID) == "" {
		definition, ok, err = s.store.GetCustomTool(name)
	} else {
		definition, ok, err = s.store.GetCustomToolForAccount(accountScopeID, name)
	}
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if strings.TrimSpace(accountScopeID) == "" {
		err = s.store.DeleteCustomTool(name)
	} else {
		err = s.store.DeleteCustomToolForAccount(accountScopeID, name)
	}
	if err != nil {
		return false, err
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return false, err
	}
	state, err := s.currentStateLocked(2000)
	if err != nil {
		return false, err
	}
	if _, err := s.appendEventLocked("agent.custom_tool.deleted", name, map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"deleted":          name,
		"custom_tool":      definition,
		"state":            state,
		"version":          version,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) AssignCustomTool(agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	return s.assignCustomToolForAccount("", agentName, toolName)
}

func (s *Service) AssignCustomToolForAccount(accountScopeID, agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return pebblestore.AgentProfile{}, 0, nil, errors.New("account scope ID is required")
	}
	return s.assignCustomToolForAccount(accountScopeID, agentName, toolName)
}

func (s *Service) assignCustomToolForAccount(accountScopeID, agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agentName = normalizeName(agentName)
	if agentName == "" {
		return pebblestore.AgentProfile{}, 0, nil, errors.New("agent name is required")
	}
	toolName = pebblestore.NormalizeAgentCustomToolName(toolName)
	if toolName == "" {
		return pebblestore.AgentProfile{}, 0, nil, errors.New("custom tool name is required")
	}
	var toolOK bool
	var err error
	if strings.TrimSpace(accountScopeID) == "" {
		_, toolOK, err = s.store.GetCustomTool(toolName)
	} else {
		_, toolOK, err = s.store.GetCustomToolForAccount(accountScopeID, toolName)
	}
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	if !toolOK {
		return pebblestore.AgentProfile{}, 0, nil, fmt.Errorf("custom tool %q not found", toolName)
	}
	profile, ok, err := s.getProfileForAccountLocked(accountScopeID, agentName)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	if !ok {
		return pebblestore.AgentProfile{}, 0, nil, fmt.Errorf("agent %q not found", agentName)
	}
	contract := pebblestore.CloneAgentToolContract(profile.ToolContract)
	if contract == nil {
		contract = &pebblestore.AgentToolContract{}
	}
	if contract.Tools == nil {
		contract.Tools = make(map[string]pebblestore.AgentToolConfig)
	}
	contract.Tools[toolName] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
	profile.ToolContract = pebblestore.NormalizeAgentToolContract(contract)
	if runtimeMode := pebblestore.AgentRuntimeModeForToolContract(profile.ToolContract); runtimeMode == pebblestore.AgentRuntimeModeReadWrite && runtimeMode != pebblestore.AgentProfileRuntimeMode(profile) {
		return pebblestore.AgentProfile{}, 0, nil, fmt.Errorf("custom tool %q requires runtime_mode=%s", toolName, runtimeMode)
	}
	profile.UpdatedAt = time.Now().UnixMilli()
	if err := s.putProfileForAccountLocked(accountScopeID, profile); err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	version, err := s.bumpVersionForAccountLocked(accountScopeID)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	state, err := s.currentStateForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.custom_tool.assigned", agentName, map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"agent":            agentName,
		"tool_name":        toolName,
		"profile":          profile,
		"state":            state,
		"version":          version,
	})
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	return profile, version, &env, nil
}

func (s *Service) UnassignCustomTool(agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	return s.unassignCustomToolForAccount("", agentName, toolName)
}

func (s *Service) UnassignCustomToolForAccount(accountScopeID, agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return pebblestore.AgentProfile{}, 0, nil, errors.New("account scope ID is required")
	}
	return s.unassignCustomToolForAccount(accountScopeID, agentName, toolName)
}

func (s *Service) unassignCustomToolForAccount(accountScopeID, agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agentName = normalizeName(agentName)
	if agentName == "" {
		return pebblestore.AgentProfile{}, 0, nil, errors.New("agent name is required")
	}
	toolName = pebblestore.NormalizeAgentCustomToolName(toolName)
	if toolName == "" {
		return pebblestore.AgentProfile{}, 0, nil, errors.New("custom tool name is required")
	}
	profile, ok, err := s.getProfileForAccountLocked(accountScopeID, agentName)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	if !ok {
		return pebblestore.AgentProfile{}, 0, nil, fmt.Errorf("agent %q not found", agentName)
	}
	contract := pebblestore.CloneAgentToolContract(profile.ToolContract)
	if contract == nil || len(contract.Tools) == 0 {
		return profile, 0, nil, nil
	}
	if _, ok := contract.Tools[toolName]; !ok {
		return profile, 0, nil, nil
	}
	delete(contract.Tools, toolName)
	profile.ToolContract = pebblestore.NormalizeAgentToolContract(contract)
	profile.UpdatedAt = time.Now().UnixMilli()
	if err := s.putProfileForAccountLocked(accountScopeID, profile); err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	version, err := s.bumpVersionForAccountLocked(accountScopeID)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	state, err := s.currentStateForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.custom_tool.unassigned", agentName, map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"agent":            agentName,
		"tool_name":        toolName,
		"profile":          profile,
		"state":            state,
		"version":          version,
	})
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	return profile, version, &env, nil
}

func (s *Service) GetProfile(name string) (pebblestore.AgentProfile, bool, error) {
	return s.getProfileForAccount("", name)
}

func (s *Service) GetProfileForAccount(accountScopeID, name string) (pebblestore.AgentProfile, bool, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return pebblestore.AgentProfile{}, false, err
	}
	return s.getProfileForAccount(accountScopeID, name)
}

func (s *Service) getProfileForAccount(accountScopeID, name string) (pebblestore.AgentProfile, bool, error) {
	name = normalizeName(name)
	if name == "" {
		return pebblestore.AgentProfile{}, false, errors.New("agent name is required")
	}
	if IsIntegrationBuilderAgentName(name) {
		return pebblestore.AgentProfile{}, false, nil
	}
	return s.getProfileForAccountLocked(accountScopeID, name)
}

func (s *Service) Upsert(input UpsertInput) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	return s.upsertForAccount("", input)
}

func (s *Service) UpsertForAccount(accountScopeID string, input UpsertInput) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	return s.upsertForAccount(accountScopeID, input)
}

func (s *Service) upsertForAccount(accountScopeID string, input UpsertInput) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	profile, err := normalizeUpsertInput(input)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	if IsIntegrationBuilderAgentName(profile.Name) {
		return pebblestore.AgentProfile{}, 0, nil, fmt.Errorf("agent %q is reserved for the transient integration builder", profile.Name)
	}
	existing, ok, err := s.getProfileForAccountLocked(accountScopeID, profile.Name)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	if ok {
		if strings.TrimSpace(profile.Mode) == "" {
			profile.Mode = existing.Mode
		}
		if strings.TrimSpace(profile.Description) == "" {
			profile.Description = existing.Description
		}
		if !stringFieldProvided(input.ProviderSet, input.Provider) {
			profile.Provider = existing.Provider
		}
		if !stringFieldProvided(input.ModelSet, input.Model) {
			profile.Model = existing.Model
		}
		if !stringFieldProvided(input.ThinkingSet, input.Thinking) {
			profile.Thinking = existing.Thinking
		}
		if strings.TrimSpace(profile.Prompt) == "" {
			profile.Prompt = existing.Prompt
		}
		if strings.TrimSpace(profile.RuntimeMode) == "" {
			profile.RuntimeMode = existing.RuntimeMode
		}
		if strings.TrimSpace(profile.ExecutionSetting) == "" {
			profile.ExecutionSetting = existing.ExecutionSetting
		}
		if input.ExitPlanModeEnabled == nil {
			profile.ExitPlanModeEnabled = pebblestore.CloneBoolPtr(existing.ExitPlanModeEnabled)
		}
		if input.ToolScope == nil {
			profile.ToolScope = pebblestore.CloneAgentToolScope(existing.ToolScope)
		}
		if input.ToolContract == nil {
			profile.ToolContract = pebblestore.CloneAgentToolContract(existing.ToolContract)
		}
	}
	if profile.Name == "swarm" {
		profile.Mode = ModePrimary
		profile.Enabled = true
		profile.ExitPlanModeEnabled = pebblestore.BoolPtr(true)
	}
	if profile.Mode == ModePrimary {
		if profile.Name == "swarm" {
			profile.Provider = ""
			profile.Model = ""
			profile.Thinking = ""
		}
	}
	profile, err = finalizeRuntimeProfile(profile, input, ok)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	profile = pebblestore.NormalizeAgentProfile(profile)
	profile.UpdatedAt = time.Now().UnixMilli()
	if err := s.putProfileForAccountLocked(accountScopeID, profile); err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	version, err := s.bumpVersionForAccountLocked(accountScopeID)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	state, err := s.currentStateForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	eventType := "agent.profile.updated"
	if !ok {
		eventType = "agent.profile.created"
	}

	env, err := s.appendEventLocked(eventType, profile.Name, map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"profile":          profile,
		"state":            state,
		"version":          version,
	})
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	return profile, version, &env, nil
}

func (s *Service) ActivatePrimary(name string) (string, int64, *pebblestore.EventEnvelope, error) {
	return s.activatePrimaryForAccount("", name)
}

func (s *Service) ActivatePrimaryForAccount(accountScopeID, name string) (string, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return "", 0, nil, err
	}
	return s.activatePrimaryForAccount(accountScopeID, name)
}

func (s *Service) activatePrimaryForAccount(accountScopeID, name string) (string, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name = normalizeName(name)
	if name == "" {
		return "", 0, nil, errors.New("agent name is required")
	}
	profile, ok, err := s.getProfileForAccountLocked(accountScopeID, name)
	if err != nil {
		return "", 0, nil, err
	}
	if !ok {
		return "", 0, nil, fmt.Errorf("agent %q not found", name)
	}
	if !profile.Enabled {
		return "", 0, nil, fmt.Errorf("agent %q is disabled", name)
	}
	if profile.Mode != ModePrimary {
		return "", 0, nil, fmt.Errorf("agent %q is not a primary agent", name)
	}

	current, ok, err := s.getActivePrimaryForAccountLocked(accountScopeID)
	if err != nil {
		return "", 0, nil, err
	}
	version, _, err := s.getVersionForAccountLocked(accountScopeID)
	if err != nil {
		return "", 0, nil, err
	}
	if ok && strings.TrimSpace(current) == name {
		return name, version, nil, nil
	}

	if err := s.setActivePrimaryForAccountLocked(accountScopeID, name); err != nil {
		return "", 0, nil, err
	}
	version, err = s.bumpVersionForAccountLocked(accountScopeID)
	if err != nil {
		return "", 0, nil, err
	}
	state, err := s.currentStateForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return "", 0, nil, err
	}
	env, err := s.appendEventLocked("agent.active.updated", name, map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"active_primary":   name,
		"state":            state,
		"version":          version,
	})
	if err != nil {
		return "", 0, nil, err
	}
	return name, version, &env, nil
}

func (s *Service) Delete(name string) (DeleteResult, int64, *pebblestore.EventEnvelope, error) {
	return s.deleteForAccount("", name)
}

func (s *Service) DeleteForAccount(accountScopeID, name string) (DeleteResult, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	return s.deleteForAccount(accountScopeID, name)
}

func (s *Service) deleteForAccount(accountScopeID, name string) (DeleteResult, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name = normalizeName(name)
	if name == "" {
		return DeleteResult{}, 0, nil, errors.New("agent name is required")
	}
	if IsIntegrationBuilderAgentName(name) {
		return DeleteResult{}, 0, nil, fmt.Errorf("agent %q is transient and cannot be deleted", name)
	}
	if name == "memory" {
		return DeleteResult{}, 0, nil, fmt.Errorf("agent %q is protected and cannot be deleted", name)
	}

	target, ok, err := s.getProfileForAccountLocked(accountScopeID, name)
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	if !ok {
		return DeleteResult{}, 0, nil, fmt.Errorf("agent %q not found", name)
	}
	if target.Mode == ModePrimary {
		fallback, err := s.nextPrimaryForAccountLocked(accountScopeID, name)
		if err != nil {
			return DeleteResult{}, 0, nil, err
		}
		if fallback == "" {
			return DeleteResult{}, 0, nil, fmt.Errorf("agent %q is the last primary and cannot be deleted", name)
		}
		activePrimary, _, err := s.getActivePrimaryForAccountLocked(accountScopeID)
		if err != nil {
			return DeleteResult{}, 0, nil, err
		}
		validActivePrimary, err := s.activePrimaryValidForAccountLocked(accountScopeID, activePrimary)
		if err != nil {
			return DeleteResult{}, 0, nil, err
		}
		if strings.EqualFold(strings.TrimSpace(activePrimary), name) || !validActivePrimary {
			if err := s.setActivePrimaryForAccountLocked(accountScopeID, fallback); err != nil {
				return DeleteResult{}, 0, nil, err
			}
		}
	}
	if target.Mode == ModeSubagent {
		activeSubagents, err := s.getActiveSubagentsForAccountLocked(accountScopeID, 200)
		if err != nil {
			return DeleteResult{}, 0, nil, err
		}
		for purpose, assigned := range activeSubagents {
			if !strings.EqualFold(strings.TrimSpace(assigned), name) {
				continue
			}
			_ = s.deleteActiveSubagentForAccountLocked(accountScopeID, purpose)
		}
	}
	if err := s.deleteProfileForAccountLocked(accountScopeID, name); err != nil {
		return DeleteResult{}, 0, nil, err
	}
	version, err := s.bumpVersionForAccountLocked(accountScopeID)
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	activePrimary, _, err := s.getActivePrimaryForAccountLocked(accountScopeID)
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	result := DeleteResult{
		Deleted:       name,
		ActivePrimary: activePrimary,
	}
	state, err := s.currentStateForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.profile.deleted", name, map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"deleted":          result.Deleted,
		"active_primary":   result.ActivePrimary,
		"state":            state,
		"version":          version,
	})
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	return result, version, &env, nil
}

func (s *Service) RestoreDefaults() (State, int64, *pebblestore.EventEnvelope, error) {
	return s.restoreDefaultsForAccount("")
}

func (s *Service) RestoreDefaultsForAccount(accountScopeID string) (State, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return State{}, 0, nil, err
	}
	return s.restoreDefaultsForAccount(accountScopeID)
}

func (s *Service) restoreDefaultsForAccount(accountScopeID string) (State, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	defaults := defaultProfiles(now)
	restored := make([]string, 0, len(defaults))
	for _, profile := range defaults {
		if err := s.putProfileForAccountLocked(accountScopeID, profile); err != nil {
			return State{}, 0, nil, err
		}
		restored = append(restored, profile.Name)
	}

	activePrimary, _, err := s.getActivePrimaryForAccountLocked(accountScopeID)
	if err != nil {
		return State{}, 0, nil, err
	}
	validActivePrimary, err := s.activePrimaryValidForAccountLocked(accountScopeID, activePrimary)
	if err != nil {
		return State{}, 0, nil, err
	}
	if !validActivePrimary {
		if err := s.setActivePrimaryForAccountLocked(accountScopeID, "swarm"); err != nil {
			return State{}, 0, nil, err
		}
	}
	for purpose, profileName := range defaultSubagentAssignments() {
		if err := s.setActiveSubagentForAccountLocked(accountScopeID, purpose, profileName); err != nil {
			return State{}, 0, nil, err
		}
	}

	version, err := s.bumpVersionForAccountLocked(accountScopeID)
	if err != nil {
		return State{}, 0, nil, err
	}
	state, err := s.currentStateForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return State{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.defaults.restored", "", map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"restored":         restored,
		"active_primary":   state.ActivePrimary,
		"active_subagent":  state.ActiveSubagent,
		"state":            state,
		"version":          version,
	})
	if err != nil {
		return State{}, 0, nil, err
	}
	return state, version, &env, nil
}

func (s *Service) ResetDefaults() (State, int64, *pebblestore.EventEnvelope, error) {
	return s.resetDefaultsForAccount("")
}

func (s *Service) ResetDefaultsForAccount(accountScopeID string) (State, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return State{}, 0, nil, err
	}
	return s.resetDefaultsForAccount(accountScopeID)
}

func (s *Service) resetDefaultsForAccount(accountScopeID string) (State, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	defaults := defaultProfiles(now)
	defaultNames := make(map[string]struct{}, len(defaults))
	for _, profile := range defaults {
		defaultNames[normalizeName(profile.Name)] = struct{}{}
	}

	profiles, err := s.listProfilesForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return State{}, 0, nil, err
	}
	deletedProfiles := make([]string, 0)
	for _, profile := range profiles {
		name := normalizeName(profile.Name)
		if _, ok := defaultNames[name]; ok {
			continue
		}
		if err := s.deleteProfileForAccountLocked(accountScopeID, name); err != nil {
			return State{}, 0, nil, err
		}
		deletedProfiles = append(deletedProfiles, name)
	}

	var customTools []pebblestore.AgentCustomToolDefinition
	if strings.TrimSpace(accountScopeID) == "" {
		customTools, err = s.store.ListCustomTools(2000)
	} else {
		customTools, err = s.store.ListCustomToolsForAccount(accountScopeID, 2000)
	}
	if err != nil {
		return State{}, 0, nil, err
	}
	deletedTools := make([]string, 0, len(customTools))
	for _, tool := range customTools {
		name := pebblestore.NormalizeAgentCustomToolName(tool.Name)
		if name == "" {
			continue
		}
		if strings.TrimSpace(accountScopeID) == "" {
			err = s.store.DeleteCustomTool(name)
		} else {
			err = s.store.DeleteCustomToolForAccount(accountScopeID, name)
		}
		if err != nil {
			return State{}, 0, nil, err
		}
		deletedTools = append(deletedTools, name)
	}

	activeSubagents, err := s.getActiveSubagentsForAccountLocked(accountScopeID, 200)
	if err != nil {
		return State{}, 0, nil, err
	}
	for purpose := range activeSubagents {
		if err := s.deleteActiveSubagentForAccountLocked(accountScopeID, purpose); err != nil {
			return State{}, 0, nil, err
		}
	}

	resetProfiles := make([]string, 0, len(defaults))
	for _, profile := range defaults {
		if err := s.putProfileForAccountLocked(accountScopeID, profile); err != nil {
			return State{}, 0, nil, err
		}
		resetProfiles = append(resetProfiles, profile.Name)
	}
	if err := s.setActivePrimaryForAccountLocked(accountScopeID, "swarm"); err != nil {
		return State{}, 0, nil, err
	}
	for purpose, profileName := range defaultSubagentAssignments() {
		if err := s.setActiveSubagentForAccountLocked(accountScopeID, purpose, profileName); err != nil {
			return State{}, 0, nil, err
		}
	}

	version, err := s.bumpVersionForAccountLocked(accountScopeID)
	if err != nil {
		return State{}, 0, nil, err
	}
	state, err := s.currentStateForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return State{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.defaults.reset", "", map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"profiles":         resetProfiles,
		"deleted_profiles": deletedProfiles,
		"deleted_tools":    deletedTools,
		"active_primary":   state.ActivePrimary,
		"active_subagent":  state.ActiveSubagent,
		"state":            state,
		"version":          version,
	})
	if err != nil {
		return State{}, 0, nil, err
	}
	return state, version, &env, nil
}

func (s *Service) PreviewUpsert(input UpsertInput) (PreviewUpsertResult, error) {
	return s.previewUpsertForAccount("", input)
}

func (s *Service) PreviewUpsertForAccount(accountScopeID string, input UpsertInput) (PreviewUpsertResult, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return PreviewUpsertResult{}, err
	}
	return s.previewUpsertForAccount(accountScopeID, input)
}

func (s *Service) previewUpsertForAccount(accountScopeID string, input UpsertInput) (PreviewUpsertResult, error) {
	profile, err := normalizeUpsertInput(input)
	if err != nil {
		return PreviewUpsertResult{}, err
	}
	if s == nil || s.store == nil {
		return PreviewUpsertResult{}, errors.New("agent store is not configured")
	}
	name := normalizeName(profile.Name)
	if name == "" {
		return PreviewUpsertResult{}, errors.New("agent name is required")
	}
	before, ok, err := s.getProfileForAccountLocked(accountScopeID, name)
	if err != nil {
		return PreviewUpsertResult{}, err
	}
	if ok {
		if strings.TrimSpace(profile.Mode) == "" {
			profile.Mode = before.Mode
		}
		if strings.TrimSpace(profile.Description) == "" {
			profile.Description = before.Description
		}
		if !stringFieldProvided(input.ProviderSet, input.Provider) {
			profile.Provider = before.Provider
		}
		if !stringFieldProvided(input.ModelSet, input.Model) {
			profile.Model = before.Model
		}
		if !stringFieldProvided(input.ThinkingSet, input.Thinking) {
			profile.Thinking = before.Thinking
		}
		if strings.TrimSpace(profile.Prompt) == "" {
			profile.Prompt = before.Prompt
		}
		if strings.TrimSpace(profile.RuntimeMode) == "" {
			profile.RuntimeMode = before.RuntimeMode
		}
		if strings.TrimSpace(profile.ExecutionSetting) == "" {
			profile.ExecutionSetting = before.ExecutionSetting
		}
		if input.ExitPlanModeEnabled == nil {
			profile.ExitPlanModeEnabled = pebblestore.CloneBoolPtr(before.ExitPlanModeEnabled)
		}
		if input.ToolScope == nil {
			profile.ToolScope = pebblestore.CloneAgentToolScope(before.ToolScope)
		}
		if input.ToolContract == nil {
			profile.ToolContract = pebblestore.CloneAgentToolContract(before.ToolContract)
		}
		if input.Enabled == nil {
			profile.Enabled = before.Enabled
		}
	}
	if profile.Name == "swarm" {
		profile.Mode = ModePrimary
		profile.Enabled = true
		profile.ExitPlanModeEnabled = pebblestore.BoolPtr(true)
	}
	if profile.Mode == ModePrimary && profile.Name == "swarm" {
		profile.Provider = ""
		profile.Model = ""
		profile.Thinking = ""
	}
	profile, err = finalizeRuntimeProfile(profile, input, ok)
	if err != nil {
		return PreviewUpsertResult{}, err
	}
	profile = pebblestore.NormalizeAgentProfile(profile)
	result := PreviewUpsertResult{After: profile, Exists: ok}
	if ok {
		beforeCopy := before
		result.Before = &beforeCopy
	}
	return result, nil
}

func (s *Service) SetActiveSubagent(purpose, name string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
	return s.setActiveSubagentForAccount("", purpose, name)
}

func (s *Service) SetActiveSubagentForAccount(accountScopeID, purpose, name string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return nil, 0, nil, err
	}
	return s.setActiveSubagentForAccount(accountScopeID, purpose, name)
}

func (s *Service) setActiveSubagentForAccount(accountScopeID, purpose, name string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	purpose = normalizeName(purpose)
	if purpose == "" {
		return nil, 0, nil, errors.New("subagent purpose is required")
	}
	name = normalizeName(name)
	if name == "" {
		return nil, 0, nil, errors.New("agent name is required")
	}
	profile, ok, err := s.getProfileForAccountLocked(accountScopeID, name)
	if err != nil {
		return nil, 0, nil, err
	}
	if !ok {
		return nil, 0, nil, fmt.Errorf("agent %q not found", name)
	}
	if !profile.Enabled {
		return nil, 0, nil, fmt.Errorf("agent %q is disabled", name)
	}
	if profile.Mode != ModeSubagent {
		return nil, 0, nil, fmt.Errorf("agent %q is not a subagent", name)
	}
	if err := s.setActiveSubagentForAccountLocked(accountScopeID, purpose, name); err != nil {
		return nil, 0, nil, err
	}
	assignments, err := s.getActiveSubagentsForAccountLocked(accountScopeID, 200)
	if err != nil {
		return nil, 0, nil, err
	}
	version, err := s.bumpVersionForAccountLocked(accountScopeID)
	if err != nil {
		return nil, 0, nil, err
	}
	state, err := s.currentStateForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return nil, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.active_subagent.updated", purpose, map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"purpose":          purpose,
		"agent":            name,
		"active_subagent":  assignments,
		"state":            state,
		"version":          version,
	})
	if err != nil {
		return nil, 0, nil, err
	}
	return assignments, version, &env, nil
}

func (s *Service) DeleteActiveSubagent(purpose string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
	return s.deleteActiveSubagentForAccount("", purpose)
}

func (s *Service) DeleteActiveSubagentForAccount(accountScopeID, purpose string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return nil, 0, nil, err
	}
	return s.deleteActiveSubagentForAccount(accountScopeID, purpose)
}

func (s *Service) deleteActiveSubagentForAccount(accountScopeID, purpose string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	purpose = normalizeName(purpose)
	if purpose == "" {
		return nil, 0, nil, errors.New("subagent purpose is required")
	}
	if err := s.deleteActiveSubagentForAccountLocked(accountScopeID, purpose); err != nil {
		return nil, 0, nil, err
	}
	assignments, err := s.getActiveSubagentsForAccountLocked(accountScopeID, 200)
	if err != nil {
		return nil, 0, nil, err
	}
	version, err := s.bumpVersionForAccountLocked(accountScopeID)
	if err != nil {
		return nil, 0, nil, err
	}
	state, err := s.currentStateForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return nil, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.active_subagent.deleted", purpose, map[string]any{
		"account_scope_id": strings.TrimSpace(accountScopeID),
		"purpose":          purpose,
		"active_subagent":  assignments,
		"state":            state,
		"version":          version,
	})
	if err != nil {
		return nil, 0, nil, err
	}
	return assignments, version, &env, nil
}

func (s *Service) ResolvePrimary(name string) (pebblestore.AgentProfile, error) {
	return s.resolvePrimaryForAccount("", name)
}

func (s *Service) ResolvePrimaryForAccount(accountScopeID, name string) (pebblestore.AgentProfile, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	return s.resolvePrimaryForAccount(accountScopeID, name)
}

func (s *Service) resolvePrimaryForAccount(accountScopeID, name string) (pebblestore.AgentProfile, error) {
	profile, err := s.resolveProfileForAccount(accountScopeID, name)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	if profile.Mode != ModePrimary {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q is not primary", strings.TrimSpace(profile.Name))
	}
	return profile, nil
}

func (s *Service) ResolveAgent(name string) (pebblestore.AgentProfile, error) {
	return s.resolveProfileForAccount("", name)
}

func (s *Service) ResolveAgentForAccount(accountScopeID, name string) (pebblestore.AgentProfile, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	return s.resolveProfileForAccount(accountScopeID, name)
}

func (s *Service) ResolveIntegrationBuilderAgent(name string) (pebblestore.AgentProfile, error) {
	if !IsIntegrationBuilderAgentName(name) {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q is not the integration builder", strings.TrimSpace(name))
	}
	return IntegrationBuilderProfile(), nil
}

func (s *Service) resolveProfile(name string) (pebblestore.AgentProfile, error) {
	return s.resolveProfileForAccount("", name)
}

func (s *Service) resolveProfileForAccount(accountScopeID, name string) (pebblestore.AgentProfile, error) {
	name = normalizeName(name)
	if name == "" {
		active, ok, err := s.getActivePrimaryForAccountLocked(accountScopeID)
		if err != nil {
			return pebblestore.AgentProfile{}, err
		}
		if ok {
			name = normalizeName(active)
		}
	}
	if name == "" {
		name = "swarm"
	}
	if IsIntegrationBuilderAgentName(name) {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q not found", name)
	}
	profile, ok, err := s.getProfileForAccountLocked(accountScopeID, name)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	if !ok {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q not found", name)
	}
	if !profile.Enabled {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q is disabled", name)
	}
	return profile, nil
}

func (s *Service) ResolveSubagent(nameOrPurpose string) (pebblestore.AgentProfile, error) {
	return s.resolveSubagentForAccount("", nameOrPurpose)
}

func (s *Service) ResolveSubagentForAccount(accountScopeID, nameOrPurpose string) (pebblestore.AgentProfile, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	return s.resolveSubagentForAccount(accountScopeID, nameOrPurpose)
}

func (s *Service) resolveSubagentForAccount(accountScopeID, nameOrPurpose string) (pebblestore.AgentProfile, error) {
	key := normalizeName(nameOrPurpose)
	if key == "" {
		key = "explorer"
	}
	if IsIntegrationBuilderAgentName(key) {
		return pebblestore.AgentProfile{}, fmt.Errorf("subagent %q not found", strings.TrimSpace(nameOrPurpose))
	}

	if profile, ok, err := s.getProfileForAccountLocked(accountScopeID, key); err != nil {
		return pebblestore.AgentProfile{}, err
	} else if ok {
		if !profile.Enabled {
			return pebblestore.AgentProfile{}, fmt.Errorf("agent %q is disabled", key)
		}
		if profile.Mode != ModeSubagent {
			return pebblestore.AgentProfile{}, fmt.Errorf("agent %q is not subagent", key)
		}
		return profile, nil
	}

	activeSubagents, err := s.getActiveSubagentsForAccountLocked(accountScopeID, 200)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	mappedName := normalizeName(activeSubagents[key])
	if mappedName == "" {
		return pebblestore.AgentProfile{}, fmt.Errorf("subagent %q not found", strings.TrimSpace(nameOrPurpose))
	}

	profile, ok, err := s.getProfileForAccountLocked(accountScopeID, mappedName)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	if !ok {
		return pebblestore.AgentProfile{}, fmt.Errorf("subagent %q resolves to missing profile %q", key, mappedName)
	}
	if !profile.Enabled {
		return pebblestore.AgentProfile{}, fmt.Errorf("subagent %q resolves to disabled profile %q", key, mappedName)
	}
	if profile.Mode != ModeSubagent {
		return pebblestore.AgentProfile{}, fmt.Errorf("subagent %q resolves to non-subagent profile %q", key, mappedName)
	}
	return profile, nil
}

func (s *Service) ResolveBackground(name string) (pebblestore.AgentProfile, error) {
	return s.resolveBackgroundForAccount("", name)
}

func (s *Service) ResolveBackgroundForAccount(accountScopeID, name string) (pebblestore.AgentProfile, error) {
	accountScopeID, err := s.requireAccountScopeID(accountScopeID)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	return s.resolveBackgroundForAccount(accountScopeID, name)
}

func (s *Service) resolveBackgroundForAccount(accountScopeID, name string) (pebblestore.AgentProfile, error) {
	name = normalizeName(name)
	if name == "" {
		return pebblestore.AgentProfile{}, errors.New("background agent name is required")
	}
	profile, ok, err := s.getProfileForAccountLocked(accountScopeID, name)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	if !ok {
		return pebblestore.AgentProfile{}, fmt.Errorf("background agent %q not found", name)
	}
	if !profile.Enabled {
		return pebblestore.AgentProfile{}, fmt.Errorf("background agent %q is disabled", name)
	}
	if profile.Mode != ModeBackground {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q is not background", name)
	}
	return profile, nil
}

func defaultProfiles(now int64) []pebblestore.AgentProfile {
	return []pebblestore.AgentProfile{
		{
			Name:                "swarm",
			Mode:                ModePrimary,
			RuntimeMode:         pebblestore.AgentRuntimeModePlanAuto,
			Description:         "Primary orchestrator",
			Provider:            "",
			Model:               "",
			Thinking:            "",
			ExitPlanModeEnabled: pebblestore.BoolPtr(true),
			Prompt: strings.TrimSpace("" +
				"You are Swarm, the primary orchestration agent.\n" +
				"Drive the user task to completion with clear progress, explicit decisions, and concrete outputs.\n" +
				"Match execution depth to request scope: handle narrow asks directly, escalate to deeper investigation/delegation only when scope is broad or unclear.\n" +
				"Delegate specialized work when needed, then merge results into one coherent answer.\n" +
				"Keep responses concise, factual, and implementation-focused.\n" +
				"Respect workspace boundaries and permission outcomes at all times."),
			ToolContract: defaultSwarmToolContract(),
			Enabled:      true,
			UpdatedAt:    now,
		},
		{
			Name:             "explorer",
			Mode:             ModeSubagent,
			Description:      "Repository explorer",
			Provider:         "",
			RuntimeMode:      pebblestore.AgentRuntimeModeRead,
			ExecutionSetting: pebblestore.AgentExecutionSettingRead,
			Prompt: strings.TrimSpace("" +
				"You are Explorer, a subagent focused on repository inspection and evidence collection.\n" +
				"Map files, summarize architecture and execution flow, and surface likely attack points.\n" +
				"Provide precise findings with path/line evidence, then end with a `Relevant filepaths:` list and why each file matters."),
			ToolContract: defaultExplorerToolContract(),
			Enabled:      true,
			UpdatedAt:    now,
		},
		{
			Name:                "memory",
			Mode:                ModeSubagent,
			Description:         "Durable artifacts and commits",
			Provider:            "",
			RuntimeMode:         pebblestore.AgentRuntimeModeReadWrite,
			ExecutionSetting:    pebblestore.AgentExecutionSettingReadWrite,
			ExitPlanModeEnabled: pebblestore.BoolPtr(false),
			Prompt:              defaultMemoryPrompt(),
			ToolContract:        defaultMemoryToolContract(),
			Enabled:             true,
			UpdatedAt:           now,
		},
		{
			Name:             "parallel",
			Mode:             ModeSubagent,
			Description:      "Creative worker",
			Provider:         "",
			RuntimeMode:      pebblestore.AgentRuntimeModeReadWrite,
			ExecutionSetting: pebblestore.AgentExecutionSettingReadWrite,
			Prompt: strings.TrimSpace("" +
				"You are Parallel, a creative execution subagent.\n" +
				"Generate component-level outputs and parallel alternatives while keeping implementation practical."),
			ToolContract: defaultReadWriteSubagentToolContract(),
			Enabled:      true,
			UpdatedAt:    now,
		},
		{
			Name:             "clone",
			Mode:             ModeSubagent,
			Description:      "Swarm clone",
			Provider:         "",
			RuntimeMode:      pebblestore.AgentRuntimeModeReadWrite,
			ExecutionSetting: pebblestore.AgentExecutionSettingReadWrite,
			Prompt: strings.TrimSpace("" +
				"You are Clone, a fast implementation subagent mirroring Swarm behavior.\n" +
				"Execute concrete file-change tasks and report exact edits with minimal narrative."),
			ToolContract: defaultReadWriteSubagentToolContract(),
			Enabled:      true,
			UpdatedAt:    now,
		},
	}
}

func defaultSubagentAssignments() map[string]string {
	return map[string]string{
		"explorer": "explorer",
		"memory":   "memory",
		"parallel": "parallel",
		"clone":    "clone",
	}
}

func DefaultProfileByName(name string) (pebblestore.AgentProfile, bool) {
	return defaultProfileByName(name, time.Now().UnixMilli())
}

func defaultProfileByName(name string, now int64) (pebblestore.AgentProfile, bool) {
	name = normalizeName(name)
	for _, profile := range defaultProfiles(now) {
		if strings.EqualFold(strings.TrimSpace(profile.Name), name) {
			return profile, true
		}
	}
	return pebblestore.AgentProfile{}, false
}

func finalizeRuntimeProfile(profile pebblestore.AgentProfile, input UpsertInput, existing bool) (pebblestore.AgentProfile, error) {
	requestedRuntimeMode := pebblestore.NormalizeAgentRuntimeMode(input.RuntimeMode)
	runtimeMode := pebblestore.NormalizeAgentRuntimeMode(profile.RuntimeMode)
	executionSetting := pebblestore.NormalizeAgentExecutionSetting(profile.ExecutionSetting)
	if requestedExecutionSetting := pebblestore.NormalizeAgentExecutionSetting(input.ExecutionSetting); requestedExecutionSetting != "" {
		executionSetting = requestedExecutionSetting
	}
	exitPlanModeEnabled := pebblestore.AgentExitPlanModeEnabled(profile)
	toolRuntimeMode := pebblestore.AgentRuntimeModeForToolContractWithDefault(profile.ToolContract, !existing)

	if requestedRuntimeMode != "" {
		runtimeMode = requestedRuntimeMode
	}
	if runtimeMode == "" && !existing {
		runtimeMode = pebblestore.AgentRuntimeModeForToolContractWithDefault(profile.ToolContract, true)
	}
	if runtimeMode == "" && !existing {
		runtimeMode = pebblestore.AgentRuntimeModeRead
	}
	if runtimeMode == "" {
		return profile, errors.New("agent runtime_mode is required")
	}
	if runtimeMode == pebblestore.AgentRuntimeModePlanAuto {
		if input.ExitPlanModeEnabled != nil && !*input.ExitPlanModeEnabled {
			return profile, errors.New("agent runtime_mode=plan_auto contradicts exit_plan_mode_enabled=false")
		}
		if strings.TrimSpace(input.ExecutionSetting) != "" {
			return profile, errors.New("agent runtime_mode=plan_auto cannot include execution_setting")
		}
		profile.RuntimeMode = pebblestore.AgentRuntimeModePlanAuto
		profile.ExitPlanModeEnabled = pebblestore.BoolPtr(true)
		profile.ExecutionSetting = ""
		return profile, nil
	}
	if exitPlanModeEnabled {
		return profile, errors.New("agent direct runtime cannot have exit_plan_mode_enabled=true; use runtime_mode=plan_auto")
	}
	if executionSetting != "" && executionSetting != runtimeMode {
		return profile, fmt.Errorf("agent runtime_mode=%s contradicts execution_setting=%s", runtimeMode, executionSetting)
	}
	if toolRuntimeMode != "" && toolRuntimeMode != runtimeMode && requestedRuntimeMode == "" && strings.TrimSpace(input.ExecutionSetting) == "" && !existing {
		runtimeMode = toolRuntimeMode
	}
	profile.RuntimeMode = runtimeMode
	profile.ExitPlanModeEnabled = pebblestore.BoolPtr(false)
	profile.ExecutionSetting = runtimeMode
	return profile, nil
}

func normalizeUpsertInput(input UpsertInput) (pebblestore.AgentProfile, error) {
	name := normalizeName(input.Name)
	if name == "" {
		return pebblestore.AgentProfile{}, errors.New("agent name is required")
	}
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	if mode == "" {
		mode = ModeSubagent
	}
	if mode != ModePrimary && mode != ModeSubagent && mode != ModeBackground {
		return pebblestore.AgentProfile{}, fmt.Errorf("invalid mode %q", input.Mode)
	}
	runtimeMode := pebblestore.NormalizeAgentRuntimeMode(input.RuntimeMode)
	if strings.TrimSpace(input.RuntimeMode) != "" && runtimeMode == "" {
		return pebblestore.AgentProfile{}, fmt.Errorf("invalid runtime_mode %q", input.RuntimeMode)
	}
	executionSetting := pebblestore.NormalizeAgentExecutionSetting(input.ExecutionSetting)
	if strings.TrimSpace(input.ExecutionSetting) != "" && executionSetting == "" {
		return pebblestore.AgentProfile{}, fmt.Errorf("invalid execution_setting %q", input.ExecutionSetting)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	toolScope := pebblestore.CloneAgentToolScope(input.ToolScope)
	toolContract := pebblestore.CloneAgentToolContract(input.ToolContract)
	return pebblestore.NormalizeAgentProfile(pebblestore.AgentProfile{
		Name:                name,
		Mode:                mode,
		Description:         strings.TrimSpace(input.Description),
		Provider:            strings.ToLower(strings.TrimSpace(input.Provider)),
		Model:               strings.TrimSpace(input.Model),
		Thinking:            strings.ToLower(strings.TrimSpace(input.Thinking)),
		Prompt:              strings.TrimSpace(input.Prompt),
		RuntimeMode:         runtimeMode,
		ExecutionSetting:    executionSetting,
		ExitPlanModeEnabled: pebblestore.CloneBoolPtr(input.ExitPlanModeEnabled),
		ToolScope:           toolScope,
		ToolContract:        toolContract,
		Enabled:             enabled,
	}), nil
}

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func stringFieldProvided(explicit bool, value string) bool {
	if explicit {
		return true
	}
	return strings.TrimSpace(value) != ""
}

func (s *Service) requireAccountScopeID(accountScopeID string) (string, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return "", errors.New("account scope ID is required")
	}
	return accountScopeID, nil
}

func (s *Service) getProfileForAccountLocked(accountScopeID, name string) (pebblestore.AgentProfile, bool, error) {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.GetProfile(name)
	}
	return s.store.GetProfileForAccount(accountScopeID, name)
}

func (s *Service) putProfileForAccountLocked(accountScopeID string, profile pebblestore.AgentProfile) error {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.PutProfile(profile)
	}
	return s.store.PutProfileForAccount(accountScopeID, profile)
}

func (s *Service) deleteProfileForAccountLocked(accountScopeID, name string) error {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.DeleteProfile(name)
	}
	return s.store.DeleteProfileForAccount(accountScopeID, name)
}

func (s *Service) listProfilesForAccountLocked(accountScopeID string, limit int) ([]pebblestore.AgentProfile, error) {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.ListProfiles(limit)
	}
	return s.store.ListProfilesForAccount(accountScopeID, limit)
}

func (s *Service) getActivePrimaryForAccountLocked(accountScopeID string) (string, bool, error) {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.GetActivePrimary()
	}
	return s.store.GetActivePrimaryForAccount(accountScopeID)
}

func (s *Service) setActivePrimaryForAccountLocked(accountScopeID, name string) error {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.SetActivePrimary(name)
	}
	return s.store.SetActivePrimaryForAccount(accountScopeID, name)
}

func (s *Service) getActiveSubagentsForAccountLocked(accountScopeID string, limit int) (map[string]string, error) {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.GetActiveSubagents(limit)
	}
	return s.store.GetActiveSubagentsForAccount(accountScopeID, limit)
}

func (s *Service) setActiveSubagentForAccountLocked(accountScopeID, purpose, name string) error {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.SetActiveSubagent(purpose, name)
	}
	return s.store.SetActiveSubagentForAccount(accountScopeID, purpose, name)
}

func (s *Service) deleteActiveSubagentForAccountLocked(accountScopeID, purpose string) error {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.DeleteActiveSubagent(purpose)
	}
	return s.store.DeleteActiveSubagentForAccount(accountScopeID, purpose)
}

func (s *Service) getVersionForAccountLocked(accountScopeID string) (int64, bool, error) {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.GetVersion()
	}
	return s.store.GetVersionForAccount(accountScopeID)
}

func (s *Service) setVersionForAccountLocked(accountScopeID string, version int64) error {
	if strings.TrimSpace(accountScopeID) == "" {
		return s.store.SetVersion(version)
	}
	return s.store.SetVersionForAccount(accountScopeID, version)
}

func (s *Service) bumpVersionLocked() (int64, error) {
	return s.bumpVersionForAccountLocked("")
}

func (s *Service) bumpVersionForAccountLocked(accountScopeID string) (int64, error) {
	version, ok, err := s.getVersionForAccountLocked(accountScopeID)
	if err != nil {
		return 0, err
	}
	if !ok {
		version = 0
	}
	version++
	if err := s.setVersionForAccountLocked(accountScopeID, version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Service) currentStateLocked(limit int) (State, error) {
	return s.currentStateForAccountLocked("", limit)
}

func (s *Service) currentStateForAccountLocked(accountScopeID string, limit int) (State, error) {
	profiles, err := s.listProfilesForAccountLocked(accountScopeID, limit)
	if err != nil {
		return State{}, err
	}
	var customTools []pebblestore.AgentCustomToolDefinition
	if strings.TrimSpace(accountScopeID) == "" {
		customTools, err = s.store.ListCustomTools(limit)
	} else {
		customTools, err = s.store.ListCustomToolsForAccount(accountScopeID, limit)
	}
	if err != nil {
		return State{}, err
	}
	activePrimary, _, err := s.getActivePrimaryForAccountLocked(accountScopeID)
	if err != nil {
		return State{}, err
	}
	activeSubagent, err := s.getActiveSubagentsForAccountLocked(accountScopeID, 200)
	if err != nil {
		return State{}, err
	}
	version, _, err := s.getVersionForAccountLocked(accountScopeID)
	if err != nil {
		return State{}, err
	}
	return State{
		Profiles:       profiles,
		CustomTools:    customTools,
		ActivePrimary:  strings.TrimSpace(activePrimary),
		ActiveSubagent: activeSubagent,
		Version:        version,
	}, nil
}

func (s *Service) activePrimaryValidLocked(name string) (bool, error) {
	return s.activePrimaryValidForAccountLocked("", name)
}

func (s *Service) activePrimaryValidForAccountLocked(accountScopeID, name string) (bool, error) {
	name = normalizeName(name)
	if name == "" {
		return false, nil
	}
	profile, ok, err := s.getProfileForAccountLocked(accountScopeID, name)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if !profile.Enabled {
		return false, nil
	}
	return profile.Mode == ModePrimary, nil
}

func (s *Service) nextPrimaryLocked(exclude string) (string, error) {
	return s.nextPrimaryForAccountLocked("", exclude)
}

func (s *Service) nextPrimaryForAccountLocked(accountScopeID, exclude string) (string, error) {
	exclude = normalizeName(exclude)
	profiles, err := s.listProfilesForAccountLocked(accountScopeID, 2000)
	if err != nil {
		return "", err
	}
	for _, profile := range profiles {
		if normalizeName(profile.Name) == exclude {
			continue
		}
		if !profile.Enabled {
			continue
		}
		if profile.Mode != ModePrimary {
			continue
		}
		return strings.TrimSpace(profile.Name), nil
	}
	return "", nil
}

func (s *Service) appendEventLocked(eventType, entityID string, payload any) (pebblestore.EventEnvelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return pebblestore.EventEnvelope{}, err
	}
	env, err := s.events.Append("system:agent", eventType, strings.TrimSpace(entityID), raw, "", "")
	if err != nil {
		return pebblestore.EventEnvelope{}, err
	}
	if s.publish != nil {
		s.publish(env)
	}
	return env, nil
}
