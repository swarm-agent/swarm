package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"swarm-refactor/swarmtui/internal/buildinfo"
	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

// refreshHomeV3Model builds only the lightweight state needed to compose a
// new V3 session. Session membership, history, transcripts, hydration, and
// realtime are intentionally absent and are loaded by explicit chat features.
func (a *App) refreshHomeV3Model(ctx context.Context) (model.HomeModel, error) {
	next := model.EmptyHome()
	next.ServerURL = a.api.BaseURL()
	next.CWD = emptyFallback(normalizePath(a.startupCWD), ".")

	if strings.TrimSpace(a.api.Token()) == "" {
		if err := a.api.EnsureLocalAuth(ctx); err != nil {
			if errors.Is(err, client.ErrLocalIdentityBootstrapRequired) {
				status, statusErr := a.api.GetOnboardingStatus(ctx)
				if statusErr == nil {
					next.OnboardingRequired = status.NeedsOnboarding
					next.OnboardingUsername = strings.TrimSpace(status.Identity.Username)
					next.OnboardingSwarmName = strings.TrimSpace(status.Config.SwarmName)
				}
				next.OnboardingRequired = true
				next.HintLine = "Required onboarding: create username + swarm name before using Swarm."
				return next, nil
			}
			return next, fmt.Errorf("local auth bootstrap: %w", err)
		}
	}

	if vault, err := a.api.GetVaultStatus(ctx); err == nil {
		a.vault = vault
		if vault.Enabled && !vault.Unlocked {
			next.HintLine = "Vault is locked. Unlock it before using Swarm."
			next.TipLine = "/vault"
			return next, nil
		}
	}

	var (
		workspaceData homeBootstrapData
		health        client.HealthStatus
		healthErr     error
		providers     []client.ProviderStatus
		providersErr  error
		resolved      client.ModelResolved
		resolvedErr   error
		profiles      client.ModelProfileState
		profilesErr   error
		agents        client.AgentState
		agentsErr     error
		update        client.UpdateStatus
		updateErr     error
	)
	var wg sync.WaitGroup
	wg.Add(7)
	go func() { defer wg.Done(); workspaceData = a.bootstrapHomeWorkspace(ctx) }()
	go func() { defer wg.Done(); health, healthErr = a.api.GetHealth(ctx) }()
	go func() { defer wg.Done(); providers, providersErr = a.api.ListProviders(ctx) }()
	go func() { defer wg.Done(); resolved, resolvedErr = a.api.GetModel(ctx) }()
	go func() { defer wg.Done(); profiles, profilesErr = a.api.ListModelProfiles(ctx) }()
	go func() { defer wg.Done(); agents, agentsErr = a.api.ListAgents(ctx, 200) }()
	go func() { defer wg.Done(); update, updateErr = a.api.GetUpdateStatus(ctx) }()
	wg.Wait()

	selectedPath := ""
	warnings := make([]string, 0, 8)
	next, selectedPath, warnings = applyHomeWorkspaceBootstrap(next, workspaceData, a.startupCWD)
	if selectedPath != "" {
		next.CWD = selectedPath
	}
	if len(next.ChatRoutes) > 0 {
		a.selectedChatRouteID = a.resolveSelectedChatRouteIDForWorkspace(selectedPath, next.ChatRoutes)
		next.SelectedChatRouteID = a.selectedChatRouteID
	}

	if healthErr == nil {
		next.ServerMode = emptyFallback(strings.TrimSpace(health.Mode), next.ServerMode)
		next.BypassPermissions = health.BypassPermissions
	} else {
		warnings = append(warnings, "daemon status unavailable")
	}
	if providersErr == nil {
		for _, provider := range providers {
			if provider.Runnable {
				next.AuthConfigured = true
				break
			}
		}
	} else {
		warnings = append(warnings, "provider status unavailable")
	}
	if resolvedErr == nil {
		next = applyHomeModelResolved(next, resolved)
	} else {
		warnings = append(warnings, "model preference unavailable")
	}
	if profilesErr == nil {
		next = applyHomeModelProfiles(next, profiles)
	} else {
		warnings = append(warnings, "model profiles unavailable")
	}
	if agentsErr == nil {
		a.agentState = agents
		next.ActiveAgent, next.ActiveAgentExecutionSetting, next.ActiveAgentExitPlanMode, next.ActiveAgentRuntimeKnown = activeAgentRuntime(agents)
		next.Subagents = chatMentionSubagentNames(agents)
		next = applyActiveAgentModels(next, agents)
	} else {
		next.ActiveAgent = "swarm"
		next.ActiveAgentExitPlanMode = true
		next.ActiveAgentRuntimeKnown = true
		warnings = append(warnings, "agent state unavailable")
	}
	if updateErr == nil {
		next.UpdateStatus = &update
		if version := strings.TrimSpace(update.CurrentVersion); version != "" {
			next.Version = version
		}
	} else if strings.TrimSpace(buildinfo.DisplayVersion()) != "dev" {
		warnings = append(warnings, "update status unavailable")
	}

	activePath := normalizePath(next.CWD)
	gitStatus, _ := gitStatusForPath(activePath)
	for i := range next.Directories {
		if pathsEqual(next.Directories[i].ResolvedPath, activePath) {
			applyGitStatusToDirectory(&next.Directories[i], gitStatus)
			break
		}
	}
	if activePath != "" {
		if worktree, err := a.api.GetWorktreeSettings(ctx, activePath); err == nil {
			next.WorktreesEnabled = worktree.Enabled
		}
		if report, err := a.api.ContextSources(ctx, activePath); err == nil {
			next.RuleCount = len(report.Rules)
			next.SkillCount = len(report.Skills)
			agentsToken := contextAgentsToken(report.Rules)
			for i := range next.Directories {
				if pathsEqual(next.Directories[i].ResolvedPath, activePath) {
					next.Directories[i].AgentsToken = agentsToken
				}
			}
		}
	}

	next.QuickActions = homeQuickActions(next)
	switch {
	case activeWorkspaceIndex(next.Workspaces) < 0 && !next.AuthConfigured:
		next.HintLine = "Choose a workspace and configure auth to start"
		next.TipLine = "/workspace  •  /auth"
	case activeWorkspaceIndex(next.Workspaces) < 0:
		next.HintLine = "Choose a workspace to start"
		next.TipLine = "/workspace"
	case !next.AuthConfigured:
		next.HintLine = "Auth is missing, run /auth"
		next.TipLine = "/auth"
	default:
		next.HintLine = ""
		next.TipLine = ""
	}
	if len(warnings) > 0 {
		warning := strings.Join(warnings, "; ")
		if len(warning) > 120 {
			warning = warning[:120] + "..."
		}
		next.HintLine = strings.TrimSpace(next.HintLine + " • " + warning)
	}
	return next, nil
}
