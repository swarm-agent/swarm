package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

const taskCommandTimeout = 30 * time.Second

type taskCommandRequest struct {
	request         string
	mode            string
	originSessionID string
	requestID       string
	workspaceName   string
	authority       client.RoutedTaskWorkspaceAuthority
}

type taskCommandResult struct {
	response client.RoutedTaskSessionResponse
	err      error
}

type taskCommandOptions struct {
	request           string
	mode              string
	workspaceSelector string
}

func parseTaskCommand(args []string) (taskCommandOptions, error) {
	options := taskCommandOptions{mode: "auto"}
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "plan") {
		options.mode = "plan"
		args = args[1:]
	}
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if !strings.EqualFold(arg, "--workspace") {
			continue
		}
		if options.workspaceSelector != "" {
			return taskCommandOptions{}, fmt.Errorf("--workspace may be specified only once")
		}
		if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
			return taskCommandOptions{}, fmt.Errorf("--workspace requires a saved workspace name")
		}
		options.workspaceSelector = strings.TrimSpace(args[i+1])
		args = append(args[:i], args[i+2:]...)
		i--
	}
	options.request = strings.TrimSpace(strings.Join(args, " "))
	return options, nil
}

func resolveTaskWorkspace(selector, currentPath string, workspaces []model.Workspace) (model.Workspace, error) {
	selector = strings.TrimSpace(selector)
	var selected model.Workspace
	if selector == "" {
		currentPath = normalizePath(strings.TrimSpace(currentPath))
		if currentPath != "" {
			for _, workspace := range workspaces {
				if pathsEqual(workspace.Path, currentPath) {
					selected = workspace
					break
				}
			}
		}
		if strings.TrimSpace(selected.Path) == "" {
			for _, workspace := range workspaces {
				if workspace.Active {
					selected = workspace
					break
				}
			}
		}
		if strings.TrimSpace(selected.Path) == "" {
			return model.Workspace{}, fmt.Errorf("current source workspace is unavailable")
		}
	} else {
		if strings.ContainsAny(selector, `/\\`) || selector == "." || selector == ".." {
			return model.Workspace{}, fmt.Errorf("workspace selector %q must be a saved workspace name, not a path", selector)
		}
		matches := make([]model.Workspace, 0, 1)
		for _, workspace := range workspaces {
			if strings.EqualFold(strings.TrimSpace(workspace.Name), selector) {
				matches = append(matches, workspace)
			}
		}
		switch len(matches) {
		case 0:
			return model.Workspace{}, fmt.Errorf("saved workspace %q was not found", selector)
		case 1:
			selected = matches[0]
		default:
			return model.Workspace{}, fmt.Errorf("saved workspace %q is ambiguous", selector)
		}
	}
	if normalizePath(strings.TrimSpace(selected.Path)) == "" || strings.TrimSpace(selected.LocalWorkspaceBindingID) == "" {
		return model.Workspace{}, fmt.Errorf("saved workspace %q lacks canonical path or binding authority", strings.TrimSpace(selected.Name))
	}
	return selected, nil
}

func (a *App) handleTaskCommand(args []string) {
	a.home.ClearCommandOverlay()
	options, err := parseTaskCommand(args)
	if err != nil {
		message := fmt.Sprintf("/task failed: %v", err)
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}
	if options.request == "" {
		message := "usage: /task [plan] [--workspace <saved-workspace>] <request>"
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}
	if a.api == nil {
		message := "/task failed: api client is not configured"
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}

	workspace, err := resolveTaskWorkspace(options.workspaceSelector, a.activeWorkspacePath(), a.homeModel.Workspaces)
	if err != nil {
		message := fmt.Sprintf("/task failed: resolve source workspace: %v", err)
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}
	workspacePath := normalizePath(strings.TrimSpace(workspace.Path))
	if workspacePath == "" {
		message := "/task failed: source workspace path is unavailable"
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}
	routes := buildChatRoutesForWorkspacesWithHostTarget([]model.Workspace{workspace}, workspacePath, a.homeModel.CurrentSwarmTarget)
	if len(routes) == 0 {
		message := "/task failed: source workspace route is unavailable"
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}
	route := routes[0]
	authority := client.RoutedTaskWorkspaceAuthority{
		WorkspacePath:      workspacePath,
		WorkspaceBindingID: strings.TrimSpace(route.WorkspaceBindingID),
		SwarmID:            strings.TrimSpace(route.SwarmID),
	}
	if authority.WorkspaceBindingID == "" || authority.SwarmID == "" {
		message := "/task failed: source workspace authority is unavailable"
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}

	originSessionID := ""
	if a.route == "chat" && a.chat != nil {
		originSessionID = strings.TrimSpace(a.chat.SessionID())
	} else if a.route == "v3chat" && a.v3Chat != nil {
		originSessionID = strings.TrimSpace(a.v3Chat.SessionID())
	}

	dispatch := taskCommandRequest{
		request:         options.request,
		mode:            options.mode,
		originSessionID: originSessionID,
		requestID:       uuid.NewString(),
		workspaceName:   strings.TrimSpace(workspace.Name),
		authority:       authority,
	}
	status := "dispatching task..."
	if dispatch.workspaceName != "" {
		status = fmt.Sprintf("dispatching task to %s...", dispatch.workspaceName)
	}
	a.home.SetStatus(status)
	go a.dispatchTaskCommand(dispatch)
}

func (a *App) dispatchTaskCommand(dispatch taskCommandRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), taskCommandTimeout)
	defer cancel()
	response, err := a.api.CreateRoutedTaskSession(ctx, dispatch.request, dispatch.requestID, dispatch.mode == "plan", dispatch.originSessionID, dispatch.authority)
	result := taskCommandResult{response: response, err: err}

	if a.taskCommandCh == nil {
		return
	}
	a.taskCommandCh <- result
	if a.screen != nil {
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptTaskCommandReady))
	}
}

func (a *App) consumeTaskCommandResults() {
	if a == nil || a.taskCommandCh == nil {
		return
	}
	for {
		select {
		case result := <-a.taskCommandCh:
			a.presentTaskCommandResult(result)
		default:
			return
		}
	}
}

func (a *App) presentTaskCommandResult(result taskCommandResult) {
	if result.err != nil {
		message := fmt.Sprintf("/task failed: %v", result.err)
		a.setTaskCommandStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}

	title := strings.TrimSpace(result.response.Title)
	if title == "" {
		title = strings.TrimSpace(result.response.Session.Title)
	}
	if title == "" {
		title = "Task"
	}
	message := title + " started in a worktree."
	a.setTaskCommandStatus(message)
	a.showToast(ui.ToastInfo, message)
}

func (a *App) setTaskCommandStatus(message string) {
	if a.home != nil {
		a.home.SetStatus(message)
	}
	switch {
	case a.route == "v3chat" && a.v3Chat != nil:
		a.v3Chat.SetStatus(message)
	case a.route == "chat" && a.chat != nil:
		a.chat.SetStatus(message)
	}
}
