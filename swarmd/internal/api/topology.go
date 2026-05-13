package api

import (
	"errors"
	"net/http"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

type topologyRuntimeResponse struct {
	SwarmID              string   `json:"swarm_id"`
	Name                 string   `json:"name"`
	Role                 string   `json:"role,omitempty"`
	Relationship         string   `json:"relationship,omitempty"`
	BackendURL           string   `json:"backend_url,omitempty"`
	DesktopURL           string   `json:"desktop_url,omitempty"`
	Status               string   `json:"status,omitempty"`
	Transport            string   `json:"transport,omitempty"`
	OwnerHostSwarmID     string   `json:"owner_host_swarm_id,omitempty"`
	OwnerHostContainerID string   `json:"owner_host_container_id,omitempty"`
	GroupIDs             []string `json:"group_ids,omitempty"`
	ObservedSources      []string `json:"observed_sources,omitempty"`
	CreatedAt            int64    `json:"created_at"`
	UpdatedAt            int64    `json:"updated_at"`
}

type topologyHostContainerResponse struct {
	HostContainerID     string   `json:"host_container_id"`
	HostSwarmID         string   `json:"host_swarm_id"`
	RuntimeContainerRef string   `json:"runtime_container_ref"`
	Name                string   `json:"name"`
	ContainerName       string   `json:"container_name,omitempty"`
	ContainerID         string   `json:"container_id,omitempty"`
	Runtime             string   `json:"runtime,omitempty"`
	Image               string   `json:"image,omitempty"`
	Status              string   `json:"status,omitempty"`
	HostAPIBaseURL      string   `json:"host_api_base_url,omitempty"`
	HostPort            int      `json:"host_port,omitempty"`
	RuntimePort         int      `json:"runtime_port,omitempty"`
	ObservedSources     []string `json:"observed_sources,omitempty"`
	CreatedAt           int64    `json:"created_at"`
	UpdatedAt           int64    `json:"updated_at"`
}

type topologyAttachmentResponse struct {
	AttachmentID          string `json:"attachment_id"`
	HostContainerID       string `json:"host_container_id"`
	RuntimeSwarmID        string `json:"runtime_swarm_id"`
	State                 string `json:"state,omitempty"`
	DeploymentID          string `json:"deployment_id,omitempty"`
	RemoteDeploySessionID string `json:"remote_deploy_session_id,omitempty"`
	LastError             string `json:"last_error,omitempty"`
	CreatedAt             int64  `json:"created_at"`
	UpdatedAt             int64  `json:"updated_at"`
}

type topologyWorkspaceBindingResponse struct {
	BindingID                 string `json:"binding_id"`
	SourceWorkspacePath       string `json:"source_workspace_path"`
	SourceWorkspaceName       string `json:"source_workspace_name,omitempty"`
	DestinationRuntimeSwarmID string `json:"destination_runtime_swarm_id,omitempty"`
	DestinationHostSwarmID    string `json:"destination_host_swarm_id,omitempty"`
	DestinationContainerID    string `json:"destination_container_id,omitempty"`
	DestinationWorkspacePath  string `json:"destination_workspace_path,omitempty"`
	ReplicationMode           string `json:"replication_mode,omitempty"`
	Writable                  bool   `json:"writable"`
	LegacyTargetKind          string `json:"legacy_target_kind,omitempty"`
	CreatedAt                 int64  `json:"created_at"`
	UpdatedAt                 int64  `json:"updated_at"`
}

type topologySessionRouteResponse struct {
	SessionID            string `json:"session_id"`
	RuntimeSwarmID       string `json:"runtime_swarm_id,omitempty"`
	HostSwarmID          string `json:"host_swarm_id,omitempty"`
	HostContainerID      string `json:"host_container_id,omitempty"`
	WorkspaceBindingID   string `json:"workspace_binding_id,omitempty"`
	BackendURL           string `json:"backend_url,omitempty"`
	HostWorkspacePath    string `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath string `json:"runtime_workspace_path,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

type topologyMigrationStatusResponse struct {
	ID                    string `json:"id"`
	Version               string `json:"version"`
	RebuiltAt             int64  `json:"rebuilt_at"`
	RuntimeCount          int    `json:"runtime_count"`
	HostContainerCount    int    `json:"host_container_count"`
	AttachmentCount       int    `json:"attachment_count"`
	WorkspaceBindingCount int    `json:"workspace_binding_count"`
	SessionRouteCount     int    `json:"session_route_count"`
}

type topologySnapshotResponse struct {
	OK                bool                               `json:"ok"`
	PathID            string                             `json:"path_id"`
	Runtimes          []topologyRuntimeResponse          `json:"runtimes,omitempty"`
	HostContainers    []topologyHostContainerResponse    `json:"host_containers,omitempty"`
	Attachments       []topologyAttachmentResponse       `json:"attachments,omitempty"`
	WorkspaceBindings []topologyWorkspaceBindingResponse `json:"workspace_bindings,omitempty"`
	SessionRoutes     []topologySessionRouteResponse     `json:"session_routes,omitempty"`
	MigrationStatus   topologyMigrationStatusResponse    `json:"migration_status"`
}

type topologyHostContainersResponse struct {
	OK             bool                            `json:"ok"`
	PathID         string                          `json:"path_id"`
	HostSwarmID    string                          `json:"host_swarm_id"`
	HostContainers []topologyHostContainerResponse `json:"host_containers,omitempty"`
}

type topologyRuntimeOwnerResponse struct {
	OK             bool                           `json:"ok"`
	PathID         string                         `json:"path_id"`
	RuntimeSwarmID string                         `json:"runtime_swarm_id"`
	Attachment     *topologyAttachmentResponse    `json:"attachment,omitempty"`
	HostContainer  *topologyHostContainerResponse `json:"host_container,omitempty"`
}

type topologyWorkspaceBindingsResponse struct {
	OK                  bool                               `json:"ok"`
	PathID              string                             `json:"path_id"`
	SourceWorkspacePath string                             `json:"source_workspace_path"`
	Bindings            []topologyWorkspaceBindingResponse `json:"bindings,omitempty"`
}

type topologySessionRouteLookupResponse struct {
	OK     bool                          `json:"ok"`
	PathID string                        `json:"path_id"`
	Route  *topologySessionRouteResponse `json:"route,omitempty"`
}

func (s *Server) SetTopologyService(service *topologyruntime.Service) {
	if s == nil {
		return
	}
	s.topology = service
}

func (s *Server) handleSwarmTopologySnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.topology == nil {
		writeError(w, http.StatusInternalServerError, errors.New("topology service not configured"))
		return
	}
	if _, err := s.topology.Rebuild(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	snapshot, err := s.topology.Snapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, topologySnapshotResponse{
		OK:                true,
		PathID:            "swarm.topology.snapshot.v1",
		Runtimes:          mapTopologyRuntimeResponses(snapshot.Runtimes),
		HostContainers:    mapTopologyHostContainerResponses(snapshot.HostContainers),
		Attachments:       mapTopologyAttachmentResponses(snapshot.Attachments),
		WorkspaceBindings: mapTopologyWorkspaceBindingResponses(snapshot.WorkspaceBindings),
		SessionRoutes:     mapTopologySessionRouteResponses(snapshot.SessionRoutes),
		MigrationStatus:   mapTopologyMigrationStatusResponse(snapshot.MigrationStatus),
	})
}

func (s *Server) handleSwarmTopologyHostContainers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.topology == nil {
		writeError(w, http.StatusInternalServerError, errors.New("topology service not configured"))
		return
	}
	hostSwarmID := strings.TrimSpace(r.URL.Query().Get("host_swarm_id"))
	if hostSwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("host_swarm_id is required"))
		return
	}
	if _, err := s.topology.Rebuild(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	hostContainers, err := s.topology.ListHostContainersByHost(hostSwarmID, 100000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, topologyHostContainersResponse{
		OK:             true,
		PathID:         "swarm.topology.host_containers.v1",
		HostSwarmID:    hostSwarmID,
		HostContainers: mapTopologyHostContainerResponses(hostContainers),
	})
}

func (s *Server) handleSwarmTopologyRuntimeOwner(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.topology == nil {
		writeError(w, http.StatusInternalServerError, errors.New("topology service not configured"))
		return
	}
	runtimeSwarmID := strings.TrimSpace(r.URL.Query().Get("runtime_swarm_id"))
	if runtimeSwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("runtime_swarm_id is required"))
		return
	}
	if _, err := s.topology.Rebuild(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	hostContainer, attachment, ok, err := s.topology.ResolveRuntimeHostContainer(runtimeSwarmID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	response := topologyRuntimeOwnerResponse{OK: true, PathID: "swarm.topology.runtime_owner.v1", RuntimeSwarmID: runtimeSwarmID}
	if ok {
		attachmentCopy := mapTopologyAttachmentResponse(attachment)
		hostContainerCopy := mapTopologyHostContainerResponse(hostContainer)
		response.Attachment = &attachmentCopy
		response.HostContainer = &hostContainerCopy
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSwarmTopologyWorkspaceBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.topology == nil {
		writeError(w, http.StatusInternalServerError, errors.New("topology service not configured"))
		return
	}
	sourceWorkspacePath := strings.TrimSpace(r.URL.Query().Get("source_workspace_path"))
	if sourceWorkspacePath == "" {
		writeError(w, http.StatusBadRequest, errors.New("source_workspace_path is required"))
		return
	}
	if _, err := s.topology.Rebuild(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	bindings, err := s.topology.ListWorkspaceBindingsBySourcePath(sourceWorkspacePath, 100000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, topologyWorkspaceBindingsResponse{
		OK:                  true,
		PathID:              "swarm.topology.workspace_bindings.v1",
		SourceWorkspacePath: sourceWorkspacePath,
		Bindings:            mapTopologyWorkspaceBindingResponses(bindings),
	})
}

func (s *Server) handleSwarmTopologySessionRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.topology == nil {
		writeError(w, http.StatusInternalServerError, errors.New("topology service not configured"))
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
		return
	}
	if _, err := s.topology.Rebuild(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	route, ok, err := s.topology.GetSessionRoute(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	response := topologySessionRouteLookupResponse{OK: true, PathID: "swarm.topology.session_route.v1"}
	if ok {
		routeCopy := mapTopologySessionRouteResponse(route)
		response.Route = &routeCopy
	}
	writeJSON(w, http.StatusOK, response)
}

func mapTopologyRuntimeResponses(records []pebblestore.TopologyRuntimeRecord) []topologyRuntimeResponse {
	out := make([]topologyRuntimeResponse, 0, len(records))
	for _, record := range records {
		out = append(out, mapTopologyRuntimeResponse(record))
	}
	return out
}

func mapTopologyRuntimeResponse(record pebblestore.TopologyRuntimeRecord) topologyRuntimeResponse {
	return topologyRuntimeResponse{
		SwarmID:              record.SwarmID,
		Name:                 record.Name,
		Role:                 record.Role,
		Relationship:         record.Relationship,
		BackendURL:           record.BackendURL,
		DesktopURL:           record.DesktopURL,
		Status:               record.Status,
		Transport:            record.Transport,
		OwnerHostSwarmID:     record.OwnerHostSwarmID,
		OwnerHostContainerID: record.OwnerHostContainerID,
		GroupIDs:             append([]string(nil), record.GroupIDs...),
		ObservedSources:      append([]string(nil), record.ObservedSources...),
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}
}

func mapTopologyHostContainerResponses(records []pebblestore.TopologyHostContainerRecord) []topologyHostContainerResponse {
	out := make([]topologyHostContainerResponse, 0, len(records))
	for _, record := range records {
		out = append(out, mapTopologyHostContainerResponse(record))
	}
	return out
}

func mapTopologyHostContainerResponse(record pebblestore.TopologyHostContainerRecord) topologyHostContainerResponse {
	return topologyHostContainerResponse{
		HostContainerID:     record.HostContainerID,
		HostSwarmID:         record.HostSwarmID,
		RuntimeContainerRef: record.RuntimeContainerRef,
		Name:                record.Name,
		ContainerName:       record.ContainerName,
		ContainerID:         record.ContainerID,
		Runtime:             record.Runtime,
		Image:               record.Image,
		Status:              record.Status,
		HostAPIBaseURL:      record.HostAPIBaseURL,
		HostPort:            record.HostPort,
		RuntimePort:         record.RuntimePort,
		ObservedSources:     append([]string(nil), record.ObservedSources...),
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	}
}

func mapTopologyAttachmentResponses(records []pebblestore.TopologyAttachmentRecord) []topologyAttachmentResponse {
	out := make([]topologyAttachmentResponse, 0, len(records))
	for _, record := range records {
		out = append(out, mapTopologyAttachmentResponse(record))
	}
	return out
}

func mapTopologyAttachmentResponse(record pebblestore.TopologyAttachmentRecord) topologyAttachmentResponse {
	return topologyAttachmentResponse{
		AttachmentID:          record.AttachmentID,
		HostContainerID:       record.HostContainerID,
		RuntimeSwarmID:        record.RuntimeSwarmID,
		State:                 record.State,
		DeploymentID:          record.DeploymentID,
		RemoteDeploySessionID: record.RemoteDeploySessionID,
		LastError:             record.LastError,
		CreatedAt:             record.CreatedAt,
		UpdatedAt:             record.UpdatedAt,
	}
}

func mapTopologyWorkspaceBindingResponses(records []pebblestore.TopologyWorkspaceBindingRecord) []topologyWorkspaceBindingResponse {
	out := make([]topologyWorkspaceBindingResponse, 0, len(records))
	for _, record := range records {
		out = append(out, mapTopologyWorkspaceBindingResponse(record))
	}
	return out
}

func mapTopologyWorkspaceBindingResponse(record pebblestore.TopologyWorkspaceBindingRecord) topologyWorkspaceBindingResponse {
	return topologyWorkspaceBindingResponse{
		BindingID:                 record.BindingID,
		SourceWorkspacePath:       record.SourceWorkspacePath,
		SourceWorkspaceName:       record.SourceWorkspaceName,
		DestinationRuntimeSwarmID: record.DestinationRuntimeSwarmID,
		DestinationHostSwarmID:    record.DestinationHostSwarmID,
		DestinationContainerID:    record.DestinationContainerID,
		DestinationWorkspacePath:  record.DestinationWorkspacePath,
		ReplicationMode:           record.ReplicationMode,
		Writable:                  record.Writable,
		LegacyTargetKind:          record.LegacyTargetKind,
		CreatedAt:                 record.CreatedAt,
		UpdatedAt:                 record.UpdatedAt,
	}
}

func mapTopologySessionRouteResponses(records []pebblestore.TopologySessionRouteRecord) []topologySessionRouteResponse {
	out := make([]topologySessionRouteResponse, 0, len(records))
	for _, record := range records {
		out = append(out, mapTopologySessionRouteResponse(record))
	}
	return out
}

func mapTopologySessionRouteResponse(record pebblestore.TopologySessionRouteRecord) topologySessionRouteResponse {
	return topologySessionRouteResponse{
		SessionID:            record.SessionID,
		RuntimeSwarmID:       record.RuntimeSwarmID,
		HostSwarmID:          record.HostSwarmID,
		HostContainerID:      record.HostContainerID,
		WorkspaceBindingID:   record.WorkspaceBindingID,
		BackendURL:           record.BackendURL,
		HostWorkspacePath:    record.HostWorkspacePath,
		RuntimeWorkspacePath: record.RuntimeWorkspacePath,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}
}

func mapTopologyMigrationStatusResponse(record pebblestore.TopologyMigrationStatusRecord) topologyMigrationStatusResponse {
	return topologyMigrationStatusResponse{
		ID:                    record.ID,
		Version:               record.Version,
		RebuiltAt:             record.RebuiltAt,
		RuntimeCount:          record.RuntimeCount,
		HostContainerCount:    record.HostContainerCount,
		AttachmentCount:       record.AttachmentCount,
		WorkspaceBindingCount: record.WorkspaceBindingCount,
		SessionRouteCount:     record.SessionRouteCount,
	}
}
