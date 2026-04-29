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

const flowManagementPath = "/v1/flows"

type flowManagementListResponse struct {
	OK    bool                    `json:"ok"`
	Flows []flowManagementSummary `json:"flows"`
}

type flowManagementGetResponse struct {
	OK   bool                 `json:"ok"`
	Flow flowManagementDetail `json:"flow"`
}

type flowManagementMutationResponse struct {
	OK     bool                            `json:"ok"`
	Flow   flowManagementDetail            `json:"flow"`
	Result *flowAssignmentDeliverResult    `json:"result,omitempty"`
	Run    *flowManagementRunNowResultView `json:"run,omitempty"`
}

type flowManagementHistoryResponse struct {
	OK      bool                               `json:"ok"`
	FlowID  string                             `json:"flow_id"`
	History []pebblestore.FlowRunSummaryRecord `json:"history"`
}

type flowManagementStatusResponse struct {
	OK                 bool                                     `json:"ok"`
	FlowID             string                                   `json:"flow_id"`
	AssignmentStatuses []pebblestore.FlowAssignmentStatusRecord `json:"assignment_statuses"`
	Outbox             []pebblestore.FlowOutboxCommandRecord    `json:"outbox"`
	History            []pebblestore.FlowRunSummaryRecord       `json:"history"`
}

type flowManagementSummary struct {
	Definition         pebblestore.FlowDefinitionRecord         `json:"definition"`
	AssignmentStatuses []pebblestore.FlowAssignmentStatusRecord `json:"assignment_statuses,omitempty"`
	LastRun            *pebblestore.FlowRunSummaryRecord        `json:"last_run,omitempty"`
	HistoryCount       int                                      `json:"history_count"`
}

type flowManagementDetail struct {
	Definition         pebblestore.FlowDefinitionRecord         `json:"definition"`
	AssignmentStatuses []pebblestore.FlowAssignmentStatusRecord `json:"assignment_statuses"`
	Outbox             []pebblestore.FlowOutboxCommandRecord    `json:"outbox"`
	History            []pebblestore.FlowRunSummaryRecord       `json:"history"`
}

type flowManagementCreateRequest struct {
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

type flowManagementRunNowResponse struct {
	OK     bool                           `json:"ok"`
	Result flowAssignmentDeliverResult    `json:"result"`
	Run    flowManagementRunNowResultView `json:"run"`
}

type flowManagementRunNowResultView struct {
	CommandID   string `json:"command_id"`
	PendingSync bool   `json:"pending_sync"`
	Reason      string `json:"reason,omitempty"`
}

func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, http.StatusInternalServerError, errors.New("flow store is not configured"))
		return
	}
	path := strings.Trim(strings.TrimPrefix(strings.TrimSpace(r.URL.Path), flowManagementPath), "/")
	if path == "" {
		s.handleFlowsCollection(w, r)
		return
	}
	parts := strings.Split(path, "/")
	flowID := strings.TrimSpace(parts[0])
	if flowID == "" {
		writeError(w, http.StatusBadRequest, errors.New("flow_id is required"))
		return
	}
	if len(parts) == 1 {
		s.handleFlowByID(w, r, flowID)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "history":
			s.handleFlowHistory(w, r, flowID)
			return
		case "status":
			s.handleFlowStatus(w, r, flowID)
			return
		case "run-now":
			s.handleFlowRunNow(w, r, flowID)
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("unsupported flow path %q", r.URL.Path))
}

func (s *Server) handleFlowsCollection(w http.ResponseWriter, r *http.Request) {
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
		items := make([]flowManagementSummary, 0, len(definitions))
		for _, definition := range definitions {
			summary, err := s.flowSummary(definition)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			items = append(items, summary)
		}
		writeJSON(w, http.StatusOK, flowManagementListResponse{OK: true, Flows: items})
	case http.MethodPost:
		var req flowManagementCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		detail, result, err := s.createFlowFromManagementRequest(r, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, flowManagementMutationResponse{OK: true, Flow: detail, Result: &result})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleFlowByID(w http.ResponseWriter, r *http.Request, flowID string) {
	switch r.Method {
	case http.MethodGet:
		detail, ok, err := s.flowDetail(flowID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("flow %q was not found", flowID))
			return
		}
		writeJSON(w, http.StatusOK, flowManagementGetResponse{OK: true, Flow: detail})
	case http.MethodDelete:
		detail, result, err := s.deleteFlow(r, flowID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, flowManagementMutationResponse{OK: true, Flow: detail, Result: &result})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleFlowHistory(w http.ResponseWriter, r *http.Request, flowID string) {
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
	writeJSON(w, http.StatusOK, flowManagementHistoryResponse{OK: true, FlowID: flowID, History: history})
}

func (s *Server) handleFlowStatus(w http.ResponseWriter, r *http.Request, flowID string) {
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
	writeJSON(w, http.StatusOK, flowManagementStatusResponse{OK: true, FlowID: flowID, AssignmentStatuses: statuses, Outbox: flowOutboxForFlow(outbox, flowID), History: history})
}

func (s *Server) handleFlowRunNow(w http.ResponseWriter, r *http.Request, flowID string) {
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
	writeJSON(w, http.StatusAccepted, flowManagementRunNowResponse{OK: true, Result: result, Run: flowRunNowResultView(result)})
}

func (s *Server) createFlowFromManagementRequest(r *http.Request, req flowManagementCreateRequest) (flowManagementDetail, flowAssignmentDeliverResult, error) {
	assignment, err := s.assignmentFromManagementRequest(r, req)
	if err != nil {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
	}
	if _, exists, err := s.flows.GetDefinition(assignment.FlowID); err != nil {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
	} else if exists {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, fmt.Errorf("flow %q already exists", assignment.FlowID)
	}
	now := time.Now().UTC()
	nextDueAt, _, err := flow.NextFire(assignment, now)
	if err != nil {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
	}
	definition, err := s.flows.PutDefinition(pebblestore.FlowDefinitionRecord{FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment, NextDueAt: nextDueAt})
	if err != nil {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
	}
	command := flow.AssignmentCommand{CommandID: newFlowCommandID(definition.FlowID, definition.Revision, flow.CommandInstall), FlowID: definition.FlowID, Revision: definition.Revision, Action: flow.CommandInstall, CreatedAt: now, Assignment: definition.Assignment}
	result, err := s.EnqueueAndDeliverFlowAssignmentCommand(r.Context(), command)
	if err != nil {
		if cleanupErr := s.flows.DeleteDefinition(definition.FlowID); cleanupErr != nil {
			return flowManagementDetail{}, flowAssignmentDeliverResult{}, fmt.Errorf("%w; cleanup flow definition: %v", err, cleanupErr)
		}
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
	}
	detail, ok, err := s.flowDetail(definition.FlowID)
	if err != nil {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
	}
	if !ok {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, fmt.Errorf("flow %q was not found after create", definition.FlowID)
	}
	return detail, result, nil
}

func (s *Server) deleteFlow(r *http.Request, flowID string) (flowManagementDetail, flowAssignmentDeliverResult, error) {
	definition, ok, err := s.flows.GetDefinition(flowID)
	if err != nil {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
	}
	if !ok {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, fmt.Errorf("flow %q was not found", flowID)
	}
	deletedDefinition := definition
	deletedDefinition.DeletedAt = time.Now().UTC()
	command := flow.AssignmentCommand{CommandID: newFlowCommandID(definition.FlowID, definition.Revision, flow.CommandDelete), FlowID: definition.FlowID, Revision: definition.Revision, Action: flow.CommandDelete, CreatedAt: deletedDefinition.DeletedAt, Assignment: definition.Assignment}
	result, err := s.EnqueueAndDeliverFlowAssignmentCommand(r.Context(), command)
	if err != nil {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
	}
	if result.PendingSync {
		if _, err := s.flows.PutDefinition(deletedDefinition); err != nil {
			return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
		}
		detail, _, err := s.flowDetail(definition.FlowID)
		return detail, result, err
	}
	if err := s.flows.DeleteDefinition(definition.FlowID); err != nil {
		return flowManagementDetail{}, flowAssignmentDeliverResult{}, err
	}
	statuses, _ := s.flows.ListAssignmentStatuses(definition.FlowID, 100)
	outbox, _ := s.flows.ListOutboxCommands("", 100)
	history, _ := s.flows.ListMirroredRunSummaries(definition.FlowID, 100)
	return flowManagementDetail{Definition: deletedDefinition, AssignmentStatuses: statuses, Outbox: flowOutboxForFlow(outbox, definition.FlowID), History: history}, result, nil
}

func (s *Server) assignmentFromManagementRequest(r *http.Request, req flowManagementCreateRequest) (flow.Assignment, error) {
	flowID := strings.TrimSpace(req.FlowID)
	if flowID == "" {
		flowID = "flow-" + randomHex(8)
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	assignment := flow.Assignment{
		FlowID:        flowID,
		Revision:      1,
		Name:          strings.TrimSpace(req.Name),
		Enabled:       enabled,
		Target:        normalizeFlowTargetSelection(req.Target),
		Agent:         normalizeManagementAgentSelection(req.Agent),
		Workspace:     normalizeManagementWorkspace(req.Workspace),
		Schedule:      normalizeManagementSchedule(req.Schedule),
		CatchUpPolicy: flow.NormalizeCatchUpPolicy(req.CatchUpPolicy),
		Intent:        normalizeManagementIntent(req.Intent),
	}
	if assignment.Name == "" {
		assignment.Name = assignment.FlowID
	}
	if assignment.Target.SwarmID == "" && assignment.Target.Kind == "" && assignment.Target.Name == "" && assignment.Target.DeploymentID == "" {
		assignment.Target = currentFlowTargetSelection(r)
	}
	if assignment.Agent.TargetKind == "" {
		assignment.Agent.TargetKind = "background"
	}
	if assignment.Agent.TargetName == "" {
		assignment.Agent.TargetName = "memory"
	}
	if assignment.Workspace.WorkspacePath == "" || assignment.Workspace.WorkspacePath == "." {
		if s.workspace != nil {
			if current, ok, err := s.workspace.CurrentBinding(); err != nil {
				return flow.Assignment{}, err
			} else if ok && strings.TrimSpace(current.ResolvedPath) != "" {
				assignment.Workspace.WorkspacePath = strings.TrimSpace(current.ResolvedPath)
			}
		}
		if assignment.Workspace.WorkspacePath == "" {
			return flow.Assignment{}, errors.New("workspace_path is required")
		}
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
	if err := flow.ValidateAssignment(assignment); err != nil {
		return flow.Assignment{}, err
	}
	return assignment, nil
}

func (s *Server) flowSummary(definition pebblestore.FlowDefinitionRecord) (flowManagementSummary, error) {
	statuses, err := s.flows.ListAssignmentStatuses(definition.FlowID, 20)
	if err != nil {
		return flowManagementSummary{}, err
	}
	history, err := s.flows.ListMirroredRunSummaries(definition.FlowID, 20)
	if err != nil {
		return flowManagementSummary{}, err
	}
	var lastRun *pebblestore.FlowRunSummaryRecord
	if len(history) > 0 {
		copy := history[0]
		lastRun = &copy
	}
	return flowManagementSummary{Definition: definition, AssignmentStatuses: statuses, LastRun: lastRun, HistoryCount: len(history)}, nil
}

func (s *Server) flowDetail(flowID string) (flowManagementDetail, bool, error) {
	definition, ok, err := s.flows.GetDefinition(flowID)
	if err != nil || !ok {
		return flowManagementDetail{}, ok, err
	}
	statuses, err := s.flows.ListAssignmentStatuses(flowID, 100)
	if err != nil {
		return flowManagementDetail{}, false, err
	}
	outbox, err := s.flows.ListOutboxCommands("", 100)
	if err != nil {
		return flowManagementDetail{}, false, err
	}
	history, err := s.flows.ListMirroredRunSummaries(flowID, 100)
	if err != nil {
		return flowManagementDetail{}, false, err
	}
	return flowManagementDetail{Definition: definition, AssignmentStatuses: statuses, Outbox: flowOutboxForFlow(outbox, flowID), History: history}, true, nil
}

func flowOutboxForFlow(records []pebblestore.FlowOutboxCommandRecord, flowID string) []pebblestore.FlowOutboxCommandRecord {
	flowID = strings.TrimSpace(flowID)
	out := make([]pebblestore.FlowOutboxCommandRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.FlowID), flowID) {
			out = append(out, record)
		}
	}
	return out
}

func flowRunNowResultView(result flowAssignmentDeliverResult) flowManagementRunNowResultView {
	return flowManagementRunNowResultView{CommandID: strings.TrimSpace(result.Outbox.CommandID), PendingSync: result.PendingSync, Reason: firstNonEmpty(strings.TrimSpace(result.Ack.Reason), strings.TrimSpace(result.AssignmentState.Reason), strings.TrimSpace(result.Outbox.LastError))}
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

func currentFlowTargetSelection(r *http.Request) flow.TargetSelection {
	selection := flow.TargetSelection{Kind: "self"}
	if r != nil && r.URL != nil {
		if swarmID := strings.TrimSpace(r.URL.Query().Get("swarm_id")); swarmID != "" {
			selection.SwarmID = swarmID
		}
	}
	return selection
}

func normalizeManagementAgentSelection(agent flow.AgentSelection) flow.AgentSelection {
	agent.TargetKind = strings.TrimSpace(agent.TargetKind)
	agent.TargetName = strings.TrimSpace(agent.TargetName)
	return agent
}

func normalizeManagementWorkspace(workspace flow.WorkspaceContext) flow.WorkspaceContext {
	workspace.WorkspacePath = strings.TrimSpace(workspace.WorkspacePath)
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
