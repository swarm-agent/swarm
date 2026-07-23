package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	manageSessionsDeployManifestVersion = 1
	manageSessionsDeployMaxProposals    = 8
)

type manageSessionsDeployProposal struct {
	ID                  string `json:"id"`
	Title               string `json:"title,omitempty"`
	Prompt              string `json:"prompt"`
	Mode                string `json:"mode"`
	AgentName           string `json:"agent_name"`
	AgentMode           string `json:"agent_mode"`
	RuntimeMode         string `json:"runtime_mode"`
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model,omitempty"`
	Thinking            string `json:"thinking,omitempty"`
	ServiceTier         string `json:"service_tier,omitempty"`
	ContextMode         string `json:"context_mode,omitempty"`
	WorkspaceID         string `json:"workspace_id,omitempty"`
	WorkspaceBindingID  string `json:"workspace_binding_id,omitempty"`
	WorkspaceGeneration int64  `json:"workspace_generation,omitempty"`
	WorkspacePath       string `json:"workspace_path"`
	WorkspaceName       string `json:"workspace_name,omitempty"`
	ManagedWorktree     bool   `json:"managed_worktree"`
	WorktreeBaseBranch  string `json:"worktree_base_branch,omitempty"`
	WorktreeBranch      string `json:"worktree_branch,omitempty"`
	Selected            bool   `json:"selected"`
}

type manageSessionsDeployWorkspace struct {
	ID         string `json:"id"`
	Generation int64  `json:"generation"`
	Path       string `json:"path"`
	Name       string `json:"name,omitempty"`
}

type manageSessionsDeployManifest struct {
	ManifestVersion   int                             `json:"manifest_version"`
	Action            string                          `json:"action"`
	ParentSessionID   string                          `json:"parent_session_id"`
	AccountScopeID    string                          `json:"account_scope_id"`
	UserID            string                          `json:"user_id"`
	Proposals         []manageSessionsDeployProposal  `json:"proposals"`
	AllowedWorkspaces []manageSessionsDeployWorkspace `json:"allowed_workspaces"`
	ManifestDigest    string                          `json:"manifest_digest"`
	ApprovedArguments map[string]any                  `json:"approved_arguments"`
}

type manageSessionsDeployInput struct {
	Title         string
	Prompt        string
	Mode          string
	Agent         string
	WorkspacePath string
	Worktree      bool
	WorktreeName  string
}

// manageSessionsDeployAgentResolution deliberately separates the immutable
// execution identity from the stored profile that owns model preferences.
// Swarm's compiled profile is model-less; its account row remains the model
// selection authority without becoming executable identity metadata. Queued AI
// tasks additionally preserve the account's active primary profile when it is a
// plan/auto split profile, so a plan-mode task can switch to that profile's auto
// lane after exit_plan_mode.
type manageSessionsDeployAgentResolution struct {
	ExecutionProfile  pebblestore.AgentProfile
	PreferenceProfile pebblestore.AgentProfile
}

func (r manageSessionsDeployAgentResolution) preferenceForMode(base pebblestore.ModelPreference, mode string) pebblestore.ModelPreference {
	source := r.PreferenceProfile
	if strings.EqualFold(r.ExecutionProfile.Name, agentruntime.SwarmAgentID) {
		// The stored Swarm row intentionally has no executable runtime/tool
		// metadata. Borrow only the compiled identity's plan capability so the
		// generic selector can choose the stored split lane.
		source.RuntimeMode = r.ExecutionProfile.RuntimeMode
		source.ExitPlanModeEnabled = pebblestore.CloneBoolPtr(r.ExecutionProfile.ExitPlanModeEnabled)
		source.ToolContract = pebblestore.CloneAgentToolContract(r.ExecutionProfile.ToolContract)
	}
	return applyAgentPreferenceOverridesForMode(base, source, mode)
}

func (s *Service) resolveQueuedAITaskDeployAgent(profiles map[string]pebblestore.AgentProfile, activeName string) (manageSessionsDeployAgentResolution, bool, error) {
	activeName = strings.ToLower(strings.TrimSpace(activeName))
	if active := profiles[activeName]; active.Enabled && active.Mode == agentruntime.ModePrimary && pebblestore.AgentProfileRuntimeMode(active) == pebblestore.AgentRuntimeModePlanAuto && pebblestore.AgentModelMode(active) == "split" && pebblestore.AgentSupportsSplitModel(active) {
		if strings.EqualFold(active.Name, agentruntime.SwarmAgentID) {
			return s.resolveManageSessionsDeployAgent(profiles, agentruntime.SwarmAgentID)
		}
		return manageSessionsDeployAgentResolution{ExecutionProfile: active, PreferenceProfile: active}, true, nil
	}
	return s.resolveManageSessionsDeployAgent(profiles, agentruntime.SwarmAgentID)
}

func (s *Service) resolveManageSessionsDeployAgent(profiles map[string]pebblestore.AgentProfile, name string) (manageSessionsDeployAgentResolution, bool, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	stored, found := profiles[name]
	if strings.EqualFold(name, agentruntime.SwarmAgentID) {
		execution, err := s.agents.ResolveSystemAgent(agentruntime.SwarmAgentID, stored)
		if err != nil {
			return manageSessionsDeployAgentResolution{}, false, fmt.Errorf("resolve Swarm system agent: %w", err)
		}
		return manageSessionsDeployAgentResolution{ExecutionProfile: execution, PreferenceProfile: stored}, true, nil
	}
	if !found {
		return manageSessionsDeployAgentResolution{}, false, nil
	}
	return manageSessionsDeployAgentResolution{ExecutionProfile: stored, PreferenceProfile: stored}, true, nil
}

func parseManageSessionsDeployArguments(arguments string) ([]manageSessionsDeployInput, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(arguments)), &root); err != nil {
		return nil, fmt.Errorf("manage-sessions deploy arguments invalid: %w", err)
	}
	for key := range root {
		if key != "action" && key != "proposals" {
			return nil, fmt.Errorf("manage-sessions deploy rejects untrusted field %q", key)
		}
	}
	var action string
	if err := json.Unmarshal(root["action"], &action); err != nil || !strings.EqualFold(strings.TrimSpace(action), "deploy") {
		return nil, fmt.Errorf("manage-sessions deploy requires action deploy")
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(root["proposals"], &raw); err != nil {
		return nil, fmt.Errorf("manage-sessions deploy proposals invalid: %w", err)
	}
	if len(raw) == 0 || len(raw) > manageSessionsDeployMaxProposals {
		return nil, fmt.Errorf("manage-sessions deploy requires 1 to %d proposals", manageSessionsDeployMaxProposals)
	}
	out := make([]manageSessionsDeployInput, 0, len(raw))
	for i, item := range raw {
		for key := range item {
			switch key {
			case "title", "prompt", "mode", "agent", "workspace_path", "worktree", "worktree_name":
			default:
				return nil, fmt.Errorf("manage-sessions deploy proposals[%d] rejects untrusted field %q", i, key)
			}
		}
		var input struct {
			Title         string `json:"title"`
			Prompt        string `json:"prompt"`
			Mode          string `json:"mode"`
			Agent         string `json:"agent"`
			WorkspacePath string `json:"workspace_path"`
			Worktree      *bool  `json:"worktree"`
			WorktreeName  string `json:"worktree_name"`
		}
		encoded, _ := json.Marshal(item)
		if err := json.Unmarshal(encoded, &input); err != nil {
			return nil, fmt.Errorf("manage-sessions deploy proposals[%d] invalid: %w", i, err)
		}
		if strings.TrimSpace(input.Prompt) == "" {
			return nil, fmt.Errorf("manage-sessions deploy proposals[%d] prompt is required", i)
		}
		mode := strings.ToLower(strings.TrimSpace(input.Mode))
		if mode == "" {
			mode = sessionruntime.ModeAuto
		}
		if mode != sessionruntime.ModePlan && mode != sessionruntime.ModeAuto {
			return nil, fmt.Errorf("manage-sessions deploy proposals[%d] mode must be plan or auto", i)
		}
		worktree := true
		if input.Worktree != nil {
			worktree = *input.Worktree
		}
		out = append(out, manageSessionsDeployInput{Title: strings.TrimSpace(input.Title), Prompt: input.Prompt, Mode: mode, Agent: strings.TrimSpace(input.Agent), WorkspacePath: strings.TrimSpace(input.WorkspacePath), Worktree: worktree, WorktreeName: strings.TrimSpace(input.WorktreeName)})
	}
	return out, nil
}

func (s *Service) buildManageSessionsDeployManifest(sessionID string, call tool.Call) (manageSessionsDeployManifest, error) {
	return s.buildManageSessionsDeployManifestBound(sessionID, call, nil)
}

func (s *Service) buildManageSessionsDeployManifestBound(sessionID string, call tool.Call, aiTask *AITaskDeployBinding) (manageSessionsDeployManifest, error) {
	inputs, err := parseManageSessionsDeployArguments(call.Arguments)
	if err != nil {
		return manageSessionsDeployManifest{}, err
	}
	if s == nil || s.sessions == nil || s.agents == nil || s.workspace == nil {
		return manageSessionsDeployManifest{}, fmt.Errorf("manage-sessions deploy resolution services are not configured")
	}
	var parent pebblestore.SessionSnapshot
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		var ok bool
		parent, ok, err = s.sessions.GetSession(sessionID)
		if err != nil {
			return manageSessionsDeployManifest{}, err
		}
		if !ok {
			return manageSessionsDeployManifest{}, fmt.Errorf("session %q not found", sessionID)
		}
	} else {
		if aiTask == nil {
			return manageSessionsDeployManifest{}, errors.New("manage-sessions deploy requires a calling session")
		}
		parent = pebblestore.SessionSnapshot{UserID: strings.TrimSpace(aiTask.UserID), AccountScopeID: strings.TrimSpace(aiTask.AccountScopeID), WorkspacePath: strings.TrimSpace(aiTask.WorkspacePath)}
	}
	if aiTask != nil && (strings.TrimSpace(aiTask.UserID) != strings.TrimSpace(parent.UserID) || strings.TrimSpace(aiTask.AccountScopeID) != strings.TrimSpace(parent.AccountScopeID) || strings.TrimSpace(aiTask.WorkspacePath) != strings.TrimSpace(parent.WorkspacePath)) {
		return manageSessionsDeployManifest{}, errors.New("AI task deployment binding does not match the authorized origin")
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, SessionID: parent.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	if parent.ID == "" {
		principal.AccountScopeSource = identity.AccountScopeSourceServerState
	}
	if !principal.Valid() {
		return manageSessionsDeployManifest{}, identity.ErrPrincipalRequired
	}
	state, err := s.agents.ListStateForAccount(parent.AccountScopeID, 2000)
	if err != nil {
		return manageSessionsDeployManifest{}, err
	}
	profiles := make(map[string]pebblestore.AgentProfile, len(state.Profiles))
	for _, profile := range state.Profiles {
		profiles[strings.ToLower(strings.TrimSpace(profile.Name))] = profile
	}
	activeName := strings.TrimSpace(state.ActivePrimary)
	activeResolution, found, err := s.resolveManageSessionsDeployAgent(profiles, activeName)
	if err != nil {
		return manageSessionsDeployManifest{}, fmt.Errorf("resolve active primary agent: %w", err)
	}
	active := activeResolution.ExecutionProfile
	if !found || !active.Enabled || active.Mode != agentruntime.ModePrimary {
		return manageSessionsDeployManifest{}, fmt.Errorf("active primary agent is missing, disabled, or invalid")
	}
	caller, err := sessionV3AgentProfileFromMetadataMap(parent.Metadata)
	if err != nil || parent.ID == "" {
		caller = active
	}
	callerContract, _, err := s.CompileStoredV3AgentToolContract(parent.AccountScopeID, caller)
	if err != nil {
		return manageSessionsDeployManifest{}, fmt.Errorf("resolve calling agent capability: %w", err)
	}
	canDelegate := callerContract.Tools["task"].Enabled

	knownWorkspaces, err := s.workspace.ListKnownForPrincipal(principal, 2000)
	if err != nil {
		return manageSessionsDeployManifest{}, fmt.Errorf("list deployment workspaces: %w", err)
	}
	manifest := manageSessionsDeployManifest{
		ManifestVersion:   manageSessionsDeployManifestVersion,
		Action:            "deploy",
		ParentSessionID:   parent.ID,
		AccountScopeID:    parent.AccountScopeID,
		UserID:            parent.UserID,
		Proposals:         make([]manageSessionsDeployProposal, 0, len(inputs)),
		AllowedWorkspaces: make([]manageSessionsDeployWorkspace, 0, len(knownWorkspaces)),
	}
	for _, workspace := range knownWorkspaces {
		manifest.AllowedWorkspaces = append(manifest.AllowedWorkspaces, manageSessionsDeployWorkspace{ID: workspace.WorkspaceID, Generation: workspace.WorkspaceGeneration, Path: workspace.Path, Name: workspace.WorkspaceName})
	}
	for i, input := range inputs {
		targetName := active.Name
		if input.Agent != "" {
			targetName = input.Agent
		}
		var resolution manageSessionsDeployAgentResolution
		var exists bool
		var resolveErr error
		if aiTask != nil {
			resolution, exists, resolveErr = s.resolveQueuedAITaskDeployAgent(profiles, activeName)
		} else {
			resolution, exists, resolveErr = s.resolveManageSessionsDeployAgent(profiles, targetName)
		}
		if resolveErr != nil {
			return manageSessionsDeployManifest{}, fmt.Errorf("deploy proposals[%d] agent resolution: %w", i, resolveErr)
		}
		if !exists {
			return manageSessionsDeployManifest{}, fmt.Errorf("deploy proposals[%d] agent %q not found", i, targetName)
		}
		profile := resolution.ExecutionProfile
		if aiTask == nil {
			if err := validateManageSessionsDeployAgent(active, profile, canDelegate); err != nil {
				return manageSessionsDeployManifest{}, fmt.Errorf("deploy proposals[%d]: %w", i, err)
			}
		} else if !profile.Enabled || profile.Mode != agentruntime.ModePrimary || pebblestore.AgentProfileRuntimeMode(profile) != pebblestore.AgentRuntimeModePlanAuto {
			return manageSessionsDeployManifest{}, fmt.Errorf("deploy proposals[%d]: queued AI task requires an enabled plan/auto primary agent", i)
		}
		executionMode, _, err := s.resolveExecutionMode(input.Mode, profile)
		if err != nil {
			return manageSessionsDeployManifest{}, fmt.Errorf("deploy proposals[%d] execution mode: %w", i, err)
		}
		preference := resolution.preferenceForMode(parent.Preference, input.Mode)
		bindingPath, bindingPathErr := resolveManageSessionsDeployBindingPath(parent, input)
		if bindingPathErr != nil {
			return manageSessionsDeployManifest{}, fmt.Errorf("deploy proposals[%d] workspace: %w", i, bindingPathErr)
		}
		workspace, err := s.workspace.ScopeForPathForPrincipal(principal, bindingPath)
		if err != nil || !workspace.Matched {
			if err == nil {
				err = fmt.Errorf("workspace is not an account-owned binding")
			}
			return manageSessionsDeployManifest{}, fmt.Errorf("deploy proposals[%d] workspace: %w", i, err)
		}
		proposal := manageSessionsDeployProposal{ID: fmt.Sprintf("proposal-%d", i+1), Title: input.Title, Prompt: input.Prompt, Mode: input.Mode, AgentName: profile.Name, AgentMode: profile.Mode, RuntimeMode: executionMode, Provider: preference.Provider, Model: preference.Model, Thinking: preference.Thinking, ServiceTier: preference.ServiceTier, ContextMode: preference.ContextMode, WorkspaceID: workspace.WorkspaceID, WorkspaceGeneration: workspace.WorkspaceGeneration, WorkspacePath: workspace.WorkspacePath, WorkspaceName: workspace.WorkspaceName, ManagedWorktree: input.Worktree, Selected: i == 0}
		if input.Worktree {
			if s.worktrees == nil {
				return manageSessionsDeployManifest{}, fmt.Errorf("deploy proposals[%d] requires the managed worktree service", i)
			}
			config, configErr := s.worktrees.GetConfigForPrincipal(principal, workspace.WorkspacePath)
			if configErr != nil {
				return manageSessionsDeployManifest{}, fmt.Errorf("deploy proposals[%d] worktree settings: %w", i, configErr)
			}
			branchSuffix := canonicalDeployWorktreeBranchSuffix(firstNonEmptyString(input.WorktreeName, input.Title), fmt.Sprintf("session-%d", i+1))
			proposal.WorktreeBaseBranch = strings.TrimSpace(config.BaseBranch)
			proposal.WorktreeBranch = canonicalDeployWorktreeBranch(config.BranchName, branchSuffix)
		}
		manifest.Proposals = append(manifest.Proposals, proposal)
	}
	digest, err := manageSessionsDeployDigest(manifest)
	if err != nil {
		return manageSessionsDeployManifest{}, err
	}
	manifest.ManifestDigest = digest
	selected := []string{manifest.Proposals[0].ID}
	manifest.ApprovedArguments = map[string]any{"action": "deploy", "manifest_version": manifest.ManifestVersion, "manifest_digest": digest, "parent_session_id": manifest.ParentSessionID, "account_scope_id": manifest.AccountScopeID, "user_id": manifest.UserID, "selected_proposal_ids": selected, "proposals": manifest.Proposals}
	return manifest, nil
}

func resolveManageSessionsDeployBindingPath(parent pebblestore.SessionSnapshot, input manageSessionsDeployInput) (string, error) {
	if requested := strings.TrimSpace(input.WorkspacePath); requested != "" {
		return requested, nil
	}
	if parent.WorktreeEnabled {
		if source := strings.TrimSpace(mapString(parent.Metadata, "swarm_v3_source_workspace_path")); source != "" {
			return source, nil
		}
		return "", errors.New("calling managed-worktree session is missing backend field swarm_v3_source_workspace_path")
	}
	if workspacePath := strings.TrimSpace(parent.WorkspacePath); workspacePath != "" {
		return workspacePath, nil
	}
	return "", errors.New("calling session is missing backend field workspace_path")
}

func canonicalDeployWorktreeBranchSuffix(title, fallback string) string {
	value := strings.ToLower(strings.TrimSpace(title))
	var b strings.Builder
	separator := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			separator = false
			continue
		}
		if b.Len() > 0 && !separator {
			b.WriteByte('-')
			separator = true
		}
	}
	value = strings.Trim(b.String(), "-")
	if value == "" {
		value = strings.Trim(strings.ToLower(strings.TrimSpace(fallback)), "-/")
	}
	if len(value) > 48 {
		value = strings.Trim(value[:48], "-")
	}
	return value
}

func canonicalDeployWorktreeBranch(configuredPrefix, suffix string) string {
	prefix := strings.Trim(strings.TrimSpace(configuredPrefix), "/")
	if strings.HasSuffix(strings.ToLower(prefix), "/<id>") {
		prefix = strings.Trim(prefix[:len(prefix)-len("/<id>")], "/")
	}
	if prefix == "" || strings.EqualFold(prefix, "<id>") {
		prefix = "agent"
	}
	return prefix + "/" + strings.Trim(suffix, "/")
}

func validateManageSessionsDeployAgent(active, target pebblestore.AgentProfile, canDelegate bool) error {
	if !target.Enabled {
		return fmt.Errorf("agent %q is disabled", target.Name)
	}
	if target.Mode == agentruntime.ModeBackground || (target.Mode != agentruntime.ModePrimary && target.Mode != agentruntime.ModeSubagent) {
		return fmt.Errorf("agent %q is not an allowed primary or subagent", target.Name)
	}
	if !strings.EqualFold(target.Name, active.Name) && !canDelegate {
		return fmt.Errorf("alternate agent %q requires calling primary task/delegation capability", target.Name)
	}
	return nil
}

func manageSessionsDeployDigest(manifest manageSessionsDeployManifest) (string, error) {
	manifest.ManifestDigest = ""
	manifest.ApprovedArguments = nil
	// Workspace choices are server-resolved permission UI data, not deployment
	// authority. The selected proposal's canonical binding is digest-bound.
	manifest.AllowedWorkspaces = nil
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	var canonical any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return "", err
	}
	raw, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
