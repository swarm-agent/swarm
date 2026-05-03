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

const flowV3Path = "/v3/flows"

type flowV3Definition struct {
	FlowID        string                `json:"flow_id"`
	Name          string                `json:"name"`
	Enabled       bool                  `json:"enabled"`
	Target        flow.TargetSelection  `json:"target"`
	Agent         flow.AgentSelection   `json:"agent"`
	Workspace     flow.WorkspaceContext `json:"workspace"`
	Schedule      flow.ScheduleSpec     `json:"schedule"`
	CatchUpPolicy flow.CatchUpPolicy    `json:"catch_up_policy"`
	Intent        flow.PromptIntent     `json:"intent"`
}

type flowV3Record struct {
	Definition   flowV3Definition          `json:"definition"`
	TargetDetail *swarmTarget              `json:"target_detail,omitempty"`
	AgentDetail  *pebblestore.AgentProfile `json:"agent_detail,omitempty"`
}

type flowV3RecordResponse struct {
	OK bool `json:"ok"`
	flowV3Record
}

type flowV3ListResponse struct {
	OK    bool           `json:"ok"`
	Flows []flowV3Record `json:"flows"`
}

type flowV3UpsertRequest struct {
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

func (s *Server) handleFlowsV3(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, http.StatusInternalServerError, errors.New("flow store is not configured"))
		return
	}
	path := strings.Trim(strings.TrimPrefix(strings.TrimSpace(r.URL.Path), flowV3Path), "/")
	if path == "" {
		s.handleFlowsV3Collection(w, r)
		return
	}
	parts := strings.Split(path, "/")
	flowID := strings.TrimSpace(parts[0])
	if flowID == "" {
		writeError(w, http.StatusBadRequest, errors.New("flow_id is required"))
		return
	}
	if len(parts) == 1 {
		s.handleFlowV3ByID(w, r, flowID)
		return
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("unsupported flow v3 path %q", r.URL.Path))
}

func (s *Server) handleFlowsV3Collection(w http.ResponseWriter, r *http.Request) {
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
		items := make([]flowV3Record, 0, len(definitions))
		for _, definition := range definitions {
			item, err := s.flowV3RecordForDefinition(r, definition)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			items = append(items, item)
		}
		writeJSON(w, http.StatusOK, flowV3ListResponse{OK: true, Flows: items})
	case http.MethodPost:
		var req flowV3UpsertRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		record, err := s.createFlowV3(r, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, flowV3RecordResponse{OK: true, flowV3Record: record})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleFlowV3ByID(w http.ResponseWriter, r *http.Request, flowID string) {
	switch r.Method {
	case http.MethodGet:
		record, ok, err := s.flowV3Detail(r, flowID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("flow %q was not found", flowID))
			return
		}
		writeJSON(w, http.StatusOK, flowV3RecordResponse{OK: true, flowV3Record: record})
	case http.MethodPut:
		var req flowV3UpsertRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		record, err := s.updateFlowV3(r, flowID, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, flowV3RecordResponse{OK: true, flowV3Record: record})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) createFlowV3(r *http.Request, req flowV3UpsertRequest) (flowV3Record, error) {
	flowID := strings.TrimSpace(req.FlowID)
	if flowID == "" {
		flowID = "flow-" + randomHex(8)
	}
	if _, exists, err := s.flows.GetDefinition(flowID); err != nil {
		return flowV3Record{}, err
	} else if exists {
		return flowV3Record{}, fmt.Errorf("flow %q already exists", flowID)
	}
	assignment, err := s.flowV3AssignmentFromRequest(r, req, nil, flowID, 1)
	if err != nil {
		return flowV3Record{}, err
	}
	now := time.Now().UTC()
	nextDueAt, _, err := flow.NextFire(assignment, now)
	if err != nil {
		return flowV3Record{}, err
	}
	definition, err := s.flows.PutDefinition(pebblestore.FlowDefinitionRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment, NextDueAt: nextDueAt})
	if err != nil {
		return flowV3Record{}, err
	}
	command := flow.AssignmentCommand{CommandID: newFlowCommandID(definition.FlowID, definition.Revision, flow.CommandInstall), FlowID: definition.FlowID, Revision: definition.Revision, Action: flow.CommandInstall, CreatedAt: now, Assignment: definition.Assignment}
	if _, err := s.EnqueueAndDeliverFlowAssignmentCommand(r.Context(), command); err != nil {
		if cleanupErr := s.flows.DeleteDefinition(definition.FlowID); cleanupErr != nil {
			return flowV3Record{}, fmt.Errorf("%w; cleanup flow definition: %v", err, cleanupErr)
		}
		return flowV3Record{}, err
	}
	return s.flowV3RecordForDefinition(r, definition)
}

func (s *Server) updateFlowV3(r *http.Request, flowID string, req flowV3UpsertRequest) (flowV3Record, error) {
	existing, ok, err := s.flows.GetDefinition(flowID)
	if err != nil {
		return flowV3Record{}, err
	}
	if !ok {
		return flowV3Record{}, fmt.Errorf("flow %q was not found", flowID)
	}
	assignment, err := s.flowV3AssignmentFromRequest(r, req, &existing.Assignment, flowID, existing.Revision+1)
	if err != nil {
		return flowV3Record{}, err
	}
	now := time.Now().UTC()
	nextDueAt, _, err := flow.NextFire(assignment, now)
	if err != nil {
		return flowV3Record{}, err
	}
	updatedDefinition, err := s.flows.PutDefinition(pebblestore.FlowDefinitionRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment, NextDueAt: nextDueAt, CreatedAt: existing.CreatedAt})
	if err != nil {
		return flowV3Record{}, err
	}
	command := flow.AssignmentCommand{CommandID: newFlowCommandID(updatedDefinition.FlowID, updatedDefinition.Revision, flow.CommandUpdate), FlowID: updatedDefinition.FlowID, Revision: updatedDefinition.Revision, Action: flow.CommandUpdate, CreatedAt: now, Assignment: updatedDefinition.Assignment}
	if _, err := s.EnqueueAndDeliverFlowAssignmentCommand(r.Context(), command); err != nil {
		if _, restoreErr := s.flows.PutDefinition(existing); restoreErr != nil {
			return flowV3Record{}, fmt.Errorf("%w; restore previous flow definition: %v", err, restoreErr)
		}
		return flowV3Record{}, err
	}
	return s.flowV3RecordForDefinition(r, updatedDefinition)
}

func (s *Server) flowV3Detail(r *http.Request, flowID string) (flowV3Record, bool, error) {
	definition, ok, err := s.flows.GetDefinition(flowID)
	if err != nil || !ok {
		return flowV3Record{}, ok, err
	}
	record, err := s.flowV3RecordForDefinition(r, definition)
	return record, true, err
}

func (s *Server) flowV3RecordForDefinition(r *http.Request, definition pebblestore.FlowDefinitionRecord) (flowV3Record, error) {
	targetDetail, err := s.flowV3TargetDetail(r, definition.Assignment.Target)
	if err != nil {
		return flowV3Record{}, err
	}
	agentDetail, err := s.flowV3AgentDetail(definition.Assignment.Agent)
	if err != nil {
		return flowV3Record{}, err
	}
	return flowV3Record{
		Definition: flowV3Definition{
			FlowID:        strings.TrimSpace(definition.Assignment.FlowID),
			Name:          strings.TrimSpace(definition.Assignment.Name),
			Enabled:       definition.Assignment.Enabled,
			Target:        normalizeFlowTargetSelection(definition.Assignment.Target),
			Agent:         normalizeManagementAgentSelection(definition.Assignment.Agent),
			Workspace:     normalizeManagementWorkspace(definition.Assignment.Workspace),
			Schedule:      normalizeManagementSchedule(definition.Assignment.Schedule),
			CatchUpPolicy: flow.NormalizeCatchUpPolicy(definition.Assignment.CatchUpPolicy),
			Intent:        normalizeManagementIntent(definition.Assignment.Intent),
		},
		TargetDetail: targetDetail,
		AgentDetail:  agentDetail,
	}, nil
}

func (s *Server) flowV3AssignmentFromRequest(r *http.Request, req flowV3UpsertRequest, base *flow.Assignment, flowID string, revision int64) (flow.Assignment, error) {
	flowID = strings.TrimSpace(flowID)
	if flowID == "" {
		return flow.Assignment{}, errors.New("flow_id is required")
	}
	if reqID := strings.TrimSpace(req.FlowID); reqID != "" && !strings.EqualFold(reqID, flowID) {
		return flow.Assignment{}, errors.New("flow_id in path must match payload flow_id")
	}
	enabled := true
	if base != nil {
		enabled = base.Enabled
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	name := strings.TrimSpace(req.Name)
	if name == "" && base != nil {
		name = strings.TrimSpace(base.Name)
	}
	if name == "" {
		name = flowID
	}
	target := normalizeFlowTargetSelection(req.Target)
	if !flowV3HasTargetSelection(target) && base != nil {
		target = normalizeFlowTargetSelection(base.Target)
	}
	if !flowV3HasTargetSelection(target) {
		return flow.Assignment{}, errors.New("target selection is required")
	}
	if _, err := s.requireFlowV3TargetDetail(r, target); err != nil {
		return flow.Assignment{}, err
	}
	agent := normalizeManagementAgentSelection(req.Agent)
	if !flowV3HasAgentSelection(agent) && base != nil {
		agent = normalizeManagementAgentSelection(base.Agent)
	}
	if strings.TrimSpace(agent.ProfileName) == "" {
		return flow.Assignment{}, errors.New("agent profile_name is required")
	}
	if strings.TrimSpace(agent.ProfileMode) == "" {
		return flow.Assignment{}, errors.New("agent profile_mode is required")
	}
	if _, err := s.requireFlowV3AgentDetail(agent); err != nil {
		return flow.Assignment{}, err
	}
	workspace := normalizeManagementWorkspace(req.Workspace)
	if !flowV3HasWorkspaceInput(workspace) && base != nil {
		workspace = normalizeManagementWorkspace(base.Workspace)
	}
	if workspace.WorkspacePath == "" || workspace.WorkspacePath == "." {
		if s.workspace != nil {
			if current, ok, err := s.workspace.CurrentBinding(); err != nil {
				return flow.Assignment{}, err
			} else if ok && strings.TrimSpace(current.ResolvedPath) != "" {
				workspace.WorkspacePath = strings.TrimSpace(current.ResolvedPath)
			}
		}
		if workspace.WorkspacePath == "" {
			return flow.Assignment{}, errors.New("workspace_path is required")
		}
	}
	schedule := normalizeManagementSchedule(req.Schedule)
	if !flowV3HasScheduleInput(schedule) && base != nil {
		schedule = normalizeManagementSchedule(base.Schedule)
	}
	if schedule.Cadence == "" {
		schedule.Cadence = flow.CadenceOnDemand
	}
	if schedule.Timezone == "" && flow.NormalizeCadence(schedule.Cadence) != flow.CadenceOnDemand {
		schedule.Timezone = "UTC"
	}
	catchUpPolicy := flow.NormalizeCatchUpPolicy(req.CatchUpPolicy)
	if !flowV3HasCatchUpPolicyInput(catchUpPolicy) && base != nil {
		catchUpPolicy = flow.NormalizeCatchUpPolicy(base.CatchUpPolicy)
	}
	if catchUpPolicy.Mode == "" {
		catchUpPolicy.Mode = flow.CatchUpOnce
	}
	intent := normalizeManagementIntent(req.Intent)
	if !flowV3HasIntentInput(intent) && base != nil {
		intent = normalizeManagementIntent(base.Intent)
	}
	assignment := flow.Assignment{
		FlowID:        flowID,
		Revision:      revision,
		Name:          name,
		Enabled:       enabled,
		Target:        target,
		Agent:         agent,
		Workspace:     workspace,
		Schedule:      schedule,
		CatchUpPolicy: catchUpPolicy,
		Intent:        intent,
	}
	if err := flow.ValidateAssignment(assignment); err != nil {
		return flow.Assignment{}, err
	}
	return assignment, nil
}

func (s *Server) flowV3TargetDetail(r *http.Request, selection flow.TargetSelection) (*swarmTarget, error) {
	selection = normalizeFlowTargetSelection(selection)
	if !flowV3HasTargetSelection(selection) {
		return nil, nil
	}
	if r == nil {
		var err error
		r, err = http.NewRequest(http.MethodGet, "/v1/swarm/targets", nil)
		if err != nil {
			return nil, err
		}
	}
	targets, _, err := s.swarmTargetsForRequest(r)
	if err != nil {
		return nil, err
	}
	for _, candidate := range targets {
		if !flowTargetMatchesSelection(candidate, selection) {
			continue
		}
		copy := candidate
		return &copy, nil
	}
	return nil, nil
}

func (s *Server) requireFlowV3TargetDetail(r *http.Request, selection flow.TargetSelection) (*swarmTarget, error) {
	detail, err := s.flowV3TargetDetail(r, selection)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, fmt.Errorf("flow target %q was not found", firstNonEmpty(selection.SwarmID, selection.DeploymentID, selection.Name, selection.Kind))
	}
	return detail, nil
}

func (s *Server) flowV3AgentDetail(agent flow.AgentSelection) (*pebblestore.AgentProfile, error) {
	if s.agents == nil {
		return nil, errors.New("agent service not configured")
	}
	agent = normalizeManagementAgentSelection(agent)
	if strings.TrimSpace(agent.ProfileName) == "" {
		return nil, nil
	}
	state, err := s.agents.ListState(2000)
	if err != nil {
		return nil, err
	}
	for _, profile := range state.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Name), agent.ProfileName) {
			copy := profile
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *Server) requireFlowV3AgentDetail(agent flow.AgentSelection) (*pebblestore.AgentProfile, error) {
	agent = normalizeManagementAgentSelection(agent)
	profile, err := s.flowV3AgentDetail(agent)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, fmt.Errorf("saved agent profile %q was not found", agent.ProfileName)
	}
	if !profile.Enabled {
		return nil, fmt.Errorf("saved agent profile %q is disabled", profile.Name)
	}
	profileMode := flow.NormalizeAgentProfileMode(profile.Mode)
	requestedMode := flow.NormalizeAgentProfileMode(agent.ProfileMode)
	if profileMode != "" && requestedMode != "" && profileMode != requestedMode {
		return nil, fmt.Errorf("saved agent profile %q mode %q does not match requested profile_mode %q", profile.Name, profileMode, requestedMode)
	}
	return profile, nil
}

func flowV3HasTargetSelection(selection flow.TargetSelection) bool {
	selection = normalizeFlowTargetSelection(selection)
	return selection.SwarmID != "" || selection.Kind != "" || selection.DeploymentID != "" || selection.Name != ""
}

func flowV3HasAgentSelection(agent flow.AgentSelection) bool {
	agent = normalizeManagementAgentSelection(agent)
	return agent.ProfileName != "" || agent.ProfileMode != ""
}

func flowV3HasWorkspaceInput(workspace flow.WorkspaceContext) bool {
	return workspace.WorkspacePath != "" || workspace.HostWorkspacePath != "" || workspace.RuntimeWorkspacePath != "" || workspace.CWD != "" || workspace.WorktreeMode != ""
}

func flowV3HasScheduleInput(schedule flow.ScheduleSpec) bool {
	return schedule.Cadence != "" || schedule.Time != "" || schedule.Weekday != "" || schedule.MonthDay != 0 || schedule.Timezone != ""
}

func flowV3HasCatchUpPolicyInput(policy flow.CatchUpPolicy) bool {
	return policy.Mode != "" || policy.MaxCatchUp != 0
}

func flowV3HasIntentInput(intent flow.PromptIntent) bool {
	return intent.Prompt != "" || intent.Mode != "" || len(intent.Tasks) > 0
}
