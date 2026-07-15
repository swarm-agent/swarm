package app

import "swarm-refactor/swarmtui/internal/model"

const testWorkspacePath = "/workspace"
const testRemoteRouteID = "swarm:child-swarm:binding:binding-1"

func testRoutingWorkspaces() []model.Workspace {
	return []model.Workspace{{
		Name: "Workspace",
		Path: testWorkspacePath,
		TopologyRoutes: []model.WorkspaceTopologyRoute{{
			RouteID:              testRemoteRouteID,
			WorkspaceBindingID:   "binding-1",
			RuntimeSwarmID:       "child-swarm",
			RuntimeSwarmName:     "Child",
			RuntimeKind:          "remote",
			RuntimeRelationship:  "child",
			HostWorkspacePath:    testWorkspacePath,
			HostWorkspaceName:    "Workspace",
			RuntimeWorkspacePath: "/workspaces/project",
		}},
	}}
}
