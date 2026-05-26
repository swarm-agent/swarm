package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const (
	manageFlowDefaultLimit = 100
	manageFlowMaxLimit     = 500
)

type manageFlowService interface {
	PutDefinition(record pebblestore.FlowDefinitionRecord) (pebblestore.FlowDefinitionRecord, error)
	GetDefinition(flowID string) (pebblestore.FlowDefinitionRecord, bool, error)
	ListDefinitions(limit int) ([]pebblestore.FlowDefinitionRecord, error)
	DeleteDefinition(flowID string) error
	PutAcceptedAssignment(record flow.AcceptedAssignment) (flow.AcceptedAssignment, error)
	GetAcceptedAssignment(flowID string) (flow.AcceptedAssignment, bool, error)
	ListAcceptedAssignments(limit int) ([]flow.AcceptedAssignment, error)
	DeleteAcceptedAssignment(flowID string) error
	PutDue(record pebblestore.FlowDueRecord) (pebblestore.FlowDueRecord, error)
	DeleteDue(record pebblestore.FlowDueRecord) error
	ListMirroredRunSummaries(flowID string, limit int) ([]pebblestore.FlowRunSummaryRecord, error)
	ListAssignmentStatuses(flowID string, limit int) ([]pebblestore.FlowAssignmentStatusRecord, error)
	ListOutboxCommands(status string, limit int) ([]pebblestore.FlowOutboxCommandRecord, error)
}

type manageFlowWorkspaceService interface {
	CurrentBinding() (manageFlowWorkspaceResolution, bool, error)
	ScopeForPath(path string) (manageFlowWorkspaceScope, error)
	ListKnown(limit int) ([]manageFlowWorkspaceEntry, error)
}

type manageFlowWorkspaceResolution = workspaceruntime.Resolution

type manageFlowWorkspaceScope = workspaceruntime.Scope

type manageFlowWorkspaceEntry = workspaceruntime.Entry

func (r *Runtime) SetManageFlowServices(flows manageFlowService, workspace manageFlowWorkspaceService) {
	if r == nil {
		return
	}
	r.flows = flows
	r.flowWorkspace = workspace
}

func (r *Runtime) executeManageFlow(scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "" {
		action = "inspect"
	}
	confirm := asBool(args["confirm"])
	switch action {
	case "inspect", "list":
		return r.manageFlowInspect(scope, args)
	case "get", "read":
		return r.manageFlowGet(args)
	case "history":
		return r.manageFlowHistory(args)
	case "status":
		return r.manageFlowStatus(args)
	case "create":
		return r.manageFlowUpsert(scope, args, false, confirm)
	case "update":
		return r.manageFlowUpsert(scope, args, true, confirm)
	case "delete", "remove":
		return r.manageFlowDelete(args, confirm)
	default:
		return "", fmt.Errorf("manage-flow action %q is unsupported", action)
	}
}

func (r *Runtime) manageFlowInspect(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.flows == nil {
		return "", errors.New("manage-flow service is not configured")
	}
	limit := clampManageFlowLimit(asInt(args["limit"], manageFlowDefaultLimit))
	definitions, err := r.flows.ListDefinitions(limit)
	if err != nil {
		return "", fmt.Errorf("manage-flow inspect definitions failed: %w", err)
	}
	accepted, err := r.flows.ListAcceptedAssignments(limit)
	if err != nil {
		return "", fmt.Errorf("manage-flow inspect accepted assignments failed: %w", err)
	}
	items := r.manageFlowMergedRecords(definitions, accepted)
	if len(items) > limit {
		items = items[:limit]
	}
	workspaceSummary := r.manageFlowWorkspaceSummary(scope)
	agentInventory := r.manageFlowAgentInventory(limit)
	response := map[string]any{
		"status":            "ok",
		"action":            "inspect",
		"flows":             items,
		"count":             len(items),
		"limit":             limit,
		"workspace":         workspaceSummary,
		"available_agents":  agentInventory,
		"supported_actions": []string{"inspect", "list", "get", "history", "status", "create", "update", "delete"},
		"instructions":      "Use manage-flow to inspect and manage Flows. A Flow is a user-configured background task run by a saved agent profile on a schedule. Call inspect/list first in a conversation after the user states a flow request so you can see existing flows, compact available_agents, schedules, workspace context, and exact required fields without needing manage-agent just to choose a profile. Use manage-agent get only when you need full agent prompt/tool details before configuring a flow. Read-only actions inspect/list/get/history/status do not need approval. Mutating actions create/update/delete return approval-ready previews unless confirm=true after user approval. Be specific: include flow name, saved agent profile_name/profile_mode, workspace_path, target selection, schedule cadence/time/times/weekday/month_day/timezone/cron, catch_up_policy, and the exact prompt/tasks the background agent will run.",
		"examples": []map[string]any{
			{"action": "inspect"},
			{"action": "create", "content": map[string]any{"name": "Daily AGENTS.md memory refresh", "agent": map[string]any{"profile_name": "memory", "profile_mode": "background"}, "target": map[string]any{"kind": "self"}, "workspace": map[string]any{"workspace_path": workspaceSummary["workspace_path"]}, "schedule": map[string]any{"cadence": "daily", "time": "09:00", "timezone": "UTC"}, "intent": map[string]any{"prompt": "Check the last day's git diffs and update AGENTS.md with durable agent guidance when needed."}}},
			{"action": "get", "flow_id": "flow_..."},
		},
		"path_id":              toolPathID("manage-flow"),
		"summary":              fmt.Sprintf("found %d flows", len(items)),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(""),
	}
	return encodeManageFlowResponse(response)
}

func (r *Runtime) manageFlowGet(args map[string]any) (string, error) {
	record, ok, err := r.manageFlowDefinitionOrAccepted(manageFlowIDFromArgs(args))
	if err != nil || !ok {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("flow %q was not found", manageFlowIDFromArgs(args))
	}
	view, err := r.manageFlowRecordMap(record, true)
	if err != nil {
		return "", err
	}
	return encodeManageFlowResponse(map[string]any{
		"status":               "ok",
		"action":               "get",
		"flow":                 view,
		"path_id":              toolPathID("manage-flow"),
		"summary":              fmt.Sprintf("loaded flow %s", record.FlowID),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(record.Assignment.Intent.Prompt),
	})
}

func (r *Runtime) manageFlowHistory(args map[string]any) (string, error) {
	if r == nil || r.flows == nil {
		return "", errors.New("manage-flow service is not configured")
	}
	flowID := manageFlowIDFromArgs(args)
	if flowID == "" {
		return "", errors.New("flow_id is required")
	}
	limit := clampManageFlowLimit(asInt(args["limit"], 100))
	history, err := r.flows.ListMirroredRunSummaries(flowID, limit)
	if err != nil {
		return "", err
	}
	return encodeManageFlowResponse(map[string]any{"status": "ok", "action": "history", "flow_id": flowID, "history": history, "count": len(history), "path_id": toolPathID("manage-flow"), "summary": fmt.Sprintf("returned %d history records for %s", len(history), flowID), "details_truncated": false, "prompt_injection_tag": "tool_output_untrusted", "safety": buildUntrustedSafety("")})
}

func (r *Runtime) manageFlowStatus(args map[string]any) (string, error) {
	if r == nil || r.flows == nil {
		return "", errors.New("manage-flow service is not configured")
	}
	flowID := manageFlowIDFromArgs(args)
	if flowID == "" {
		return "", errors.New("flow_id is required")
	}
	limit := clampManageFlowLimit(asInt(args["limit"], 100))
	statuses, err := r.flows.ListAssignmentStatuses(flowID, limit)
	if err != nil {
		return "", err
	}
	outbox, err := r.flows.ListOutboxCommands("", limit)
	if err != nil {
		return "", err
	}
	history, err := r.flows.ListMirroredRunSummaries(flowID, limit)
	if err != nil {
		return "", err
	}
	return encodeManageFlowResponse(map[string]any{"status": "ok", "action": "status", "flow_id": flowID, "assignment_statuses": statuses, "outbox": manageFlowOutboxForFlow(outbox, flowID), "history": history, "path_id": toolPathID("manage-flow"), "summary": fmt.Sprintf("loaded flow status for %s", flowID), "details_truncated": false, "prompt_injection_tag": "tool_output_untrusted", "safety": buildUntrustedSafety("")})
}

func (r *Runtime) manageFlowUpsert(scope WorkspaceScope, args map[string]any, mustExist, confirm bool) (string, error) {
	if r == nil || r.flows == nil {
		return "", errors.New("manage-flow service is not configured")
	}
	flowID := strings.TrimSpace(firstNonEmptyString(asString(args["flow_id"]), asString(args["id"])))
	content, err := manageFlowContentMap(args)
	if err != nil {
		return "", err
	}
	if candidate := strings.TrimSpace(firstNonEmptyString(mapString(content, "flow_id"), mapString(content, "id"))); candidate != "" {
		flowID = candidate
	}
	if flowID == "" {
		flowID = manageFlowGeneratedID(mapString(content, "name"))
	}
	existing, exists, err := r.manageFlowDefinitionOrAccepted(flowID)
	if err != nil {
		return "", err
	}
	if mustExist && !exists {
		return "", fmt.Errorf("flow %q does not exist", flowID)
	}
	if !mustExist && exists {
		return "", fmt.Errorf("flow %q already exists; use update", flowID)
	}
	var base *flow.Assignment
	createdAt := time.Time{}
	if exists {
		baseAssignment := existing.Assignment
		base = &baseAssignment
		createdAt = existing.CreatedAt
	}
	assignment, err := r.manageFlowAssignmentFromContent(scope, content, base, flowID, firstNonZeroInt64(existing.Revision+1, 1))
	if err != nil {
		return "", err
	}
	nextDueAt, _, err := flow.NextFire(assignment, time.Now().UTC())
	if err != nil {
		return "", err
	}
	proposed := pebblestore.FlowDefinitionRecord{AccountScopeID: scope.Principal.AccountScopeID, UserID: scope.Principal.UserID, FlowID: flowID, Revision: assignment.Revision, Assignment: assignment, NextDueAt: nextDueAt, CreatedAt: createdAt}
	before := any(nil)
	if exists {
		before, _ = r.manageFlowRecordMap(existing, true)
	}
	after, err := r.manageFlowRecordMap(proposed, true)
	if err != nil {
		return "", err
	}
	action := "create"
	status := "proposed_create"
	summary := fmt.Sprintf("proposed new flow %s", flowID)
	if exists {
		action = "update"
		status = "proposed_update"
		summary = fmt.Sprintf("proposed update for flow %s", flowID)
	}
	change := map[string]any{"kind": "flow_change", "target": "flow", "operation": action, "before": before, "after": after, "approval_summary": manageFlowApprovalSummary(action, after)}
	if confirm {
		applied, err := r.manageFlowApplyDefinition(proposed)
		if err != nil {
			return "", err
		}
		appliedMap, err := r.manageFlowRecordMap(applied, true)
		if err != nil {
			return "", err
		}
		return encodeManageFlowResponse(map[string]any{"status": "ok", "action": action, "applied": true, "flow": appliedMap, "change": change, "path_id": toolPathID("manage-flow"), "summary": fmt.Sprintf("applied %s for flow %s", action, flowID), "details_truncated": false, "prompt_injection_tag": "tool_output_untrusted", "safety": buildUntrustedSafety(assignment.Intent.Prompt)})
	}
	return encodeManageFlowResponse(map[string]any{"status": status, "action": action, "applied": false, "flow": after, "change": change, "approved_arguments": manageFlowApprovedArguments(args), "path_id": toolPathID("manage-flow"), "summary": summary, "details_truncated": false, "prompt_injection_tag": "tool_output_untrusted", "safety": buildUntrustedSafety(assignment.Intent.Prompt)})
}

func (r *Runtime) manageFlowDelete(args map[string]any, confirm bool) (string, error) {
	if r == nil || r.flows == nil {
		return "", errors.New("manage-flow service is not configured")
	}
	flowID := manageFlowIDFromArgs(args)
	if flowID == "" {
		return "", errors.New("flow_id is required")
	}
	existing, ok, err := r.manageFlowDefinitionOrAccepted(flowID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("flow %q was not found", flowID)
	}
	before, err := r.manageFlowRecordMap(existing, true)
	if err != nil {
		return "", err
	}
	change := map[string]any{"kind": "flow_change", "target": "flow", "operation": "delete", "before": before, "after": nil, "approval_summary": manageFlowApprovalSummary("delete", before)}
	if confirm {
		if err := r.flows.DeleteDefinition(flowID); err != nil {
			return "", err
		}
		if err := r.flows.DeleteAcceptedAssignment(flowID); err != nil {
			return "", err
		}
		_ = r.flows.DeleteDue(pebblestore.FlowDueRecord{AccountScopeID: existing.AccountScopeID, UserID: existing.UserID, FlowID: flowID, Revision: existing.Revision, DueAt: existing.NextDueAt, ScheduledAt: existing.NextDueAt})
		return encodeManageFlowResponse(map[string]any{"status": "ok", "action": "delete", "applied": true, "flow_id": flowID, "change": change, "path_id": toolPathID("manage-flow"), "summary": fmt.Sprintf("applied delete for flow %s", flowID), "details_truncated": false, "prompt_injection_tag": "tool_output_untrusted", "safety": buildUntrustedSafety("")})
	}
	return encodeManageFlowResponse(map[string]any{"status": "proposed_delete", "action": "delete", "applied": false, "flow": before, "change": change, "approved_arguments": manageFlowApprovedArguments(args), "path_id": toolPathID("manage-flow"), "summary": fmt.Sprintf("proposed delete for flow %s", flowID), "details_truncated": false, "prompt_injection_tag": "tool_output_untrusted", "safety": buildUntrustedSafety(existing.Assignment.Intent.Prompt)})
}

func (r *Runtime) manageFlowApplyDefinition(record pebblestore.FlowDefinitionRecord) (pebblestore.FlowDefinitionRecord, error) {
	applied, err := r.flows.PutDefinition(record)
	if err != nil {
		return pebblestore.FlowDefinitionRecord{}, err
	}
	accepted := flow.AcceptedAssignment{AccountScopeID: applied.AccountScopeID, UserID: applied.UserID, Assignment: applied.Assignment, AcceptedAt: time.Now().UTC()}
	if _, err := r.flows.PutAcceptedAssignment(accepted); err != nil {
		return pebblestore.FlowDefinitionRecord{}, err
	}
	if !applied.NextDueAt.IsZero() {
		if _, err := r.flows.PutDue(pebblestore.FlowDueRecord{AccountScopeID: applied.AccountScopeID, UserID: applied.UserID, FlowID: applied.FlowID, Revision: applied.Revision, DueAt: applied.NextDueAt, ScheduledAt: applied.NextDueAt}); err != nil {
			return pebblestore.FlowDefinitionRecord{}, err
		}
	}
	return applied, nil
}

func (r *Runtime) manageFlowAssignmentFromContent(scope WorkspaceScope, content map[string]any, base *flow.Assignment, flowID string, revision int64) (flow.Assignment, error) {
	if revision <= 0 {
		revision = 1
	}
	name := strings.TrimSpace(mapString(content, "name"))
	if name == "" && base != nil {
		name = base.Name
	}
	if name == "" {
		name = flowID
	}
	enabled := true
	if base != nil {
		enabled = base.Enabled
	}
	if raw, ok := content["enabled"]; ok {
		enabled = asBool(raw)
	}
	target := manageFlowTargetFromContent(content["target"])
	if !manageFlowHasTarget(target) && base != nil {
		target = base.Target
	}
	if !manageFlowHasTarget(target) {
		target = flow.TargetSelection{Kind: "self"}
	}
	agent := manageFlowAgentFromContent(content["agent"])
	if !manageFlowHasAgent(agent) {
		agent = flow.AgentSelection{ProfileName: firstNonEmptyString(mapString(content, "profile_name"), mapString(content, "agent")), ProfileMode: firstNonEmptyString(mapString(content, "profile_mode"), mapString(content, "agent_mode"))}
	}
	if !manageFlowHasAgent(agent) && base != nil {
		agent = base.Agent
	}
	agent = flow.NormalizeAgentSelection(agent)
	if strings.TrimSpace(agent.ProfileName) == "" {
		return flow.Assignment{}, errors.New("agent.profile_name is required")
	}
	if strings.TrimSpace(agent.ProfileMode) == "" {
		agent.ProfileMode = "background"
		agent = flow.NormalizeAgentSelection(agent)
	}
	workspace := manageFlowWorkspaceFromContent(content["workspace"])
	if !manageFlowHasWorkspace(workspace) {
		workspace.WorkspacePath = strings.TrimSpace(firstNonEmptyString(mapString(content, "workspace_path"), scope.PrimaryPath))
	}
	if !manageFlowHasWorkspace(workspace) && base != nil {
		workspace = base.Workspace
	}
	if strings.TrimSpace(workspace.WorkspacePath) == "" {
		workspace.WorkspacePath = strings.TrimSpace(r.manageFlowWorkspaceSummary(scope)["workspace_path"])
	}
	schedule := manageFlowScheduleFromContent(content["schedule"])
	if !manageFlowHasSchedule(schedule) && base != nil {
		schedule = base.Schedule
	}
	if strings.TrimSpace(schedule.Cadence) == "" {
		schedule.Cadence = flow.CadenceOnDemand
	}
	catchUp := manageFlowCatchUpPolicyFromContent(content["catch_up_policy"])
	if !manageFlowHasCatchUp(catchUp) && base != nil {
		catchUp = base.CatchUpPolicy
	}
	if strings.TrimSpace(catchUp.Mode) == "" {
		catchUp.Mode = flow.CatchUpOnce
	}
	intent := manageFlowIntentFromContent(content["intent"])
	if strings.TrimSpace(intent.Prompt) == "" {
		intent.Prompt = strings.TrimSpace(firstNonEmptyString(mapString(content, "prompt"), mapString(content, "instructions")))
	}
	if !manageFlowHasIntent(intent) && base != nil {
		intent = base.Intent
	}
	assignment := flow.Assignment{FlowID: flowID, Revision: revision, Name: name, Enabled: enabled, Target: target, Agent: agent, Workspace: workspace, Schedule: schedule, CatchUpPolicy: catchUp, Intent: intent}
	if err := flow.ValidateAssignment(assignment); err != nil {
		return flow.Assignment{}, err
	}
	return assignment, nil
}

func (r *Runtime) manageFlowDefinitionOrAccepted(flowID string) (pebblestore.FlowDefinitionRecord, bool, error) {
	if r == nil || r.flows == nil {
		return pebblestore.FlowDefinitionRecord{}, false, errors.New("manage-flow service is not configured")
	}
	flowID = strings.TrimSpace(flowID)
	if flowID == "" {
		return pebblestore.FlowDefinitionRecord{}, false, errors.New("flow_id is required")
	}
	definition, ok, err := r.flows.GetDefinition(flowID)
	if err != nil || ok {
		return definition, ok, err
	}
	accepted, ok, err := r.flows.GetAcceptedAssignment(flowID)
	if err != nil || !ok {
		return pebblestore.FlowDefinitionRecord{}, false, err
	}
	return pebblestore.FlowDefinitionRecord{AccountScopeID: accepted.AccountScopeID, UserID: accepted.UserID, FlowID: accepted.FlowID, Revision: accepted.Revision, Assignment: accepted.Assignment, CreatedAt: accepted.AcceptedAt, UpdatedAt: accepted.AcceptedAt}, true, nil
}

func (r *Runtime) manageFlowMergedRecords(definitions []pebblestore.FlowDefinitionRecord, accepted []flow.AcceptedAssignment) []map[string]any {
	byID := make(map[string]pebblestore.FlowDefinitionRecord, len(definitions)+len(accepted))
	for _, definition := range definitions {
		if definition.FlowID != "" {
			byID[definition.FlowID] = definition
		}
	}
	for _, assignment := range accepted {
		if assignment.FlowID == "" {
			continue
		}
		if _, exists := byID[assignment.FlowID]; !exists {
			byID[assignment.FlowID] = pebblestore.FlowDefinitionRecord{AccountScopeID: assignment.AccountScopeID, UserID: assignment.UserID, FlowID: assignment.FlowID, Revision: assignment.Revision, Assignment: assignment.Assignment, CreatedAt: assignment.AcceptedAt, UpdatedAt: assignment.AcceptedAt}
		}
	}
	out := make([]map[string]any, 0, len(byID))
	for _, record := range byID {
		view, err := r.manageFlowRecordMap(record, false)
		if err == nil {
			out = append(out, view)
		}
	}
	return out
}

func (r *Runtime) manageFlowRecordMap(record pebblestore.FlowDefinitionRecord, detailed bool) (map[string]any, error) {
	assignment := record.Assignment
	nextDueAt := record.NextDueAt
	if nextDueAt.IsZero() {
		next, ok, err := flow.NextFire(assignment, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if ok {
			nextDueAt = next
		}
	}
	view := map[string]any{"flow_id": record.FlowID, "revision": firstNonZeroInt64(record.Revision, assignment.Revision), "name": assignment.Name, "enabled": assignment.Enabled, "target": assignment.Target, "agent": assignment.Agent, "workspace": assignment.Workspace, "schedule": assignment.Schedule, "catch_up_policy": assignment.CatchUpPolicy, "intent": assignment.Intent, "next_due_at": nextDueAt, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt, "deleted_at": record.DeletedAt}
	if detailed && r != nil && r.flows != nil {
		history, _ := r.flows.ListMirroredRunSummaries(record.FlowID, 20)
		statuses, _ := r.flows.ListAssignmentStatuses(record.FlowID, 20)
		outbox, _ := r.flows.ListOutboxCommands("", 100)
		view["history"] = history
		view["history_count"] = len(history)
		view["assignment_statuses"] = statuses
		view["outbox"] = manageFlowOutboxForFlow(outbox, record.FlowID)
	}
	return view, nil
}

func (r *Runtime) manageFlowWorkspaceSummary(scope WorkspaceScope) map[string]string {
	workspacePath := strings.TrimSpace(scope.PrimaryPath)
	workspaceName := ""
	if r != nil && r.flowWorkspace != nil {
		if current, ok, err := r.flowWorkspace.CurrentBinding(); err == nil && ok {
			workspacePath = firstNonEmptyString(current.ResolvedPath, current.WorkspacePath, workspacePath)
			workspaceName = current.WorkspaceName
		}
		if workspacePath != "" {
			if info, err := r.flowWorkspace.ScopeForPath(workspacePath); err == nil {
				workspacePath = firstNonEmptyString(info.ResolvedPath, info.WorkspacePath, workspacePath)
				workspaceName = firstNonEmptyString(info.WorkspaceName, workspaceName)
			}
		}
	}
	if workspaceName == "" && workspacePath != "" {
		parts := strings.Split(strings.TrimRight(workspacePath, "/"), "/")
		workspaceName = parts[len(parts)-1]
	}
	return map[string]string{"workspace_path": workspacePath, "workspace_name": workspaceName}
}

func manageFlowContentMap(args map[string]any) (map[string]any, error) {
	if raw, ok := args["content"]; ok {
		switch typed := raw.(type) {
		case map[string]any:
			return typed, nil
		case string:
			if strings.TrimSpace(typed) == "" {
				return map[string]any{}, nil
			}
			var decoded map[string]any
			if err := json.Unmarshal([]byte(typed), &decoded); err != nil {
				return nil, fmt.Errorf("decode flow content: %w", err)
			}
			return decoded, nil
		default:
			return nil, errors.New("content must be an object or JSON object string")
		}
	}
	return args, nil
}

func manageFlowApprovedArguments(args map[string]any) map[string]any {
	approved := make(map[string]any, len(args)+1)
	for key, value := range args {
		if key == "approved_arguments" {
			continue
		}
		approved[key] = value
	}
	approved["confirm"] = true
	return approved
}

func manageFlowApprovalSummary(action string, flowView map[string]any) string {
	name := strings.TrimSpace(asString(flowView["name"]))
	if name == "" {
		name = strings.TrimSpace(asString(flowView["flow_id"]))
	}
	agent := ""
	if rawAgent, ok := flowView["agent"].(flow.AgentSelection); ok {
		agent = strings.TrimSpace(rawAgent.ProfileName)
	}
	scheduleText := manageFlowScheduleSummary(flowView["schedule"])
	return fmt.Sprintf("%s flow %q using agent %q on %s", action, name, agent, scheduleText)
}

func manageFlowScheduleSummary(raw any) string {
	schedule, ok := raw.(flow.ScheduleSpec)
	if !ok {
		return "configured schedule"
	}
	if strings.TrimSpace(schedule.Cron) != "" {
		return fmt.Sprintf("cron %q (%s)", schedule.Cron, schedule.Timezone)
	}
	parts := []string{strings.TrimSpace(schedule.Cadence)}
	if len(schedule.Times) > 0 {
		parts = append(parts, strings.Join(schedule.Times, ","))
	} else if strings.TrimSpace(schedule.Time) != "" {
		parts = append(parts, strings.TrimSpace(schedule.Time))
	}
	if strings.TrimSpace(schedule.Weekday) != "" {
		parts = append(parts, strings.TrimSpace(schedule.Weekday))
	}
	if strings.TrimSpace(schedule.Timezone) != "" {
		parts = append(parts, strings.TrimSpace(schedule.Timezone))
	}
	return strings.Join(parts, " ")
}

func manageFlowIDFromArgs(args map[string]any) string {
	return strings.TrimSpace(firstNonEmptyString(asString(args["flow_id"]), asString(args["id"]), asString(args["name"])))
}

func manageFlowGeneratedID(name string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" {
		base = "flow"
	}
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('_')
		}
	}
	id := strings.Trim(b.String(), "_")
	if id == "" {
		id = "flow"
	}
	return id
}

func manageFlowTargetFromContent(raw any) flow.TargetSelection {
	m, _ := raw.(map[string]any)
	return flow.TargetSelection{SwarmID: mapString(m, "swarm_id"), Kind: mapString(m, "kind"), DeploymentID: mapString(m, "deployment_id"), Name: mapString(m, "name")}
}

func manageFlowAgentFromContent(raw any) flow.AgentSelection {
	m, _ := raw.(map[string]any)
	return flow.NormalizeAgentSelection(flow.AgentSelection{ProfileName: mapString(m, "profile_name"), ProfileMode: mapString(m, "profile_mode")})
}

func manageFlowWorkspaceFromContent(raw any) flow.WorkspaceContext {
	m, _ := raw.(map[string]any)
	return flow.WorkspaceContext{WorkspacePath: mapString(m, "workspace_path"), HostWorkspacePath: mapString(m, "host_workspace_path"), RuntimeWorkspacePath: mapString(m, "runtime_workspace_path"), CWD: mapString(m, "cwd"), WorktreeMode: mapString(m, "worktree_mode")}
}

func manageFlowScheduleFromContent(raw any) flow.ScheduleSpec {
	m, _ := raw.(map[string]any)
	return flow.NormalizeScheduleSpec(flow.ScheduleSpec{Cadence: mapString(m, "cadence"), Time: mapString(m, "time"), Times: asStringSlice(m["times"]), Weekday: mapString(m, "weekday"), MonthDay: asInt(m["month_day"], 0), Timezone: mapString(m, "timezone"), Cron: mapString(m, "cron")})
}

func manageFlowCatchUpPolicyFromContent(raw any) flow.CatchUpPolicy {
	m, _ := raw.(map[string]any)
	return flow.NormalizeCatchUpPolicy(flow.CatchUpPolicy{Mode: mapString(m, "mode"), MaxCatchUp: asInt(m["max_catch_up"], 0)})
}

func manageFlowIntentFromContent(raw any) flow.PromptIntent {
	m, _ := raw.(map[string]any)
	intent := flow.PromptIntent{Prompt: mapString(m, "prompt"), Mode: mapString(m, "mode")}
	if rawTasks, ok := m["tasks"].([]any); ok {
		for _, rawTask := range rawTasks {
			item, ok := rawTask.(map[string]any)
			if !ok {
				continue
			}
			intent.Tasks = append(intent.Tasks, flow.TaskStep{ID: mapString(item, "id"), Title: mapString(item, "title"), Detail: mapString(item, "detail"), Action: mapString(item, "action")})
		}
	}
	return intent
}

func manageFlowHasTarget(target flow.TargetSelection) bool {
	return strings.TrimSpace(target.SwarmID) != "" || strings.TrimSpace(target.Kind) != "" || strings.TrimSpace(target.DeploymentID) != "" || strings.TrimSpace(target.Name) != ""
}

func manageFlowHasAgent(agent flow.AgentSelection) bool {
	return strings.TrimSpace(agent.ProfileName) != "" || strings.TrimSpace(agent.ProfileMode) != ""
}

func manageFlowHasWorkspace(workspace flow.WorkspaceContext) bool {
	return strings.TrimSpace(workspace.WorkspacePath) != "" || strings.TrimSpace(workspace.HostWorkspacePath) != "" || strings.TrimSpace(workspace.RuntimeWorkspacePath) != "" || strings.TrimSpace(workspace.CWD) != ""
}

func manageFlowHasSchedule(schedule flow.ScheduleSpec) bool {
	return strings.TrimSpace(schedule.Cadence) != "" || strings.TrimSpace(schedule.Time) != "" || len(schedule.Times) > 0 || strings.TrimSpace(schedule.Weekday) != "" || schedule.MonthDay != 0 || strings.TrimSpace(schedule.Timezone) != "" || strings.TrimSpace(schedule.Cron) != ""
}

func manageFlowHasCatchUp(policy flow.CatchUpPolicy) bool {
	return strings.TrimSpace(policy.Mode) != "" || policy.MaxCatchUp != 0
}

func manageFlowHasIntent(intent flow.PromptIntent) bool {
	return strings.TrimSpace(intent.Prompt) != "" || strings.TrimSpace(intent.Mode) != "" || len(intent.Tasks) > 0
}

func (r *Runtime) manageFlowAgentInventory(limit int) map[string]any {
	inventory := map[string]any{
		"configured": false,
		"agents":     []map[string]any{},
		"count":      0,
	}
	if r == nil || r.agents == nil {
		inventory["note"] = "manage-agent service is not configured; use manage-agent inspect if available"
		return inventory
	}
	state, err := r.agents.ListState(clampManageFlowLimit(limit))
	if err != nil {
		inventory["error"] = fmt.Sprintf("list saved agents failed: %v", err)
		return inventory
	}
	agents := make([]map[string]any, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		mode := strings.TrimSpace(profile.Mode)
		flowProfileMode := flow.NormalizeAgentProfileMode(mode)
		agents = append(agents, map[string]any{
			"name":                        strings.TrimSpace(profile.Name),
			"mode":                        mode,
			"flow_profile_mode":           flowProfileMode,
			"description":                 strings.TrimSpace(profile.Description),
			"enabled":                     profile.Enabled,
			"active_primary":              strings.EqualFold(strings.TrimSpace(state.ActivePrimary), strings.TrimSpace(profile.Name)),
			"active_purposes":             manageAgentPurposesForProfile(state.ActiveSubagent, profile.Name),
			"execution_setting":           strings.TrimSpace(profile.ExecutionSetting),
			"effective_execution_setting": manageAgentEffectiveExecutionSetting(profile),
			"exit_plan_mode_enabled":      pebblestore.AgentExitPlanModeEnabled(profile),
			"flow_agent": map[string]any{
				"profile_name": strings.TrimSpace(profile.Name),
				"profile_mode": flowProfileMode,
			},
		})
	}
	inventory["configured"] = true
	inventory["agents"] = agents
	inventory["count"] = len(agents)
	inventory["active_primary"] = strings.TrimSpace(state.ActivePrimary)
	inventory["active_subagent"] = cloneStringMap(state.ActiveSubagent)
	inventory["note"] = "Compact inventory for choosing flow content.agent.profile_name/profile_mode; call manage-agent get for full prompt/tool contract details."
	return inventory
}

func manageFlowOutboxForFlow(records []pebblestore.FlowOutboxCommandRecord, flowID string) []pebblestore.FlowOutboxCommandRecord {
	out := make([]pebblestore.FlowOutboxCommandRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.FlowID), strings.TrimSpace(flowID)) {
			out = append(out, record)
		}
	}
	return out
}

func clampManageFlowLimit(limit int) int {
	if limit <= 0 {
		limit = manageFlowDefaultLimit
	}
	if limit > manageFlowMaxLimit {
		limit = manageFlowMaxLimit
	}
	return limit
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func encodeManageFlowResponse(response map[string]any) (string, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
