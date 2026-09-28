package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// workerDocumentInPlanCall rejects worker authoring through session-plan tools.
// Only the dedicated manage_workers proposal action may publish a worker review.
func workerDocumentInPlanCall(call tool.Call) bool {
	name := canonicalToolName(call.Name)
	if name != "exit_plan_mode" && name != "plan_manage" {
		return false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return false
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(args["document"], &doc) != nil {
		var encoded string
		if json.Unmarshal(args["document"], &encoded) != nil || json.Unmarshal([]byte(encoded), &doc) != nil {
			return false
		}
	}
	if _, ok := doc["worker_v2"]; ok {
		return true
	}
	_, ok := doc["automation_v2"]
	return ok
}

func workerProposalCall(call tool.Call) bool {
	name := canonicalToolName(call.Name)
	if name != "manage_workers" {
		return false
	}
	var args struct {
		Action string `json:"action"`
	}
	return json.Unmarshal([]byte(call.Arguments), &args) == nil && args.Action == "propose"
}

func (s *Service) automationV2ToolSession(id string) (store.SessionSnapshot, string, error) {
	if s.sessions == nil {
		return store.SessionSnapshot{}, "", errors.New("session service required")
	}
	current, ok, err := s.sessions.GetSession(id)
	if err != nil {
		return current, "", err
	}
	if !ok {
		return current, "", errors.New("session unavailable")
	}
	if mapString(current.Metadata, "lineage_kind") != "" {
		return current, "", errors.New("automation proposal requires a primary conversation; Plan sidechat uses edit_pending_plan")
	}
	for _, g := range current.WorkspaceGrants {
		if g.Kind == store.WorkspaceGrantPrimary && g.Available != nil && *g.Available && strings.TrimSpace(g.WorkspaceID) != "" {
			return current, strings.TrimSpace(g.WorkspaceID), nil
		}
	}
	for _, g := range current.WorkspaceGrants {
		if g.Available != nil && *g.Available && strings.TrimSpace(g.WorkspaceID) != "" {
			return current, strings.TrimSpace(g.WorkspaceID), nil
		}
	}
	if current.Metadata != nil {
		if wsID := mapString(current.Metadata, "swarm_v3_source_workspace_id"); wsID != "" {
			return current, wsID, nil
		}
		if wsID := mapString(current.Metadata, "automation_review_workspace_id"); wsID != "" {
			return current, wsID, nil
		}
		if wsID := mapString(current.Metadata, store.SessionPurposeWorkspaceMetadataKey); wsID != "" {
			return current, wsID, nil
		}
	}
	principal := identity.Principal{UserID: current.UserID, AccountScopeID: current.AccountScopeID}
	if current.Metadata != nil && s.sessions != nil {
		if pid := mapString(current.Metadata, "project_id"); pid != "" {
			if db := s.sessions.Store(); db != nil {
				if proj, found, err := db.GetProject(current.AccountScopeID, pid); err == nil && found && proj != nil {
					for _, ws := range proj.Workspaces {
						if strings.TrimSpace(ws.WorkspaceID) != "" {
							return current, strings.TrimSpace(ws.WorkspaceID), nil
						}
					}
					if len(proj.Workspaces) > 0 && s.workspace != nil && strings.TrimSpace(proj.Workspaces[0].Path) != "" {
						if scope, err := s.workspace.ScopeForPathForPrincipal(principal, proj.Workspaces[0].Path); err == nil && strings.TrimSpace(scope.WorkspaceID) != "" {
							return current, strings.TrimSpace(scope.WorkspaceID), nil
						}
					}
				}
			}
		}
	}
	if s.workspace != nil && strings.TrimSpace(current.WorkspacePath) != "" && strings.TrimSpace(current.WorkspacePath) != "." {
		if scope, err := s.workspace.ScopeForPathForPrincipal(principal, current.WorkspacePath); err == nil && strings.TrimSpace(scope.WorkspaceID) != "" {
			return current, strings.TrimSpace(scope.WorkspaceID), nil
		}
	}
	if s.workspace != nil {
		if binding, ok, err := s.workspace.CurrentBindingForPrincipal(principal); err == nil && ok && strings.TrimSpace(binding.WorkspaceID) != "" {
			return current, strings.TrimSpace(binding.WorkspaceID), nil
		}
		if entries, err := s.workspace.ListKnownForPrincipal(principal, 1); err == nil && len(entries) > 0 && strings.TrimSpace(entries[0].WorkspaceID) != "" {
			return current, strings.TrimSpace(entries[0].WorkspaceID), nil
		}
	}
	return current, "", nil
}

func (s *Service) executeWorkerProposalTool(id string, call tool.Call) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return "", err
	}
	if mapString(args, "action") != "propose" {
		return "", errors.New("manage_workers action=propose required")
	}
	for key := range args {
		if key != "document" && key != "action" && key != "worker_review" && key != "workspace_id" && key != "workspace_ids" && key != "project_id" {
			return "", fmt.Errorf("worker proposal does not accept %s; submit complete instructions and worker_review only for an exact pending edit", key)
		}
	}
	doc, err := planDocumentFromArgsForTool(args, call.Name)
	if err != nil {
		return "", err
	}
	if doc == nil || doc.WorkerV2 == nil {
		return "", errors.New("complete worker_v2 document required")
	}
	if wsID := mapString(args, "workspace_id"); wsID != "" && doc.WorkerV2.WorkspaceID == "" {
		doc.WorkerV2.WorkspaceID = wsID
	}
	if rawIDs, ok := args["workspace_ids"].([]any); ok && len(doc.WorkerV2.WorkspaceIDs) == 0 {
		for _, item := range rawIDs {
			if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
				doc.WorkerV2.WorkspaceIDs = append(doc.WorkerV2.WorkspaceIDs, strings.TrimSpace(str))
			}
		}
	}
	current, defaultWorkspace, err := s.automationV2ToolSession(id)
	if err != nil {
		return "", err
	}
	projectID := mapString(args, "project_id")
	if projectID == "" && doc.WorkerV2 != nil {
		projectID = strings.TrimSpace(doc.WorkerV2.ProjectID)
	}
	if projectID == "" && current.Metadata != nil {
		projectID = mapString(current.Metadata, "project_id")
	}
	if projectID == "" {
		return "", errors.New("workers must be deployed in a project with scoped project context; deploy from Swarm Orchestrate mode or provide project_id")
	}
	if s.sessions != nil && s.sessions.Store() != nil {
		proj, found, err := s.sessions.Store().GetProject(current.AccountScopeID, projectID)
		if err != nil {
			return "", err
		}
		if !found || proj == nil {
			return "", fmt.Errorf("project %q not found", projectID)
		}
		validWS := make(map[string]bool)
		for _, ws := range proj.Workspaces {
			if strings.TrimSpace(ws.WorkspaceID) != "" {
				validWS[strings.TrimSpace(ws.WorkspaceID)] = true
			}
		}
		if len(doc.WorkerV2.WorkspaceIDs) > 0 {
			for _, wid := range doc.WorkerV2.WorkspaceIDs {
				if len(validWS) > 0 && !validWS[wid] {
					return "", fmt.Errorf("workspace %q is not part of project %q", wid, projectID)
				}
			}
			if doc.WorkerV2.WorkspaceID == "" {
				doc.WorkerV2.WorkspaceID = doc.WorkerV2.WorkspaceIDs[0]
			}
		} else if doc.WorkerV2.WorkspaceID != "" {
			if len(validWS) > 0 && !validWS[doc.WorkerV2.WorkspaceID] {
				return "", fmt.Errorf("workspace %q is not part of project %q", doc.WorkerV2.WorkspaceID, projectID)
			}
			doc.WorkerV2.WorkspaceIDs = []string{doc.WorkerV2.WorkspaceID}
		} else if len(proj.Workspaces) > 0 {
			for _, ws := range proj.Workspaces {
				if strings.TrimSpace(ws.WorkspaceID) != "" {
					doc.WorkerV2.WorkspaceIDs = append(doc.WorkerV2.WorkspaceIDs, strings.TrimSpace(ws.WorkspaceID))
				}
			}
			if len(doc.WorkerV2.WorkspaceIDs) > 0 {
				doc.WorkerV2.WorkspaceID = doc.WorkerV2.WorkspaceIDs[0]
			}
		}
		doc.WorkerV2.ProjectID = projectID
		if doc.AutomationV2 != nil {
			doc.AutomationV2.ProjectID = projectID
			doc.AutomationV2.WorkspaceID = doc.WorkerV2.WorkspaceID
			doc.AutomationV2.WorkspaceIDs = doc.WorkerV2.WorkspaceIDs
		}
	}
	targetWorkspace := defaultWorkspace
	if doc.WorkerV2 != nil && strings.TrimSpace(doc.WorkerV2.WorkspaceID) != "" {
		targetWorkspace = strings.TrimSpace(doc.WorkerV2.WorkspaceID)
	} else if doc.AutomationV2 != nil && strings.TrimSpace(doc.AutomationV2.WorkspaceID) != "" {
		targetWorkspace = strings.TrimSpace(doc.AutomationV2.WorkspaceID)
	}
	if targetWorkspace == "" {
		return "", errors.New("target workspace required")
	}
	var review store.AutomationV2Review
	if raw, ok := args["worker_review"]; ok {
		if err := unmarshalPlanToolArg(raw, &review, "worker_review"); err != nil {
			return "", err
		}
	}
	p, err := s.sessions.ProposeAutomationV2(current.AccountScopeID, current.UserID, targetWorkspace, id, doc, review)
	if err != nil {
		return "", err
	}
	return automationV2ToolOutput(p)
}
func automationV2ToolOutput(p store.AutomationV2Proposal) (string, error) {
	raw, err := json.Marshal(map[string]any{
		"status":            "pending_review",
		"review_kind":       "worker_v2",
		"title":             "Worker plan",
		"project_id":        p.ProjectID,
		"workspace_id":      p.WorkspaceID,
		"workspace_ids":     p.WorkspaceIDs,
		"created":           false,
		"enabled":           false,
		"next_action":       "await_worker_acceptance",
		"worker_review":     p.AutomationV2Review,
		"automation_review": p.AutomationV2Review,
		"permission_id":     store.AutomationV2PermissionID(p.ProposalID),
		"document":          p.Document,
		"instruction":       "Stop authoring. Only explicit user Accept worker creates and activates this schedule; no ordinary plan execution or immediate run.",
	})
	return string(raw), err
}
func parseUint64Arg(args map[string]any, key string) (uint64, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		if v > 0 {
			return uint64(v), true
		}
	case int:
		if v > 0 {
			return uint64(v), true
		}
	case int64:
		if v > 0 {
			return uint64(v), true
		}
	case uint64:
		if v > 0 {
			return v, true
		}
	case string:
		if u, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil && u > 0 {
			return u, true
		}
	case json.Number:
		if u, err := strconv.ParseUint(string(v), 10, 64); err == nil && u > 0 {
			return u, true
		}
	}
	return 0, false
}

func unmarshalJSONArg(src any, dst any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func (s *Service) cancelWorkerRuns(account, workerID string, automationID ...string) {
	if s == nil || s.sessions == nil {
		return
	}
	targetAuto := ""
	if len(automationID) > 0 {
		targetAuto = strings.TrimSpace(automationID[0])
	}
	runs, _, err := s.sessions.ListWorkerRuns(account, workerID, 50, "")
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	for _, r := range runs {
		if targetAuto != "" && r.AutomationID != targetAuto {
			continue
		}
		if r.Status == "running" || r.Status == "admitted" {
			for _, sid := range r.SessionIDs {
				_ = s.StopSessionRun(sid, "", "worker execution stopped")
			}
			r.Status = "cancelled"
			r.ErrorMessage = "worker execution cancelled by lifecycle control"
			r.UpdatedAt = now
			_, _ = s.sessions.RecordWorkerRun(account, r)
		}
	}
}

func (s *Service) executeManageAutomationV2Tool(id, arguments string) (string, error) {
	return s.executeManageWorkersTool(id, arguments)
}

func (s *Service) executeManageWorkersTool(id, arguments string, profile ...store.AgentProfile) (string, error) {
	if s.sessions == nil {
		return "", errors.New("session service required")
	}
	readID := id
	if child, found, err := s.sessions.GetSession(id); err != nil {
		return "", err
	} else if found && mapString(child.Metadata, "system_sidechat_kind") == "plan" && mapString(child.Metadata, "lineage_kind") == "system_sidechat" {
		parentID := mapString(child.Metadata, "parent_session_id")
		parent, found, err := s.sessions.GetSession(parentID)
		if err != nil || !found || parent.AccountScopeID != child.AccountScopeID || parent.UserID != child.UserID {
			return "", errors.New("automation sidechat ownership mismatch")
		}
		readID = parentID
	}
	current, defaultWorkspace, err := s.automationV2ToolSession(readID)
	if err != nil {
		return "", err
	}
	if len(profile) > 0 && strings.TrimSpace(profile[0].Name) != "" {
		if !agentruntime.IsOrchestratorAgentName(profile[0].Name) {
			return "", errors.New("Worker and automation management is exclusive to Swarm Orchestrator in Swarm mode")
		}
	} else {
		sessionAgent := strings.TrimSpace(firstNonEmptyString(
			mapString(current.Metadata, "resolved_agent_name"),
			mapString(current.Metadata, "agent_name"),
		))
		if sessionAgent != "" && !agentruntime.IsOrchestratorAgentName(sessionAgent) && mapString(current.Metadata, "role") != "project_orchestrator" {
			return "", errors.New("Worker and automation management is exclusive to Swarm Orchestrator in Swarm mode")
		}
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", err
	}
	action := strings.TrimSpace(mapString(args, "action"))
	if action == "" {
		return "", errors.New("manage_workers action is required")
	}

	workspace := defaultWorkspace
	if wsID := mapString(args, "workspace_id"); wsID != "" {
		workspace = wsID
	}

	switch action {
	case "help":
		raw, err := json.Marshal(map[string]any{"action": "help", "status": "ok", "instructions": tool.WorkerV2AuthoringInstructions})
		return string(raw), err

	case "propose":
		return s.executeWorkerProposalTool(readID, tool.Call{Name: "manage_workers", Arguments: arguments})

	case "review", "context":
		if workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id"))); workerID != "" {
			w, found, err := s.sessions.GetWorker(current.AccountScopeID, workerID)
			if err != nil {
				return "", err
			}
			if !found {
				return "", store.ErrWorkerNotFound
			}
			raw, err := json.Marshal(map[string]any{
				"found":  true,
				"worker": w,
			})
			return string(raw), err
		}
		if workspace == "" {
			return "", errors.New("target workspace required")
		}
		p, found, err := s.sessions.GetAutomationV2Proposal(current.AccountScopeID, current.UserID, workspace, readID)
		if err != nil {
			return "", err
		}
		out := map[string]any{"found": found, "state": "not_created", "instruction": tool.WorkerV2AuthoringInstructions}
		if found {
			out = map[string]any{"found": true, "proposal": p, "worker_review": p.AutomationV2Review, "automation_review": p.AutomationV2Review}
		}
		raw, err := json.Marshal(out)
		return string(raw), err

	case "progress":
		if workspace == "" {
			return "", errors.New("target workspace required")
		}
		progress, err := s.sessions.AutomationV2Progress(current.AccountScopeID, current.UserID, workspace, readID, mapString(args, "timezone"), mapString(args, "cursor"), time.Now().UnixMilli())
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(progress)
		return string(raw), err

	case "list":
		limit := mapInt(args, "limit")
		if limit == 0 {
			limit = 20
		}
		if limit < 1 || limit > 50 {
			return "", errors.New("limit must be 1–50")
		}
		cursor := mapString(args, "cursor")
		includeDeleted := false
		if rawDel, ok := args["include_deleted"].(bool); ok {
			includeDeleted = rawDel
		}
		query := store.ListWorkersQuery{
			Limit:          limit,
			Cursor:         cursor,
			IncludeDeleted: includeDeleted,
		}
		res, err := s.sessions.ListWorkers(current.AccountScopeID, query)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"workers":     res.Workers,
			"next_cursor": res.NextCursor,
		})
		return string(raw), err

	case "inspect", "get":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for inspect")
		}
		w, found, err := s.sessions.GetWorker(current.AccountScopeID, workerID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", store.ErrWorkerNotFound
		}
		out := map[string]any{
			"found":  true,
			"worker": w,
		}
		if rev, ok := parseUint64Arg(args, "revision"); ok && rev > 0 {
			snap, sFound, sErr := s.sessions.GetWorkerRevision(current.AccountScopeID, workerID, rev)
			if sErr != nil {
				return "", sErr
			}
			if !sFound {
				return "", errors.New("worker revision not found")
			}
			out["revision_snapshot"] = snap
		}
		if mapBool(args, "runs") {
			runs, nextCursor, rErr := s.sessions.ListWorkerRuns(current.AccountScopeID, workerID, 20, mapString(args, "cursor"))
			if rErr != nil {
				return "", rErr
			}
			out["runs"] = runs
			out["runs_cursor"] = nextCursor
		}
		raw, err := json.Marshal(out)
		return string(raw), err

	case "create":
		name := strings.TrimSpace(mapString(args, "name"))
		if name == "" {
			return "", errors.New("worker name is required")
		}
		instructions := strings.TrimSpace(mapString(args, "instructions"))
		if instructions == "" {
			return "", errors.New("worker instructions are required")
		}
		req := store.CreateWorkerRequest{
			Name:           name,
			Description:    strings.TrimSpace(mapString(args, "description")),
			Instructions:   instructions,
			IdempotencyKey: strings.TrimSpace(mapString(args, "idempotency_key")),
		}
		if capsRaw, ok := args["requested_capabilities"]; ok {
			if err := unmarshalJSONArg(capsRaw, &req.RequestedCapabilities); err != nil {
				return "", err
			}
		}
		if wsReqsRaw, ok := args["workspace_requirements"]; ok {
			if err := unmarshalJSONArg(wsReqsRaw, &req.WorkspaceRequirements); err != nil {
				return "", err
			}
		}
		if autosRaw, ok := args["automations"]; ok {
			if err := unmarshalJSONArg(autosRaw, &req.Automations); err != nil {
				return "", err
			}
		}
		if metaRaw, ok := args["metadata"].(map[string]any); ok {
			req.Metadata = metaRaw
		}
		w, err := s.sessions.CreateWorker(context.Background(), current.AccountScopeID, current.UserID, req)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status": "created",
			"worker": w,
		})
		return string(raw), err

	case "update":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for update")
		}
		expectedRevision, ok := parseUint64Arg(args, "expected_revision")
		if !ok || expectedRevision == 0 {
			return "", errors.New("expected_revision is required for update")
		}
		req := store.UpdateWorkerRequest{
			ChangeSummary: strings.TrimSpace(mapString(args, "change_summary")),
		}
		if val, ok := args["name"].(string); ok {
			trimmed := strings.TrimSpace(val)
			req.Name = &trimmed
		}
		if val, ok := args["description"].(string); ok {
			trimmed := strings.TrimSpace(val)
			req.Description = &trimmed
		}
		if val, ok := args["instructions"].(string); ok {
			req.Instructions = &val
		}
		if capsRaw, ok := args["requested_capabilities"]; ok {
			if err := unmarshalJSONArg(capsRaw, &req.RequestedCapabilities); err != nil {
				return "", err
			}
		}
		if wsReqsRaw, ok := args["workspace_requirements"]; ok {
			if err := unmarshalJSONArg(wsReqsRaw, &req.WorkspaceRequirements); err != nil {
				return "", err
			}
		}
		if autosRaw, ok := args["automations"]; ok {
			if err := unmarshalJSONArg(autosRaw, &req.Automations); err != nil {
				return "", err
			}
		}
		if metaRaw, ok := args["metadata"].(map[string]any); ok {
			req.Metadata = metaRaw
		}
		w, err := s.sessions.UpdateWorker(current.AccountScopeID, current.UserID, workerID, expectedRevision, req)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status": "updated",
			"worker": w,
		})
		return string(raw), err

	case "attach":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for attach")
		}
		expectedRevision, ok := parseUint64Arg(args, "expected_revision")
		if !ok || expectedRevision == 0 {
			return "", errors.New("expected_revision is required for attach")
		}
		var auto store.WorkerAutomationDefinition
		if autoRaw, ok := args["automation"]; ok {
			if err := unmarshalJSONArg(autoRaw, &auto); err != nil {
				return "", err
			}
		} else {
			auto.Name = strings.TrimSpace(mapString(args, "name"))
			auto.Description = strings.TrimSpace(mapString(args, "description"))
			auto.ActivationMode = strings.TrimSpace(mapString(args, "activation_mode"))
			if auto.ActivationMode == "" {
				auto.ActivationMode = "manual"
			}
			if rawSched, ok := args["schedule"]; ok {
				if err := unmarshalJSONArg(rawSched, &auto.Schedule); err != nil {
					return "", err
				}
			}
			if rawTrig, ok := args["trigger"]; ok {
				if err := unmarshalJSONArg(rawTrig, &auto.Trigger); err != nil {
					return "", err
				}
			}
			if rawPlan, ok := args["plan_document"]; ok {
				if err := unmarshalJSONArg(rawPlan, &auto.PlanDocument); err != nil {
					return "", err
				}
			}
			if enabled, ok := args["enabled"].(bool); ok {
				auto.Enabled = enabled
			} else {
				auto.Enabled = true
			}
		}
		w, err := s.sessions.AttachWorkerAutomation(current.AccountScopeID, current.UserID, workerID, expectedRevision, auto)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status": "attached",
			"worker": w,
		})
		return string(raw), err

	case "disable_automation":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for disable_automation")
		}
		automationID := strings.TrimSpace(mapString(args, "automation_id"))
		if automationID == "" {
			return "", errors.New("automation_id is required for disable_automation")
		}
		expectedRevision, ok := parseUint64Arg(args, "expected_revision")
		if !ok || expectedRevision == 0 {
			return "", errors.New("expected_revision is required for disable_automation")
		}
		w, found, err := s.sessions.GetWorker(current.AccountScopeID, workerID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", store.ErrWorkerNotFound
		}
		var targetAuto *store.WorkerAutomationDefinition
		for i := range w.Automations {
			if w.Automations[i].ID == automationID {
				a := w.Automations[i]
				targetAuto = &a
				break
			}
		}
		if targetAuto == nil {
			return "", fmt.Errorf("automation %q not found on worker %q", automationID, workerID)
		}
		targetAuto.Enabled = false
		updated, err := s.sessions.UpdateWorkerAutomation(current.AccountScopeID, current.UserID, workerID, automationID, expectedRevision, *targetAuto)
		if err != nil {
			return "", err
		}
		s.cancelWorkerRuns(current.AccountScopeID, workerID, automationID)
		raw, err := json.Marshal(map[string]any{
			"status":        "automation_disabled",
			"worker_id":    workerID,
			"automation_id": automationID,
			"worker":        updated,
		})
		return string(raw), err

	case "delete":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for delete")
		}
		expectedRevision, ok := parseUint64Arg(args, "expected_revision")
		if !ok || expectedRevision == 0 {
			return "", errors.New("expected_revision is required for delete")
		}
		err := s.sessions.DeleteWorker(current.AccountScopeID, current.UserID, workerID, expectedRevision)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status":    "deleted",
			"worker_id": workerID,
		})
		return string(raw), err

	case "activate", "approve":
		return "", errors.New("worker activation requires explicit user approval; AI cannot self-approve worker activation or capability grants. Submit proposals for user acceptance via manage_workers action=propose.")

	case "test":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for test")
		}
		w, found, err := s.sessions.GetWorker(current.AccountScopeID, workerID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", store.ErrWorkerNotFound
		}
		if w.LifecycleState == store.WorkerLifecycleStateDeleted || w.LifecycleState == store.WorkerLifecycleStateArchived {
			return "", fmt.Errorf("cannot run test on worker in %s state", w.LifecycleState)
		}
		automationID := strings.TrimSpace(mapString(args, "automation_id"))
		var automationRevision uint64
		if automationID != "" {
			foundAuto := false
			for _, a := range w.Automations {
				if a.ID == automationID {
					automationRevision = a.Revision
					foundAuto = true
					break
				}
			}
			if !foundAuto {
				return "", fmt.Errorf("automation %q not found on worker %q", automationID, workerID)
			}
		}
		now := time.Now().UnixMilli()
		runID := store.GenerateWorkerRunID()
		var acceptedInput map[string]any
		if inRaw, ok := args["input"].(map[string]any); ok {
			acceptedInput = inRaw
		} else if prompt := strings.TrimSpace(mapString(args, "prompt")); prompt != "" {
			acceptedInput = map[string]any{"prompt": prompt}
		}
		runRec := store.WorkerRunRecord{
			RunID:              runID,
			WorkerID:           workerID,
			WorkerRevision:     w.Revision,
			AutomationID:       automationID,
			AutomationRevision: automationRevision,
			RequestSource:      "test",
			IsTest:             true,
			Status:             "admitted",
			AcceptedInput:      acceptedInput,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		rec, err := s.sessions.RecordWorkerRun(current.AccountScopeID, runRec)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status":  "admitted",
			"is_test": true,
			"run":     rec,
		})
		return string(raw), err

	case "request":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for request")
		}
		prompt := strings.TrimSpace(mapString(args, "prompt"))
		if prompt == "" {
			return "", errors.New("prompt is required for worker request")
		}
		w, found, err := s.sessions.GetWorker(current.AccountScopeID, workerID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", store.ErrWorkerNotFound
		}
		if w.LifecycleState == store.WorkerLifecycleStateDeleted || w.LifecycleState == store.WorkerLifecycleStateArchived {
			return "", fmt.Errorf("cannot dispatch request to worker in %s state", w.LifecycleState)
		}
		if w.LifecycleState == store.WorkerLifecycleStatePaused || (w.Metadata != nil && mapBool(w.Metadata, "lifecycle_pause")) {
			return "", errors.New("cannot dispatch request: admission is closed for paused worker")
		}
		now := time.Now().UnixMilli()
		runID := store.GenerateWorkerRunID()
		acceptedInput := map[string]any{"prompt": prompt}
		if inRaw, ok := args["input"].(map[string]any); ok {
			for k, v := range inRaw {
				acceptedInput[k] = v
			}
		}
		runRec := store.WorkerRunRecord{
			RunID:          runID,
			WorkerID:       workerID,
			WorkerRevision: w.Revision,
			RequestSource:  "direct",
			IsTest:         false,
			Status:         "admitted",
			AcceptedInput:  acceptedInput,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		rec, err := s.sessions.RecordWorkerRun(current.AccountScopeID, runRec)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status":  "admitted",
			"is_test": false,
			"run":     rec,
		})
		return string(raw), err

	case "pause":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for pause")
		}
		expectedRevision, ok := parseUint64Arg(args, "expected_revision")
		if !ok || expectedRevision == 0 {
			return "", errors.New("expected_revision is required for pause")
		}
		w, found, err := s.sessions.GetWorker(current.AccountScopeID, workerID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", store.ErrWorkerNotFound
		}
		if w.LifecycleState == store.WorkerLifecycleStateDeleted || w.LifecycleState == store.WorkerLifecycleStateArchived {
			return "", fmt.Errorf("cannot pause worker in %s state", w.LifecycleState)
		}
		s.cancelWorkerRuns(current.AccountScopeID, workerID)
		automations := make([]store.WorkerAutomationDefinition, len(w.Automations))
		copy(automations, w.Automations)
		for i := range automations {
			automations[i].Enabled = false
		}
		meta := make(map[string]any)
		for k, v := range w.Metadata {
			meta[k] = v
		}
		meta["paused_at"] = time.Now().UnixMilli()
		meta["lifecycle_pause"] = true
		req := store.UpdateWorkerRequest{
			Automations:   automations,
			Metadata:      meta,
			ChangeSummary: "paused worker",
		}
		updated, err := s.sessions.UpdateWorker(current.AccountScopeID, current.UserID, workerID, expectedRevision, req)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status":    "paused",
			"worker_id": workerID,
			"worker":    updated,
		})
		return string(raw), err

	case "resume":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for resume")
		}
		expectedRevision, ok := parseUint64Arg(args, "expected_revision")
		if !ok || expectedRevision == 0 {
			return "", errors.New("expected_revision is required for resume")
		}
		w, found, err := s.sessions.GetWorker(current.AccountScopeID, workerID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", store.ErrWorkerNotFound
		}
		if w.LifecycleState == store.WorkerLifecycleStateDeleted || w.LifecycleState == store.WorkerLifecycleStateArchived {
			return "", fmt.Errorf("cannot resume worker in %s state", w.LifecycleState)
		}
		meta := make(map[string]any)
		for k, v := range w.Metadata {
			meta[k] = v
		}
		delete(meta, "lifecycle_pause")
		meta["resumed_at"] = time.Now().UnixMilli()
		req := store.UpdateWorkerRequest{
			Metadata:      meta,
			ChangeSummary: "resumed worker",
		}
		updated, err := s.sessions.UpdateWorker(current.AccountScopeID, current.UserID, workerID, expectedRevision, req)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status":    "resumed",
			"worker_id": workerID,
			"worker":    updated,
		})
		return string(raw), err

	case "archive":
		workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
		if workerID == "" {
			return "", errors.New("worker_id is required for archive")
		}
		expectedRevision, ok := parseUint64Arg(args, "expected_revision")
		if !ok || expectedRevision == 0 {
			return "", errors.New("expected_revision is required for archive")
		}
		w, found, err := s.sessions.GetWorker(current.AccountScopeID, workerID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", store.ErrWorkerNotFound
		}
		if w.LifecycleState == store.WorkerLifecycleStateDeleted {
			return "", errors.New("cannot archive deleted worker")
		}
		s.cancelWorkerRuns(current.AccountScopeID, workerID)
		automations := make([]store.WorkerAutomationDefinition, len(w.Automations))
		copy(automations, w.Automations)
		for i := range automations {
			automations[i].Enabled = false
		}
		meta := make(map[string]any)
		for k, v := range w.Metadata {
			meta[k] = v
		}
		meta["archived_at"] = time.Now().UnixMilli()
		meta["lifecycle_archived"] = true
		req := store.UpdateWorkerRequest{
			Automations:   automations,
			Metadata:      meta,
			ChangeSummary: "archived worker",
		}
		updated, err := s.sessions.UpdateWorker(current.AccountScopeID, current.UserID, workerID, expectedRevision, req)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status":    "archived",
			"worker_id": workerID,
			"worker":    updated,
		})
		return string(raw), err

	default:
		return "", errors.New("V1 automation operations are retired; V2 mutations require a Worker plan review and explicit user acceptance")
	}
}
