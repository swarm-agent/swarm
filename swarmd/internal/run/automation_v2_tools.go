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
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: current.UserID, AccountScopeID: current.AccountScopeID, AccountScopeSource: identity.AccountScopeSourceServerState}
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
	// An account's current/first workspace is not an explicit session target.
	// Unbound headless sessions must supply workspace_id rather than silently
	// selecting an unrelated authorized repository.
	return current, "", nil
}

func (s *Service) executeWorkerProposalTool(id string, call tool.Call, profile ...store.AgentProfile) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return "", err
	}
	if action := strings.TrimSpace(mapString(args, "action")); action != "propose" {
		return "", errors.New("manage_workers action=propose required")
	}
	return s.executeCreateOrProposePendingWorker(id, args, call.Name, false, profile...)
}

func (s *Service) authorizeProposedWorkspace(current store.SessionSnapshot, workspaceID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return errors.New("target workspace required")
	}
	for _, g := range current.WorkspaceGrants {
		if (g.Available == nil || *g.Available) && strings.TrimSpace(g.WorkspaceID) == workspaceID {
			return nil
		}
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: current.UserID, AccountScopeID: current.AccountScopeID, AccountScopeSource: identity.AccountScopeSourceServerState}
	if s.workspace != nil {
		entry, found, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, workspaceID)
		if err != nil {
			return err
		}
		if found && strings.EqualFold(entry.State, "active") {
			return nil
		}
		return fmt.Errorf("workspace %q not found or not accessible in account scope", workspaceID)
	}
	if s.sessions != nil && s.sessions.Store() != nil && s.sessions.Store().Underlying() != nil {
		wsStore := store.NewWorkspaceStore(s.sessions.Store().Underlying())
		entry, found, err := wsStore.GetByWorkspaceIDForAccount(current.AccountScopeID, workspaceID)
		if err != nil {
			return err
		}
		if found && strings.TrimSpace(entry.WorkspaceID) != "" {
			return nil
		}
	}
	return fmt.Errorf("workspace %q not found or not accessible in account scope", workspaceID)
}

func (s *Service) executeCreateOrProposePendingWorker(id string, args map[string]any, toolName string, isCreate bool, profile ...store.AgentProfile) (string, error) {
	if s.sessions == nil {
		return "", errors.New("session service required")
	}
	current, defaultWorkspace, err := s.automationV2ToolSession(id)
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
		if sessionAgent == "" && current.Metadata != nil {
			if prof, pErr := sessionV3AgentProfileFromMetadataMap(current.Metadata); pErr == nil {
				sessionAgent = strings.TrimSpace(prof.Name)
			}
		}
		if !agentruntime.IsOrchestratorAgentName(sessionAgent) {
			return "", errors.New("Worker and automation management is exclusive to Swarm Orchestrator in Swarm mode")
		}
	}

	var name, description, instructions string
	var automations []store.WorkerAutomationDefinition
	var requestedCaps []store.WorkerCapabilityRequest
	var workspaceReqs []store.WorkerWorkspaceRequirement
	var proposedPrimary string
	idempKey := strings.TrimSpace(mapString(args, "idempotency_key"))
	workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
	expectedRevision, hasExpectedRevision := parseUint64Arg(args, "expected_revision")

	if isCreate && workerID != "" {
		return "", errors.New("worker_id cannot be specified for action=create; use action=propose or action=update")
	}
	if !isCreate && workerID != "" && (!hasExpectedRevision || expectedRevision == 0) {
		return "", errors.New("expected_revision is required when reproposing worker")
	}

	// Workspace binding from args
	if pb, ok := args["proposed_bindings"].(map[string]any); ok && mapString(pb, "primary") != "" {
		proposedPrimary = strings.TrimSpace(mapString(pb, "primary"))
	} else if lb, ok := args["local_bindings"].(map[string]any); ok && mapString(lb, "primary") != "" {
		proposedPrimary = strings.TrimSpace(mapString(lb, "primary"))
	} else if wsID := strings.TrimSpace(mapString(args, "workspace_id")); wsID != "" {
		proposedPrimary = wsID
	}

	if workerID != "" {
		existing, found, getErr := s.sessions.GetWorker(current.AccountScopeID, workerID)
		if getErr != nil {
			return "", getErr
		}
		if !found {
			return "", store.ErrWorkerNotFound
		}
		if existing.Revision != expectedRevision {
			return "", store.ErrWorkerConflict
		}
		name, description, instructions = existing.Name, existing.Description, existing.Instructions
		if proposedPrimary == "" {
			proposedPrimary = firstNonEmptyString(existing.LocalBindings["primary"], existing.ProposedBindings["primary"])
		}
	}
	// Support legacy document format
	doc, err := planDocumentFromArgsForTool(args, toolName)
	if err != nil {
		return "", err
	}
	if doc != nil {
		var v2 *store.AutomationV2Settings
		if doc.WorkerV2 != nil {
			v2 = doc.WorkerV2
		} else if doc.AutomationV2 != nil {
			v2 = doc.AutomationV2
		}
		if v2 != nil {
			exp := v2.Expiration
			if exp.Kind != "" && exp.Kind != "never" && exp.Kind != "indefinite" {
				return "", fmt.Errorf("worker automation expiration %q is unrepresentable in durable workers", exp.Kind)
			}
			if len(v2.WorkspaceIDs) > 1 {
				return "", fmt.Errorf("multiple workspace_ids in worker_v2 is unsupported: %v", v2.WorkspaceIDs)
			}
			if v2.WorkspaceID != "" && len(v2.WorkspaceIDs) == 1 && strings.TrimSpace(v2.WorkspaceIDs[0]) != strings.TrimSpace(v2.WorkspaceID) {
				return "", errors.New("conflicting workspace_id and workspace_ids in worker_v2")
			}
			if proposedPrimary == "" {
				if v2.WorkspaceID != "" {
					proposedPrimary = strings.TrimSpace(v2.WorkspaceID)
				} else if len(v2.WorkspaceIDs) == 1 {
					proposedPrimary = strings.TrimSpace(v2.WorkspaceIDs[0])
				}
			}
			if v2.Missed != "" && v2.Missed != "skip" {
				return "", fmt.Errorf("unsupported missed policy %q; durable workers require skip", v2.Missed)
			}
			if v2.Overlap != "" && v2.Overlap != "serialize" {
				return "", fmt.Errorf("unsupported overlap policy %q; durable workers require serialize", v2.Overlap)
			}
			var rawDoc map[string]any
			if err := unmarshalPlanToolArg(args["document"], &rawDoc, fmt.Sprintf("%s document", toolName)); err != nil {
				return "", err
			}
			if rawDoc != nil {
				for _, k := range []string{"worker_v2", "automation_v2"} {
					if v2Raw, ok := rawDoc[k].(map[string]any); ok {
						if act, exists := v2Raw["activate_on_accept"]; exists {
							if actBool, ok := act.(bool); ok && !actBool {
								return "", errors.New("unsupported activate_on_accept=false; durable workers require activate_on_accept")
							}
						}
					}
				}
			}
			if (v2.Schedule.Kind == "interval" || v2.Schedule.Kind == "cron") && len(doc.Checkpoints) == 0 {
				return "", fmt.Errorf("scheduled %s worker requires checkpoints; cannot discard schedule", v2.Schedule.Kind)
			}
		}
		if n := strings.TrimSpace(doc.Title); n != "" {
			name = n
		}
		if g := strings.TrimSpace(doc.Info.Goal); g != "" {
			description = g
		}
		if c := strings.TrimSpace(doc.Info.Context); c != "" {
			instructions = c
		} else if description != "" {
			instructions = description
		}
		if len(doc.Checkpoints) > 0 {
			autoName := name
			if autoName == "" {
				autoName = "Scheduled Job"
			}
			actMode := "manual"
			var sched *store.AutomationV2Schedule
			var trig *store.WorkerTriggerConfig
			if v2 != nil {
				switch v2.Schedule.Kind {
				case "interval":
					actMode = "interval"
					sCopy := v2.Schedule
					sched = &sCopy
				case "cron":
					actMode = "cron"
					sCopy := v2.Schedule
					sched = &sCopy
				case "trigger":
					actMode = "external_trigger"
					trig = &store.WorkerTriggerConfig{TriggerKind: "event"}
				case "manual", "":
					actMode = "manual"
				default:
					return "", fmt.Errorf("unsupported schedule kind %q", v2.Schedule.Kind)
				}
			}
			planCopy := *doc
			planCopy.WorkerV2 = nil
			planCopy.AutomationV2 = nil
			planCopy.Automation = nil
			if strings.TrimSpace(planCopy.Title) == "" {
				planCopy.Title = autoName
			}
			if strings.TrimSpace(planCopy.Info.Goal) == "" {
				planCopy.Info.Goal = description
				if planCopy.Info.Goal == "" {
					planCopy.Info.Goal = autoName
				}
			}
			automations = append(automations, store.WorkerAutomationDefinition{
				Name:           autoName,
				Description:    description,
				ActivationMode: actMode,
				Schedule:       sched,
				Trigger:        trig,
				Enabled:        true,
				PlanDocument:   planCopy,
			})
		}
	}

	// Flat field overrides
	if val := strings.TrimSpace(mapString(args, "name")); val != "" {
		name = val
	}
	if val := strings.TrimSpace(mapString(args, "description")); val != "" {
		description = val
	}
	if val := strings.TrimSpace(mapString(args, "instructions")); val != "" {
		instructions = val
	}
	if capsRaw, ok := args["requested_capabilities"]; ok {
		if err := unmarshalJSONArg(capsRaw, &requestedCaps); err != nil {
			return "", err
		}
	}
	if wsReqsRaw, ok := args["workspace_requirements"]; ok {
		if err := unmarshalJSONArg(wsReqsRaw, &workspaceReqs); err != nil {
			return "", err
		}
	}
	hasExplicitAutomations := false
	if autosRaw, ok := args["automations"]; ok {
		hasExplicitAutomations = true
		var flatAutos []store.WorkerAutomationDefinition
		if err := unmarshalJSONArg(autosRaw, &flatAutos); err != nil {
			return "", err
		}
		automations = flatAutos
	}

	if proposedPrimary == "" && defaultWorkspace != "" {
		proposedPrimary = defaultWorkspace
	}
	if proposedPrimary == "" {
		return "", errors.New("target workspace required")
	}

	// Authorize workspace before persistence
	if err := s.authorizeProposedWorkspace(current, proposedPrimary); err != nil {
		return "", err
	}

	if name == "" {
		return "", errors.New("worker name is required")
	}
	if instructions == "" {
		return "", errors.New("worker instructions are required")
	}
	if len(workspaceReqs) == 0 && proposedPrimary != "" {
		workspaceReqs = []store.WorkerWorkspaceRequirement{
			{Role: "primary", Description: "Primary workspace", Required: true},
		}
	}
	var proposedBindings map[string]string
	if proposedPrimary != "" {
		proposedBindings = map[string]string{"primary": proposedPrimary}
	}

	if workerID != "" {
		existing, found, getErr := s.sessions.GetWorker(current.AccountScopeID, workerID)
		if getErr != nil {
			return "", getErr
		}
		if !found {
			return "", store.ErrWorkerNotFound
		}
		if existing.Revision != expectedRevision {
			return "", store.ErrWorkerConflict
		}
		updateReq := store.UpdateWorkerRequest{
			Name:                  &name,
			Description:           &description,
			Instructions:          &instructions,
			RequestedCapabilities: requestedCaps,
			WorkspaceRequirements: workspaceReqs,
			ProposedBindings:      proposedBindings,
			ChangeSummary:         "updated worker proposal",
		}
		if hasExplicitAutomations || len(automations) > 0 {
			updateReq.Automations = automations
			if existing.LifecycleState != store.WorkerLifecycleStatePending {
				// A job document is not a replacement worker definition.
				updateReq.Name, updateReq.Description, updateReq.Instructions = nil, nil, nil
				updateReq.RequestedCapabilities, updateReq.WorkspaceRequirements = nil, nil
				updateReq.ProposedBindings = nil
			}
		}
		if metaRaw, ok := args["metadata"].(map[string]any); ok {
			updateReq.Metadata = metaRaw
		}
		if updateReq.Metadata != nil && mapString(updateReq.Metadata, "project_id") == "" {
			if pid := strings.TrimSpace(mapString(args, "project_id")); pid != "" {
				updateReq.Metadata["project_id"] = pid
			} else if pid := strings.TrimSpace(mapString(existing.Metadata, "project_id")); pid != "" {
				updateReq.Metadata["project_id"] = pid
			} else if current.Metadata != nil {
				if pid := strings.TrimSpace(mapString(current.Metadata, "project_id")); pid != "" {
					updateReq.Metadata["project_id"] = pid
				}
			}
		}
		updated, updErr := s.sessions.UpdateWorker(current.AccountScopeID, current.UserID, workerID, existing.Revision, updateReq)
		if updErr != nil {
			return "", updErr
		}
		return workerProposalToolOutput(updated)
	}

	workerModel, err := s.ResolveWorkerModelProfile(current.AccountScopeID, nil)
	if err != nil {
		return "", err
	}
	createReq := store.CreateWorkerRequest{
		ModelProfile:          workerModel,
		Name:                  name,
		Description:           description,
		Instructions:          instructions,
		RequestedCapabilities: requestedCaps,
		WorkspaceRequirements: workspaceReqs,
		ProposedBindings:      proposedBindings,
		InitialLifecycleState: store.WorkerLifecycleStatePending,
		Automations:           automations,
		IdempotencyKey:        idempKey,
	}
	if metaRaw, ok := args["metadata"].(map[string]any); ok {
		createReq.Metadata = metaRaw
	}
	if createReq.Metadata == nil {
		createReq.Metadata = make(map[string]any)
	}
	if mapString(createReq.Metadata, "project_id") == "" {
		if pid := strings.TrimSpace(mapString(args, "project_id")); pid != "" {
			createReq.Metadata["project_id"] = pid
		} else if current.Metadata != nil {
			if pid := strings.TrimSpace(mapString(current.Metadata, "project_id")); pid != "" {
				createReq.Metadata["project_id"] = pid
			}
		}
	}
	if mapString(createReq.Metadata, "source_session_id") == "" && current.ID != "" {
		createReq.Metadata["source_session_id"] = current.ID
	}
	w, createErr := s.sessions.CreateWorker(context.Background(), current.AccountScopeID, current.UserID, createReq)
	if createErr != nil {
		return "", createErr
	}
	if isCreate {
		raw, err := json.Marshal(map[string]any{
			"status":            "created",
			"lifecycle_state":   w.LifecycleState,
			"worker":            w,
			"next_action":       "await_worker_acceptance",
			"proposed_bindings": w.ProposedBindings,
		})
		return string(raw), err
	}
	return workerProposalToolOutput(w)
}

func automationV2ToolOutput(proposal store.AutomationV2Proposal) (string, error) {
	raw, err := json.Marshal(map[string]any{
		"ok":                true,
		"status":            "pending_review",
		"review_kind":       "automation_v2",
		"title":             proposal.Document.Title,
		"proposal_id":       proposal.ProposalID,
		"proposal_revision": proposal.Revision,
		"proposal":          proposal,
		"worker_review":     proposal.AutomationV2Review,
		"automation_review": proposal.AutomationV2Review,
		"next_action":       "await_automation_acceptance",
		"instruction":       "Stop authoring. Only explicit user Accept worker creates and activates this schedule; no ordinary plan execution or immediate run.",
	})
	return string(raw), err
}

func workerProposalToolOutput(w store.WorkerRecord) (string, error) {
	raw, err := json.Marshal(map[string]any{
		"status":            "pending_review",
		"review_kind":       "worker",
		"title":             "Worker proposal",
		"worker_id":         w.ID,
		"lifecycle_state":   w.LifecycleState,
		"worker":            w,
		"pending_review":    w.PendingReview,
		"revision":          w.Revision,
		"next_action":       "await_worker_acceptance",
		"proposed_bindings": w.ProposedBindings,
		"worker_review": map[string]any{
			"worker_id": w.ID,
			"revision":  w.Revision,
		},
		"instruction": "The durable worker proposal is saved for review. Existing approved work remains unchanged and continues in its current lifecycle. Stop and let the user review it in Tasks > Workers or its worker detail page. Only explicit Accept worker approves this exact revision and workspace. No immediate run; workers without a job wait for a task. Edit using worker_id and expected_revision.",
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

func (s *Service) workerExecutionService() (*WorkerExecutionService, error) {
	if s == nil || s.workerExecution == nil {
		return nil, errors.New("worker execution service is not configured")
	}
	return s.workerExecution, nil
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
		if !agentruntime.IsOrchestratorAgentName(sessionAgent) {
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
		return s.executeWorkerProposalTool(readID, tool.Call{Name: "manage_workers", Arguments: arguments}, profile...)

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
			After:          cursor,
			IncludeDeleted: includeDeleted,
		}
		if ls := mapString(args, "lifecycle_state"); ls != "" {
			query.LifecycleState = store.WorkerLifecycleState(ls)
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
		return s.executeCreateOrProposePendingWorker(readID, args, "manage_workers", true, profile...)

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
		service, err := s.workerExecutionService()
		if err != nil {
			return "", err
		}
		updated, err := service.DisableAutomation(context.Background(), current.AccountScopeID, current.UserID, workerID, automationID, expectedRevision)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status":        "automation_disabled",
			"worker_id":     workerID,
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
		service, err := s.workerExecutionService()
		if err != nil {
			return "", err
		}
		_, err = service.Stop(context.Background(), current.AccountScopeID, current.UserID, workerID, expectedRevision, store.WorkerLifecycleStateDeleted)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(map[string]any{
			"status":    "deleted",
			"worker_id": workerID,
		})
		return string(raw), err

	case "activate", "approve", "accept":
		return "", errors.New("worker activation requires explicit user approval; AI cannot self-approve worker activation or capability grants. Submit proposals for user acceptance via manage_workers action=propose.")

	case "test":
		return s.dispatchWorkerTool(current, args, "test_run")
	case "request":
		return s.dispatchWorkerTool(current, args, "orchestrator")
	case "pause":
		return s.stopWorkerTool(current, args, store.WorkerLifecycleStatePaused)
	case "resume":
		return s.resumeWorkerTool(current, args)
	case "archive":
		return s.stopWorkerTool(current, args, store.WorkerLifecycleStateArchived)

	default:
		return "", errors.New("V1 automation operations are retired; V2 mutations require a Worker plan review and explicit user acceptance")
	}
}
