package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// automationV2PlanCall recognizes typed documents only, never titles or prose.
func automationV2PlanCall(call tool.Call) bool {
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
		return false
	}
	_, ok := doc["automation_v2"]
	return ok
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
		if g.Kind == store.WorkspaceGrantPrimary && g.Available != nil && *g.Available {
			return current, g.WorkspaceID, nil
		}
	}
	return current, "", errors.New("available primary workspace required")
}

func (s *Service) executeAutomationV2PlanTool(id, mode string, call tool.Call) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return "", err
	}
	if canonicalToolName(call.Name) == "exit_plan_mode" {
		if mode != "plan" {
			return "", errors.New("exit_plan_mode requires Plan mode; use plan_manage request_new_plan in Auto")
		}
	} else if mapString(args, "action") != "request_new_plan" {
		return "", errors.New("automation_v2 uses request_new_plan with automation_review for edits, never ordinary plan mutation or approval")
	}
	for key := range args {
		if key != "document" && key != "action" && key != "title" && key != "plan" && key != "automation_review" {
			return "", fmt.Errorf("automation proposal does not accept %s; submit complete instructions and automation_review only for an exact pending edit", key)
		}
	}
	doc, err := planDocumentFromArgsForTool(args, call.Name)
	if err != nil {
		return "", err
	}
	current, workspace, err := s.automationV2ToolSession(id)
	if err != nil {
		return "", err
	}
	var review store.AutomationV2Review
	if raw, ok := args["automation_review"]; ok {
		if err := unmarshalPlanToolArg(raw, &review, "automation_review"); err != nil {
			return "", err
		}
	}
	p, err := s.sessions.ProposeAutomationV2(current.AccountScopeID, current.UserID, workspace, id, doc, review)
	if err != nil {
		return "", err
	}
	return automationV2ToolOutput(p)
}
func automationV2ToolOutput(p store.AutomationV2Proposal) (string, error) {
	raw, err := json.Marshal(map[string]any{"status": "pending_review", "review_kind": "automation_v2", "title": "Automation plan", "created": false, "enabled": false, "next_action": "await_automation_acceptance", "automation_review": p.AutomationV2Review, "permission_id": store.AutomationV2PermissionID(p.ProposalID), "document": p.Document, "instruction": "Stop authoring. Only explicit user Accept automation creates and activates this schedule; no ordinary plan execution or immediate run."})
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
		if k != "action" && k != "cursor" && k != "limit" && k != "timezone" {
			return "", errors.New("V2 reads accept only action, cursor and limit; submit edits through the canonical plan review")
		}
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
	current, workspace, err := s.automationV2ToolSession(readID)
	if err != nil {
		return "", err
	}
	id = readID
	var out any
	switch mapString(args, "action") {
	case "review", "context":
		p, found, err := s.sessions.GetAutomationV2Proposal(current.AccountScopeID, current.UserID, workspace, id)
		if err != nil {
			return "", err
		}
		out = map[string]any{"found": found, "state": "not_created", "instruction": tool.AutomationV2AuthoringInstructions}
		if found {
			out = map[string]any{"found": true, "proposal": p, "automation_review": p.AutomationV2Review}
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
		return "", errors.New("V1 automation operations are retired; V2 mutations require an Automation plan review and explicit user acceptance")
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}
