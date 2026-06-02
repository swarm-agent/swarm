package app

import (
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestSwarmSelectorOverlayContentIsMinimal(t *testing.T) {
	workspacePath := t.TempDir()
	app := &App{
		workspacePath:       workspacePath,
		selectedChatRouteID: "swarm:child:" + workspacePath,
		homeModel:           model.HomeModel{},
	}
	app.homeModel.ChatRoutes = modelChatRoutesFromCWDResolve(client.WorkspaceCWDResolveResponse{Routes: []client.WorkspaceTopologyRoute{{RouteID: "swarm:child:" + workspacePath, WorkspaceBindingID: "binding-1", RuntimeSwarmID: "child", RuntimeSwarmName: "Child", RuntimeKind: "remote", RuntimeRelationship: "child", HostWorkspacePath: workspacePath, RuntimeWorkspacePath: workspacePath}}})
	app.config.Chat.DefaultWorkspaceRoutes = map[string]string{app.workspacePath: "swarm:child:" + app.workspacePath}

	routes := app.homeModel.ChatRoutes
	selected := normalizeSelectedRouteID(app.selectedChatRouteID, routes)
	lines := []string{
		"current: " + app.selectedChatRouteLabelForWorkspace(app.workspacePath),
		"default: " + app.chatRouteLabelForID(routes, app.config.Chat.DefaultWorkspaceRoutes[app.workspacePath]),
	}
	if target := app.homeModel.CurrentSwarmTarget; target != nil && strings.TrimSpace(target.SwarmID) != "" {
		lines = append(lines, "current target swarm_id: "+strings.TrimSpace(target.SwarmID))
	}
	lines = append(lines, "selectors:")
	for _, route := range routes {
		marker := "  "
		if strings.TrimSpace(route.ID) == selected {
			marker = "* "
		}
		lines = append(lines, marker+app.displayChatRouteLabel(route))
	}
	lines = append(lines,
		"commands:",
		"  Alt+R: switch route for this TUI",
		"  /swarm status: pairing status",
		"  /swarm pending: pending enrollments",
	)

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "selectors:") || !strings.Contains(joined, "* Child") {
		t.Fatalf("selector lines missing expected selector content: %q", joined)
	}
	for _, noisy := range []string{"fingerprint", "parent swarm", "dashboard:", "advanced settings", "trusted "} {
		if strings.Contains(joined, noisy) {
			t.Fatalf("selector modal contains noisy detail %q in %q", noisy, joined)
		}
	}
}
