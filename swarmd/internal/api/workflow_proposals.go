package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/tool"
)

const workflowHelp = `Workflows run when tasks or worker jobs end. Scope by role (coder, swarm, finder, designer) or worker ID, with project/workspace selecting the folder. Steps: agent {worker, brief}; reopen {from, message, max_rounds}; run {command, args, timeout_seconds} (owner-authorized script); deliver {to: folder|merge|publish|outbox, path, what}. Conditions use if:[{step, verdict: ok|block}] or {step, status: succeeded|failed}. Optional fields: when: succeeded|failed|always, retries, enabled, result:{kind,path,describe}; agent supports checkout, fixes:security and produce. Example native reviewer with send-back: scope:{role:"coder"}, steps:[{id:"review",type:"agent",worker:"reviewer",brief:"Review the changes"},{type:"reopen",from:"review",if:[{step:"review",verdict:"block"}],max_rounds:2}]. Custom reviewer: replace worker:"reviewer" with the reviewer worker's ID. Worker job reviewed natively: use scope:{worker:"worker-id"} with that same native reviewer and send-back. Propose saves a pending proposal; only the person accepts or discards it. Enable/disable immediately toggle an existing workflow. Fork or reuse: export {name} returns portable JSON (path also writes .swarm/workflows/<name>.json into its folder); import {definition, name:"copy"} adds it under a new name, or import {path, project} reads a committed file or directory. Roll back: history {name} lists revisions, history {name, revision} reads one, restore {name, revision} makes it current as a new revision (also after delete). Import and restore refuse run steps; the machine owner sets those. stop_all {stopped:true} stops every new workflow step on the machine; stopped:false resumes.`

// workflowScopesHeld keeps the scopes of want that held contains.
func workflowScopesHeld(want, held []string) []string {
	var out []string
	for _, scope := range want {
		for _, h := range held {
			if h == scope {
				out = append(out, scope)
				break
			}
		}
	}
	return out
}

// ManageWorkflows is the agent lifecycle boundary: the one service function
// behind every manage_workflows action, for the Orchestrator and Swarm
// Control (tool catalog) alike. It never exposes acceptance and never derives
// machine-owner authority from the caller or its arguments: run steps stay
// the owner's. who names the caller (its session, or its AI key). Changes
// carry automations:write, the scope of the routes behind them. A Swarm
// Control caller holds only those of these scopes its key holds
// (tool.ControlScopesFromContext), so the routes' own checks (export's
// path needs automations:write) see the key, never a wider set.
func (s *Server) ManageWorkflows(ctx context.Context, p identity.Principal, who string, args map[string]any) (any, error) {
	action := controlMCPString(args, "action")
	scopes := []string{"sessions:read", "automations:read", "swarm:read"}
	switch action {
	case "export", "import", "restore", "delete", "stop_all":
		scopes = append(scopes, "automations:write")
	}
	if held, ok := tool.ControlScopesFromContext(ctx); ok {
		scopes = workflowScopesHeld(scopes, held)
	}
	caller := WorkflowCaller{Context: ctx, Principal: p, Who: who, Scopes: scopes}
	switch action {
	case "help":
		return map[string]any{"guide": workflowHelp}, nil
	case "list":
		opts, err := workflowListOptions(func(key string) string { return controlMCPString(args, key) })
		if err != nil {
			return nil, err
		}
		return s.ListWorkflows(caller, opts)
	case "history":
		out, err := s.WorkflowHistory(caller, args)
		if err != nil {
			return nil, err
		}
		if view, ok := out.(map[string]any); ok && view["history"] != nil {
			if proposals, _ := view["pending_proposals"].([]map[string]any); len(proposals) > 0 {
				view["next_action"] = "The pending proposals wait for the person to accept or discard them in their Swarm app; an AI cannot accept one. history with a revision reads that definition; restore with it makes it current."
			}
		}
		return out, nil
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
		return s.proposeWorkflow(caller, args)
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
	case "delete":
		return s.DeleteWorkflow(caller, args)
	case "export":
		return s.ExportWorkflow(caller, args)
	case "import", "restore":
		var out any
		var err error
		if action == "import" {
			out, err = s.ImportWorkflow(caller, args)
		} else {
			out, err = s.RestoreWorkflow(caller, args)
		}
		if errors.Is(err, errRunStepNeedsOwner) {
			// The run-step rule stays (the owner's routes answer 403); an
			// agent's import or restore is refused in its answer, with what
			// to do instead.
			return workflowRunStepRefusal(action), nil
		}
		if err != nil {
			return nil, err
		}
		return workflowCatalogSaveAnswer(out), nil
	case "stop_all":
		stopped, ok := args["stopped"].(bool)
		if !ok {
			return nil, errors.New("stop_all needs stopped: true or false")
		}
		stop, err := s.StopWorkflows(caller, &stopped)
		if err != nil {
			return nil, err
		}
		return workflowStopView(stop), nil
	default:
		return nil, errors.New("unknown manage_workflows action; use help")
	}
}

// workflowByReference reads the workflow a call names by id or name (in id
// or name).
func (s *Server) workflowByReference(caller WorkflowCaller, args map[string]any) (jobautomation.Automation, error) {
	return s.resolveWorkflow(caller, workflowReference(args))
}

// workflowProposeNextAction is what follows a proposal: the person's own
// acceptance, in their app.
const workflowProposeNextAction = "The person accepts or discards this proposal in their Swarm app (the workflow's pending proposal); an AI cannot accept it and nothing changes until then. manage_workflows history with the workflow's name lists it as pending."

// workflowProposeControlKeys are propose's inputs that say how to apply the
// change, never part of the definition.
var workflowProposeControlKeys = map[string]bool{"action": true, "machine": true, "id": true, "replace": true, "expected_revision": true, "rotate_key": true}

// proposeWorkflow saves a proposal for the person to accept. Naming an
// existing workflow (id: its id or name, or name: its exact name) makes the
// call a change merged onto its current revision: only the fields given
// change (each step given by its id merges into that step), and
// expected_revision, when given, must be that revision. replace: true makes
// the given fields the whole definition instead. Any other workflow is new
// and needs its full definition. Fields propose does not take are refused,
// naming them, before anything is saved.
func (s *Server) proposeWorkflow(caller WorkflowCaller, args map[string]any) (any, error) {
	if unknown, declared := tool.CatalogUndeclaredInputs("manage_workflows", "propose", args); len(unknown) > 0 {
		return nil, controlMCPToolFailure("propose does not take %s; nothing was proposed. It takes: %s", strings.Join(unknown, ", "), strings.Join(declared, ", "))
	}
	if scope, ok := args["scope"].(map[string]any); ok {
		if _, folder := scope["folder"]; folder {
			return nil, controlMCPToolFailure("use project/workspace, not scope.folder")
		}
	}
	replace := false
	if raw, ok := args["replace"]; ok {
		if replace, ok = raw.(bool); !ok {
			return nil, controlMCPToolFailure("replace must be true or false")
		}
	}
	expected, err := workflowRevisionField(args, "expected_revision")
	if err != nil {
		return nil, err
	}
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return nil, err
	}
	var current *jobautomation.Automation
	if ref := controlMCPString(args, "id"); ref != "" {
		a, err := s.resolveWorkflow(caller, ref)
		if err != nil {
			return nil, err
		}
		current = &a
	} else if name := controlMCPString(args, "name"); name != "" {
		a, err := store.FindByName(account, name)
		if err == nil {
			current = &a
		} else if !errors.Is(err, jobautomation.ErrNotFound) {
			return nil, err
		}
	}
	if current != nil && current.Proposed {
		return nil, controlMCPToolFailure("%s (%s) is a pending proposal, not a workflow: name the workflow it changes, or propose a new workflow under its own name", current.Name, current.ID)
	}
	if expected != 0 {
		if current == nil {
			return nil, controlMCPToolFailure("expected_revision guards a change of an existing workflow; no workflow is named %q", workflowReference(args))
		}
		if current.Revision != expected {
			return nil, controlMCPToolFailure("%s is at revision %d, not %d: it changed since it was read; read it again (manage_workflows get) and propose on revision %d", current.Name, current.Revision, expected, current.Revision)
		}
	}
	fields := map[string]any{}
	for key, value := range args {
		if !workflowProposeControlKeys[key] {
			fields[key] = value
		}
	}
	definition := fields
	if current != nil && !replace {
		if definition, err = mergeWorkflowChange(*current, fields); err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	var body WorkflowDefinition
	if err = json.Unmarshal(data, &body); err != nil {
		return nil, controlMCPToolFailure("invalid workflow: %s", err)
	}
	if current != nil {
		body.baseID = current.ID
		if replace {
			// A full replace keeps the workflow's name and folder unless
			// it names new ones.
			if strings.TrimSpace(body.Name) == "" {
				body.Name = current.Name
			}
			if body.Project == "" && body.Workspace == "" {
				body.Scope.Folder = current.Scope.Folder
			}
		}
	}
	a, _, _, err := s.SaveWorkflow(caller, body, "propose", false)
	if err != nil {
		return nil, err
	}
	effect := "Accepting adds a new workflow."
	answer := map[string]any{"automation": jobAutomationView(a), "next_action": workflowProposeNextAction}
	if current != nil {
		effect = fmt.Sprintf("Accepting replaces %s (revision %d); it keeps that workflow's id.", current.Name, current.Revision)
		answer["changes"] = jobautomation.ChangedFields(current, &a)
		answer["merged_onto_revision"] = current.Revision
		if replace {
			delete(answer, "merged_onto_revision")
			answer["replaced"] = true
		}
	}
	answer["next"] = effect + " This workflow waits for the person to accept or discard it. Nothing more is needed for this request."
	return answer, nil
}

// mergeWorkflowChange merges a change onto a workflow's current definition:
// each given field replaces the current one (null removes it); scope merges
// key by key; each given step merges into the current step with its id.
// project or workspace binds a new folder.
func mergeWorkflowChange(current jobautomation.Automation, change map[string]any) (map[string]any, error) {
	data, err := json.Marshal(portableWorkflow(current, ""))
	if err != nil {
		return nil, err
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		return nil, err
	}
	delete(merged, "swarm_workflow")
	if len(current.With) > 0 {
		merged["with"] = current.With
	}
	scope, _ := merged["scope"].(map[string]any)
	if scope == nil {
		scope = map[string]any{}
	}
	_, project := change["project"]
	_, workspace := change["workspace"]
	if project || workspace {
		delete(scope, "folder")
	}
	merged["scope"] = scope
	for key, value := range change {
		switch key {
		case "scope":
			given, ok := value.(map[string]any)
			if !ok {
				return nil, controlMCPToolFailure("scope must be an object")
			}
			for k, v := range given {
				if v == nil {
					delete(scope, k)
				} else {
					scope[k] = v
				}
			}
		case "steps":
			if err := mergeWorkflowSteps(current.Name, merged, value); err != nil {
				return nil, err
			}
		default:
			if value == nil {
				delete(merged, key)
			} else {
				merged[key] = value
			}
		}
	}
	return merged, nil
}

// mergeWorkflowSteps merges each given step into the current step with its
// id. Adding, removing or reordering steps is a full replace.
func mergeWorkflowSteps(name string, merged map[string]any, value any) error {
	given, ok := value.([]any)
	if !ok {
		return controlMCPToolFailure("steps must be a list")
	}
	current, _ := merged["steps"].([]any)
	index := map[string]int{}
	ids := []string{}
	for i, raw := range current {
		if step, ok := raw.(map[string]any); ok {
			id, _ := step["id"].(string)
			index[id] = i
			ids = append(ids, id)
		}
	}
	for _, raw := range given {
		step, ok := raw.(map[string]any)
		if !ok {
			return controlMCPToolFailure("each step must be an object")
		}
		id, _ := step["id"].(string)
		i, found := index[strings.TrimSpace(id)]
		if !found {
			named := fmt.Sprintf("no step %q", id)
			if strings.TrimSpace(id) == "" {
				named = "a step without an id"
			}
			return controlMCPToolFailure("a change merges each step into the step with its id: %s has steps %s, and the change has %s. To add, remove or reorder steps, send every step with replace: true", name, strings.Join(ids, ", "), named)
		}
		target, _ := current[i].(map[string]any)
		for k, v := range step {
			if v == nil {
				delete(target, k)
			} else {
				target[k] = v
			}
		}
	}
	return nil
}

// workflowRevisionField reads a positive revision field, or 0 when absent.
func workflowRevisionField(args map[string]any, key string) (uint64, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, nil
	}
	n, err := strconv.ParseUint(fmt.Sprint(raw), 10, 64)
	if err != nil || n == 0 {
		return 0, controlMCPToolFailure("%s must be a positive integer (the revision manage_workflows get shows)", key)
	}
	return n, nil
}

// workflowRunStepRefusal is manage_workflows import's or restore's answer
// when the definition has a run step: refused (envelope state refused), the
// reason, and what to do instead.
func workflowRunStepRefusal(action string) map[string]any {
	next := "Import it without the run step (remove that step from the definition), or ask this machine's owner to import it: the owner imports run steps in their Swarm app or with the owner's own full key."
	if action == "restore" {
		next = "Restore a revision without a run step (history lists them), or ask this machine's owner to restore it: the owner restores run steps in their Swarm app or with the owner's own full key."
	}
	return map[string]any{"status": tool.EnvelopeStateRefused, "reason": errRunStepNeedsOwner.Error(), "next_action": next}
}

// workflowCatalogSaveAnswer is an import's or restore's answer in catalog
// terms: what the saved workflow does now, by its state, and the catalog
// actions that read it.
func workflowCatalogSaveAnswer(out any) any {
	view, ok := out.(map[string]any)
	if !ok {
		return out
	}
	automation, ok := view["automation"].(map[string]any)
	if !ok {
		return out
	}
	view["next"] = workflowCatalogNext(automation["enabled"] == true)
	return view
}

// workflowCatalogNext says what a saved workflow does, enabled or not, in
// catalog tools and actions.
func workflowCatalogNext(enabled bool) string {
	if !enabled {
		return "It is disabled: it does not run until it is enabled (manage_workflows enable with its id or name). manage_workflows get or history reads it."
	}
	return "It runs when matching work ends (a task or worker job in its scope, or its trigger). manage_workflows get or history reads it and deliveries lists its runs; manage_jobs get_job reads a job's steps."
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
