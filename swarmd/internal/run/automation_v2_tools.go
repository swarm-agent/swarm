package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
func (s *Service) executeManageAutomationV2Tool(id, arguments string) (string, error) {
	if s.sessions == nil {
		return "", errors.New("session service required")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", err
	}
	for k := range args {
		if k != "action" && k != "cursor" && k != "limit" && k != "timezone" && k != "workspace_id" {
			return "", errors.New("V2 reads accept only action, cursor, limit, timezone and workspace_id; submit edits through the canonical plan review")
		}
	}
	if mapString(args, "action") == "help" {
		raw, err := json.Marshal(map[string]any{"action": "help", "status": "ok", "instructions": tool.WorkerV2AuthoringInstructions})
		return string(raw), err
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
	workspace := defaultWorkspace
	if wsID := mapString(args, "workspace_id"); wsID != "" {
		workspace = wsID
	}
	if workspace == "" {
		return "", errors.New("target workspace required")
	}
	id = readID
	var out any
	switch mapString(args, "action") {
	case "help":
		out = map[string]any{"action": "help", "status": "ok", "instructions": tool.WorkerV2AuthoringInstructions}
	case "review", "context":
		p, found, err := s.sessions.GetAutomationV2Proposal(current.AccountScopeID, current.UserID, workspace, id)
		if err != nil {
			return "", err
		}
		out = map[string]any{"found": found, "state": "not_created", "instruction": tool.WorkerV2AuthoringInstructions}
		if found {
			out = map[string]any{"found": true, "proposal": p, "worker_review": p.AutomationV2Review, "automation_review": p.AutomationV2Review}
		}
	case "progress":
		progress, err := s.sessions.AutomationV2Progress(current.AccountScopeID, current.UserID, workspace, id, mapString(args, "timezone"), mapString(args, "cursor"), time.Now().UnixMilli())
		if err != nil {
			return "", err
		}
		out = progress
	case "list":
		limit := mapInt(args, "limit")
		if limit == 0 {
			limit = 20
		}
		if limit < 1 || limit > 50 {
			return "", errors.New("limit must be 1–50")
		}
		rows, next, err := s.sessions.ListAutomationV2Records(current.AccountScopeID, current.UserID, workspace, mapString(args, "cursor"), limit)
		if err != nil {
			return "", err
		}
		out = map[string]any{"records": rows, "next_cursor": next}
	default:
		return "", errors.New("V1 automation operations are retired; V2 mutations require a Worker plan review and explicit user acceptance")
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}
