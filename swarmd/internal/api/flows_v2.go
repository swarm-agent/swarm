package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const flowV2Path = "/v2/flows"

type flowV2ListResponse struct {
	OK    bool            `json:"ok"`
	Flows []flowV2Summary `json:"flows"`
}

type flowV2GetResponse struct {
	OK   bool         `json:"ok"`
	Flow flowV2Detail `json:"flow"`
}

type flowV2MutationResponse struct {
	OK   bool         `json:"ok"`
	Flow flowV2Detail `json:"flow"`
}

type flowV2RunNowView struct {
	RunID     string `json:"run_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type flowV2Summary struct {
	Definition         pebblestore.FlowDefinitionRecord         `json:"definition"`
	TargetDetail       *swarmTarget                             `json:"target_detail,omitempty"`
	AgentDetail        *pebblestore.AgentProfile                `json:"agent_detail,omitempty"`
	WorkspaceDetail    *flowV2WorkspaceDetail                   `json:"workspace_detail,omitempty"`
	AssignmentStatuses []pebblestore.FlowAssignmentStatusRecord `json:"assignment_statuses,omitempty"`
	LastRun            *pebblestore.FlowRunSummaryRecord        `json:"last_run,omitempty"`
	HistoryCount       int                                      `json:"history_count"`
}

type flowV2Detail struct {
	Definition         pebblestore.FlowDefinitionRecord         `json:"definition"`
	TargetDetail       *swarmTarget                             `json:"target_detail,omitempty"`
	AgentDetail        *pebblestore.AgentProfile                `json:"agent_detail,omitempty"`
	WorkspaceDetail    *flowV2WorkspaceDetail                   `json:"workspace_detail,omitempty"`
	AssignmentStatuses []pebblestore.FlowAssignmentStatusRecord `json:"assignment_statuses"`
	History            []pebblestore.FlowRunSummaryRecord       `json:"history"`
}

type flowV2WorkspaceDetail struct {
	WorkspacePath        string `json:"workspace_path"`
	HostWorkspacePath    string `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath string `json:"runtime_workspace_path,omitempty"`
	CWD                  string `json:"cwd,omitempty"`
	WorktreeMode         string `json:"worktree_mode,omitempty"`
}

type flowV2CreateRequest struct {
	FlowID        string                `json:"flow_id,omitempty"`
	Name          string                `json:"name"`
	Enabled       *bool                 `json:"enabled,omitempty"`
	Target        flow.TargetSelection  `json:"target"`
	Agent         flow.AgentSelection   `json:"agent"`
	Workspace     flow.WorkspaceContext `json:"workspace"`
	Schedule      flow.ScheduleSpec     `json:"schedule"`
	CatchUpPolicy flow.CatchUpPolicy    `json:"catch_up_policy"`
	Intent        flow.PromptIntent     `json:"intent"`
}

func (s *Server) handleFlowsV2(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, http.StatusInternalServerError, errors.New("flow store is not configured"))
		return
	}
	path := strings.Trim(strings.TrimPrefix(strings.TrimSpace(r.URL.Path), flowV2Path), "/")
	if path == "" {
		s.handleFlowsV2Collection(w, r)
		return
	}
	parts := strings.Split(path, "/")
	flowID := strings.TrimSpace(parts[0])
	if flowID == "" {
		writeError(w, http.StatusBadRequest, errors.New("flow_id is required"))
		return
	}
	if len(parts) == 1 {
		s.handleFlowV2ByID(w, r, flowID)
		return
	}
	if len(parts) == 2 && parts[1] == "run-now" {
		s.handleFlowV2RunNow(w, r, flowID)
		return
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("unsupported flow v2 path %q", r.URL.Path))
}

func (s *Server) handleFlowsV2Collection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, err := positiveQueryLimit(r, 200)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		definitions, err := s.flows.ListDefinitions(limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items := make([]flowV2Summary, 0, len(definitions))
		for _, definition := range definitions {
			summary, err := s.flowV2Summary(r, definition)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			items = append(items, summary)
		}
		writeJSON(w, http.StatusOK, flowV2ListResponse{OK: true, Flows: items})
	case http.MethodPost:
		var req flowV2CreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		detail, err := s.createHostFlowV2(r, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, flowV2MutationResponse{OK: true, Flow: detail})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleFlowV2ByID(w http.ResponseWriter, r *http.Request, flowID string) {
	switch r.Method {
	case http.MethodGet:
		detail, ok, err := s.flowV2Detail(r, flowID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("flow %q was not found", flowID))
			return
		}
		writeJSON(w, http.StatusOK, flowV2GetResponse{OK: true, Flow: detail})
	case http.MethodDelete:
		detail, err := s.deleteHostFlowV2(r, flowID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, flowV2MutationResponse{OK: true, Flow: detail})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleFlowV2RunNow(w http.ResponseWriter, r *http.Request, flowID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	definition, ok, err := s.flows.GetDefinition(flowID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("flow %q was not found", flowID))
		return
	}
	if err := s.ensureHostOnlyFlowV2Assignment(r, definition.Assignment); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	start, err := s.RunAcceptedFlowNowAt(r.Context(), flow.AcceptedAssignment{Assignment: definition.Assignment}, time.Now().UTC(), newFlowCommandID(definition.FlowID, definition.Revision, flow.CommandRunNow))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		OK  bool             `json:"ok"`
		Run flowV2RunNowView `json:"run"`
	}{OK: true, Run: flowV2RunNowView{RunID: start.RunID, SessionID: start.SessionID, Status: "started"}})
}

func (s *Server) createHostFlowV2(r *http.Request, req flowV2CreateRequest) (flowV2Detail, error) {
	assignment, err := s.assignmentFromFlowV2Request(r, req)
	if err != nil {
		return flowV2Detail{}, err
	}
	if _, exists, err := s.flows.GetDefinition(assignment.FlowID); err != nil {
		return flowV2Detail{}, err
	} else if exists {
		return flowV2Detail{}, fmt.Errorf("flow %q already exists", assignment.FlowID)
	}
	now := time.Now().UTC()
	nextDueAt, _, err := flow.NextFire(assignment, now)
	if err != nil {
		return flowV2Detail{}, err
	}
	definition, err := s.flows.PutDefinition(pebblestore.FlowDefinitionRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment, NextDueAt: nextDueAt})
	if err != nil {
		return flowV2Detail{}, err
	}
	accepted, err := s.flows.PutAcceptedAssignment(flow.AcceptedAssignment{Assignment: definition.Assignment, AcceptedAt: now})
	if err != nil {
		_ = s.flows.DeleteDefinition(definition.FlowID)
		return flowV2Detail{}, err
	}
	if _, err := s.flows.PutAssignmentStatus(pebblestore.FlowAssignmentStatusRecord{FlowID: definition.FlowID, TargetSwarmID: definition.Assignment.Target.SwarmID, Target: definition.Assignment.Target, DesiredRevision: definition.Revision, AcceptedRevision: accepted.Revision, Status: flow.AssignmentAccepted, Reason: "host-local v2 flow installed"}); err != nil {
		_ = s.flows.DeleteAcceptedAssignment(definition.FlowID)
		_ = s.flows.DeleteDefinition(definition.FlowID)
		return flowV2Detail{}, err
	}
	detail, ok, err := s.flowV2Detail(r, definition.FlowID)
	if err != nil {
		return flowV2Detail{}, err
	}
	if !ok {
		return flowV2Detail{}, fmt.Errorf("flow %q was not found after create", definition.FlowID)
	}
	return detail, nil
}

func (s *Server) deleteHostFlowV2(r *http.Request, flowID string) (flowV2Detail, error) {
	definition, ok, err := s.flows.GetDefinition(flowID)
	if err != nil {
		return flowV2Detail{}, err
	}
	if !ok {
		return flowV2Detail{}, fmt.Errorf("flow %q was not found", flowID)
	}
	if err := s.ensureHostOnlyFlowV2Assignment(r, definition.Assignment); err != nil {
		return flowV2Detail{}, err
	}
	deletedDefinition := definition
	deletedDefinition.DeletedAt = time.Now().UTC()
	if err := s.flows.DeleteDefinition(definition.FlowID); err != nil {
		return flowV2Detail{}, err
	}
	if err := s.flows.DeleteAcceptedAssignment(definition.FlowID); err != nil {
		return flowV2Detail{}, err
	}
	statuses, _ := s.flows.ListAssignmentStatuses(definition.FlowID, 100)
	history, _ := s.flows.ListMirroredRunSummaries(definition.FlowID, 100)
	targetDetail, _ := s.flowV2HostTargetDetail(r, deletedDefinition.Assignment.Target)
	agentDetail, _ := s.flowV2AgentDetail(deletedDefinition.Assignment.Agent)
	return flowV2Detail{Definition: deletedDefinition, TargetDetail: targetDetail, AgentDetail: agentDetail, WorkspaceDetail: flowV2WorkspaceDetailFromContext(deletedDefinition.Assignment.Workspace), AssignmentStatuses: statuses, History: history}, nil
}

func (s *Server) assignmentFromFlowV2Request(r *http.Request, req flowV2CreateRequest) (flow.Assignment, error) {
	flowID := strings.TrimSpace(req.FlowID)
	if flowID == "" {
		flowID = "flow-" + randomHex(8)
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	assignment := flow.Assignment{FlowID: flowID, Revision: 1, Name: strings.TrimSpace(req.Name), Enabled: enabled, Target: normalizeFlowTargetSelection(req.Target), Agent: normalizeManagementAgentSelection(req.Agent), Workspace: normalizeManagementWorkspace(req.Workspace), Schedule: normalizeManagementSchedule(req.Schedule), CatchUpPolicy: flow.NormalizeCatchUpPolicy(req.CatchUpPolicy), Intent: normalizeManagementIntent(req.Intent)}
	if assignment.Name == "" {
		assignment.Name = assignment.FlowID
	}
	if assignment.Schedule.Cadence == "" {
		assignment.Schedule.Cadence = flow.CadenceOnDemand
	}
	if assignment.Schedule.Timezone == "" && flow.NormalizeCadence(assignment.Schedule.Cadence) != flow.CadenceOnDemand {
		assignment.Schedule.Timezone = "UTC"
	}
	if assignment.CatchUpPolicy.Mode == "" {
		assignment.CatchUpPolicy.Mode = flow.CatchUpOnce
	}
	if err := s.ensureHostOnlyFlowV2Assignment(r, assignment); err != nil {
		return flow.Assignment{}, err
	}
	local, err := s.localFlowV2Target(r)
	if err != nil {
		return flow.Assignment{}, err
	}
	assignment.Target = flow.TargetSelection{SwarmID: local.SwarmID, Kind: "self", Name: local.Name}
	if err := flow.ValidateAssignment(assignment); err != nil {
		return flow.Assignment{}, err
	}
	return assignment, nil
}

func (s *Server) ensureHostOnlyFlowV2Assignment(r *http.Request, assignment flow.Assignment) error {
	local, err := s.localFlowV2Target(r)
	if err != nil {
		return err
	}
	requested := normalizeFlowTargetSelection(assignment.Target)
	if requested.SwarmID != "" && !strings.EqualFold(requested.SwarmID, local.SwarmID) {
		return errors.New("/v2/flows is host-only: target must be the local host")
	}
	if requested.DeploymentID != "" {
		return errors.New("/v2/flows is host-only: deployment targets are not supported")
	}
	if requested.Kind != "" && !strings.EqualFold(requested.Kind, "self") && !strings.EqualFold(requested.Kind, "local") {
		return errors.New("/v2/flows is host-only: target kind must be self")
	}
	if assignment.Workspace.WorkspacePath == "" || assignment.Workspace.WorkspacePath == "." {
		return errors.New("workspace_path is required")
	}
	if _, err := s.flowV2AgentDetail(assignment.Agent); err != nil {
		return err
	}
	return nil
}

func (s *Server) localFlowV2Target(_ *http.Request) (swarmTarget, error) {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return swarmTarget{}, err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return swarmTarget{}, err
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if localSwarmID == "" {
		return swarmTarget{}, errors.New("local host swarm_id is not configured")
	}
	return swarmTarget{
		SwarmID:      localSwarmID,
		Name:         firstNonEmpty(strings.TrimSpace(state.Node.Name), strings.TrimSpace(cfg.SwarmName), "Local"),
		Role:         firstNonEmpty(strings.TrimSpace(state.Node.Role), hostRoleFromConfig(cfg), "master"),
		Relationship: "self",
		Kind:         "self",
		Online:       true,
		Selectable:   true,
		Current:      true,
	}, nil
}

func (s *Server) flowV2HostTargetDetail(r *http.Request, selection flow.TargetSelection) (*swarmTarget, error) {
	local, err := s.localFlowV2Target(r)
	if err != nil {
		return nil, err
	}
	selection = normalizeFlowTargetSelection(selection)
	if selection.SwarmID != "" && !strings.EqualFold(selection.SwarmID, local.SwarmID) {
		return nil, nil
	}
	copy := local
	return &copy, nil
}

func (s *Server) flowV2AgentDetail(agent flow.AgentSelection) (*pebblestore.AgentProfile, error) {
	if s.agents == nil {
		return nil, errors.New("agent service not configured")
	}
	agent = normalizeManagementAgentSelection(agent)
	if agent.ProfileMode == "" {
		agent.ProfileMode = "background"
	}
	agent = flow.NormalizeAgentSelection(agent)
	if agent.ProfileName == "" {
		return nil, errors.New("agent profile_name is required")
	}
	state, err := s.agents.ListState(2000)
	if err != nil {
		return nil, err
	}
	for _, profile := range state.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Name), agent.ProfileName) && flowV2ProfileMatchesSelection(profile, agent) {
			copy := profile
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("saved agent profile %q with mode %q was not found", agent.ProfileName, agent.ProfileMode)
}

func flowV2ProfileMatchesSelection(profile pebblestore.AgentProfile, agent flow.AgentSelection) bool {
	if !profile.Enabled {
		return false
	}
	if normalizedMode := flow.NormalizeAgentProfileMode(profile.Mode); normalizedMode != "" && normalizedMode != flow.NormalizeAgentProfileMode(agent.ProfileMode) {
		return false
	}
	return true
}

func (s *Server) flowV2Summary(r *http.Request, definition pebblestore.FlowDefinitionRecord) (flowV2Summary, error) {
	statuses, err := s.flows.ListAssignmentStatuses(definition.FlowID, 20)
	if err != nil {
		return flowV2Summary{}, err
	}
	history, err := s.flows.ListMirroredRunSummaries(definition.FlowID, 20)
	if err != nil {
		return flowV2Summary{}, err
	}
	var lastRun *pebblestore.FlowRunSummaryRecord
	if len(history) > 0 {
		copy := history[0]
		lastRun = &copy
	}
	targetDetail, _ := s.flowV2HostTargetDetail(r, definition.Assignment.Target)
	agentDetail, _ := s.flowV2AgentDetail(definition.Assignment.Agent)
	return flowV2Summary{Definition: definition, TargetDetail: targetDetail, AgentDetail: agentDetail, WorkspaceDetail: flowV2WorkspaceDetailFromContext(definition.Assignment.Workspace), AssignmentStatuses: statuses, LastRun: lastRun, HistoryCount: len(history)}, nil
}

func (s *Server) flowV2Detail(r *http.Request, flowID string) (flowV2Detail, bool, error) {
	definition, ok, err := s.flows.GetDefinition(flowID)
	if err != nil || !ok {
		return flowV2Detail{}, ok, err
	}
	statuses, err := s.flows.ListAssignmentStatuses(flowID, 100)
	if err != nil {
		return flowV2Detail{}, false, err
	}
	history, err := s.flows.ListMirroredRunSummaries(flowID, 100)
	if err != nil {
		return flowV2Detail{}, false, err
	}
	targetDetail, _ := s.flowV2HostTargetDetail(r, definition.Assignment.Target)
	agentDetail, _ := s.flowV2AgentDetail(definition.Assignment.Agent)
	return flowV2Detail{Definition: definition, TargetDetail: targetDetail, AgentDetail: agentDetail, WorkspaceDetail: flowV2WorkspaceDetailFromContext(definition.Assignment.Workspace), AssignmentStatuses: statuses, History: history}, true, nil
}

func flowV2WorkspaceDetailFromContext(workspace flow.WorkspaceContext) *flowV2WorkspaceDetail {
	workspace = normalizeManagementWorkspace(workspace)
	if workspace.WorkspacePath == "" && workspace.HostWorkspacePath == "" && workspace.RuntimeWorkspacePath == "" && workspace.CWD == "" {
		return nil
	}
	return &flowV2WorkspaceDetail{WorkspacePath: workspace.WorkspacePath, HostWorkspacePath: workspace.HostWorkspacePath, RuntimeWorkspacePath: workspace.RuntimeWorkspacePath, CWD: workspace.CWD, WorktreeMode: workspace.WorktreeMode}
}
