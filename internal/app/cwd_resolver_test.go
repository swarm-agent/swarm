package app

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestModelChatRoutesFromCWDResolveUsesResolverRoutesAndPrimaryName(t *testing.T) {
	resolve := client.WorkspaceCWDResolveResponse{
		ResolvedPath: "/tmp/not-workspace",
		Routes: []client.WorkspaceTopologyRoute{{
			RouteID:              "host",
			RuntimeSwarmID:       "host-swarm",
			RuntimeSwarmName:     "Primary Desk",
			RuntimeKind:          "host",
			RuntimeRelationship:  "self",
			HostWorkspacePath:    "/tmp/not-workspace",
			RuntimeWorkspacePath: "/tmp/not-workspace",
			TUIPrimaryCWD:        true,
		}},
	}

	routes := modelChatRoutesFromCWDResolve(resolve)
	if len(routes) != 1 {
		t.Fatalf("route count = %d, want 1", len(routes))
	}
	if routes[0].Label != "Primary Desk" || routes[0].SwarmID != "host-swarm" || !routes[0].TUIPrimaryCWD {
		t.Fatalf("route = %+v, want primary resolver route", routes[0])
	}
}

func TestSelectedChatRouteUsesResolverRoutesOverOverviewWorkspaceRoutes(t *testing.T) {
	app := &App{
		selectedChatRouteID: "swarm:container:binding:binding-container",
		homeModel: model.HomeModel{
			ChatRoutes: []model.ChatRoute{{
				ID:                   "swarm:container:binding:binding-container",
				Label:                "Container Desk",
				SwarmID:              "container",
				WorkspaceBindingID:   "binding-container",
				HostWorkspacePath:    testWorkspacePath,
				RuntimeWorkspacePath: "/workspace/runtime",
			}},
			Workspaces: []model.Workspace{{
				Name: "Host Repo",
				Path: testWorkspacePath,
				TopologyRoutes: []model.WorkspaceTopologyRoute{{
					RouteID:              testRemoteRouteID,
					WorkspaceBindingID:   "binding-1",
					RuntimeSwarmID:       "child-swarm",
					RuntimeSwarmName:     "Stale Overview Route",
					RuntimeWorkspacePath: "/stale/runtime",
				}},
			}},
		},
	}

	route := app.selectedChatRouteForWorkspace(testWorkspacePath)
	if route.SwarmID != "container" || route.WorkspaceBindingID != "binding-container" {
		t.Fatalf("selected route = %+v, want resolver route", route)
	}
}

func TestV2SessionRoutePrefersFrozenExecutionFactsOverCWDResolver(t *testing.T) {
	app := &App{homeModel: model.HomeModel{
		ChatRoutes: []model.ChatRoute{{
			ID:                 "swarm:cwd-container:binding:cwd-binding",
			Label:              "CWD Container",
			SwarmID:            "cwd-container",
			WorkspaceBindingID: "cwd-binding",
		}},
	}}
	metadata := map[string]any{
		"swarm_v2_execution_class":        "local_container",
		"swarm_v2_runtime_swarm_id":       "frozen-container",
		"swarm_v2_runtime_kind":           "container",
		"swarm_v2_workspace_binding_id":   "frozen-binding",
		"swarm_v2_source_workspace_path":  testWorkspacePath,
		"swarm_v2_runtime_workspace_path": "/frozen/runtime",
	}

	route, ok := app.v2SessionRouteFromMetadata(testWorkspacePath, metadata)
	if !ok {
		t.Fatalf("v2 route not resolved")
	}
	if route.SwarmID != "frozen-container" || route.WorkspaceBindingID != "frozen-binding" || route.Label != "frozen-container" {
		t.Fatalf("route = %+v, want frozen execution route", route)
	}
}

func TestSelectedChatRouteDoesNotInventOverviewFallbackWhenResolverRoutesEmpty(t *testing.T) {
	app := &App{
		homeModel: model.HomeModel{
			CurrentSwarmTarget: &model.SwarmTarget{SwarmID: "primary-swarm", Name: "Primary Swarm", Relationship: "self", Kind: "host"},
			Workspaces: []model.Workspace{{
				Name:                    "Host Repo",
				Path:                    testWorkspacePath,
				LocalWorkspaceBindingID: "binding-primary",
			}},
		},
	}

	route := app.selectedChatRouteForWorkspace(testWorkspacePath)
	if route.ID != "" || route.WorkspaceBindingID != "" || route.SwarmID != "" {
		t.Fatalf("selected route = %+v, want no invented overview fallback", route)
	}
}

func TestCycleChatRouteDoesNotInventOverviewFallbackWhenResolverRoutesEmpty(t *testing.T) {
	app := &App{
		startupCWD: testWorkspacePath,
		homeModel: model.HomeModel{
			CurrentSwarmTarget: &model.SwarmTarget{SwarmID: "primary-swarm", Name: "Primary Swarm", Relationship: "self", Kind: "host"},
			Workspaces: []model.Workspace{{
				Name:                    "Host Repo",
				Path:                    testWorkspacePath,
				LocalWorkspaceBindingID: "binding-primary",
			}},
		},
	}

	app.cycleChatRoute()
	if len(app.homeModel.ChatRoutes) != 0 || app.selectedChatRouteID != "" || app.homeModel.SelectedChatRouteID != "" {
		t.Fatalf("routes=%+v selected=%q modelSelected=%q, want resolver route state unchanged", app.homeModel.ChatRoutes, app.selectedChatRouteID, app.homeModel.SelectedChatRouteID)
	}
	if app.homeModel.ChatRoutes != nil {
		t.Fatalf("ChatRoutes = %#v, want nil/no fallback routes", app.homeModel.ChatRoutes)
	}
}
