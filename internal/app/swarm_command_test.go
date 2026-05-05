package app

import (
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestSwarmSelectorOverlayContentIsMinimal(t *testing.T) {
	workspacePath := t.TempDir()
	app := &App{
		workspacePath:       workspacePath,
		selectedChatRouteID: "swarm:child:" + workspacePath,
		homeModel: model.HomeModel{Workspaces: []model.Workspace{{
			Name: "repo",
			Path: workspacePath,
			ReplicationLinks: []model.WorkspaceReplicationLink{{
				TargetSwarmID:       "child",
				TargetSwarmName:     "Child",
				TargetWorkspacePath: workspacePath,
			}},
		}}},
	}
	app.config.Chat.DefaultWorkspaceRoutes = map[string]string{app.workspacePath: "swarm:child:" + app.workspacePath}

	routes := buildChatRoutesForWorkspaces(app.homeModel.Workspaces, app.workspacePath)
	selected := normalizeSelectedRouteID(app.selectedChatRouteID, routes)
	lines := []string{
		"current: " + app.selectedChatRouteLabelForWorkspace(app.workspacePath),
		"default: " + chatRouteLabelForID(routes, app.config.Chat.DefaultWorkspaceRoutes[app.workspacePath]),
		"selectors:",
	}
	for _, route := range routes {
		marker := "  "
		if strings.TrimSpace(route.ID) == selected {
			marker = "* "
		}
		lines = append(lines, marker+route.Label)
	}
	lines = append(lines,
		"commands:",
		"  Tab/Shift+Tab: change default route",
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
