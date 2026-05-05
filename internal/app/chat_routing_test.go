package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

const testWorkspacePath = "/host/workspace"
const testRemoteRouteID = "swarm:child-swarm:/workspaces/swarm-go"

func testRoutingWorkspaces() []model.Workspace {
	return []model.Workspace{{
		Name: "Host Repo",
		Path: testWorkspacePath,
		ReplicationLinks: []model.WorkspaceReplicationLink{{
			TargetSwarmID:       "child-swarm",
			TargetSwarmName:     "Child Desk",
			TargetWorkspacePath: "/workspaces/swarm-go",
		}},
	}}
}

func TestBuildChatRoutesForWorkspacesKeepsTargetSwarmID(t *testing.T) {
	routes := buildChatRoutesForWorkspaces(testRoutingWorkspaces(), testWorkspacePath)

	if len(routes) != 2 {
		t.Fatalf("route count = %d, want 2", len(routes))
	}
	remote := routes[1]
	if remote.SwarmID != "child-swarm" {
		t.Fatalf("remote SwarmID = %q, want child-swarm", remote.SwarmID)
	}
	if remote.HostWorkspacePath != testWorkspacePath {
		t.Fatalf("remote HostWorkspacePath = %q, want %s", remote.HostWorkspacePath, testWorkspacePath)
	}
	if remote.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("remote RuntimeWorkspacePath = %q, want /workspaces/swarm-go", remote.RuntimeWorkspacePath)
	}
}

func TestSelectedChatRouteUsesServerBackedWorkspaceDefaultWhenUnset(t *testing.T) {
	app := &App{
		config: AppConfig{Chat: ChatConfig{DefaultWorkspaceRoutes: map[string]string{
			testWorkspacePath: testRemoteRouteID,
		}}},
		homeModel: model.HomeModel{Workspaces: testRoutingWorkspaces()},
	}

	route := app.selectedChatRouteForWorkspace(testWorkspacePath)
	if route.ID != testRemoteRouteID {
		t.Fatalf("selected route ID = %q, want %q", route.ID, testRemoteRouteID)
	}
	if route.SwarmID != "child-swarm" {
		t.Fatalf("selected route SwarmID = %q, want child-swarm", route.SwarmID)
	}
	if route.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("selected route runtime path = %q, want /workspaces/swarm-go", route.RuntimeWorkspacePath)
	}
}

func TestRefreshHomeModelSelectsServerBackedWorkspaceDefault(t *testing.T) {
	app := &App{
		workspacePath: testWorkspacePath,
		config: AppConfig{Chat: ChatConfig{DefaultWorkspaceRoutes: map[string]string{
			testWorkspacePath: testRemoteRouteID,
		}}},
	}
	next := model.EmptyHome()
	next.Workspaces = testRoutingWorkspaces()

	next.ChatRoutes = buildChatRoutesForWorkspaces(next.Workspaces, testWorkspacePath)
	selectedRouteID := app.resolveSelectedChatRouteIDForWorkspace(testWorkspacePath, next.ChatRoutes)
	app.selectedChatRouteID = selectedRouteID
	next.SelectedChatRouteID = selectedRouteID

	if app.selectedChatRouteID != testRemoteRouteID {
		t.Fatalf("app selected route ID = %q, want %q", app.selectedChatRouteID, testRemoteRouteID)
	}
	if next.SelectedChatRouteID != testRemoteRouteID {
		t.Fatalf("home selected route ID = %q, want %q", next.SelectedChatRouteID, testRemoteRouteID)
	}
}

func TestRemoteUISettingsUpdateMovesSelectionWhenTrackingDefault(t *testing.T) {
	app := &App{
		workspacePath:       testWorkspacePath,
		selectedChatRouteID: "host",
		config:              AppConfig{Chat: ChatConfig{DefaultWorkspaceRoutes: map[string]string{testWorkspacePath: "host"}}},
		home:                ui.NewHomePage(model.EmptyHome()),
		homeModel:           model.HomeModel{Workspaces: testRoutingWorkspaces()},
	}
	app.homeModel.ChatRoutes = buildChatRoutesForWorkspaces(app.homeModel.Workspaces, testWorkspacePath)
	app.homeModel.SelectedChatRouteID = "host"

	changed := app.applyRemoteUISettings(client.UISettings{Chat: client.UIChatSettings{DefaultWorkspaceRoutes: map[string]string{
		testWorkspacePath: testRemoteRouteID,
	}}})
	if !changed {
		t.Fatalf("applyRemoteUISettings returned false")
	}
	if app.selectedChatRouteID != testRemoteRouteID {
		t.Fatalf("selected route ID = %q, want %q", app.selectedChatRouteID, testRemoteRouteID)
	}
	if app.homeModel.SelectedChatRouteID != testRemoteRouteID {
		t.Fatalf("home model selected route ID = %q, want %q", app.homeModel.SelectedChatRouteID, testRemoteRouteID)
	}
}

func TestRemoteUISettingsUpdateKeepsExplicitSelection(t *testing.T) {
	app := &App{
		workspacePath:       testWorkspacePath,
		selectedChatRouteID: testRemoteRouteID,
		config:              AppConfig{Chat: ChatConfig{DefaultWorkspaceRoutes: map[string]string{testWorkspacePath: "host"}}},
		home:                ui.NewHomePage(model.EmptyHome()),
		homeModel:           model.HomeModel{Workspaces: testRoutingWorkspaces()},
	}
	app.homeModel.ChatRoutes = buildChatRoutesForWorkspaces(app.homeModel.Workspaces, testWorkspacePath)
	app.homeModel.SelectedChatRouteID = testRemoteRouteID

	changed := app.applyRemoteUISettings(client.UISettings{Chat: client.UIChatSettings{DefaultWorkspaceRoutes: map[string]string{
		testWorkspacePath: "host",
	}}})
	if !changed {
		t.Fatalf("applyRemoteUISettings returned false")
	}
	if app.selectedChatRouteID != testRemoteRouteID {
		t.Fatalf("selected route ID = %q, want explicit %q", app.selectedChatRouteID, testRemoteRouteID)
	}
}
