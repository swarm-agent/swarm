package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
)

func (a *App) v3ChatStyles() v3chat.PageStyles {
	theme := a.effectiveThemeOption().Theme
	return v3chat.PageStyles{
		Background:   theme.Background,
		Panel:        theme.Panel,
		Element:      theme.Element,
		Border:       theme.Border,
		BorderActive: theme.BorderActive,
		Text:         theme.Text,
		Muted:        theme.TextMuted,
		Primary:      theme.Primary,
		Accent:       theme.Accent,
		Secondary:    theme.Secondary,
		Success:      theme.Success,
		Warning:      theme.Warning,
		Error:        theme.Error,
		Prompt:       theme.Prompt,
		Cursor:       theme.PromptCursor,
		RenderMarkdown: func(body string, width int) []v3chat.MarkdownLine {
			lines := ui.RenderMarkdownLines(theme, body, width)
			out := make([]v3chat.MarkdownLine, 0, len(lines))
			for _, line := range lines {
				converted := v3chat.MarkdownLine{Text: line.Text, Style: line.Style, Spans: make([]v3chat.MarkdownSpan, 0, len(line.Spans))}
				for _, span := range line.Spans {
					converted.Spans = append(converted.Spans, v3chat.MarkdownSpan{Text: span.Text, Style: span.Style})
				}
				out = append(out, converted)
			}
			return out
		},
	}
}

// requestV3ChatRender coalesces token bursts into one queued terminal wakeup.
func (a *App) requestV3ChatRender() {
	if a == nil || a.screen == nil {
		return
	}
	select {
	case a.pendingV3ChatRender <- struct{}{}:
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptV3Chat))
	default:
	}
}

func (a *App) consumeV3ChatRender() {
	select {
	case <-a.pendingV3ChatRender:
	default:
	}
}

func (a *App) newV3Runtime() *v3chat.Runtime {
	store := v3chat.NewStore()
	return v3chat.NewRuntime(a.api, store, a.requestV3ChatRender)
}

func (a *App) v3ChatFooterRouteLabel(route model.ChatRoute) string {
	label := a.displayChatRouteLabel(route)
	if label == "" || strings.EqualFold(label, "host") {
		label = a.currentSwarmName()
	}
	return strings.TrimSpace(label)
}

func (a *App) v3ChatSessionFooterRouteLabel(summary model.SessionSummary) string {
	if route, ok := a.sessionRouteFromMetadata(summary.WorkspacePath, summary.Metadata); ok {
		return a.v3ChatFooterRouteLabel(route)
	}
	return a.v3ChatFooterRouteLabel(a.selectedChatRouteForWorkspace(summary.WorkspacePath))
}

func (a *App) newV3ChatPage(runtime *v3chat.Runtime, routeLabel, profileLabel string) *v3chat.Page {
	page := v3chat.NewPage(runtime, a.v3ChatStyles())
	page.SetRouteLabel(routeLabel)
	page.SetProfileLabel(profileLabel)
	page.SetHeaderVisible(a.config.Chat.ShowHeader)
	page.SetCommandSuggestions(v3ChatCommandSuggestions(buildHomeCommandSuggestions(a.startupDevMode())))
	page.SetKeyMatcher(func(ev *tcell.EventKey, action string) bool {
		keybinds := a.activeKeyBindings()
		switch action {
		case v3chat.KeyEscape:
			return keybinds.Match(ev, ui.KeybindChatEscape)
		case v3chat.KeyMoveUp:
			return keybinds.Match(ev, ui.KeybindChatMoveUp)
		case v3chat.KeyMoveDown:
			return keybinds.Match(ev, ui.KeybindChatMoveDown)
		case v3chat.KeyMoveUpAlt:
			return keybinds.Match(ev, ui.KeybindChatMoveUpAlt)
		case v3chat.KeyMoveDownAlt:
			return keybinds.Match(ev, ui.KeybindChatMoveDownAlt)
		case v3chat.KeyPageUp:
			return keybinds.Match(ev, ui.KeybindChatPageUp)
		case v3chat.KeyPageDown:
			return keybinds.Match(ev, ui.KeybindChatPageDown)
		case v3chat.KeyJumpHome:
			return keybinds.Match(ev, ui.KeybindChatJumpHome)
		case v3chat.KeyJumpEnd:
			return keybinds.Match(ev, ui.KeybindChatJumpEnd)
		case v3chat.KeyBackspace:
			return keybinds.Match(ev, ui.KeybindChatBackspace)
		case v3chat.KeyMoveLeft:
			return keybinds.Match(ev, ui.KeybindEditorMoveLeft)
		case v3chat.KeyMoveRight:
			return keybinds.Match(ev, ui.KeybindEditorMoveRight)
		case v3chat.KeyClear:
			return keybinds.Match(ev, ui.KeybindChatClear)
		case v3chat.KeyCycleMode:
			return keybinds.Match(ev, ui.KeybindChatCycleMode)
		case v3chat.KeyComplete:
			return keybinds.Match(ev, ui.KeybindChatComplete)
		case v3chat.KeyInsertNewline:
			return keybinds.Match(ev, ui.KeybindChatInsertNewline)
		case v3chat.KeySubmit:
			return keybinds.Match(ev, ui.KeybindChatSubmit)
		default:
			return false
		}
	})
	return page
}

func (a *App) handleV3ChatCommand() {
	if a == nil || a.v3Chat == nil {
		return
	}
	raw := strings.TrimSpace(a.v3Chat.ConsumeCommand())
	if raw == "" {
		return
	}
	a.executeCommand(raw)
	if a.route != "v3chat" || a.v3Chat == nil {
		return
	}
	a.v3Chat.SetCommandEmission("Used " + raw)
}

func v3ChatCommandSuggestions(items []ui.CommandSuggestion) []v3chat.CommandSuggestion {
	out := make([]v3chat.CommandSuggestion, 0, len(items))
	for _, item := range items {
		out = append(out, v3chat.CommandSuggestion{Command: item.Command, Hint: item.Hint, QuickTips: append([]string(nil), item.QuickTips...)})
	}
	return out
}

func v3ChatHomeProfileLabel(profile model.ActiveModelProfile) string {
	switch strings.ToLower(strings.TrimSpace(profile.Source)) {
	case "saved":
		return emptyFallback(strings.TrimSpace(profile.Name), "Saved profile")
	case "temporary":
		return "Temporary/customized"
	default:
		return "Agent model default"
	}
}

func v3ChatCreateModelProfile(profile model.ActiveModelProfile) *client.SessionV3ModelProfileChoice {
	switch strings.ToLower(strings.TrimSpace(profile.Source)) {
	case "saved":
		if profileID := strings.TrimSpace(profile.ProfileID); profileID != "" {
			return &client.SessionV3ModelProfileChoice{SavedProfileID: profileID}
		}
	case "agent-default":
		value := true
		return &client.SessionV3ModelProfileChoice{UseAgentDefault: &value}
	}
	return nil
}

func (a *App) openNewV3Chat(intent ui.HomeSessionIntent, route model.ChatRoute, worktreeSuffix string) error {
	if a == nil || a.api == nil {
		return errors.New("api client is not configured")
	}
	prompt := strings.TrimSpace(intent.InitialPrompt)
	if prompt == "" {
		return errors.New("initial prompt is required")
	}
	workspacePath := strings.TrimSpace(intent.Workspace.Path)
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.activeContextPath())
	}
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.startupCWD)
	}
	if workspacePath == "" {
		return errors.New("workspace path is required")
	}
	knownWorkspace := strings.TrimSpace(a.activeWorkspacePath()) != ""
	useTUIPrimaryCWD := !knownWorkspace
	if knownWorkspace && strings.TrimSpace(route.WorkspaceBindingID) == "" {
		return errors.New("workspace binding id is required")
	}

	worktreeMode := "off"
	var useCurrent *bool
	baseBranch, branchName := "", ""
	if knownWorkspace {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		settings, err := a.api.GetWorktreeSettings(ctx, workspacePath)
		cancel()
		if err != nil {
			return err
		}
		if settings.Enabled || strings.TrimSpace(worktreeSuffix) != "" {
			worktreeMode = "on"
			value := settings.UseCurrentBranch
			if !settings.Enabled && strings.TrimSpace(worktreeSuffix) != "" {
				value = true
			}
			useCurrent = &value
			if !value {
				baseBranch = strings.TrimSpace(settings.BaseBranch)
			}
			branchName = normalizeWorktreeBranchPrefix(settings.BranchName)
			if suffix := strings.Trim(strings.TrimSpace(worktreeSuffix), "/"); suffix != "" {
				branchName = strings.Trim(branchName, "/") + "/" + suffix
			}
		}
	}

	mode := normalizeAppSessionMode(intent.Mode)
	if mode != "auto" {
		mode = "plan"
	}
	cwdPath := ""
	if useTUIPrimaryCWD {
		cwdPath = workspacePath
	}
	create := client.SessionCreateOptions{
		Title:                    emptyFallback(strings.TrimSpace(intent.Title), "New Session"),
		WorkspacePath:            workspacePath,
		CWDPath:                  cwdPath,
		WorkspaceName:            intent.Workspace.Name,
		WorkspaceBindingID:       route.WorkspaceBindingID,
		TUIPrimaryCWD:            useTUIPrimaryCWD,
		Mode:                     mode,
		AgentName:                emptyFallback(strings.TrimSpace(intent.Agent), "swarm"),
		SwarmID:                  createSessionSwarmIDForRoute(route, a.homeModel.CurrentSwarmTarget),
		TargetKind:               route.TargetKind,
		TargetRelationship:       route.TargetRelationship,
		Preference:               intent.Preference,
		ModelProfile:             v3ChatCreateModelProfile(intent.Profile),
		WorktreeMode:             worktreeMode,
		WorktreeUseCurrentBranch: useCurrent,
		WorktreeBaseBranch:       baseBranch,
		WorktreeBranchName:       branchName,
	}
	runtime := a.newV3Runtime()
	a.closeV3Chat()
	a.v3Chat = a.newV3ChatPage(runtime, a.v3ChatFooterRouteLabel(route), v3ChatHomeProfileLabel(intent.Profile))
	a.route = "v3chat"
	a.home.ClearPrompt()
	a.v3Chat.OpenNew(v3chat.NewSessionRequest{Create: create, InitialPrompt: prompt})
	return nil
}

func (a *App) openExistingV3Chat(summary model.SessionSummary) error {
	if a == nil || a.api == nil {
		return errors.New("api client is not configured")
	}
	sessionID := strings.TrimSpace(summary.ID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	scope := strings.TrimSpace(summary.WorkspacePath)
	if scope == "" {
		scope = strings.TrimSpace(a.activeContextPath())
	}
	workspacePath, cwdPath := "", ""
	if strings.TrimSpace(a.activeWorkspacePath()) != "" {
		workspacePath = scope
	} else {
		cwdPath = scope
	}
	runtime := a.newV3Runtime()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := runtime.Hydrate(ctx, sessionID, workspacePath, cwdPath); err != nil {
		return err
	}
	if err := runtime.Connect(ctx); err != nil {
		return err
	}
	a.closeV3Chat()
	a.v3Chat = a.newV3ChatPage(runtime, a.v3ChatSessionFooterRouteLabel(summary), "")
	a.route = "v3chat"
	return nil
}

func (a *App) closeV3Chat() {
	if a != nil && a.v3Chat != nil {
		a.v3Chat.Close()
		if a.v3Chat.Runtime() != nil {
			a.v3Chat.Runtime().Stop()
		}
	}
	if a != nil {
		a.v3Chat = nil
	}
}
