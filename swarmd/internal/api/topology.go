package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

type topologyRuntimeResponse struct {
	SwarmID         string   `json:"swarm_id"`
	Name            string   `json:"name"`
	Role            string   `json:"role,omitempty"`
	Relationship    string   `json:"relationship,omitempty"`
	Status          string   `json:"status,omitempty"`
	Transport       string   `json:"transport,omitempty"`
	GroupIDs        []string `json:"group_ids,omitempty"`
	ObservedSources []string `json:"observed_sources,omitempty"`
	CreatedAt       int64    `json:"created_at"`
	UpdatedAt       int64    `json:"updated_at"`
}

type topologyWorkspaceBindingResponse struct {
	BindingID                       string                               `json:"binding_id"`
	WorkspaceBindingID              string                               `json:"workspace_binding_id,omitempty"`
	SourceWorkspaceID               string                               `json:"source_workspace_id,omitempty"`
	SourceWorkspaceGeneration       int64                                `json:"source_workspace_generation,omitempty"`
	SourceWorkspacePath             string                               `json:"source_workspace_path"`
	SourceWorkspaceName             string                               `json:"source_workspace_name,omitempty"`
	DestinationRuntimeSwarmID       string                               `json:"destination_runtime_swarm_id,omitempty"`
	DestinationAuthorityHostSwarmID string                               `json:"destination_authority_host_swarm_id,omitempty"`
	DestinationRuntimeKind          string                               `json:"destination_runtime_kind,omitempty"`
	DestinationHostSwarmID          string                               `json:"destination_host_swarm_id,omitempty"`
	DestinationWorkspacePath        string                               `json:"destination_workspace_path,omitempty"`
	PlacementGeneration             int                                  `json:"placement_generation,omitempty"`
	BindingGeneration               int                                  `json:"binding_generation,omitempty"`
	State                           string                               `json:"state,omitempty"`
	AccessMode                      string                               `json:"access_mode,omitempty"`
	MaterializationKind             string                               `json:"materialization_kind,omitempty"`
	AttestedByHostSwarmID           string                               `json:"attested_by_host_swarm_id,omitempty"`
	AttestedAt                      int64                                `json:"attested_at,omitempty"`
	ReplicationMode                 string                               `json:"replication_mode,omitempty"`
	Writable                        bool                                 `json:"writable"`
	Sync                            pebblestore.WorkspaceReplicationSync `json:"sync,omitempty"`
	LegacyTargetKind                string                               `json:"legacy_target_kind,omitempty"`
	CreatedAt                       int64                                `json:"created_at"`
	UpdatedAt                       int64                                `json:"updated_at"`
}

type topologySnapshotResponse struct {
	OK                bool                               `json:"ok"`
	PathID            string                             `json:"path_id"`
	Runtimes          []topologyRuntimeResponse          `json:"runtimes,omitempty"`
	WorkspaceBindings []topologyWorkspaceBindingResponse `json:"workspace_bindings,omitempty"`
}

type topologyWorkspaceBindingsResponse struct {
	OK                  bool                               `json:"ok"`
	PathID              string                             `json:"path_id"`
	WorkspaceBindingID  string                             `json:"workspace_binding_id,omitempty"`
	SourceWorkspaceID   string                             `json:"source_workspace_id,omitempty"`
	SourceWorkspacePath string                             `json:"source_workspace_path,omitempty"`
	Bindings            []topologyWorkspaceBindingResponse `json:"bindings,omitempty"`
}

func (s *Server) SetTopologyService(service *topologyruntime.Service) {
	if s == nil {
		return
	}
	s.topology = service
	if workspaceCanonical, ok := s.runner.(interface {
		SetSessionWorkspaceCanonicalizer(runruntime.SessionWorkspaceCanonicalizer)
	}); ok && workspaceCanonical != nil {
		workspaceCanonical.SetSessionWorkspaceCanonicalizer(s.CanonicalizeSessionWorkspace)
	}
}

func topologyPrincipalFromRequest(r *http.Request) (identity.Principal, error) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		return identity.Principal{}, identity.ErrPrincipalRequired
	}
	return principal, nil
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
	principal, err := topologyPrincipalFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	snapshot, err := s.topology.SnapshotForAccount(principal.AccountScopeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, topologySnapshotResponse{
		OK:                true,
		PathID:            "swarm.topology.snapshot.v1",
		Runtimes:          mapTopologyRuntimeResponses(snapshot.Runtimes),
		WorkspaceBindings: mapTopologyWorkspaceBindingResponses(snapshot.WorkspaceBindings),
	})
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
	principal, err := topologyPrincipalFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	query := r.URL.Query()
	sourceWorkspacePath := strings.TrimSpace(query.Get("source_workspace_path"))
	sourceWorkspaceID := strings.TrimSpace(query.Get("source_workspace_id"))
	workspaceBindingID := strings.TrimSpace(query.Get("workspace_binding_id"))
	if sourceWorkspacePath == "" && sourceWorkspaceID == "" && workspaceBindingID == "" {
		writeError(w, http.StatusBadRequest, errors.New("source_workspace_path, source_workspace_id, or workspace_binding_id is required"))
		return
	}

	var bindings []pebblestore.TopologyWorkspaceBindingRecord
	if workspaceBindingID != "" {
		binding, ok, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, workspaceBindingID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if ok && topologyWorkspaceBindingMatchesQuery(binding, sourceWorkspaceID, sourceWorkspacePath) {
			bindings = append(bindings, binding)
		}
	} else if sourceWorkspaceID != "" {
		records, err := s.topology.ListWorkspaceBindingsForAccount(principal.AccountScopeID, 100000)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, record := range records {
			if topologyWorkspaceBindingMatchesQuery(record, sourceWorkspaceID, sourceWorkspacePath) {
				bindings = append(bindings, record)
			}
		}
	} else {
		var err error
		bindings, err = s.topology.ListWorkspaceBindingsBySourcePathForAccount(principal.AccountScopeID, sourceWorkspacePath, 100000)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, topologyWorkspaceBindingsResponse{
		OK:                  true,
		PathID:              "swarm.topology.workspace_bindings.v1",
		WorkspaceBindingID:  workspaceBindingID,
		SourceWorkspaceID:   sourceWorkspaceID,
		SourceWorkspacePath: sourceWorkspacePath,
		Bindings:            mapTopologyWorkspaceBindingResponses(bindings),
	})
}

func topologyWorkspaceBindingMatchesQuery(record pebblestore.TopologyWorkspaceBindingRecord, sourceWorkspaceID, sourceWorkspacePath string) bool {
	if sourceWorkspaceID != "" && strings.TrimSpace(record.SourceWorkspaceID) != sourceWorkspaceID {
		return false
	}
	if sourceWorkspacePath != "" && !strings.EqualFold(strings.TrimSpace(record.SourceWorkspacePath), sourceWorkspacePath) {
		return false
	}
	return true
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
		SwarmID:         record.SwarmID,
		Name:            record.Name,
		Role:            record.Role,
		Relationship:    record.Relationship,
		Status:          record.Status,
		Transport:       record.Transport,
		GroupIDs:        append([]string(nil), record.GroupIDs...),
		ObservedSources: append([]string(nil), record.ObservedSources...),
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
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
		BindingID:                       record.BindingID,
		WorkspaceBindingID:              record.BindingID,
		SourceWorkspaceID:               record.SourceWorkspaceID,
		SourceWorkspaceGeneration:       record.SourceWorkspaceGeneration,
		SourceWorkspacePath:             record.SourceWorkspacePath,
		SourceWorkspaceName:             record.SourceWorkspaceName,
		DestinationRuntimeSwarmID:       record.DestinationRuntimeSwarmID,
		DestinationAuthorityHostSwarmID: record.DestinationAuthorityHostSwarmID,
		DestinationRuntimeKind:          record.DestinationRuntimeKind,
		DestinationHostSwarmID:          record.DestinationHostSwarmID,
		DestinationWorkspacePath:        record.DestinationWorkspacePath,
		PlacementGeneration:             record.PlacementGeneration,
		BindingGeneration:               record.BindingGeneration,
		State:                           record.State,
		AccessMode:                      record.AccessMode,
		MaterializationKind:             record.MaterializationKind,
		AttestedByHostSwarmID:           record.AttestedByHostSwarmID,
		AttestedAt:                      record.AttestedAt,
		ReplicationMode:                 record.ReplicationMode,
		Writable:                        record.Writable,
		Sync:                            record.Sync,
		LegacyTargetKind:                record.LegacyTargetKind,
		CreatedAt:                       record.CreatedAt,
		UpdatedAt:                       record.UpdatedAt,
	}
}
