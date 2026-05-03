package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const flowV3Path = "/v3/flows"

type flowV3Definition struct {
	FlowID        string                `json:"flow_id"`
	Revision      int64                 `json:"revision"`
	Name          string                `json:"name"`
	Enabled       bool                  `json:"enabled"`
	Target        flow.TargetSelection  `json:"target"`
	Agent         flow.AgentSelection   `json:"agent"`
	Workspace     flow.WorkspaceContext `json:"workspace"`
	Schedule      flow.ScheduleSpec     `json:"schedule"`
	CatchUpPolicy flow.CatchUpPolicy    `json:"catch_up_policy"`
	Intent        flow.PromptIntent     `json:"intent"`
	NextDueAt     time.Time             `json:"next_due_at,omitempty"`
	CreatedAt     time.Time             `json:"created_at,omitempty"`
	UpdatedAt     time.Time             `json:"updated_at,omitempty"`
	DeletedAt     time.Time             `json:"deleted_at,omitempty"`
}

type flowV3RunNowView struct {
	CommandID   string `json:"command_id"`
	PendingSync bool   `json:"pending_sync"`
	Reason      string `json:"reason,omitempty"`
}

type flowV3WorkspaceDetail struct {
	WorkspacePath        string `json:"workspace_path"`
	HostWorkspacePath    string `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath string `json:"runtime_workspace_path,omitempty"`
	CWD                  string `json:"cwd,omitempty"`
	WorktreeMode         string `json:"worktree_mode,omitempty"`
}

type flowV3Record struct {
	Definition         flowV3Definition                         `json:"definition"`
	TargetDetail       *swarmTarget                             `json:"target_detail,omitempty"`
	AgentDetail        *pebblestore.AgentProfile                `json:"agent_detail,omitempty"`
	WorkspaceDetail    *flowV3WorkspaceDetail                   `json:"workspace_detail,omitempty"`
	AssignmentStatuses []pebblestore.FlowAssignmentStatusRecord `json:"assignment_statuses,omitempty"`
	LastRun            *pebblestore.FlowRunSummaryRecord        `json:"last_run,omitempty"`
	HistoryCount       int                                      `json:"history_count,omitempty"`
	History            []pebblestore.FlowRunSummaryRecord       `json:"history,omitempty"`
	Outbox             []pebblestore.FlowOutboxCommandRecord    `json:"outbox,omitempty"`
}

type flowV3RecordResponse struct {
	OK bool `json:"ok"`
	flowV3Record
}

type flowV3ListResponse struct {
	OK    bool           `json:"ok"`
	Flows []flowV3Record `json:"flows"`
}

type flowV3MutationResponse struct {
	OK     bool                         `json:"ok"`
	Flow   flowV3Record                 `json:"flow"`
	Result *flowAssignmentDeliverResult `json:"result,omitempty"`
	Run    *flowV3RunNowView            `json:"run,omitempty"`
}

type flowV3HistoryResponse struct {
	OK      bool                               `json:"ok"`
	FlowID  string                             `json:"flow_id"`
	History []pebblestore.FlowRunSummaryRecord `json:"history"`
}

type flowV3StatusResponse struct {
	OK                 bool                                     `json:"ok"`
	FlowID             string                                   `json:"flow_id"`
	AssignmentStatuses []pebblestore.FlowAssignmentStatusRecord `json:"assignment_statuses"`
	Outbox             []pebblestore.FlowOutboxCommandRecord    `json:"outbox"`
	History            []pebblestore.FlowRunSummaryRecord       `json:"history"`
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
	if len(parts) == 2 {
		switch parts[1] {
		case "history":
			s.handleFlowV3History(w, r, flowID)
			return
		case "status":
			s.handleFlowV3Status(w, r, flowID)
			return
		case "run-now":
			s.handleFlowV3RunNow(w, r, flowID)
			return
		}
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
			item, err := s.flowV3Summary(r, definition)
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
		record, result, err := s.createFlowV3(r, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, flowV3MutationResponse{OK: true, Flow: record, Result: &result})
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
		record, result, err := s.updateFlowV3(r, flowID, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, flowV3MutationResponse{OK: true, Flow: record, Result: &result})
	case http.MethodDelete:
		record, result, err := s.deleteFlowV3(r, flowID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, flowV3MutationResponse{OK: true, Flow: record, Result: &result})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleFlowV3History(w http.ResponseWriter, r *http.Request, flowID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit, err := positiveQueryLimit(r, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	history, err := s.flows.ListMirroredRunSummaries(flowID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, flowV3HistoryResponse{OK: true, FlowID: flowID, History: history})
}

func (s *Server) handleFlowV3Status(w http.ResponseWriter, r *http.Request, flowID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit, err := positiveQueryLimit(r, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	statuses, err := s.flows.ListAssignmentStatuses(flowID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	outbox, err := s.flows.ListOutboxCommands("", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	history, err := s.flows.ListMirroredRunSummaries(flowID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, flowV3StatusResponse{OK: true, FlowID: flowID, AssignmentStatuses: statuses, Outbox: flowV3OutboxForFlow(outbox, flowID), History: history})
}

func (s *Server) handleFlowV3RunNow(w http.ResponseWriter, r *http.Request, flowID string) {
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
	commandID := newFlowCommandID(flowID, definition.Revision, flow.CommandRunNow)
	command := flow.AssignmentCommand{CommandID: commandID, FlowID: definition.FlowID, Revision: definition.Revision, Action: flow.CommandRunNow, CreatedAt: time.Now().UTC(), Assignment: definition.Assignment}
	result, err := s.EnqueueAndDeliverFlowAssignmentCommand(r.Context(), command)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, _, detailErr := s.flowV3Detail(r, flowID)
	if detailErr != nil {
		writeError(w, http.StatusInternalServerError, detailErr)
		return
	}
	run := flowV3RunNowResultView(result)
	writeJSON(w, http.StatusAccepted, flowV3MutationResponse{OK: true, Flow: record, Result: &result, Run: &run})
}

func (s *Server) createFlowV3(r *http.Request, req flowV3UpsertRequest) (flowV3Record, flowAssignmentDeliverResult, error) {
	flowID := strings.TrimSpace(req.FlowID)
	if flowID == "" {
		flowID = "flow-" + randomHex(8)
	}
	if _, exists, err := s.flows.GetDefinition(flowID); err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	} else if exists {
		return flowV3Record{}, flowAssignmentDeliverResult{}, fmt.Errorf("flow %q already exists", flowID)
	}
	assignment, err := s.flowV3AssignmentFromRequest(r, req, nil, flowID, 1)
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	now := time.Now().UTC()
	nextDueAt, _, err := flow.NextFire(assignment, now)
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	definition, err := s.flows.PutDefinition(pebblestore.FlowDefinitionRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment, NextDueAt: nextDueAt})
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	command := flow.AssignmentCommand{CommandID: newFlowCommandID(definition.FlowID, definition.Revision, flow.CommandInstall), FlowID: definition.FlowID, Revision: definition.Revision, Action: flow.CommandInstall, CreatedAt: now, Assignment: definition.Assignment}
	result, err := s.createFlowV3InstallResult(r, command)
	if err != nil {
		if cleanupErr := s.flows.DeleteDefinition(definition.FlowID); cleanupErr != nil {
			return flowV3Record{}, flowAssignmentDeliverResult{}, fmt.Errorf("%w; cleanup flow definition: %v", err, cleanupErr)
		}
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	record, ok, err := s.flowV3Detail(r, definition.FlowID)
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	if !ok {
		return flowV3Record{}, flowAssignmentDeliverResult{}, fmt.Errorf("flow %q was not found after create", definition.FlowID)
	}
	return record, result, nil
}

func (s *Server) createFlowV3InstallResult(r *http.Request, command flow.AssignmentCommand) (flowAssignmentDeliverResult, error) {
	target, resolved, resolveErr := s.resolveFlowAssignmentTarget(r.Context(), command.Assignment.Target)
	if resolveErr != nil && strings.TrimSpace(resolved.SwarmID) == "" {
		return s.queuePendingFlowAssignmentCommand(command, resolveErr)
	}
	record, err := s.enqueueFlowAssignmentCommandForTarget(command, target, resolved)
	if err != nil {
		return flowAssignmentDeliverResult{}, err
	}
	return s.deliverFlowAssignmentOutboxCommand(r.Context(), record, target)
}

func (s *Server) queuePendingFlowAssignmentCommand(command flow.AssignmentCommand, deliverErr error) (flowAssignmentDeliverResult, error) {
	reason := strings.TrimSpace(firstNonEmpty(errString(deliverErr), "flow assignment delivery is pending"))
	selection := normalizeFlowTargetSelection(command.Assignment.Target)
	record, err := s.enqueueFlowAssignmentCommandForTarget(command, swarmTarget{}, flow.ResolvedTarget{
		Selection:    selection,
		SwarmID:      strings.TrimSpace(selection.SwarmID),
		Name:         strings.TrimSpace(selection.Name),
		Kind:         strings.TrimSpace(selection.Kind),
		DeploymentID: strings.TrimSpace(selection.DeploymentID),
		LastError:    reason,
	})
	if err != nil {
		return flowAssignmentDeliverResult{}, err
	}
	updated, state, err := s.markFlowAssignmentPending(record, flow.AssignmentTargetUnusable, reason, nil)
	if err != nil {
		return flowAssignmentDeliverResult{}, err
	}
	return flowAssignmentDeliverResult{Outbox: updated, AssignmentState: state, PendingSync: true}, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) updateFlowV3(r *http.Request, flowID string, req flowV3UpsertRequest) (flowV3Record, flowAssignmentDeliverResult, error) {
	existing, ok, err := s.flows.GetDefinition(flowID)
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	if !ok {
		return flowV3Record{}, flowAssignmentDeliverResult{}, fmt.Errorf("flow %q was not found", flowID)
	}
	assignment, err := s.flowV3AssignmentFromRequest(r, req, &existing.Assignment, flowID, existing.Revision+1)
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	now := time.Now().UTC()
	nextDueAt, _, err := flow.NextFire(assignment, now)
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	updatedDefinition, err := s.flows.PutDefinition(pebblestore.FlowDefinitionRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment, NextDueAt: nextDueAt, CreatedAt: existing.CreatedAt})
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	command := flow.AssignmentCommand{CommandID: newFlowCommandID(updatedDefinition.FlowID, updatedDefinition.Revision, flow.CommandUpdate), FlowID: updatedDefinition.FlowID, Revision: updatedDefinition.Revision, Action: flow.CommandUpdate, CreatedAt: now, Assignment: updatedDefinition.Assignment}
	result, err := s.EnqueueAndDeliverFlowAssignmentCommand(r.Context(), command)
	if err != nil {
		if _, restoreErr := s.flows.PutDefinition(existing); restoreErr != nil {
			return flowV3Record{}, flowAssignmentDeliverResult{}, fmt.Errorf("%w; restore previous flow definition: %v", err, restoreErr)
		}
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	record, ok, err := s.flowV3Detail(r, updatedDefinition.FlowID)
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	if !ok {
		return flowV3Record{}, flowAssignmentDeliverResult{}, fmt.Errorf("flow %q was not found after update", updatedDefinition.FlowID)
	}
	return record, result, nil
}

func (s *Server) deleteFlowV3(r *http.Request, flowID string) (flowV3Record, flowAssignmentDeliverResult, error) {
	definition, ok, err := s.flows.GetDefinition(flowID)
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	if !ok {
		return flowV3Record{}, flowAssignmentDeliverResult{}, fmt.Errorf("flow %q was not found", flowID)
	}
	deletedDefinition := definition
	deletedDefinition.DeletedAt = time.Now().UTC()
	command := flow.AssignmentCommand{CommandID: newFlowCommandID(definition.FlowID, definition.Revision, flow.CommandDelete), FlowID: definition.FlowID, Revision: definition.Revision, Action: flow.CommandDelete, CreatedAt: deletedDefinition.DeletedAt, Assignment: definition.Assignment}
	result, err := s.EnqueueAndDeliverFlowAssignmentCommand(r.Context(), command)
	if err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	if result.PendingSync {
		if _, err := s.flows.PutDefinition(deletedDefinition); err != nil {
			return flowV3Record{}, flowAssignmentDeliverResult{}, err
		}
		record, _, err := s.flowV3Detail(r, definition.FlowID)
		return record, result, err
	}
	if err := s.flows.DeleteDefinition(definition.FlowID); err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	if err := s.flows.DeleteAcceptedAssignment(definition.FlowID); err != nil {
		return flowV3Record{}, flowAssignmentDeliverResult{}, err
	}
	statuses, _ := s.flows.ListAssignmentStatuses(definition.FlowID, 100)
	outbox, _ := s.flows.ListOutboxCommands("", 100)
	history, _ := s.flows.ListMirroredRunSummaries(definition.FlowID, 100)
	record, err := s.flowV3RecordForDefinition(r, deletedDefinition, statuses, history, flowV3OutboxForFlow(outbox, definition.FlowID))
	return record, result, err
}

func (s *Server) flowV3Detail(r *http.Request, flowID string) (flowV3Record, bool, error) {
	definition, ok, err := s.flows.GetDefinition(flowID)
	if err != nil || !ok {
		return flowV3Record{}, ok, err
	}
	statuses, err := s.flows.ListAssignmentStatuses(flowID, 100)
	if err != nil {
		return flowV3Record{}, false, err
	}
	history, err := s.flows.ListMirroredRunSummaries(flowID, 100)
	if err != nil {
		return flowV3Record{}, false, err
	}
	outbox, err := s.flows.ListOutboxCommands("", 100)
	if err != nil {
		return flowV3Record{}, false, err
	}
	record, err := s.flowV3RecordForDefinition(r, definition, statuses, history, flowV3OutboxForFlow(outbox, flowID))
	return record, true, err
}

func (s *Server) flowV3Summary(r *http.Request, definition pebblestore.FlowDefinitionRecord) (flowV3Record, error) {
	statuses, err := s.flows.ListAssignmentStatuses(definition.FlowID, 20)
	if err != nil {
		return flowV3Record{}, err
	}
	history, err := s.flows.ListMirroredRunSummaries(definition.FlowID, 20)
	if err != nil {
		return flowV3Record{}, err
	}
	return s.flowV3RecordForDefinition(r, definition, statuses, history, nil)
}

func (s *Server) flowV3RecordForDefinition(r *http.Request, definition pebblestore.FlowDefinitionRecord, statuses []pebblestore.FlowAssignmentStatusRecord, history []pebblestore.FlowRunSummaryRecord, outbox []pebblestore.FlowOutboxCommandRecord) (flowV3Record, error) {
	targetDetail, err := s.flowV3TargetDetail(r, definition.Assignment.Target)
	if err != nil {
		return flowV3Record{}, err
	}
	agentDetail, err := s.flowV3AgentDetail(definition.Assignment.Agent)
	if err != nil {
		return flowV3Record{}, err
	}
	var lastRun *pebblestore.FlowRunSummaryRecord
	if len(history) > 0 {
		copy := history[0]
		lastRun = &copy
	}
	return flowV3Record{
		Definition: flowV3Definition{
			FlowID:        strings.TrimSpace(definition.Assignment.FlowID),
			Revision:      firstNonZeroInt64(definition.Revision, definition.Assignment.Revision),
			Name:          strings.TrimSpace(definition.Assignment.Name),
			Enabled:       definition.Assignment.Enabled,
			Target:        normalizeFlowTargetSelection(definition.Assignment.Target),
			Agent:         normalizeManagementAgentSelection(definition.Assignment.Agent),
			Workspace:     normalizeManagementWorkspace(definition.Assignment.Workspace),
			Schedule:      normalizeManagementSchedule(definition.Assignment.Schedule),
			CatchUpPolicy: flow.NormalizeCatchUpPolicy(definition.Assignment.CatchUpPolicy),
			Intent:        normalizeManagementIntent(definition.Assignment.Intent),
			NextDueAt:     definition.NextDueAt,
			CreatedAt:     definition.CreatedAt,
			UpdatedAt:     definition.UpdatedAt,
			DeletedAt:     definition.DeletedAt,
		},
		TargetDetail:       targetDetail,
		AgentDetail:        agentDetail,
		WorkspaceDetail:    flowV3WorkspaceDetailFromContext(definition.Assignment.Workspace),
		AssignmentStatuses: append([]pebblestore.FlowAssignmentStatusRecord(nil), statuses...),
		LastRun:            lastRun,
		HistoryCount:       len(history),
		History:            append([]pebblestore.FlowRunSummaryRecord(nil), history...),
		Outbox:             append([]pebblestore.FlowOutboxCommandRecord(nil), outbox...),
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

func flowV3OutboxForFlow(records []pebblestore.FlowOutboxCommandRecord, flowID string) []pebblestore.FlowOutboxCommandRecord {
	flowID = strings.TrimSpace(flowID)
	out := make([]pebblestore.FlowOutboxCommandRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.FlowID), flowID) {
			out = append(out, record)
		}
	}
	return out
}

func flowV3RunNowResultView(result flowAssignmentDeliverResult) flowV3RunNowView {
	return flowV3RunNowView{CommandID: strings.TrimSpace(result.Outbox.CommandID), PendingSync: result.PendingSync, Reason: firstNonEmpty(strings.TrimSpace(result.Ack.Reason), strings.TrimSpace(result.AssignmentState.Reason), strings.TrimSpace(result.Outbox.LastError))}
}

func flowV3WorkspaceDetailFromContext(workspace flow.WorkspaceContext) *flowV3WorkspaceDetail {
	workspace = normalizeManagementWorkspace(workspace)
	if workspace.WorkspacePath == "" && workspace.HostWorkspacePath == "" && workspace.RuntimeWorkspacePath == "" && workspace.CWD == "" {
		return nil
	}
	return &flowV3WorkspaceDetail{WorkspacePath: workspace.WorkspacePath, HostWorkspacePath: workspace.HostWorkspacePath, RuntimeWorkspacePath: workspace.RuntimeWorkspacePath, CWD: workspace.CWD, WorktreeMode: workspace.WorktreeMode}
}

func positiveQueryLimit(r *http.Request, defaultLimit int) (int, error) {
	limit := defaultLimit
	if r != nil && r.URL != nil {
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				return 0, errors.New("limit must be a positive integer")
			}
			limit = parsed
		}
	}
	return limit, nil
}

func normalizeManagementAgentSelection(agent flow.AgentSelection) flow.AgentSelection {
	agent.ProfileName = strings.TrimSpace(agent.ProfileName)
	agent.ProfileMode = flow.NormalizeAgentProfileMode(agent.ProfileMode)
	return flow.NormalizeAgentSelection(agent)
}

func normalizeManagementWorkspace(workspace flow.WorkspaceContext) flow.WorkspaceContext {
	workspace.WorkspacePath = strings.TrimSpace(workspace.WorkspacePath)
	workspace.HostWorkspacePath = strings.TrimSpace(workspace.HostWorkspacePath)
	workspace.RuntimeWorkspacePath = strings.TrimSpace(workspace.RuntimeWorkspacePath)
	workspace.CWD = strings.TrimSpace(workspace.CWD)
	workspace.WorktreeMode = strings.TrimSpace(workspace.WorktreeMode)
	return workspace
}

func normalizeManagementSchedule(schedule flow.ScheduleSpec) flow.ScheduleSpec {
	schedule.Cadence = flow.NormalizeCadence(schedule.Cadence)
	schedule.Time = strings.TrimSpace(schedule.Time)
	schedule.Weekday = strings.TrimSpace(schedule.Weekday)
	schedule.Timezone = strings.TrimSpace(schedule.Timezone)
	return schedule
}

func normalizeManagementIntent(intent flow.PromptIntent) flow.PromptIntent {
	intent.Prompt = strings.TrimSpace(intent.Prompt)
	intent.Mode = strings.TrimSpace(intent.Mode)
	for index := range intent.Tasks {
		intent.Tasks[index].ID = strings.TrimSpace(intent.Tasks[index].ID)
		intent.Tasks[index].Title = strings.TrimSpace(intent.Tasks[index].Title)
		intent.Tasks[index].Detail = strings.TrimSpace(intent.Tasks[index].Detail)
		intent.Tasks[index].Action = strings.TrimSpace(intent.Tasks[index].Action)
	}
	return intent
}

func newFlowCommandID(flowID string, revision int64, action flow.CommandAction) string {
	return fmt.Sprintf("%s-%d-%s-%s", strings.TrimSpace(flowID), revision, strings.TrimSpace(string(action)), randomHex(6))
}

func randomHex(n int) string {
	if n <= 0 {
		n = 8
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
