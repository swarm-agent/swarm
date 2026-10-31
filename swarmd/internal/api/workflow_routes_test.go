package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// Test helpers for the owner's workflow routes (handleJobAutomations). The
// route tests drive those routes through a Swarm Control call's request (its
// key, its principal), the way the removed swarm_* workflow tools did, so the
// routes' own checks decide: an AI key's run-step refusal, import and export
// path safety, history and listing. They are not a Swarm Control surface:
// Swarm Control serves workflows only through manage_workflows, whose propose
// saves a proposal for the person, not a workflow.

// routeWorkflow calls one workflow route and answers its JSON, or the route's
// own refusal text.
func routeWorkflow(c *controlMCPCall, method, path string, query url.Values, body any) (map[string]any, error) {
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

// routeWorkflowSave saves a workflow: POST /v3/workflows.
func routeWorkflowSave(c *controlMCPCall, args map[string]any) (any, error) {
	return routeWorkflow(c, http.MethodPost, JobAutomationsPath, nil, args)
}

// routeWorkflowExport is POST /v3/workflows/export.
func routeWorkflowExport(c *controlMCPCall, args map[string]any) (any, error) {
	return routeWorkflow(c, http.MethodPost, JobAutomationsPath+"/export", nil, args)
}

// routeWorkflowImport is POST /v3/workflows/import.
func routeWorkflowImport(c *controlMCPCall, args map[string]any) (any, error) {
	return routeWorkflow(c, http.MethodPost, JobAutomationsPath+"/import", nil, args)
}

// routeWorkflowRestore is POST /v3/workflows/restore.
func routeWorkflowRestore(c *controlMCPCall, args map[string]any) (any, error) {
	return routeWorkflow(c, http.MethodPost, JobAutomationsPath+"/restore", nil, args)
}

// routeWorkflowStop is POST /v3/workflows/stop.
func routeWorkflowStop(c *controlMCPCall, args map[string]any) (any, error) {
	return routeWorkflow(c, http.MethodPost, JobAutomationsPath+"/stop", nil, args)
}

// routeWorkflowList is GET /v3/workflows with its delivery_id and detail.
func routeWorkflowList(c *controlMCPCall, args map[string]any) (any, error) {
	query := url.Values{}
	for _, key := range []string{"delivery_id", "detail"} {
		if value := controlMCPString(args, key); value != "" {
			query.Set(key, value)
		}
	}
	return routeWorkflow(c, http.MethodGet, JobAutomationsPath, query, nil)
}

// routeWorkflowDelete is DELETE /v3/workflows/{id}.
func routeWorkflowDelete(c *controlMCPCall, args map[string]any) (any, error) {
	return routeWorkflow(c, http.MethodDelete, JobAutomationsPath+"/"+controlMCPString(args, "id"), nil, nil)
}

// routeWorkflowHistory is GET /v3/workflows/history with id, name and
// revision.
func routeWorkflowHistory(c *controlMCPCall, args map[string]any) (any, error) {
	query := url.Values{}
	for _, key := range []string{"id", "name", "revision"} {
		if value, ok := args[key]; ok {
			if text, isText := value.(string); isText {
				query.Set(key, text)
			} else {
				data, _ := json.Marshal(value)
				query.Set(key, string(data))
			}
		}
	}
	return routeWorkflow(c, http.MethodGet, JobAutomationsPath+"/history", query, nil)
}
