package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

const testWorkspacePath = "/host/workspace"
const testRemoteRouteID = "swarm:child-swarm:binding:binding-1"

func testRoutingWorkspaces() []model.Workspace {
	return []model.Workspace{{
		Name: "Host Repo",
		Path: testWorkspacePath,
		TopologyRoutes: []model.WorkspaceTopologyRoute{{
			RouteID:              testRemoteRouteID,
			WorkspaceBindingID:   "binding-1",
			RuntimeSwarmID:       "child-swarm",
			RuntimeSwarmName:     "Child Desk",
			RuntimeKind:          "remote",
			RuntimeRelationship:  "child",
			HostWorkspacePath:    testWorkspacePath,
			HostWorkspaceName:    "Host Repo",
			RuntimeWorkspacePath: "/workspaces/swarm-go",
		}},
	}}
}

func TestBuildChatRoutesForWorkspacesKeepsTargetSwarmID(t *testing.T) {
	routes := buildChatRoutesForWorkspaces(testRoutingWorkspaces(), testWorkspacePath)

	if len(routes) != 2 {
		t.Fatalf("route count = %d, want 2", len(routes))
	}
	host := routes[0]
	if host.ID != "host" {
		t.Fatalf("host route ID = %q, want host", host.ID)
	}
	if host.SwarmID != "" {
		t.Fatalf("legacy host route SwarmID = %q, want empty without workspace overview swarm_target", host.SwarmID)
	}
	remote := routes[1]
	if remote.SwarmID != "child-swarm" {
		t.Fatalf("remote SwarmID = %q, want child-swarm", remote.SwarmID)
	}
	if remote.SwarmID == remote.WorkspaceBindingID {
		t.Fatalf("remote SwarmID must come from runtime swarm id, not workspace binding id")
	}
	if remote.HostWorkspacePath != testWorkspacePath {
		t.Fatalf("remote HostWorkspacePath = %q, want %s", remote.HostWorkspacePath, testWorkspacePath)
	}
	if remote.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("remote RuntimeWorkspacePath = %q, want /workspaces/swarm-go", remote.RuntimeWorkspacePath)
	}
	if remote.WorkspaceBindingID != "binding-1" {
		t.Fatalf("remote WorkspaceBindingID = %q, want binding-1", remote.WorkspaceBindingID)
	}
	if remote.TargetRelationship != "child" || remote.TargetKind != "remote" {
		t.Fatalf("remote target = %q/%q, want child/remote", remote.TargetRelationship, remote.TargetKind)
	}
}

func TestBuildChatRoutesForHomeModelUsesWorkspaceOverviewPrimaryHostTarget(t *testing.T) {
	model := model.HomeModel{
		CurrentSwarmTarget: &model.SwarmTarget{SwarmID: "primary-swarm", Name: "Primary Swarm", Relationship: "self", Kind: "host"},
		Workspaces: []model.Workspace{{
			Name:                    "Host Repo",
			Path:                    testWorkspacePath,
			LocalWorkspaceBindingID: "binding-primary",
		}},
	}

	routes := buildChatRoutesForHomeModel(model, testWorkspacePath)
	if len(routes) != 1 {
		t.Fatalf("route count = %d, want 1", len(routes))
	}
	host := routes[0]
	if host.ID != "swarm:primary-swarm:binding:binding-primary" {
		t.Fatalf("host route ID = %q, want router-backed primary binding route", host.ID)
	}
	if host.Label != "Primary Swarm" || host.SwarmID != "primary-swarm" || host.WorkspaceBindingID != "binding-primary" {
		t.Fatalf("host route did not preserve overview target/binding: %+v", host)
	}
	if !isPrimaryHostChatRoute(host) {
		t.Fatalf("host target = %q/%q, want self/host", host.TargetRelationship, host.TargetKind)
	}
}

func TestBuildChatRoutesForWorkspacesKeepsPrimarySelfHostTarget(t *testing.T) {
	workspaces := []model.Workspace{{
		Name: "Host Repo",
		Path: testWorkspacePath,
		TopologyRoutes: []model.WorkspaceTopologyRoute{{
			RouteID:              "swarm:primary-swarm:binding:binding-primary",
			WorkspaceBindingID:   "binding-primary",
			RuntimeSwarmID:       "primary-swarm",
			RuntimeSwarmName:     "Primary Swarm",
			RuntimeKind:          "host",
			RuntimeRelationship:  "self",
			HostWorkspacePath:    testWorkspacePath,
			HostWorkspaceName:    "Host Repo",
			RuntimeWorkspacePath: testWorkspacePath,
		}},
	}}

	routes := buildChatRoutesForWorkspaces(workspaces, testWorkspacePath)
	if len(routes) != 2 {
		t.Fatalf("route count = %d, want 2", len(routes))
	}
	primary := routes[1]
	if !isPrimaryHostChatRoute(primary) {
		t.Fatalf("primary target = %q/%q, want self/host", primary.TargetRelationship, primary.TargetKind)
	}
	if primary.SwarmID != "primary-swarm" || primary.WorkspaceBindingID != "binding-primary" {
		t.Fatalf("primary identifiers not preserved: %+v", primary)
	}
}

func TestModelSwarmTargetFromClientPropagatesWorkspaceOverviewTarget(t *testing.T) {
	target := modelSwarmTargetFromClient(&client.WorkspaceOverviewSwarmTarget{
		SwarmID:      " target-swarm ",
		Name:         " Target Swarm ",
		Role:         " master ",
		Relationship: " self ",
		Kind:         " local ",
		Online:       true,
		Selectable:   true,
		Current:      true,
	})

	if target == nil {
		t.Fatalf("target is nil")
	}
	if target.SwarmID != "target-swarm" {
		t.Fatalf("SwarmID = %q, want target-swarm", target.SwarmID)
	}
	if !target.Online || !target.Selectable || !target.Current {
		t.Fatalf("target booleans not propagated: %+v", target)
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

func TestCycleChatRouteDoesNotPersistGlobalDefault(t *testing.T) {
	app := &App{
		route:               "home",
		workspacePath:       testWorkspacePath,
		selectedChatRouteID: "host",
		config:              AppConfig{Chat: ChatConfig{DefaultWorkspaceRoutes: map[string]string{testWorkspacePath: "host"}}},
		home:                ui.NewHomePage(model.EmptyHome()),
		homeModel: model.HomeModel{ChatRoutes: []model.ChatRoute{
			{ID: "host", Label: "host", TargetKind: "host", TargetRelationship: "self"},
			{ID: testRemoteRouteID, Label: "Child", SwarmID: "child-swarm", WorkspaceBindingID: "binding-child", TargetKind: "container", TargetRelationship: "child"},
		}},
	}

	app.cycleChatRoute()

	if app.selectedChatRouteID != testRemoteRouteID {
		t.Fatalf("selected route ID = %q, want %q", app.selectedChatRouteID, testRemoteRouteID)
	}
	if got := app.config.Chat.DefaultWorkspaceRoutes[testWorkspacePath]; got != "host" {
		t.Fatalf("persisted default route = %q, want unchanged host", got)
	}
}

func TestCycleChatRouteSinglePrimaryHostUsesResolverBackedLabel(t *testing.T) {
	app := &App{
		route:               "home",
		workspacePath:       testWorkspacePath,
		selectedChatRouteID: "host",
		home:                ui.NewHomePage(model.EmptyHome()),
		homeModel: model.HomeModel{
			CurrentSwarmTarget: &model.SwarmTarget{SwarmID: "primary-swarm", Name: "Primary Swarm", Relationship: "self", Kind: "host"},
			ChatRoutes: []model.ChatRoute{{
				ID:                   "swarm:primary-swarm:binding:binding-primary",
				Label:                "Primary Swarm",
				SwarmID:              "primary-swarm",
				WorkspaceBindingID:   "binding-primary",
				HostWorkspacePath:    testWorkspacePath,
				RuntimeWorkspacePath: testWorkspacePath,
				TargetKind:           "host",
				TargetRelationship:   "self",
			}},
		},
	}

	app.cycleChatRoute()

	if app.home.Status() != "route: Primary Swarm" {
		t.Fatalf("route status = %q, want route: Primary Swarm", app.home.Status())
	}
	if app.selectedChatRouteID != "swarm:primary-swarm:binding:binding-primary" {
		t.Fatalf("selected route ID = %q, want resolver-backed primary route", app.selectedChatRouteID)
	}
}

func TestNormalizeSelectedRouteIDMapsLegacyHostToRouterBackedPrimaryHost(t *testing.T) {
	routes := buildChatRoutesForHomeModel(model.HomeModel{
		CurrentSwarmTarget: &model.SwarmTarget{SwarmID: "primary-swarm", Name: "Primary Swarm", Relationship: "self", Kind: "host"},
		Workspaces: []model.Workspace{{
			Path:                    testWorkspacePath,
			LocalWorkspaceBindingID: "binding-primary",
		}},
	}, testWorkspacePath)

	got := normalizeSelectedRouteID("host", routes)
	if got != "swarm:primary-swarm:binding:binding-primary" {
		t.Fatalf("normalized host route ID = %q, want router-backed primary host route", got)
	}
}

func TestSelectedChatRouteLabelUsesPrimaryTargetName(t *testing.T) {
	app := &App{
		homeModel: model.HomeModel{
			CurrentSwarmTarget: &model.SwarmTarget{SwarmID: "primary-swarm", Name: "Primary Swarm", Relationship: "self", Kind: "host"},
			Workspaces: []model.Workspace{{
				Name: "Host Repo",
				Path: testWorkspacePath,
				TopologyRoutes: []model.WorkspaceTopologyRoute{{
					RouteID:              "swarm:primary-swarm:binding:binding-primary",
					WorkspaceBindingID:   "binding-primary",
					RuntimeSwarmID:       "primary-swarm",
					RuntimeSwarmName:     "Local",
					RuntimeKind:          "host",
					RuntimeRelationship:  "self",
					HostWorkspacePath:    testWorkspacePath,
					RuntimeWorkspacePath: testWorkspacePath,
				}},
			}},
		},
		selectedChatRouteID: "swarm:primary-swarm:binding:binding-primary",
	}

	label := app.selectedChatRouteLabelForWorkspace(testWorkspacePath)
	if label != "Primary Swarm" {
		t.Fatalf("selected route label = %q, want primary target name", label)
	}
}

func TestSessionRouteLabelUsesV2PrimaryTargetName(t *testing.T) {
	app := &App{homeModel: model.HomeModel{
		CurrentSwarmTarget: &model.SwarmTarget{SwarmID: "primary-swarm", Name: "Primary Swarm", Relationship: "self", Kind: "host"},
		Workspaces: []model.Workspace{{
			Name: "Host Repo",
			Path: testWorkspacePath,
			TopologyRoutes: []model.WorkspaceTopologyRoute{{
				RouteID:              "swarm:primary-swarm:binding:binding-primary",
				WorkspaceBindingID:   "binding-primary",
				RuntimeSwarmID:       "primary-swarm",
				RuntimeSwarmName:     "Local",
				RuntimeKind:          "host",
				RuntimeRelationship:  "self",
				HostWorkspacePath:    testWorkspacePath,
				RuntimeWorkspacePath: testWorkspacePath,
			}},
		}},
	}}
	metadata := map[string]any{
		"swarm_v2_execution_class":        "primary",
		"swarm_v2_runtime_swarm_id":       "primary-swarm",
		"swarm_v2_runtime_kind":           "host",
		"swarm_v2_workspace_binding_id":   "binding-primary",
		"swarm_v2_source_workspace_path":  testWorkspacePath,
		"swarm_v2_runtime_workspace_path": testWorkspacePath,
	}

	label := app.sessionRouteLabelForWorkspace(testWorkspacePath, metadata)
	if label != "Primary Swarm" {
		t.Fatalf("session route label = %q, want primary target name", label)
	}
}

func TestSessionRouteLabelUsesSessionMetadata(t *testing.T) {
	app := &App{homeModel: model.HomeModel{Workspaces: testRoutingWorkspaces()}}
	metadata := map[string]any{
		"swarm_route_id":                      testRemoteRouteID,
		"swarm_routed_child_swarm_id":         "child-swarm",
		"swarm_routed_host_workspace_path":    testWorkspacePath,
		"swarm_routed_runtime_workspace_path": "/workspaces/swarm-go",
	}

	label := app.sessionRouteLabelForWorkspace(testWorkspacePath, metadata)
	if label != "Child Desk" {
		t.Fatalf("session route label = %q, want Child Desk", label)
	}
}

func TestSessionRouteLabelUsesMetadataLabelWhenRouteListMissing(t *testing.T) {
	app := &App{}
	metadata := map[string]any{
		"swarm_route_id":    testRemoteRouteID,
		"swarm_route_label": "Child Desk",
	}

	label := app.sessionRouteLabelForWorkspace(testWorkspacePath, metadata)
	if label != "Child Desk" {
		t.Fatalf("session route label = %q, want Child Desk", label)
	}
}

func TestSessionRouteLabelFallsBackToSelectedRoute(t *testing.T) {
	app := &App{
		selectedChatRouteID: testRemoteRouteID,
		homeModel:           model.HomeModel{Workspaces: testRoutingWorkspaces()},
	}

	label := app.sessionRouteLabelForWorkspace(testWorkspacePath, nil)
	if label != "Child Desk" {
		t.Fatalf("session route label = %q, want selected route label", label)
	}
}
