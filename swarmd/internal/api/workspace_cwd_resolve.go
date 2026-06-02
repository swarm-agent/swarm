package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

const (
	workspaceCWDResolutionKindWorkspace    = "workspace"
	workspaceCWDResolutionKindNonWorkspace = "non_workspace"
	workspaceCWDRouteSourcePrimaryCWD      = "tui/primary_cwd"
)

type workspaceCWDResolveResponse struct {
	OK                 bool                             `json:"ok"`
	CWD                string                           `json:"cwd"`
	ResolvedPath       string                           `json:"resolved_path"`
	ResolutionKind     string                           `json:"resolution_kind"`
	Workspace          *workspace.Resolution            `json:"workspace,omitempty"`
	PrimarySwarmTarget *swarmTarget                     `json:"primary_swarm_target,omitempty"`
	Routes             []workspaceOverviewTopologyRoute `json:"routes"`
	UnavailableReason  string                           `json:"unavailable_reason,omitempty"`
}

func (s *Server) handleWorkspaceCWDResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("workspace service"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if cwd == "" {
		writeError(w, http.StatusBadRequest, errors.New("cwd is required"))
		return
	}

	swarmTargets, _, err := s.swarmTargetsForRequest(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	primaryTarget := primarySelfSwarmTarget(swarmTargets)

	scope, err := s.workspace.ScopeForPathForPrincipal(principal, cwd)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolvedPath := strings.TrimSpace(scope.ResolvedPath)
	if resolvedPath == "" {
		resolvedPath = cwd
	}

	if !scope.Matched {
		writeJSON(w, http.StatusOK, workspaceCWDResolveResponse{
			OK:                 true,
			CWD:                cwd,
			ResolvedPath:       resolvedPath,
			ResolutionKind:     workspaceCWDResolutionKindNonWorkspace,
			PrimarySwarmTarget: primaryTarget,
			Routes:             []workspaceOverviewTopologyRoute{workspaceCWDPrimaryRoute(primaryTarget, resolvedPath, true, "")},
			UnavailableReason:  "cwd is not a saved workspace; using TUI primary CWD route without a workspace binding",
		})
		return
	}

	routes, localBindingID, unavailableReason, err := s.workspaceCWDRoutesForScope(principal, swarmTargets, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	workspacePayload := workspace.Resolution{
		RequestedPath:           scope.RequestedPath,
		ResolvedPath:            scope.ResolvedPath,
		WorkspaceID:             strings.TrimSpace(scope.WorkspaceID),
		LocalWorkspaceBindingID: localBindingID,
		WorkspaceGeneration:     scope.WorkspaceGeneration,
		WorkspaceState:          strings.TrimSpace(scope.WorkspaceState),
		WorkspacePath:           strings.TrimSpace(scope.WorkspacePath),
		WorkspaceName:           strings.TrimSpace(scope.WorkspaceName),
		ThemeID:                 strings.TrimSpace(scope.ThemeID),
	}
	writeJSON(w, http.StatusOK, workspaceCWDResolveResponse{
		OK:                 true,
		CWD:                cwd,
		ResolvedPath:       resolvedPath,
		ResolutionKind:     workspaceCWDResolutionKindWorkspace,
		Workspace:          &workspacePayload,
		PrimarySwarmTarget: primaryTarget,
		Routes:             routes,
		UnavailableReason:  unavailableReason,
	})
}

func primarySelfSwarmTarget(targets []swarmTarget) *swarmTarget {
	for _, target := range targets {
		if strings.EqualFold(strings.TrimSpace(target.Relationship), "self") {
			copy := target
			copy.Current = true
			return &copy
		}
	}
	if len(targets) == 0 {
		return nil
	}
	copy := targets[0]
	copy.Current = true
	return &copy
}

func workspaceCWDPrimaryRoute(target *swarmTarget, workspacePath string, tuiPrimaryCWD bool, unavailableReason string) workspaceOverviewTopologyRoute {
	swarmID := ""
	name := ""
	kind := "host"
	if target != nil {
		swarmID = strings.TrimSpace(target.SwarmID)
		name = firstNonEmpty(strings.TrimSpace(target.Name), swarmID)
	}
	workspacePath = strings.TrimSpace(workspacePath)
	return workspaceOverviewTopologyRoute{
		RouteID:              "host",
		RouteSource:          workspaceCWDRouteSourcePrimaryCWD,
		RuntimeSwarmID:       swarmID,
		RuntimeSwarmName:     name,
		RuntimeKind:          kind,
		RuntimeRelationship:  "self",
		AuthorityHostSwarmID: swarmID,
		HostSwarmID:          swarmID,
		HostSwarmName:        name,
		HostWorkspacePath:    workspacePath,
		HostWorkspaceName:    "",
		RuntimeWorkspacePath: workspacePath,
		Writable:             true,
		TUIPrimaryCWD:        tuiPrimaryCWD,
		UnavailableReason:    strings.TrimSpace(unavailableReason),
	}
}

func (s *Server) workspaceCWDRoutesForScope(principal identity.Principal, swarmTargets []swarmTarget, scope workspace.Scope) ([]workspaceOverviewTopologyRoute, string, string, error) {
	out := make([]workspaceOverviewTopologyRoute, 0)
	if s == nil || s.topology == nil {
		return out, "", "topology service not configured", nil
	}
	if _, err := s.topology.EnsureSnapshot(); err != nil {
		return nil, "", "", err
	}
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	workspaceID := strings.TrimSpace(scope.WorkspaceID)
	workspacePath := strings.TrimSpace(scope.WorkspacePath)
	if accountScopeID == "" || workspaceID == "" || workspacePath == "" {
		return out, "", "matched workspace identity is incomplete", nil
	}
	bindings, err := s.topology.ListWorkspaceBindingsForAccount(accountScopeID, 100000)
	if err != nil {
		return nil, "", "", err
	}
	runtimeTargets := make(map[string]swarmTarget, len(swarmTargets))
	for _, target := range swarmTargets {
		if swarmID := strings.TrimSpace(target.SwarmID); swarmID != "" {
			runtimeTargets[strings.ToLower(swarmID)] = target
		}
	}
	topologyRuntimes := make(map[string]pebblestore.TopologyRuntimeRecord)
	runtimes, err := s.topology.ListRuntimesForAccount(accountScopeID, 100000)
	if err != nil {
		return nil, "", "", err
	}
	for _, runtime := range runtimes {
		if swarmID := strings.TrimSpace(runtime.SwarmID); swarmID != "" {
			topologyRuntimes[strings.ToLower(swarmID)] = runtime
		}
	}
	seen := map[string]struct{}{}
	localBindingID := ""
	primaryTarget := primarySelfSwarmTarget(swarmTargets)
	primarySwarmID := ""
	if primaryTarget != nil {
		primarySwarmID = strings.TrimSpace(primaryTarget.SwarmID)
	}
	for _, binding := range bindings {
		bindingWorkspaceID := strings.TrimSpace(binding.SourceWorkspaceID)
		if bindingWorkspaceID == "" || !strings.EqualFold(bindingWorkspaceID, workspaceID) {
			continue
		}
		workspaceBindingID := strings.TrimSpace(binding.BindingID)
		runtimeSwarmID := strings.TrimSpace(binding.DestinationRuntimeSwarmID)
		runtimeWorkspacePath := strings.TrimSpace(binding.DestinationWorkspacePath)
		if workspaceBindingID == "" || runtimeSwarmID == "" || runtimeWorkspacePath == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(binding.State), pebblestore.TopologyWorkspaceBindingStateBound) {
			continue
		}
		runtimeRecord := topologyRuntimes[strings.ToLower(runtimeSwarmID)]
		runtimeTarget, runtimeAlive := runtimeTargets[strings.ToLower(runtimeSwarmID)]
		if !runtimeAlive {
			continue
		}
		if !runtimeTarget.Online || !runtimeTarget.Selectable {
			if !s.topologyRouteOwnerHostSelectable(runtimeTarget, runtimeRecord, runtimeTargets) {
				continue
			}
		}
		route, ok := s.workspaceOverviewTopologyRouteForBinding(binding, runtimeTarget, runtimeRecord, runtimeTargets, workspacePath, workspacePath, strings.TrimSpace(scope.WorkspaceName))
		if !ok {
			continue
		}
		routeID := strings.TrimSpace(route.RouteID)
		if routeID == "" {
			continue
		}
		if _, exists := seen[routeID]; exists {
			continue
		}
		seen[routeID] = struct{}{}
		out = append(out, route)
		if localBindingID == "" && strings.TrimSpace(route.ContainerID) == "" && strings.EqualFold(strings.TrimSpace(route.RuntimeSwarmID), primarySwarmID) {
			localBindingID = strings.TrimSpace(route.WorkspaceBindingID)
		}
	}
	if localBindingID == "" {
		if route, bindingID, ok := s.workspaceCWDPrimaryBindingRoute(primaryTarget, scope, bindings); ok {
			routeID := strings.TrimSpace(route.RouteID)
			if _, exists := seen[routeID]; !exists {
				seen[routeID] = struct{}{}
				out = append(out, route)
			}
			localBindingID = bindingID
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		leftPrimary := strings.TrimSpace(out[i].WorkspaceBindingID) == localBindingID && localBindingID != ""
		rightPrimary := strings.TrimSpace(out[j].WorkspaceBindingID) == localBindingID && localBindingID != ""
		if leftPrimary != rightPrimary {
			return leftPrimary
		}
		left := strings.ToLower(out[i].RuntimeSwarmName)
		right := strings.ToLower(out[j].RuntimeSwarmName)
		if left == right {
			return out[i].RouteID < out[j].RouteID
		}
		return left < right
	})
	if localBindingID == "" {
		return out, "", "matched workspace has no bound primary workspace binding", nil
	}
	return out, localBindingID, "", nil
}

func (s *Server) workspaceCWDPrimaryBindingRoute(primaryTarget *swarmTarget, scope workspace.Scope, bindings []pebblestore.TopologyWorkspaceBindingRecord) (workspaceOverviewTopologyRoute, string, bool) {
	if primaryTarget == nil || strings.TrimSpace(primaryTarget.SwarmID) == "" {
		return workspaceOverviewTopologyRoute{}, "", false
	}
	workspaceID := strings.TrimSpace(scope.WorkspaceID)
	workspacePath := strings.TrimSpace(scope.WorkspacePath)
	if workspaceID == "" || workspacePath == "" {
		return workspaceOverviewTopologyRoute{}, "", false
	}
	primarySwarmID := strings.TrimSpace(primaryTarget.SwarmID)
	for _, binding := range bindings {
		bindingWorkspaceID := strings.TrimSpace(binding.SourceWorkspaceID)
		if bindingWorkspaceID == "" || !strings.EqualFold(bindingWorkspaceID, workspaceID) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(binding.DestinationRuntimeSwarmID), primarySwarmID) && strings.TrimSpace(binding.DestinationContainerID) == "" && strings.EqualFold(strings.TrimSpace(binding.State), pebblestore.TopologyWorkspaceBindingStateBound) {
			route := workspaceCWDPrimaryRoute(primaryTarget, workspacePath, false, "")
			route.RouteID = workspaceOverviewTopologyRouteID(primarySwarmID, strings.TrimSpace(binding.BindingID))
			route.RouteSource = workspaceOverviewTopologyRouteSource
			route.WorkspaceBindingID = strings.TrimSpace(binding.BindingID)
			route.RuntimeWorkspacePath = strings.TrimSpace(binding.DestinationWorkspacePath)
			if route.RuntimeWorkspacePath == "" {
				route.RuntimeWorkspacePath = workspacePath
			}
			route.HostWorkspaceName = strings.TrimSpace(scope.WorkspaceName)
			route.CreatedAt = binding.CreatedAt
			route.UpdatedAt = binding.UpdatedAt
			return route, strings.TrimSpace(binding.BindingID), true
		}
	}
	return workspaceOverviewTopologyRoute{}, "", false
}
