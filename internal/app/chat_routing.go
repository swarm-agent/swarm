package app

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) cycleChatRoute() {
	if a == nil {
		return
	}
	if a.route == "chat" && a.chat != nil {
		if a.chat.RunInProgress() {
			a.chat.SetStatus("route switch blocked while a run is active")
			a.showToast(ui.ToastWarning, "route switch blocked while a run is active")
			return
		}
		if summary, ok := a.sessionSummaryByID(a.chat.SessionID()); ok && isFlowSessionMetadata(summary.Metadata) {
			a.chat.SetStatus("route switch blocked for read-only flow sessions")
			a.showToast(ui.ToastWarning, "route switch blocked for read-only flow sessions")
			return
		}
	}
	workspacePath := strings.TrimSpace(a.activeWorkspacePath())
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.workspacePath)
	}
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.startupCWD)
	}
	routes := buildChatRoutesForWorkspaces(a.homeModel.Workspaces, workspacePath)
	if len(routes) <= 1 {
		a.setRouteStatus("route: host")
		return
	}
	current := normalizeSelectedRouteID(a.selectedChatRouteID, routes)
	nextIndex := 0
	for i, route := range routes {
		if strings.TrimSpace(route.ID) == current {
			nextIndex = (i + 1) % len(routes)
			break
		}
	}
	next := routes[nextIndex]
	a.selectedChatRouteID = strings.TrimSpace(next.ID)
	if a.selectedChatRouteID == "" {
		a.selectedChatRouteID = "host"
	}
	a.homeModel.ChatRoutes = routes
	a.homeModel.SelectedChatRouteID = a.selectedChatRouteID
	if a.config.Chat.DefaultWorkspaceRoutes == nil {
		a.config.Chat.DefaultWorkspaceRoutes = make(map[string]string)
	}
	if workspacePath != "" {
		a.config.Chat.DefaultWorkspaceRoutes[workspacePath] = a.selectedChatRouteID
		go a.persistDefaultWorkspaceRoute(workspacePath, a.selectedChatRouteID)
	}
	a.home.SetModel(a.homeModel)
	if a.chat != nil {
		meta := a.chat.Meta()
		meta.Route = emptyFallback(strings.TrimSpace(next.Label), "host")
		a.chat.SetMeta(meta)
	}
	a.setRouteStatus(fmt.Sprintf("route: %s", emptyFallback(strings.TrimSpace(next.Label), "host")))
}

func (a *App) persistDefaultWorkspaceRoute(workspacePath, routeID string) {
	workspacePath = strings.TrimSpace(workspacePath)
	routeID = strings.TrimSpace(routeID)
	if workspacePath == "" || routeID == "" || a == nil || a.api == nil {
		return
	}
	if err := updateUISettings(a.api, func(settings *client.UISettings) {
		if settings.Chat.DefaultWorkspaceRoutes == nil {
			settings.Chat.DefaultWorkspaceRoutes = make(map[string]string)
		}
		settings.Chat.DefaultWorkspaceRoutes[workspacePath] = routeID
	}); err != nil {
		if a.screen != nil {
			a.screen.PostEventWait(tcell.NewEventInterrupt(interruptReloadReady))
		}
	}
}

func (a *App) setRouteStatus(status string) {
	status = strings.TrimSpace(status)
	if status == "" {
		return
	}
	if a.route == "chat" && a.chat != nil {
		a.chat.SetStatus(status)
	} else if a.home != nil {
		a.home.SetStatus(status)
	}
	a.showToast(ui.ToastInfo, status)
}

func isFlowSessionMetadata(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	if value := metadataString(metadata, "flow_id"); value != "" {
		return true
	}
	if strings.EqualFold(metadataString(metadata, "source"), "flow") {
		return true
	}
	return strings.EqualFold(metadataString(metadata, "lineage_kind"), "flow")
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
