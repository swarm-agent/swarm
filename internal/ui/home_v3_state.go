package ui

import (
	"strings"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

// HomepageWorkspaceSelection is the lightweight workspace identity needed by
// the home surface. Session collections and transcript state intentionally do
// not belong to the homepage state boundary.
type HomepageWorkspaceSelection struct {
	Name                    string
	Path                    string
	WorkspaceID             string
	WorkspaceGeneration     int64
	LocalWorkspaceBindingID string
}

// HomeSessionIntent is the reusable boundary between editing a homepage draft
// and creating a canonical V3 session. It contains only creation intent; live
// session projections, history, and realtime cursors stay in the chat runtime.
type HomeSessionIntent struct {
	Title              string
	InitialPrompt      string
	Workspace          HomepageWorkspaceSelection
	Agent              string
	Mode               string
	Preference         client.ModelPreference
	PreferenceOverride bool
	Profile            model.ActiveModelProfile
	RouteID            string
	SwarmID            string
	TargetKind         string
	TargetRelationship string
	WorktreeRequested  bool
	WorktreeBranchName string
}

// HomepageState is a compact, copyable selector result for the V3 home page.
// It deliberately excludes recent sessions, transcript history, and realtime
// transport state so the home surface can bootstrap independently of chat.
type HomepageState struct {
	SelectedWorkspace HomepageWorkspaceSelection
	SelectedAgent     string
	ModelProvider     string
	ModelName         string
	Thinking          string
	ServiceTier       string
	ContextMode       string
	Profile           model.ActiveModelProfile
	ComposerInput     string
	SessionIntent     HomeSessionIntent
}

func (p *HomePage) HomepageState() HomepageState {
	if p == nil {
		return HomepageState{}
	}
	provider, modelName, thinking, serviceTier, contextMode := p.ModelState()
	state := HomepageState{
		SelectedAgent: strings.TrimSpace(p.model.ActiveAgent),
		ModelProvider: strings.TrimSpace(provider),
		ModelName:     strings.TrimSpace(modelName),
		Thinking:      strings.TrimSpace(thinking),
		ServiceTier:   strings.TrimSpace(serviceTier),
		ContextMode:   strings.TrimSpace(contextMode),
		Profile:       p.model.ActiveModelProfile,
		ComposerInput: p.prompt,
		SessionIntent: p.sessionIntent,
	}
	if state.SelectedAgent == "" {
		state.SelectedAgent = "swarm"
	}
	if workspace, ok := p.activeWorkspace(); ok {
		state.SelectedWorkspace = HomepageWorkspaceSelection{
			Name:                    strings.TrimSpace(workspace.Name),
			Path:                    strings.TrimSpace(workspace.Path),
			WorkspaceID:             strings.TrimSpace(workspace.WorkspaceID),
			WorkspaceGeneration:     workspace.WorkspaceGeneration,
			LocalWorkspaceBindingID: strings.TrimSpace(workspace.LocalWorkspaceBindingID),
		}
	}
	return state
}

// SetWorktreeRequested primes the next routed start locally. It does not alter
// workspace worktree settings.
func (p *HomePage) SetWorktreeRequested(requested bool) {
	if p == nil {
		return
	}
	p.sessionIntent.WorktreeRequested = requested
	p.statusLine = "Worktree: " + map[bool]string{true: "on", false: "off"}[requested]
}

func (p *HomePage) WorktreeRequested() bool {
	return p != nil && p.sessionIntent.WorktreeRequested
}

func (p *HomePage) SetSessionIntent(intent HomeSessionIntent) {
	if p == nil {
		return
	}
	p.sessionIntent = intent
}

func (p *HomePage) SetDraftChatPreference(preference client.ModelPreference) {
	if p == nil {
		return
	}
	p.sessionIntent.Preference = preference
	p.sessionIntent.PreferenceOverride = strings.TrimSpace(preference.Provider) != "" && strings.TrimSpace(preference.Model) != ""
}

func (p *HomePage) SessionIntent() HomeSessionIntent {
	if p == nil {
		return HomeSessionIntent{}
	}
	return p.SessionIntentForMode(p.SessionMode())
}

// SessionIntentForMode projects the same effective Plan/Auto selection used by
// the homepage footer without mutating the current homepage draft mode.
func (p *HomePage) SessionIntentForMode(mode string) HomeSessionIntent {
	if p == nil {
		return HomeSessionIntent{}
	}
	mode = normalizeHomeSessionMode(mode)
	intent := p.sessionIntent
	provider, modelName, thinking, serviceTier, contextMode := effectiveHomeModelState(p.model, mode)
	intent.InitialPrompt = p.prompt
	intent.Agent = strings.TrimSpace(p.model.ActiveAgent)
	if intent.Agent == "" {
		intent.Agent = "swarm"
	}
	intent.Mode = mode
	if !intent.PreferenceOverride {
		intent.Preference = client.ModelPreference{
			Provider:    strings.TrimSpace(provider),
			Model:       strings.TrimSpace(modelName),
			Thinking:    strings.TrimSpace(thinking),
			ServiceTier: strings.TrimSpace(serviceTier),
			ContextMode: strings.TrimSpace(contextMode),
		}
	}
	intent.Profile = p.model.ActiveModelProfile
	if workspace, ok := p.activeWorkspace(); ok {
		intent.Workspace = HomepageWorkspaceSelection{
			Name:                    strings.TrimSpace(workspace.Name),
			Path:                    strings.TrimSpace(workspace.Path),
			WorkspaceID:             strings.TrimSpace(workspace.WorkspaceID),
			WorkspaceGeneration:     workspace.WorkspaceGeneration,
			LocalWorkspaceBindingID: strings.TrimSpace(workspace.LocalWorkspaceBindingID),
		}
	}
	return intent
}
