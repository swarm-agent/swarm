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
)

type Service struct {
	store  *pebblestore.AgentStore
	events *pebblestore.EventLog
	mu     sync.Mutex
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

func (s *Service) EnsureDefaults() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	version, hasVersion, err := s.store.GetVersion()
	if err != nil {
		return err
	}
	profiles, err := s.store.ListProfiles(2000)
	if err != nil {
		return err
	}
	if !hasVersion && len(profiles) == 0 {
		now := time.Now().UnixMilli()
		for _, profile := range defaultProfiles(now) {
			if err := s.store.PutProfile(profile); err != nil {
				return err
			}
		}
		if err := s.store.SetActivePrimary("swarm"); err != nil {
			return err
		}
		for purpose, profileName := range defaultSubagentAssignments() {
			if err := s.store.SetActiveSubagent(purpose, profileName); err != nil {
				return err
			}
		}
		return s.store.SetVersion(1)
	}

	now := time.Now().UnixMilli()
	if current, ok, err := s.store.GetProfile("commit"); err != nil {
		return err
	} else if ok && shouldReconcileBuiltInCommit(current) {
		profile, ok := defaultProfileByName("commit", now)
		if !ok {
			return errors.New("default commit profile is missing")
		}
		if err := s.store.PutProfile(profile); err != nil {
			return err
		}
	}
	if current, ok, err := s.store.GetProfile("parallel"); err != nil {
		return err
	} else if ok && shouldReconcileBuiltInParallel(current) {
		current.ExecutionSetting = pebblestore.AgentExecutionSettingReadWrite
		current.UpdatedAt = now
		if err := s.store.PutProfile(current); err != nil {
			return err
		}
	}

	if !hasVersion {
		version = 1
		if err := s.store.SetVersion(version); err != nil {
			return err
		}
	}

	activePrimary, ok, err := s.store.GetActivePrimary()
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(activePrimary) == "" {
		fallback, err := s.nextPrimaryLocked("")
		if err != nil {
			return err
		}
		if fallback != "" {
			if err := s.store.SetActivePrimary(fallback); err != nil {
				return err
			}
		}
		return nil
	}
	valid, err := s.activePrimaryValidLocked(activePrimary)
	if err != nil {
		return err
	}
	if valid {
		return nil
	}
	fallback, err := s.nextPrimaryLocked(activePrimary)
	if err != nil {
		return err
	}
	if fallback == "" {
		return nil
	}
	return s.store.SetActivePrimary(fallback)
}

func shouldReconcileBuiltInCommit(profile pebblestore.AgentProfile) bool {
	if strings.TrimSpace(profile.Name) != "commit" {
		return false
	}
	if profile.Mode != ModeBackground {
		return true
	}
	if pebblestore.NormalizeAgentExecutionSetting(profile.ExecutionSetting) != pebblestore.AgentExecutionSettingReadWrite {
		return true
	}
	if profile.ToolContract == nil {
		return true
	}
	if strings.TrimSpace(profile.ToolContract.Preset) != "background_commit" {
		return true
	}
	bash, ok := profile.ToolContract.Tools["git_commit"]
	if !ok || bash.Enabled == nil || !*bash.Enabled {
		return true
	}
	return false
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
	profiles, err := s.store.ListProfiles(limit)
	if err != nil {
		return State{}, err
	}
	customTools, err := s.store.ListCustomTools(limit)
	if err != nil {
		return State{}, err
	}
	activePrimary, _, err := s.store.GetActivePrimary()
	if err != nil {
		return State{}, err
	}
	activeSubagent, err := s.store.GetActiveSubagents(200)
	if err != nil {
		return State{}, err
	}
	version, _, err := s.store.GetVersion()
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

func (s *Service) ReplaceManagedState(state State, syncProfiles, syncCustomTools bool) (State, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s == nil || s.store == nil {
		return State{}, 0, nil, errors.New("agent store is not configured")
	}
	if syncCustomTools {
		currentTools, err := s.store.ListCustomTools(2000)
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
			if err := s.store.DeleteCustomTool(name); err != nil {
				return State{}, 0, nil, err
			}
		}
		for _, name := range toolNames {
			if err := s.store.PutCustomTool(desiredTools[name]); err != nil {
				return State{}, 0, nil, err
			}
		}
	}
	if syncProfiles {
		currentProfiles, err := s.store.ListProfiles(2000)
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
			if err := s.store.DeleteProfile(name); err != nil {
				return State{}, 0, nil, err
			}
		}
		for _, name := range profileNames {
			if err := s.store.PutProfile(desiredProfiles[name]); err != nil {
				return State{}, 0, nil, err
			}
		}
		currentAssignments, err := s.store.GetActiveSubagents(200)
		if err != nil {
			return State{}, 0, nil, err
		}
		for purpose := range currentAssignments {
			if _, ok := state.ActiveSubagent[purpose]; ok {
				continue
			}
			if err := s.store.DeleteActiveSubagent(purpose); err != nil {
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
			if err := s.store.SetActiveSubagent(purpose, name); err != nil {
				return State{}, 0, nil, err
			}
		}
		activePrimary := normalizeName(state.ActivePrimary)
		if activePrimary != "" {
			if err := s.store.SetActivePrimary(activePrimary); err != nil {
				return State{}, 0, nil, err
			}
		}
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return State{}, 0, nil, err
	}
	current, err := s.currentStateLocked(2000)
	if err != nil {
		return State{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.state.synced", "", map[string]any{
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

func (s *Service) GetCustomTool(name string) (pebblestore.AgentCustomToolDefinition, bool, error) {
	name = pebblestore.NormalizeAgentCustomToolName(name)
	if name == "" {
		return pebblestore.AgentCustomToolDefinition{}, false, errors.New("custom tool name is required")
	}
	return s.store.GetCustomTool(name)
}

func (s *Service) ListCustomTools(limit int) ([]pebblestore.AgentCustomToolDefinition, error) {
	return s.store.ListCustomTools(limit)
}

func (s *Service) PutCustomTool(definition pebblestore.AgentCustomToolDefinition) (pebblestore.AgentCustomToolDefinition, error) {
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
	definition.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutCustomTool(definition); err != nil {
		return pebblestore.AgentCustomToolDefinition{}, err
	}
	return definition, nil
}

func (s *Service) DeleteCustomTool(name string) (bool, error) {
	name = pebblestore.NormalizeAgentCustomToolName(name)
	if name == "" {
		return false, errors.New("custom tool name is required")
	}
	if _, ok, err := s.store.GetCustomTool(name); err != nil {
		return false, err
	} else if !ok {
		return false, nil
	}
	if err := s.store.DeleteCustomTool(name); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) AssignCustomTool(agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
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
	if _, ok, err := s.store.GetCustomTool(toolName); err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	} else if !ok {
		return pebblestore.AgentProfile{}, 0, nil, fmt.Errorf("custom tool %q not found", toolName)
	}
	profile, ok, err := s.store.GetProfile(agentName)
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
	profile.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutProfile(profile); err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.custom_tool.assigned", agentName, map[string]any{
		"agent":     agentName,
		"tool_name": toolName,
		"profile":   profile,
		"version":   version,
	})
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	return profile, version, &env, nil
}

func (s *Service) UnassignCustomTool(agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
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
	profile, ok, err := s.store.GetProfile(agentName)
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
	if err := s.store.PutProfile(profile); err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.custom_tool.unassigned", agentName, map[string]any{
		"agent":     agentName,
		"tool_name": toolName,
		"profile":   profile,
		"version":   version,
	})
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	return profile, version, &env, nil
}

func (s *Service) GetProfile(name string) (pebblestore.AgentProfile, bool, error) {
	name = normalizeName(name)
	if name == "" {
		return pebblestore.AgentProfile{}, false, errors.New("agent name is required")
	}
	return s.store.GetProfile(name)
}

func (s *Service) Upsert(input UpsertInput) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	profile, err := normalizeUpsertInput(input)
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	existing, ok, err := s.store.GetProfile(profile.Name)
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
	profile = pebblestore.NormalizeAgentProfile(profile)
	profile.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutProfile(profile); err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}

	env, err := s.appendEventLocked("agent.profile.updated", profile.Name, map[string]any{
		"profile": profile,
		"version": version,
	})
	if err != nil {
		return pebblestore.AgentProfile{}, 0, nil, err
	}
	return profile, version, &env, nil
}

func (s *Service) ActivatePrimary(name string) (string, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name = normalizeName(name)
	if name == "" {
		return "", 0, nil, errors.New("agent name is required")
	}
	profile, ok, err := s.store.GetProfile(name)
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

	if err := s.store.SetActivePrimary(name); err != nil {
		return "", 0, nil, err
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return "", 0, nil, err
	}
	env, err := s.appendEventLocked("agent.active.updated", name, map[string]any{
		"active_primary": name,
		"version":        version,
	})
	if err != nil {
		return "", 0, nil, err
	}
	return name, version, &env, nil
}

func (s *Service) Delete(name string) (DeleteResult, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name = normalizeName(name)
	if name == "" {
		return DeleteResult{}, 0, nil, errors.New("agent name is required")
	}
	if name == "swarm" || name == "memory" {
		return DeleteResult{}, 0, nil, fmt.Errorf("agent %q is protected and cannot be deleted", name)
	}

	target, ok, err := s.store.GetProfile(name)
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	if !ok {
		return DeleteResult{}, 0, nil, fmt.Errorf("agent %q not found", name)
	}
	if target.Mode == ModePrimary {
		fallback, err := s.nextPrimaryLocked(name)
		if err != nil {
			return DeleteResult{}, 0, nil, err
		}
		if fallback == "" {
			return DeleteResult{}, 0, nil, fmt.Errorf("agent %q is the last primary and cannot be deleted", name)
		}
		activePrimary, _, err := s.store.GetActivePrimary()
		if err != nil {
			return DeleteResult{}, 0, nil, err
		}
		validActivePrimary, err := s.activePrimaryValidLocked(activePrimary)
		if err != nil {
			return DeleteResult{}, 0, nil, err
		}
		if strings.EqualFold(strings.TrimSpace(activePrimary), name) || !validActivePrimary {
			if err := s.store.SetActivePrimary(fallback); err != nil {
				return DeleteResult{}, 0, nil, err
			}
		}
	}
	if target.Mode == ModeSubagent {
		activeSubagents, err := s.store.GetActiveSubagents(200)
		if err != nil {
			return DeleteResult{}, 0, nil, err
		}
		for purpose, assigned := range activeSubagents {
			if !strings.EqualFold(strings.TrimSpace(assigned), name) {
				continue
			}
			_ = s.store.DeleteActiveSubagent(purpose)
		}
	}
	if err := s.store.DeleteProfile(name); err != nil {
		return DeleteResult{}, 0, nil, err
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	activePrimary, _, err := s.store.GetActivePrimary()
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	result := DeleteResult{
		Deleted:       name,
		ActivePrimary: activePrimary,
	}
	env, err := s.appendEventLocked("agent.profile.deleted", name, map[string]any{
		"deleted":        result.Deleted,
		"active_primary": result.ActivePrimary,
		"version":        version,
	})
	if err != nil {
		return DeleteResult{}, 0, nil, err
	}
	return result, version, &env, nil
}

func (s *Service) RestoreDefaults() (State, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	defaults := defaultProfiles(now)
	restored := make([]string, 0, len(defaults))
	for _, profile := range defaults {
		if err := s.store.PutProfile(profile); err != nil {
			return State{}, 0, nil, err
		}
		restored = append(restored, profile.Name)
	}

	activePrimary, _, err := s.store.GetActivePrimary()
	if err != nil {
		return State{}, 0, nil, err
	}
	validActivePrimary, err := s.activePrimaryValidLocked(activePrimary)
	if err != nil {
		return State{}, 0, nil, err
	}
	if !validActivePrimary {
		if err := s.store.SetActivePrimary("swarm"); err != nil {
			return State{}, 0, nil, err
		}
	}
	for purpose, profileName := range defaultSubagentAssignments() {
		if err := s.store.SetActiveSubagent(purpose, profileName); err != nil {
			return State{}, 0, nil, err
		}
	}

	version, err := s.bumpVersionLocked()
	if err != nil {
		return State{}, 0, nil, err
	}
	state, err := s.currentStateLocked(2000)
	if err != nil {
		return State{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.defaults.restored", "", map[string]any{
		"restored":        restored,
		"active_primary":  state.ActivePrimary,
		"active_subagent": state.ActiveSubagent,
		"version":         version,
	})
	if err != nil {
		return State{}, 0, nil, err
	}
	return state, version, &env, nil
}

func (s *Service) ResetDefaults() (State, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	defaults := defaultProfiles(now)
	defaultNames := make(map[string]struct{}, len(defaults))
	for _, profile := range defaults {
		defaultNames[normalizeName(profile.Name)] = struct{}{}
	}

	profiles, err := s.store.ListProfiles(2000)
	if err != nil {
		return State{}, 0, nil, err
	}
	deletedProfiles := make([]string, 0)
	for _, profile := range profiles {
		name := normalizeName(profile.Name)
		if _, ok := defaultNames[name]; ok {
			continue
		}
		if err := s.store.DeleteProfile(name); err != nil {
			return State{}, 0, nil, err
		}
		deletedProfiles = append(deletedProfiles, name)
	}

	customTools, err := s.store.ListCustomTools(2000)
	if err != nil {
		return State{}, 0, nil, err
	}
	deletedTools := make([]string, 0, len(customTools))
	for _, tool := range customTools {
		name := pebblestore.NormalizeAgentCustomToolName(tool.Name)
		if name == "" {
			continue
		}
		if err := s.store.DeleteCustomTool(name); err != nil {
			return State{}, 0, nil, err
		}
		deletedTools = append(deletedTools, name)
	}

	activeSubagents, err := s.store.GetActiveSubagents(200)
	if err != nil {
		return State{}, 0, nil, err
	}
	for purpose := range activeSubagents {
		if err := s.store.DeleteActiveSubagent(purpose); err != nil {
			return State{}, 0, nil, err
		}
	}

	resetProfiles := make([]string, 0, len(defaults))
	for _, profile := range defaults {
		if err := s.store.PutProfile(profile); err != nil {
			return State{}, 0, nil, err
		}
		resetProfiles = append(resetProfiles, profile.Name)
	}
	if err := s.store.SetActivePrimary("swarm"); err != nil {
		return State{}, 0, nil, err
	}
	for purpose, profileName := range defaultSubagentAssignments() {
		if err := s.store.SetActiveSubagent(purpose, profileName); err != nil {
			return State{}, 0, nil, err
		}
	}

	version, err := s.bumpVersionLocked()
	if err != nil {
		return State{}, 0, nil, err
	}
	state, err := s.currentStateLocked(2000)
	if err != nil {
		return State{}, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.defaults.reset", "", map[string]any{
		"profiles":         resetProfiles,
		"deleted_profiles": deletedProfiles,
		"deleted_tools":    deletedTools,
		"active_primary":   state.ActivePrimary,
		"active_subagent":  state.ActiveSubagent,
		"version":          version,
	})
	if err != nil {
		return State{}, 0, nil, err
	}
	return state, version, &env, nil
}

func (s *Service) PreviewUpsert(input UpsertInput) (PreviewUpsertResult, error) {
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
	before, ok, err := s.store.GetProfile(name)
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
	profile = pebblestore.NormalizeAgentProfile(profile)
	result := PreviewUpsertResult{After: profile, Exists: ok}
	if ok {
		beforeCopy := before
		result.Before = &beforeCopy
	}
	return result, nil
}

func (s *Service) SetActiveSubagent(purpose, name string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
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
	profile, ok, err := s.store.GetProfile(name)
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
	if err := s.store.SetActiveSubagent(purpose, name); err != nil {
		return nil, 0, nil, err
	}
	assignments, err := s.store.GetActiveSubagents(200)
	if err != nil {
		return nil, 0, nil, err
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return nil, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.active_subagent.updated", purpose, map[string]any{
		"purpose":         purpose,
		"agent":           name,
		"active_subagent": assignments,
		"version":         version,
	})
	if err != nil {
		return nil, 0, nil, err
	}
	return assignments, version, &env, nil
}

func (s *Service) DeleteActiveSubagent(purpose string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	purpose = normalizeName(purpose)
	if purpose == "" {
		return nil, 0, nil, errors.New("subagent purpose is required")
	}
	if err := s.store.DeleteActiveSubagent(purpose); err != nil {
		return nil, 0, nil, err
	}
	assignments, err := s.store.GetActiveSubagents(200)
	if err != nil {
		return nil, 0, nil, err
	}
	version, err := s.bumpVersionLocked()
	if err != nil {
		return nil, 0, nil, err
	}
	env, err := s.appendEventLocked("agent.active_subagent.deleted", purpose, map[string]any{
		"purpose":         purpose,
		"active_subagent": assignments,
		"version":         version,
	})
	if err != nil {
		return nil, 0, nil, err
	}
	return assignments, version, &env, nil
}

func (s *Service) ResolvePrimary(name string) (pebblestore.AgentProfile, error) {
	name = normalizeName(name)
	if name == "" {
		active, ok, err := s.store.GetActivePrimary()
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
	profile, ok, err := s.store.GetProfile(name)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	if !ok {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q not found", name)
	}
	if !profile.Enabled {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q is disabled", name)
	}
	if profile.Mode != ModePrimary {
		return pebblestore.AgentProfile{}, fmt.Errorf("agent %q is not primary", name)
	}
	return profile, nil
}

func (s *Service) ResolveSubagent(nameOrPurpose string) (pebblestore.AgentProfile, error) {
	key := normalizeName(nameOrPurpose)
	if key == "" {
		key = "explorer"
	}

	if profile, ok, err := s.store.GetProfile(key); err != nil {
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

	activeSubagents, err := s.store.GetActiveSubagents(200)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	mappedName := normalizeName(activeSubagents[key])
	if mappedName == "" {
		return pebblestore.AgentProfile{}, fmt.Errorf("subagent %q not found", strings.TrimSpace(nameOrPurpose))
	}

	profile, ok, err := s.store.GetProfile(mappedName)
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
	name = normalizeName(name)
	if name == "" {
		return pebblestore.AgentProfile{}, errors.New("background agent name is required")
	}
	profile, ok, err := s.store.GetProfile(name)
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
			Enabled:   true,
			UpdatedAt: now,
		},
		{
			Name:             "explorer",
			Mode:             ModeSubagent,
			Description:      "Repository explorer",
			Provider:         "",
			ExecutionSetting: pebblestore.AgentExecutionSettingRead,
			Prompt: strings.TrimSpace("" +
				"You are Explorer, a subagent focused on repository inspection and evidence collection.\n" +
				"Map files, summarize architecture and execution flow, and surface likely attack points.\n" +
				"Provide precise findings with path/line evidence, then end with a `Relevant filepaths:` list and why each file matters."),
			Enabled:   true,
			UpdatedAt: now,
		},
		{
			Name:             "memory",
			Mode:             ModeSubagent,
			Description:      "Summary writer",
			Provider:         "",
			ExecutionSetting: pebblestore.AgentExecutionSettingRead,
			Prompt: strings.TrimSpace("" +
				"You are Memory, a subagent for durable artifacts.\n" +
				"Produce commit messages, session titles, and compact summaries that are accurate and traceable."),
			Enabled:   true,
			UpdatedAt: now,
		},
		{
			Name:             "commit",
			Mode:             ModeBackground,
			Description:      "Commit specialist",
			Provider:         "",
			ExecutionSetting: pebblestore.AgentExecutionSettingReadWrite,
			Prompt: strings.TrimSpace("" +
				"You are Commit, a background agent for accurate git commits.\n" +
				"Inspect git status and diffs, stage the correct files, and create exactly one accurate commit.\n" +
				"Do not push unless the user explicitly requested push."),
			ToolContract: &pebblestore.AgentToolContract{
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
			},
			Enabled:   true,
			UpdatedAt: now,
		},
		{
			Name:             "parallel",
			Mode:             ModeSubagent,
			Description:      "Creative worker",
			Provider:         "",
			ExecutionSetting: pebblestore.AgentExecutionSettingReadWrite,
			Prompt: strings.TrimSpace("" +
				"You are Parallel, a creative execution subagent.\n" +
				"Generate component-level outputs and parallel alternatives while keeping implementation practical."),
			Enabled:   true,
			UpdatedAt: now,
		},
		{
			Name:             "clone",
			Mode:             ModeSubagent,
			Description:      "Swarm clone",
			Provider:         "",
			ExecutionSetting: pebblestore.AgentExecutionSettingReadWrite,
			Prompt: strings.TrimSpace("" +
				"You are Clone, a fast implementation subagent mirroring Swarm behavior.\n" +
				"Execute concrete file-change tasks and report exact edits with minimal narrative."),
			Enabled:   true,
			UpdatedAt: now,
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

func (s *Service) bumpVersionLocked() (int64, error) {
	version, ok, err := s.store.GetVersion()
	if err != nil {
		return 0, err
	}
	if !ok {
		version = 0
	}
	version++
	if err := s.store.SetVersion(version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Service) currentStateLocked(limit int) (State, error) {
	profiles, err := s.store.ListProfiles(limit)
	if err != nil {
		return State{}, err
	}
	customTools, err := s.store.ListCustomTools(limit)
	if err != nil {
		return State{}, err
	}
	activePrimary, _, err := s.store.GetActivePrimary()
	if err != nil {
		return State{}, err
	}
	activeSubagent, err := s.store.GetActiveSubagents(200)
	if err != nil {
		return State{}, err
	}
	version, _, err := s.store.GetVersion()
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
	name = normalizeName(name)
	if name == "" {
		return false, nil
	}
	profile, ok, err := s.store.GetProfile(name)
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
	exclude = normalizeName(exclude)
	profiles, err := s.store.ListProfiles(2000)
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
	return s.events.Append("system:agent", eventType, strings.TrimSpace(entityID), raw, "", "")
}
