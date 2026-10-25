package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// WorkflowCaller carries authenticated authority, not workflow-supplied data.
type WorkflowCaller struct {
	Context      context.Context
	Principal    identity.Principal
	MachineOwner bool
	Who          string
	// Scopes are retained for project and filesystem authorization.
	Scopes []string
}

func workflowCallerFromRequest(r *http.Request) WorkflowCaller {
	p, _ := PrincipalFromRequest(r)
	caller := WorkflowCaller{Context: r.Context(), Principal: p, MachineOwner: true, Who: "owner"}
	if token, ok := ScopedTokenFromRequest(r); ok {
		caller.MachineOwner = token.HasScope("admin")
		caller.Scopes = token.Scopes
		if token.ID != "" {
			caller.Who = token.ID
		}
	}
	return caller
}

// workflowContext reuses canonical project selection without making MCP the
// workflow persistence authority. Native callers supply the same trusted identity.
func (s *Server) workflowContext(caller WorkflowCaller) *controlMCPCall {
	ctx := caller.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = identity.ContextWithPrincipal(ctx, caller.Principal)
	ctx = context.WithValue(ctx, productPrincipalRequestContextKey, caller.Principal)
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, JobAutomationsPath, nil)
	scopes := caller.Scopes
	if caller.MachineOwner {
		scopes = []string{"admin"}
	}
	r = requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{ID: caller.Who, Scopes: scopes})
	return &controlMCPCall{server: s, request: r, next: s.internalRouteHandler()}
}

// WorkflowDefinition is the save contract shared by HTTP and native callers.
type WorkflowDefinition = jobAutomationBody

func (s *Server) SaveWorkflow(caller WorkflowCaller, body WorkflowDefinition, source string, skipUnchanged bool) (jobautomation.Automation, bool, bool, error) {
	var zero jobautomation.Automation
	if !caller.Principal.Valid() {
		return zero, false, false, identity.ErrPrincipalRequired
	}
	store, err := s.jobAutomationStore()
	if err != nil {
		return zero, false, false, err
	}
	a := body.automation()
	if a.Scope.Folder == "" {
		place, err := s.workflowContext(caller).place(body.Project, body.Workspace)
		if err != nil {
			return zero, false, false, err
		}
		a.Scope.Folder = place.path
	}
	if err := a.Normalize(); err != nil {
		return zero, false, false, err
	}
	if source != "propose" && jobautomation.HasRunStep(a.Steps) && !caller.MachineOwner {
		return zero, false, false, errRunStepNeedsOwner
	}
	account := caller.Principal.AccountScopeID
	if err := s.jobAutomationFixesError(account, a.Steps); err != nil {
		return zero, false, false, controlMCPToolFailure("%s", err)
	}
	if source == "propose" {
		saved, err := store.Propose(account, caller.Who, a)
		return saved, false, false, err
	}
	if skipUnchanged {
		previous, err := store.FindByName(account, a.Name)
		if err != nil && !errors.Is(err, jobautomation.ErrNotFound) {
			return zero, false, false, err
		}
		if err == nil && workflowDefinitionsEqual(previous, a) {
			return previous, false, true, nil
		}
	}
	saved, created, err := store.PutChange(account, a, jobautomation.Change{Source: source, Who: caller.Who})
	return saved, created, false, err
}

const workflowSaveNext = "it runs when a matching job ends; swarm_list_workflows shows it and its deliveries, swarm_get_job a job's steps"

func workflowSaveView(a jobautomation.Automation, created bool) map[string]any {
	return map[string]any{"automation": jobAutomationView(a), "created": created, "next": workflowSaveNext}
}

func (c *controlMCPCall) workflowDispatch(method, path string, query url.Values, body any) (map[string]any, error) {
	out, err := c.dispatch(method, path, query, body)
	if err != nil {
		var refusal controlMCPToolError
		if errors.As(err, &refusal) {
			var response map[string]any
			if json.Unmarshal([]byte(refusal.body), &response) == nil {
				if message, ok := response["error"].(string); ok {
					refusal.message = message
					return nil, refusal
				}
			}
		}
		return nil, controlMCPToolFailure("%s", err)
	}
	delete(out, "ok")
	return out, nil
}

func (s *Server) workflowAccount(caller WorkflowCaller) (*jobautomation.Store, string, error) {
	if !caller.Principal.Valid() {
		return nil, "", identity.ErrPrincipalRequired
	}
	store, err := s.jobAutomationStore()
	return store, caller.Principal.AccountScopeID, err
}
func (s *Server) GetWorkflow(caller WorkflowCaller, id string) (jobautomation.Automation, error) {
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return jobautomation.Automation{}, err
	}
	return store.Get(account, id)
}

// StopWorkflows reads the switch when stopped is nil, otherwise updates it.
func (s *Server) StopWorkflows(caller WorkflowCaller, stopped *bool) (jobautomation.StopState, error) {
	store, _, err := s.workflowAccount(caller)
	if err != nil {
		return jobautomation.StopState{}, err
	}
	if stopped == nil {
		return store.StopState()
	}
	return s.setJobAutomationsStopped(*stopped, caller.Principal.UserID)
}
func controlMCPListAutomations(c *controlMCPCall, _ map[string]any) (any, error) {
	return c.workflowDispatch(http.MethodGet, JobAutomationsPath, nil, nil)
}
func controlMCPDeleteAutomation(c *controlMCPCall, args map[string]any) (any, error) {
	id := controlMCPString(args, "id")
	if id == "" {
		return c.workflowDispatch(http.MethodDelete, JobAutomationsPath, url.Values{"name": {controlMCPString(args, "name")}}, nil)
	}
	if !controlMCPSafeID.MatchString(id) {
		return nil, controlMCPToolFailure("invalid workflow id")
	}
	return c.workflowDispatch(http.MethodDelete, JobAutomationsPath+"/"+id, nil, nil)
}
func controlMCPWorkflowHistory(c *controlMCPCall, args map[string]any) (any, error) {
	query := url.Values{}
	for _, key := range []string{"id", "name", "revision"} {
		if value, ok := args[key]; ok {
			data, _ := json.Marshal(value)
			if text, ok := value.(string); ok {
				query.Set(key, text)
			} else {
				query.Set(key, string(data))
			}
		}
	}
	return c.workflowDispatch(http.MethodGet, JobAutomationsPath+"/history", query, nil)
}
func controlMCPRestoreWorkflow(c *controlMCPCall, args map[string]any) (any, error) {
	return c.workflowDispatch(http.MethodPost, JobAutomationsPath+"/restore", nil, args)
}
func controlMCPImportWorkflow(c *controlMCPCall, args map[string]any) (any, error) {
	return c.workflowDispatch(http.MethodPost, JobAutomationsPath+"/import", nil, args)
}
func controlMCPExportWorkflow(c *controlMCPCall, args map[string]any) (any, error) {
	return c.workflowDispatch(http.MethodPost, JobAutomationsPath+"/export", nil, args)
}
