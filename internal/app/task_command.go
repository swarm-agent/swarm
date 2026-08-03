package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

const taskCommandTimeout = 30 * time.Second

type taskCommandRequest struct {
	request         string
	mode            string
	originSessionID string
	requestID       string
	authority       client.RoutedTaskWorkspaceAuthority
}

type taskCommandResult struct {
	response client.RoutedTaskSessionResponse
	err      error
}

func parseTaskCommand(args []string) (request, mode string) {
	mode = "auto"
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "plan") {
		mode = "plan"
		args = args[1:]
	}
	return strings.TrimSpace(strings.Join(args, " ")), mode
}

func (a *App) handleTaskCommand(args []string) {
	a.home.ClearCommandOverlay()
	request, mode := parseTaskCommand(args)
	if request == "" {
		message := "usage: /task [plan] <request>"
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

	workspacePath := strings.TrimSpace(a.activeWorkspacePath())
	if workspacePath == "" {
		message := "/task failed: source workspace is unavailable"
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}
	route, err := a.canonicalSelfChatRoute(workspacePath)
	if err != nil {
		message := fmt.Sprintf("/task failed: resolve source workspace: %v", err)
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}
	authority := client.RoutedTaskWorkspaceAuthority{
		WorkspacePath:      workspacePath,
		WorkspaceBindingID: strings.TrimSpace(route.WorkspaceBindingID),
		SwarmID:            createSessionSwarmIDForRoute(route, a.homeModel.CurrentSwarmTarget),
	}
	if authority.WorkspaceBindingID == "" || strings.TrimSpace(authority.SwarmID) == "" {
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
		request:         request,
		mode:            mode,
		originSessionID: originSessionID,
		requestID:       uuid.NewString(),
		authority:       authority,
	}
	a.home.SetStatus("dispatching task...")
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
