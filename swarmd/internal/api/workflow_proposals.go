package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
)

const workflowHelp = `Workflows run when tasks or worker jobs end. Scope by role (coder, swarm, finder, designer) or worker ID, with project/workspace selecting the folder. Steps: agent {worker, brief}; reopen {from, message, max_rounds}; run {command, args, timeout_seconds} (owner-authorized script); deliver {to: folder|merge|publish|outbox, path, what}. Conditions use if:[{step, verdict: ok|block}] or {step, status: succeeded|failed}. Optional fields: when: succeeded|failed|always, retries, enabled, result:{kind,path,describe}; agent supports checkout, fixes:security and produce. Example native reviewer with send-back: scope:{role:"coder"}, steps:[{id:"review",type:"agent",worker:"reviewer",brief:"Review the changes"},{type:"reopen",from:"review",if:[{step:"review",verdict:"block"}],max_rounds:2}]. Custom reviewer: replace worker:"reviewer" with the reviewer worker's ID. Worker job reviewed natively: use scope:{worker:"worker-id"} with that same native reviewer and send-back. Propose saves a pending proposal; only the person accepts or discards it. Enable/disable immediately toggle an existing workflow.`

// ManageWorkflows is the native lifecycle boundary. It never exposes acceptance
// and never derives machine-owner authority from the agent's supplied arguments.
func (s *Server) ManageWorkflows(ctx context.Context, p identity.Principal, sessionID string, args map[string]any) (any, error) {
	caller := WorkflowCaller{Context: ctx, Principal: p, Who: sessionID, Scopes: []string{"sessions:read", "automations:read", "swarm:read"}}
	switch controlMCPString(args, "action") {
	case "help":
		return map[string]any{"guide": workflowHelp}, nil
	case "list":
		opts, err := workflowListOptions(func(key string) string { return controlMCPString(args, key) })
		if err != nil {
			return nil, err
		}
		return s.ListWorkflows(caller, opts)
	case "history":
		return s.WorkflowHistory(caller, args)
	case "get":
		a, err := s.workflowByReference(caller, args)
		if err != nil {
			return nil, err
		}
		return map[string]any{"automation": jobAutomationView(a)}, nil
	case "deliveries":
		// One line per delivery; delivery_id reads one in bounded detail.
		opts, err := workflowListOptions(func(key string) string { return controlMCPString(args, key) })
		if err != nil {
			return nil, err
		}
		if opts.DeliveryID != "" {
			return s.ListWorkflows(caller, WorkflowListOptions{DeliveryID: opts.DeliveryID})
		}
		store, account, err := s.workflowAccount(caller)
		if err != nil {
			return nil, err
		}
		jobID := controlMCPString(args, "job_id")
		list, err := store.ListDeliveries(account, jobautomation.DeliveryFilter{AutomationID: controlMCPString(args, "id"), JobID: jobID, Limit: 50})
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, d := range list {
			line := workflowDeliveryLine(d)
			line["automation"] = d.AutomationName
			out = append(out, line)
		}
		answer := map[string]any{"deliveries": out, "next": "pass delivery_id for one delivery's steps, with its full findings"}
		if note := deliveryJobIDNote(jobID, len(list)); note != "" {
			answer["note"] = note
		}
		return answer, nil
	case "propose":
		data, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		var body WorkflowDefinition
		if err = json.Unmarshal(data, &body); err != nil {
			return nil, err
		}
		if body.Scope.Folder != "" {
			return nil, errors.New("use project/workspace, not scope.folder")
		}
		a, _, _, err := s.SaveWorkflow(caller, body, "propose", false)
		if err != nil {
			return nil, err
		}
		effect := "Accepting adds a new workflow."
		if a.BaseID != "" {
			effect = fmt.Sprintf("Accepting replaces %s (revision %d); it keeps that workflow's id.", a.Name, a.BaseRevision)
		}
		return map[string]any{"automation": jobAutomationView(a), "next": effect + " This workflow waits for the person to accept or discard it. Nothing more is needed for this request."}, nil
	case "enable", "disable":
		a, err := s.workflowByReference(caller, args)
		if err != nil {
			return nil, err
		}
		if a.Proposed {
			return nil, errors.New("a proposal must be accepted by the person before it can be enabled")
		}
		store, account, err := s.workflowAccount(caller)
		if err != nil {
			return nil, err
		}
		a, err = store.SetEnabled(account, a.ID, controlMCPString(args, "action") == "enable", caller.Who)
		if err != nil {
			return nil, err
		}
		return map[string]any{"automation": jobAutomationView(a)}, nil
	default:
		return nil, errors.New("unknown manage_workflows action; use help")
	}
}

func (s *Server) workflowByReference(caller WorkflowCaller, args map[string]any) (jobautomation.Automation, error) {
	if id := controlMCPString(args, "id"); id != "" {
		return s.GetWorkflow(caller, id)
	}
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return jobautomation.Automation{}, err
	}
	return store.FindByName(account, controlMCPString(args, "name"))
}

func (s *Server) handleWorkflowAccept(w http.ResponseWriter, r *http.Request, id string) {
	p, _ := PrincipalFromRequest(r)
	// Scoped AI keys cannot become people even if their principal is user-shaped.
	if (p.Type != identity.PrincipalTypeUser && p.Type != "user") || isWorkflowAIRequest(r) {
		writeError(w, http.StatusForbidden, errors.New("explicit human user required to accept workflow proposal; AI cannot self-approve"))
		return
	}
	var body struct {
		ExpectedRevision uint64 `json:"expected_revision"`
	}
	if err := decodeJSONStrict(w, r, 4096, &body); err != nil || body.ExpectedRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("expected_revision is required"))
		return
	}
	store, account, err := s.workflowAccount(workflowCallerFromRequest(r))
	if err != nil {
		jobAutomationHTTPError(w, err)
		return
	}
	a, err := store.AcceptProposal(account, id, body.ExpectedRevision, p.UserID, func(a jobautomation.Automation) error {
		if err := runStepOwnerError(r, a.Steps); err != nil {
			return err
		}
		return s.validateWorkflowTrigger(p, a)
	})
	if err != nil {
		jobAutomationHTTPError(w, err)
		return
	}
	key, err := s.saveTriggerKey(a, false)
	if err != nil {
		jobAutomationHTTPError(w, err)
		return
	}
	view := map[string]any{"ok": true, "automation": jobAutomationView(a)}
	if key != "" {
		view["trigger_key"] = key
	}
	writeJSON(w, http.StatusOK, view)
}

func isWorkflowAIRequest(r *http.Request) bool {
	if token, ok := ScopedTokenFromRequest(r); ok {
		for _, scope := range token.Scopes {
			switch scope {
			case "swarm:read", "swarm:write", "swarm:approve", "swarm:manage":
				return true
			}
		}
	}
	return false
}
