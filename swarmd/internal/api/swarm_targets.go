package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

type swarmTargetHealthEntry struct {
	online    bool
	checkedAt time.Time
	checking  bool
}

type swarmTargetHealthCache struct {
	mu      sync.Mutex
	entries map[string]swarmTargetHealthEntry
}

type swarmTarget struct {
	SwarmID         string                 `json:"swarm_id"`
	Name            string                 `json:"name"`
	Role            string                 `json:"role"`
	Relationship    string                 `json:"relationship"`
	Kind            string                 `json:"kind"`
	DeploymentID    string                 `json:"deployment_id,omitempty"`
	AttachStatus    string                 `json:"attach_status,omitempty"`
	HostSwarmID     string                 `json:"host_swarm_id,omitempty"`
	WorkspaceRoutes []targetWorkspaceRoute `json:"-"`
	Online          bool                   `json:"online"`
	Selectable      bool                   `json:"selectable"`
	Current         bool                   `json:"current"`
	BackendURL      string                 `json:"backend_url,omitempty"`
	DesktopURL      string                 `json:"desktop_url,omitempty"`
	LastError       string                 `json:"last_error,omitempty"`
}

type swarmTargetsResponse struct {
	OK      bool          `json:"ok"`
	Targets []swarmTarget `json:"targets"`
}

type swarmCurrentTargetResponse struct {
	OK     bool         `json:"ok"`
	Target *swarmTarget `json:"target,omitempty"`
}

type swarmSelectTargetRequest struct {
	SwarmID string `json:"swarm_id"`
}

func (s *Server) handleSwarmTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	targets, _, err := s.swarmTargetsForRequest(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, swarmTargetsResponse{OK: true, Targets: targets})
}

func (s *Server) handleSwarmCurrentTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	targets, current, err := s.swarmTargetsForRequest(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil {
		writeJSON(w, http.StatusOK, swarmCurrentTargetResponse{OK: true})
		return
	}
	for i := range targets {
		if strings.EqualFold(strings.TrimSpace(targets[i].SwarmID), strings.TrimSpace(current.SwarmID)) {
			targetCopy := targets[i]
			writeJSON(w, http.StatusOK, swarmCurrentTargetResponse{OK: true, Target: &targetCopy})
			return
		}
	}
	targetCopy := *current
	writeJSON(w, http.StatusOK, swarmCurrentTargetResponse{OK: true, Target: &targetCopy})
}

func (s *Server) handleSwarmSelectTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarmDesktopTargetSelection == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm desktop target selection store is not configured"))
		return
	}
	var req swarmSelectTargetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	selectedID := strings.TrimSpace(req.SwarmID)
	if selectedID == "" {
		writeError(w, http.StatusBadRequest, errors.New("swarm_id is required"))
		return
	}
	targets, _, err := s.swarmTargetsForRequest(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var selected *swarmTarget
	for i := range targets {
		if !strings.EqualFold(strings.TrimSpace(targets[i].SwarmID), selectedID) {
			continue
		}
		selected = &targets[i]
		break
	}
	if selected == nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("swarm target %q was not found", selectedID))
		return
	}
	if !selected.Selectable {
		writeError(w, http.StatusBadRequest, fmt.Errorf("swarm target %q is not selectable", selectedID))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if _, err := s.swarmDesktopTargetSelection.PutForAccount(principal.AccountScopeID, principal.UserID, selected.SwarmID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	selectedCopy := *selected
	selectedCopy.Current = true
	writeJSON(w, http.StatusOK, swarmCurrentTargetResponse{OK: true, Target: &selectedCopy})
}

func hostRoleFromConfig(cfg startupconfig.FileConfig) string {
	if cfg.Child {
		return "child"
	}
	return "master"
}

func (s *Server) swarmTargetsForRequest(r *http.Request) ([]swarmTarget, *swarmTarget, error) {
	return s.swarmTargetsForRequestWithOptions(r, false)
}

func requestedSwarmTargetID(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return strings.TrimSpace(r.URL.Query().Get("swarm_id"))
}

func (s *Server) attachTargetWorkspaceRoutesForAccount(accountScopeID string, targets []swarmTarget) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if s == nil || s.topology == nil || accountScopeID == "" || len(targets) == 0 {
		return nil
	}
	bindings, err := s.topology.ListWorkspaceBindingsForAccount(accountScopeID, 100000)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	for i := range targets {
		for _, binding := range bindings {
			if !strings.EqualFold(strings.TrimSpace(binding.DestinationRuntimeSwarmID), strings.TrimSpace(targets[i].SwarmID)) {
				continue
			}
			route, err := newTargetWorkspaceRoute(accountScopeID, targets[i], binding)
			if err != nil {
				return err
			}
			targets[i].WorkspaceRoutes = append(targets[i].WorkspaceRoutes, route)
		}
	}
	return nil
}

func (s *Server) swarmTargetsForRequestWithOptions(r *http.Request, strict bool) ([]swarmTarget, *swarmTarget, error) {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return nil, nil, err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return nil, nil, err
	}
	nodeTargets, err := s.listSwarmNodeTargets()
	if err != nil {
		return nil, nil, err
	}
	trustedPeerTargets := listTrustedPeerTargets(state.TrustedPeers)
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return nil, nil, identity.ErrPrincipalRequired
	}
	topologyTargets, err := s.listTopologyTargetsForAccount(principal.AccountScopeID)
	if err != nil {
		return nil, nil, err
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	currentGroupSwarmIDs := currentSwarmGroupMemberIDs(state)
	selectedID := requestedSwarmTargetID(r)
	if selectedID == "" {
		selectedID, err = s.selectedSwarmDesktopTargetIDForAccount(principal.AccountScopeID, principal.UserID, localSwarmID)
		if err != nil {
			return nil, nil, err
		}
	}

	targets := make([]swarmTarget, 0, len(nodeTargets)+len(trustedPeerTargets)+len(topologyTargets)+1)
	targets = append(targets, swarmTarget{
		SwarmID:      localSwarmID,
		Name:         firstNonEmpty(strings.TrimSpace(state.Node.Name), strings.TrimSpace(cfg.SwarmName), "Local"),
		Role:         firstNonEmpty(strings.TrimSpace(state.Node.Role), hostRoleFromConfig(cfg), "master"),
		Relationship: "self",
		Kind:         "self",
		Online:       true,
		Selectable:   true,
		Current:      strings.EqualFold(localSwarmID, selectedID),
	})
	seenTargets := map[string]struct{}{}
	markSwarmTargetSeen(seenTargets, targets[0])
	for _, peer := range trustedPeerTargets {
		if isLocalSwarmTargetID(peer.SwarmID, localSwarmID) {
			continue
		}
		if !swarmTargetInCurrentGroup(currentGroupSwarmIDs, peer.SwarmID) {
			continue
		}
		if swarmTargetSeen(seenTargets, peer) {
			continue
		}
		targets = append(targets, peer)
		markSwarmTargetSeen(seenTargets, peer)
	}
	for _, node := range nodeTargets {
		if isLocalSwarmTargetID(node.SwarmID, localSwarmID) {
			continue
		}
		if !swarmTargetInCurrentGroup(currentGroupSwarmIDs, node.SwarmID) {
			continue
		}
		if swarmTargetSeen(seenTargets, node) {
			continue
		}
		targets = append(targets, node)
		markSwarmTargetSeen(seenTargets, node)
	}
	for _, target := range topologyTargets {
		if selectedID == "" && !swarmTargetInCurrentGroup(currentGroupSwarmIDs, target.SwarmID) {
			continue
		}
		if selectedID != "" && !strings.EqualFold(strings.TrimSpace(selectedID), strings.TrimSpace(target.SwarmID)) && !swarmTargetInCurrentGroup(currentGroupSwarmIDs, target.SwarmID) {
			continue
		}
		if swarmTargetSeen(seenTargets, target) {
			continue
		}
		targets = append(targets, target)
		markSwarmTargetSeen(seenTargets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Relationship == "self" {
			return true
		}
		if targets[j].Relationship == "self" {
			return false
		}
		if targets[i].Current != targets[j].Current {
			return targets[i].Current
		}
		return strings.ToLower(targets[i].Name) < strings.ToLower(targets[j].Name)
	})
	for i := range targets {
		targets[i].Current = strings.EqualFold(strings.TrimSpace(targets[i].SwarmID), selectedID)
	}
	if err := s.attachTargetWorkspaceRoutesForAccount(principal.AccountScopeID, targets); err != nil {
		return nil, nil, err
	}
	for i := range targets {
		if targets[i].Current {
			current := targets[i]
			return targets, &current, nil
		}
	}
	if strict && selectedID != "" && !strings.EqualFold(selectedID, localSwarmID) {
		return nil, nil, fmt.Errorf("swarm target %q was not found", selectedID)
	}
	if len(targets) == 0 {
		return nil, nil, nil
	}
	targets[0].Current = true
	current := targets[0]
	return targets, &current, nil
}

func (s *Server) selectedSwarmDesktopTargetIDForAccount(accountScopeID, userID, localSwarmID string) (string, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return "", identity.ErrPrincipalRequired
	}
	localSwarmID = strings.TrimSpace(localSwarmID)
	if s.swarmDesktopTargetSelection == nil {
		return localSwarmID, nil
	}
	record, ok, err := s.swarmDesktopTargetSelection.GetForAccount(accountScopeID)
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(record.SwarmID) == "" {
		if localSwarmID == "" {
			return "", nil
		}
		if _, err := s.swarmDesktopTargetSelection.PutForAccount(accountScopeID, userID, localSwarmID); err != nil {
			return "", err
		}
		return localSwarmID, nil
	}
	return strings.TrimSpace(record.SwarmID), nil
}

func currentSwarmGroupMemberIDs(state swarmruntime.LocalState) map[string]struct{} {
	currentGroupID := strings.TrimSpace(state.CurrentGroupID)
	if currentGroupID == "" {
		return nil
	}
	for _, group := range state.Groups {
		if !strings.EqualFold(strings.TrimSpace(group.Group.ID), currentGroupID) {
			continue
		}
		out := make(map[string]struct{}, len(group.Members)+1)
		if localSwarmID := strings.TrimSpace(state.Node.SwarmID); localSwarmID != "" {
			out[strings.ToLower(localSwarmID)] = struct{}{}
		}
		for _, member := range group.Members {
			if swarmID := strings.TrimSpace(member.SwarmID); swarmID != "" {
				out[strings.ToLower(swarmID)] = struct{}{}
			}
		}
		return out
	}
	return nil
}

func swarmTargetInCurrentGroup(currentGroupSwarmIDs map[string]struct{}, swarmID string) bool {
	if len(currentGroupSwarmIDs) == 0 {
		return true
	}
	_, ok := currentGroupSwarmIDs[strings.ToLower(strings.TrimSpace(swarmID))]
	return ok
}

func (s *Server) listSwarmNodeTargets() ([]swarmTarget, error) {
	if s == nil || s.swarmNodes == nil {
		return nil, nil
	}
	items, err := s.swarmNodes.List(1000)
	if err != nil {
		return nil, err
	}
	out := make([]swarmTarget, 0, len(items))
	for _, item := range items {
		target, ok := mapSwarmNodeTarget(item)
		if !ok {
			continue
		}
		out = append(out, target)
	}
	return out, nil
}

func listTrustedPeerTargets(peers []swarmruntime.TrustedPeer) []swarmTarget {
	out := make([]swarmTarget, 0, len(peers))
	for _, peer := range peers {
		target, ok := mapTrustedPeerTarget(peer)
		if !ok {
			continue
		}
		out = append(out, target)
	}
	return out
}

func (s *Server) listTopologyTargetsForAccount(accountScopeID string) ([]swarmTarget, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, identity.ErrPrincipalRequired
	}
	topologySvc := topologyServiceFromServer(s)
	if topologySvc == nil {
		return nil, nil
	}
	runtimes, err := topologySvc.ListRuntimesForAccount(accountScopeID, 100000)
	if err != nil {
		return nil, err
	}
	placements, err := topologySvc.ListRuntimePlacementsForAccount(accountScopeID, 100000)
	if err != nil {
		return nil, err
	}
	placementByRuntime := make(map[string]pebblestore.TopologyRuntimePlacementRecord, len(placements))
	for _, placement := range placements {
		placementByRuntime[strings.TrimSpace(placement.RuntimeSwarmID)] = placement
	}
	out := make([]swarmTarget, 0, len(runtimes))
	for _, runtime := range runtimes {
		target, ok := mapTopologyRuntimeTargetWithPlacement(runtime, placementByRuntime[strings.TrimSpace(runtime.SwarmID)])
		if ok {
			out = append(out, target)
		}
	}
	return out, nil
}

func isLocalSwarmTargetID(swarmID, localSwarmID string) bool {
	swarmID = strings.TrimSpace(swarmID)
	localSwarmID = strings.TrimSpace(localSwarmID)
	return swarmID != "" && localSwarmID != "" && strings.EqualFold(swarmID, localSwarmID)
}

func markSwarmTargetSeen(seen map[string]struct{}, target swarmTarget) {
	if seen == nil {
		return
	}
	for _, key := range swarmTargetIdentityKeys(target) {
		seen[key] = struct{}{}
	}
}

func swarmTargetSeen(seen map[string]struct{}, target swarmTarget) bool {
	if seen == nil {
		return false
	}
	for _, key := range swarmTargetIdentityKeys(target) {
		if _, ok := seen[key]; ok {
			return true
		}
	}
	return false
}

func swarmTargetIdentityKeys(target swarmTarget) []string {
	if swarmID := strings.ToLower(strings.TrimSpace(target.SwarmID)); swarmID != "" {
		return []string{"swarm:" + swarmID}
	}
	if backendURL := strings.ToLower(strings.TrimRight(strings.TrimSpace(target.BackendURL), "/")); backendURL != "" {
		return []string{"backend:" + backendURL}
	}
	return nil
}

func mapSwarmNodeTarget(item pebblestore.SwarmNodeRecord) (swarmTarget, bool) {
	swarmID := strings.TrimSpace(item.SwarmID)
	backendURL := strings.TrimSpace(item.BackendURL)
	if swarmID == "" || backendURL == "" {
		return swarmTarget{}, false
	}
	status := strings.TrimSpace(item.Status)
	online := swarmNodeStatusOnline(status)
	role := firstNonEmpty(strings.TrimSpace(item.Role), "child")
	return swarmTarget{
		SwarmID:      swarmID,
		Name:         firstNonEmpty(strings.TrimSpace(item.Name), swarmID),
		Role:         role,
		Relationship: relationshipForSwarmNodeRole(role),
		Kind:         firstNonEmpty(strings.TrimSpace(item.Kind), "remote"),
		DeploymentID: strings.TrimSpace(item.DeploymentID),
		AttachStatus: status,
		Online:       online,
		Selectable:   online,
		BackendURL:   backendURL,
		DesktopURL:   strings.TrimSpace(item.DesktopURL),
		LastError:    strings.TrimSpace(item.LastError),
	}, true
}

func swarmNodeStatusOnline(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "online", "ready", "attached", "registered":
		return true
	default:
		return false
	}
}

func relationshipForSwarmNodeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "controller", "master", "parent":
		return swarmruntime.RelationshipParent
	case "child":
		return swarmruntime.RelationshipChild
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

func mapTrustedPeerTarget(peer swarmruntime.TrustedPeer) (swarmTarget, bool) {
	swarmID := strings.TrimSpace(peer.SwarmID)
	relationship := strings.ToLower(strings.TrimSpace(peer.Relationship))
	if swarmID == "" || (relationship != swarmruntime.RelationshipManaged && relationship != swarmruntime.RelationshipManager) {
		return swarmTarget{}, false
	}
	backendURL := trustedPeerBackendURL(peer)
	online := backendURL != ""
	role := firstNonEmpty(strings.TrimSpace(peer.Role), relationship)
	kind := "host"
	if relationship == swarmruntime.RelationshipManager {
		kind = "manager"
	}
	return swarmTarget{
		SwarmID:      swarmID,
		Name:         firstNonEmpty(strings.TrimSpace(peer.Name), swarmID),
		Role:         role,
		Relationship: relationship,
		Kind:         kind,
		AttachStatus: firstNonEmpty(strings.TrimSpace(peer.TransportMode), startupconfig.NetworkModeTailscale),
		Online:       online,
		Selectable:   online,
		BackendURL:   backendURL,
		DesktopURL:   backendURL,
	}, true
}

func trustedPeerBackendURL(peer swarmruntime.TrustedPeer) string {
	if endpoint := firstTrustedPeerTransportForKind(peer.RendezvousTransports, startupconfig.NetworkModeTailscale); endpoint != "" {
		return normalizeRemoteSwarmEndpoint(endpoint)
	}
	if endpoint := firstTrustedPeerTransportForKind(peer.RendezvousTransports, strings.TrimSpace(peer.TransportMode)); endpoint != "" {
		return normalizeRemoteSwarmEndpoint(endpoint)
	}
	for _, transport := range peer.RendezvousTransports {
		if endpoint := firstTrustedPeerTransportValue(transport); endpoint != "" {
			return normalizeRemoteSwarmEndpoint(endpoint)
		}
	}
	return ""
}

func firstTrustedPeerTransportForKind(transports []swarmruntime.TransportSummary, kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ""
	}
	for _, transport := range transports {
		if !strings.EqualFold(strings.TrimSpace(transport.Kind), kind) {
			continue
		}
		if endpoint := firstTrustedPeerTransportValue(transport); endpoint != "" {
			return endpoint
		}
	}
	return ""
}

func firstTrustedPeerTransportValue(transport swarmruntime.TransportSummary) string {
	if value := strings.TrimSpace(transport.Primary); value != "" {
		return value
	}
	for _, value := range transport.All {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

type topologyRuntimeLister interface {
	ListRuntimesForAccount(accountScopeID string, limit int) ([]pebblestore.TopologyRuntimeRecord, error)
	ListRuntimePlacementsForAccount(accountScopeID string, limit int) ([]pebblestore.TopologyRuntimePlacementRecord, error)
}

func topologyServiceFromServer(s *Server) topologyRuntimeLister {
	if s == nil || s.topology == nil {
		return nil
	}
	return s.topology
}

func mapTopologyRuntimeTarget(item pebblestore.TopologyRuntimeRecord) (swarmTarget, bool) {
	return mapTopologyRuntimeTargetWithPlacement(item, pebblestore.TopologyRuntimePlacementRecord{})
}

func mapTopologyRuntimeTargetWithPlacement(item pebblestore.TopologyRuntimeRecord, placement pebblestore.TopologyRuntimePlacementRecord) (swarmTarget, bool) {
	swarmID := strings.TrimSpace(item.SwarmID)
	backendURL := strings.TrimSpace(item.BackendURL)
	if swarmID == "" {
		return swarmTarget{}, false
	}
	status := strings.TrimSpace(item.Status)
	online := status == "" || swarmNodeStatusOnline(status)
	hostSwarmID := firstNonEmpty(strings.TrimSpace(placement.AuthorityHostSwarmID), strings.TrimSpace(item.OwnerHostSwarmID))
	hostContainerID := firstNonEmpty(strings.TrimSpace(placement.AuthorityContainerID), strings.TrimSpace(item.OwnerHostContainerID))
	role := firstNonEmpty(strings.TrimSpace(item.Role), strings.TrimSpace(item.Relationship), "child")
	kind := strings.TrimSpace(item.Transport)
	if kind == "" {
		switch strings.ToLower(strings.TrimSpace(item.Relationship)) {
		case swarmruntime.RelationshipChild:
			if hostSwarmID != "" {
				kind = "mirrored"
			} else {
				kind = "remote"
			}
		case swarmruntime.RelationshipManaged, swarmruntime.RelationshipManager:
			kind = "host"
		default:
			kind = strings.TrimSpace(item.Relationship)
		}
	}
	return swarmTarget{
		SwarmID:      swarmID,
		Name:         firstNonEmpty(strings.TrimSpace(item.Name), swarmID),
		Role:         role,
		Relationship: strings.TrimSpace(item.Relationship),
		Kind:         kind,
		DeploymentID: hostContainerID,
		AttachStatus: firstNonEmpty(status, "attached"),
		HostSwarmID:  hostSwarmID,
		Online:       online,
		Selectable:   online,
		BackendURL:   backendURL,
		DesktopURL:   firstNonEmpty(strings.TrimSpace(item.DesktopURL), backendURL),
	}, true
}

