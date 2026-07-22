package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func parseTaskCommand(args []string) (request, mode string) {
	mode = client.WorkspaceAITaskModeAuto
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), client.WorkspaceAITaskModePlan) {
		mode = client.WorkspaceAITaskModePlan
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
	workspacePath := strings.TrimSpace(a.activeContextPath())
	if workspacePath == "" {
		message := "select a workspace before queuing a task"
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := a.api.CreateWorkspaceAITask(ctx, workspacePath, request, uuid.NewString(), mode, originSessionID)
	if err != nil {
		message := fmt.Sprintf("/task failed: %v", err)
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}

	title := strings.TrimSpace(response.Item.AIDisplayTitle)
	if title == "" {
		title = strings.TrimSpace(response.Item.Text)
	}
	if title == "" {
		title = "Task"
	}
	level := ui.ToastInfo
	message := title + " queued for Swarm."
	switch strings.ToLower(strings.TrimSpace(response.Item.AIState)) {
	case "in_progress":
		message = title + " started."
	case "completed":
		level = ui.ToastSuccess
		message = title + " completed."
	case "failed":
		level = ui.ToastError
		message = strings.TrimSpace(response.Item.AIError)
		if message == "" {
			message = "Swarm could not start the task."
		}
	case "cancelled":
		message = title + " was cancelled."
	case "preparing":
		message = "Swarm is preparing the queued task."
	default:
		if strings.TrimSpace(response.Item.ManagedSessionID) != "" {
			message = title + " started."
		}
	}
	a.home.SetStatus(message)
	a.showToast(level, message)
}
