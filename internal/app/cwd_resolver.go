package app

import (
	"strings"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func modelChatRoutesFromCWDResolve(resolve client.WorkspaceCWDResolveResponse) []model.ChatRoute {
	if len(resolve.Routes) == 0 {
		return nil
	}
	routes := make([]model.ChatRoute, 0, len(resolve.Routes))
	for _, route := range resolve.Routes {
		swarmID := strings.TrimSpace(route.RuntimeSwarmID)
		bindingID := strings.TrimSpace(route.WorkspaceBindingID)
		routeID := strings.TrimSpace(route.RouteID)
		if routeID == "" && swarmID != "" && bindingID != "" {
			routeID = "swarm:" + swarmID + ":binding:" + bindingID
		}
		if routeID == "" {
			continue
		}
		hostWorkspacePath := normalizePath(strings.TrimSpace(route.HostWorkspacePath))
		if hostWorkspacePath == "" {
			hostWorkspacePath = normalizePath(strings.TrimSpace(resolve.ResolvedPath))
		}
		runtimeWorkspacePath := strings.TrimSpace(route.RuntimeWorkspacePath)
		if runtimeWorkspacePath == "" {
			runtimeWorkspacePath = hostWorkspacePath
		}
		label := strings.TrimSpace(route.RuntimeSwarmName)
		if label == "" {
			label = swarmID
		}
		if label == "" && routeID == "host" {
			label = "host"
		}
		routes = append(routes, model.ChatRoute{
			ID:                   routeID,
			Label:                label,
			SwarmID:              swarmID,
			WorkspaceBindingID:   bindingID,
			HostWorkspacePath:    hostWorkspacePath,
			RuntimeWorkspacePath: runtimeWorkspacePath,
			TargetKind:           strings.TrimSpace(route.RuntimeKind),
			TargetRelationship:   strings.TrimSpace(route.RuntimeRelationship),
			TUIPrimaryCWD:        route.TUIPrimaryCWD,
			UnavailableReason:    strings.TrimSpace(route.UnavailableReason),
		})
	}
	return routes
}

func applyCWDResolverToHomeModel(next model.HomeModel, resolve client.WorkspaceCWDResolveResponse) model.HomeModel {
	if resolve.PrimarySwarmTarget != nil {
		next.CurrentSwarmTarget = modelSwarmTargetFromClient(resolve.PrimarySwarmTarget)
	}
	next.ChatRoutes = modelChatRoutesFromCWDResolve(resolve)
	if resolve.Workspace != nil {
		workspacePath := normalizePath(strings.TrimSpace(resolve.Workspace.WorkspacePath))
		localBindingID := primaryWorkspaceBindingIDFromRoutes(resolve.Routes)
		for i := range next.Workspaces {
			if workspacePath == "" || !pathsEqual(next.Workspaces[i].Path, workspacePath) {
				continue
			}
			if localBindingID != "" {
				next.Workspaces[i].LocalWorkspaceBindingID = localBindingID
			}
			if len(next.ChatRoutes) > 0 {
				next.Workspaces[i].TopologyRoutes = modelTopologyRoutesFromClient(resolve.Routes)
			}
			break
		}
	}
	return next
}

func primaryWorkspaceBindingIDFromRoutes(routes []client.WorkspaceTopologyRoute) string {
	for _, route := range routes {
		if !strings.EqualFold(strings.TrimSpace(route.RuntimeRelationship), "self") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(route.RuntimeKind), "host") {
			continue
		}
		if bindingID := strings.TrimSpace(route.WorkspaceBindingID); bindingID != "" {
			return bindingID
		}
	}
	for _, route := range routes {
		if bindingID := strings.TrimSpace(route.WorkspaceBindingID); bindingID != "" {
			return bindingID
		}
	}
	return ""
}
