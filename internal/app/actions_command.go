package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

func (a *App) handleActionsCommand(args []string) {
	if a == nil || a.home == nil {
		return
	}
	if len(args) > 1 || (len(args) == 1 && !strings.EqualFold(strings.TrimSpace(args[0]), "list")) {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /actions [list]")
		return
	}
	workspacePath := strings.TrimSpace(a.activeWorkspacePath())
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.activeContextPath())
	}
	if workspacePath == "" {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("/actions failed: active workspace path is unavailable")
		return
	}
	if a.api == nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("/actions failed: api client is not configured")
		return
	}

	a.home.ClearCommandOverlay()
	a.home.ShowWorkspaceActionsLoading(workspacePath)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	actions, err := a.api.ListWorkspaceActions(ctx, workspacePath)
	if err != nil {
		a.home.SetWorkspaceActionsError("Could not load Actions: " + err.Error())
		a.home.SetStatus("/actions failed: " + err.Error())
		return
	}
	orderWorkspaceActionsForQuickAccess(actions)
	a.home.SetWorkspaceActions(actions)
	a.home.SetStatus(fmt.Sprintf("workspace Actions: %d", len(actions)))
}

func orderWorkspaceActionsForQuickAccess(actions []client.WorkspaceAction) {
	sort.SliceStable(actions, func(i, j int) bool {
		if actions[i].Pinned != actions[j].Pinned {
			return actions[i].Pinned
		}
		return actions[i].SortIndex < actions[j].SortIndex
	})
}

func (a *App) consumeWorkspaceActionSelection() bool {
	if a == nil || a.home == nil {
		return false
	}
	selection, ok := a.home.PopWorkspaceActionSelection()
	if !ok {
		return false
	}
	if a.api == nil {
		a.home.SetWorkspaceActionsError("Could not run Action: api client is not configured")
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	run, err := a.api.StartWorkspaceAction(ctx, selection.WorkspacePath, selection.Action.ID, nil)
	if err != nil {
		a.home.SetWorkspaceActionsError("Could not run Action: " + err.Error())
		a.home.SetStatus("Action failed to start: " + err.Error())
		return true
	}
	a.home.SetStatus(fmt.Sprintf("Action %s started · %s", strings.TrimSpace(selection.Action.Name), strings.TrimSpace(run.ID)))
	return true
}
