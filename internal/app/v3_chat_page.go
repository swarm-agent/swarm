package app

import (
	"context"
	"errors"
	"fmt"
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
	runtime := v3chat.NewRuntime(a.api, store, a.requestV3ChatRender)
	runtime.SetRoutedActivation(a.activateRoutedV3Session)
	return runtime
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
	page.SetThinkingTagsVisible(a.config.Chat.ThinkingTags)
	page.SetCommandSuggestions(v3ChatCommandSuggestions(buildChatCommandSuggestions(a.startupDevMode())))
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

func (a *App) showV3CurrentPlan() {
	if a == nil || a.v3Chat == nil {
		return
	}
	if a.v3Chat.PlanModalVisible() {
		a.v3Chat.ClosePlanModal()
		a.v3Chat.SetStatus("current plan closed")
		return
	}
	if a.api == nil {
		a.v3Chat.ClosePlanModal()
		a.v3Chat.SetStatus("/plan failed: api client is not configured")
		return
	}
	runtime := a.v3Chat.Runtime()
	if runtime == nil || runtime.Store() == nil {
		a.v3Chat.ClosePlanModal()
		a.v3Chat.SetStatus("/plan failed: session state is unavailable")
		return
	}
	sessionID := strings.TrimSpace(runtime.Store().Snapshot().Session.ID)
	if sessionID == "" {
		a.v3Chat.ClosePlanModal()
		a.v3Chat.SetStatus("/plan failed: session id is unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	plan, ok, err := a.api.GetActiveSessionPlan(ctx, sessionID)
	if err != nil {
		a.v3Chat.ClosePlanModal()
		a.v3Chat.SetStatus("/plan failed: " + err.Error())
		return
	}
	if !ok {
		a.v3Chat.ClosePlanModal()
		a.v3Chat.SetStatus("current plan: no active plan")
		return
	}
	if !a.v3Chat.OpenCurrentPlanModal(plan) {
		a.v3Chat.ClosePlanModal()
		a.v3Chat.SetStatus("/plan failed: current plan has no structured document")
		return
	}
	a.v3Chat.SetStatus("current plan: " + emptyFallback(strings.TrimSpace(plan.Title), "untitled") + " · " + strings.TrimSpace(plan.ID))
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
	if status := strings.TrimSpace(a.home.Status()); status != "" {
		a.v3Chat.SetStatus(status)
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

func (a *App) v3ChatDraftModeSelections() map[string]v3chat.DraftModeSelection {
	if a == nil || a.home == nil {
		return nil
	}
	selections := make(map[string]v3chat.DraftModeSelection, 2)
	for _, mode := range []string{"plan", "auto"} {
		intent := a.home.SessionIntentForMode(mode)
		preference := intent.Preference
		profile := intent.Profile
		contextWindow := model.CodexContextWindow(preference.Provider, preference.Model, preference.ContextMode, a.homeModel.ContextWindow)
		selections[mode] = v3chat.DraftModeSelection{
			Preference:    preference,
			ModelProfile:  v3ChatCreateModelProfile(profile),
			ContextWindow: contextWindow,
			AgentModelPolicy: client.SessionV3AgentModelPolicy{
				Preference:    preference,
				ContextWindow: contextWindow,
				ProfileID:     strings.TrimSpace(profile.ProfileID),
				ProfileName:   strings.TrimSpace(profile.Name),
				ProfileSource: strings.TrimSpace(profile.Source),
				ProfileMode:   strings.TrimSpace(profile.ModelMode),
			},
		}
	}
	return selections
}

func (a *App) openNewV3Chat(intent ui.HomeSessionIntent, _ model.ChatRoute, worktreeSuffix string) error {
	command := v3chat.NewCommand{
		Prompt:                   strings.TrimSpace(intent.InitialPrompt),
		PlanModeRequested:        strings.EqualFold(strings.TrimSpace(intent.Mode), "plan"),
		ManagedWorktreeRequested: intent.WorktreeRequested || strings.TrimSpace(worktreeSuffix) != "",
	}
	return a.openRoutedV3Primer(command)
}

// openRoutedV3Primer opens one local-only Router primer. Prompt-bearing forms
// submit immediately; bare forms remain editable and have no durable identity.
func (a *App) openRoutedV3Primer(command v3chat.NewCommand) error {
	if a == nil || a.api == nil {
		return errors.New("api client is not configured")
	}
	if a.home == nil {
		return errors.New("home page is not configured")
	}
	runtime := a.newV3Runtime()
	page := a.newV3ChatPage(runtime, "", "")
	a.closeV3Chat()
	a.v3Chat = page
	a.route = "v3chat"
	a.home.ClearPrompt()
	metadata := map[string]any{"source": "tui"}
	if err := page.OpenRoutedNew(command, emptyFallback(strings.TrimSpace(a.homeModel.ActiveAgent), "swarm"), metadata); err != nil {
		a.closeV3Chat()
		a.route = "home"
		return err
	}
	return nil
}

func (a *App) activateRoutedV3Session(ctx context.Context, response client.RoutedSessionV3StartResponse) error {
	if a == nil || a.api == nil || a.home == nil || a.v3Chat == nil {
		return errors.New("routed TUI activation is not configured")
	}
	identity := response.SessionView.Identity
	if identity == nil {
		return errors.New("Router response has no canonical identity")
	}
	sourcePath := normalizePath(identity.SourceWorkspacePath)
	if sourcePath == "" {
		return errors.New("Router response has no source workspace path")
	}
	resolve, err := a.api.WorkspaceCWDResolve(ctx, sourcePath)
	if err != nil {
		return fmt.Errorf("resolve Router-selected workspace: %w", err)
	}
	if resolve.Workspace == nil {
		return errors.New("Router-selected source workspace is not registered")
	}
	resolvedSourcePath := firstNonEmpty(normalizePath(resolve.Workspace.WorkspacePath), normalizePath(resolve.ResolvedPath))
	if resolvedSourcePath == "" || !pathsEqual(sourcePath, resolvedSourcePath) {
		return errors.New("Router-selected workspace resolved to a different source workspace")
	}
	resolution := *resolve.Workspace
	if strings.TrimSpace(resolution.WorkspaceName) == "" {
		resolution.WorkspaceName = strings.TrimSpace(identity.SourceWorkspaceName)
	}
	if strings.TrimSpace(resolution.ResolvedPath) == "" {
		resolution.ResolvedPath = sourcePath
	}
	a.homeModel = applyCWDResolverToHomeModel(a.homeModel, resolve)
	a.syncActiveWorkspaceSelection(resolution)
	a.selectedChatRouteID = a.resolveSelectedChatRouteIDForRoutedIdentity(sourcePath, identity)
	a.homeModel.SelectedChatRouteID = a.selectedChatRouteID
	a.home.SetModel(a.homeModel)
	a.home.SetSessionIntent(buildHomeSessionIntent(a.home, a.selectedChatRouteForWorkspace(sourcePath)))
	a.v3Chat.SetRouteLabel(a.v3ChatSessionFooterRouteLabel(sessionSummaryFromClient(response.Session)))
	a.v3Chat.SetProfileLabel(strings.TrimSpace(response.SessionView.AgenticSettings.AgentModelPolicy.ProfileName))
	a.v3Chat.SetStyles(a.v3ChatStyles())
	return nil
}

func (a *App) resolveSelectedChatRouteIDForRoutedIdentity(sourcePath string, identity *client.RoutedSessionV3Identity) string {
	if identity == nil {
		return a.resolveSelectedChatRouteIDForWorkspace(sourcePath, a.homeModel.ChatRoutes)
	}
	bindingID := strings.TrimSpace(identity.WorkspaceBindingID)
	runtimeSwarmID := strings.TrimSpace(identity.RuntimeSwarmID)
	for _, route := range a.homeModel.ChatRoutes {
		if bindingID != "" && !strings.EqualFold(strings.TrimSpace(route.WorkspaceBindingID), bindingID) {
			continue
		}
		if runtimeSwarmID != "" && !strings.EqualFold(strings.TrimSpace(route.SwarmID), runtimeSwarmID) {
			continue
		}
		if routeID := strings.TrimSpace(route.ID); routeID != "" {
			return routeID
		}
	}
	return a.resolveSelectedChatRouteIDForWorkspace(sourcePath, a.homeModel.ChatRoutes)
}

func sessionSummaryFromClient(summary client.SessionSummary) model.SessionSummary {
	return model.SessionSummary{
		ID:            strings.TrimSpace(summary.ID),
		Title:         strings.TrimSpace(summary.Title),
		WorkspacePath: strings.TrimSpace(summary.WorkspacePath),
		Metadata:      cloneMetadataMap(summary.Metadata),
	}
}

func (a *App) v3ChatDraftActive() bool {
	return a != nil && a.route == "v3chat" && a.v3Chat != nil && a.v3Chat.Runtime() != nil && a.v3Chat.Runtime().Store() != nil && strings.TrimSpace(a.v3Chat.Runtime().Store().Snapshot().Session.ID) == ""
}

func (a *App) syncPrimedV3ChatFromHomeDraft() {
	if !a.v3ChatDraftActive() || a.home == nil {
		return
	}
	runtime := a.v3Chat.Runtime()
	state := runtime.Store().Snapshot()
	draft, ok := v3chat.SelectRoutedDraft(state)
	if !ok || draft.Status != v3chat.RoutedDraftReady {
		return
	}
	intent := a.home.SessionIntent()
	if err := runtime.UpdateRoutedDraftIntent(draft.Prompt, strings.EqualFold(strings.TrimSpace(intent.Mode), "plan"), draft.ManagedWorktreeRequested); err != nil {
		a.v3Chat.SetStatus("refresh Router draft failed: " + err.Error())
	}
}

func (a *App) openV3ChatDraftAfterWorkspaceChange(previousWorkspacePath string) error {
	if a == nil || a.home == nil {
		return nil
	}
	workspacePath := strings.TrimSpace(a.activeWorkspacePath())
	if workspacePath == "" || pathsEqual(previousWorkspacePath, workspacePath) {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	resolve, err := a.api.WorkspaceCWDResolve(ctx, workspacePath)
	if err != nil {
		return err
	}
	routes := modelChatRoutesFromCWDResolve(resolve)
	if len(routes) == 0 {
		return errors.New("workspace has no V3 chat route")
	}
	a.homeModel = applyCWDResolverToHomeModel(a.homeModel, resolve)
	a.homeModel.ChatRoutes = routes
	a.selectedChatRouteID = a.resolveSelectedChatRouteIDForWorkspace(workspacePath, routes)
	a.homeModel.SelectedChatRouteID = a.selectedChatRouteID
	a.home.SetModel(a.homeModel)
	a.home.SetSessionIntent(buildHomeSessionIntent(a.home, a.selectedChatRouteForWorkspace(workspacePath)))
	switch a.route {
	case "v3chat":
		return a.openChatSession("New Session", "")
	case "chat":
		if a.chat == nil {
			return nil
		}
		if a.chat.RunInProgress() {
			return errors.New("workspace switching is unavailable while a run is active")
		}
		if err := a.openChatSession("New Session", ""); err != nil {
			return err
		}
		a.chat = nil
	}
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
