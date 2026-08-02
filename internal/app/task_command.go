package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"swarm-refactor/swarmtui/internal/ui"
)

func parseTaskCommand(args []string) (request, mode string) {
	mode = "auto"
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "plan") {
		mode = "plan"
		args = args[1:]
	}
	return strings.TrimSpace(strings.Join(args, " ")), mode
}

func isTaskCommand(raw string) bool {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(raw, "/")))
	return len(fields) > 0 && strings.EqualFold(fields[0], "task")
}

func (a *App) shouldSendTaskCommandAsDraftPrompt(raw string) bool {
	return a.v3ChatDraftActive() && isTaskCommand(raw)
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
	originSessionID := ""
	if a.route == "chat" && a.chat != nil {
		originSessionID = strings.TrimSpace(a.chat.SessionID())
	} else if a.route == "v3chat" && a.v3Chat != nil {
		originSessionID = strings.TrimSpace(a.v3Chat.SessionID())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := a.api.CreateRoutedTaskSession(ctx, request, uuid.NewString(), mode == "plan", originSessionID)
	if err != nil {
		message := fmt.Sprintf("/task failed: %v", err)
		a.home.SetStatus(message)
		a.showToast(ui.ToastError, message)
		return
	}

	title := strings.TrimSpace(response.Title)
	if title == "" {
		title = strings.TrimSpace(response.Session.Title)
	}
	if title == "" {
		title = "Task"
	}
	message := title + " started in a worktree."
	a.home.SetStatus(message)
	a.showToast(ui.ToastInfo, message)
}
