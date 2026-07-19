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
	return v3chat.PageStyles{Background: theme.Background, Panel: theme.Panel, Border: theme.Border, Text: theme.Text, Muted: theme.TextMuted, Primary: theme.Primary, Accent: theme.Accent, Secondary: theme.Secondary, Success: theme.Success, Warning: theme.Warning, Error: theme.Error, Prompt: theme.Prompt, Cursor: theme.PromptCursor}
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

func (a *App) newV3ChatPage(runtime *v3chat.Runtime) *v3chat.Page {
	page := v3chat.NewPage(runtime, a.v3ChatStyles())
	page.SetCycleModeMatcher(func(ev *tcell.EventKey) bool {
		return a.activeKeyBindings().Match(ev, ui.KeybindChatCycleMode)
	})
	return page
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
		WorktreeMode:             worktreeMode,
		WorktreeUseCurrentBranch: useCurrent,
		WorktreeBaseBranch:       baseBranch,
		WorktreeBranchName:       branchName,
	}
	runtime := a.newV3Runtime()
	a.closeV3Chat()
	a.v3Chat = a.newV3ChatPage(runtime)
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
	a.v3Chat = a.newV3ChatPage(runtime)
	a.route = "v3chat"
	return nil
}

func (a *App) closeV3Chat() {
	if a != nil && a.v3Chat != nil && a.v3Chat.Runtime() != nil {
		a.v3Chat.Runtime().Stop()
	}
	if a != nil {
		a.v3Chat = nil
	}
}
