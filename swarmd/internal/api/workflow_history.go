package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
)

func workflowHistorySummary(store *jobautomation.Store, account, id string, view map[string]any) error {
	records, err := store.History(account, id, "")
	if errors.Is(err, jobautomation.ErrNotFound) {
		view["history_count"] = 0
		return nil
	}
	if err != nil {
		return err
	}
	view["history_count"] = len(records)
	if len(records) > 0 {
		view["last_change"] = map[string]any{"time": records[0].Time, "who": records[0].Who}
	}
	return nil
}
func workflowHistoryNotFound(format string, args ...any) error {
	err := controlMCPToolFailure(format, args...).(controlMCPToolError)
	err.status = http.StatusNotFound
	return err
}

func workflowRevision(args map[string]any, required bool) (uint64, error) {
	raw, ok := args["revision"]
	if !ok {
		if required {
			return 0, controlMCPToolFailure("revision is required")
		}
		return 0, nil
	}
	n, err := strconv.ParseUint(fmt.Sprint(raw), 10, 64)
	if err != nil || n == 0 {
		return 0, controlMCPToolFailure("revision must be a positive integer")
	}
	return n, nil
}
func (s *Server) WorkflowHistory(caller WorkflowCaller, args map[string]any) (any, error) {
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return nil, err
	}
	revision, err := workflowRevision(args, false)
	if err != nil {
		return nil, err
	}
	records, err := store.History(account, controlMCPString(args, "id"), controlMCPString(args, "name"))
	if errors.Is(err, jobautomation.ErrNotFound) {
		return nil, workflowHistoryNotFound("workflow history was not found for id %q or name %q", controlMCPString(args, "id"), controlMCPString(args, "name"))
	}
	if err != nil {
		return nil, err
	}
	if revision != 0 {
		for _, record := range records {
			if record.Revision == revision {
				if record.Deleted {
					return map[string]any{"revision": revision, "deleted": true}, nil
				}
				return portableWorkflow(*record.Definition, record.Definition.Scope.Folder), nil
			}
		}
		return nil, workflowHistoryNotFound("revision %d was not found in retained workflow history", revision)
	}
	for i := range records {
		records[i].Definition = nil
	}
	return map[string]any{"history": records}, nil
}
func (s *Server) RestoreWorkflow(caller WorkflowCaller, args map[string]any) (any, error) {
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return nil, err
	}
	revision, err := workflowRevision(args, true)
	if err != nil {
		return nil, err
	}
	records, err := store.History(account, controlMCPString(args, "id"), controlMCPString(args, "name"))
	if errors.Is(err, jobautomation.ErrNotFound) {
		return nil, workflowHistoryNotFound("workflow history was not found for id %q or name %q", controlMCPString(args, "id"), controlMCPString(args, "name"))
	}
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.Revision == revision {
			if record.Definition != nil && record.Definition.Proposed {
				return nil, controlMCPToolFailure("a proposed revision requires explicit person acceptance, not restore")
			}
			if record.Definition == nil {
				return nil, controlMCPToolFailure("revision %d is a deleted marker and cannot be restored", revision)
			}
			data, err := json.Marshal(portableWorkflow(*record.Definition, ""))
			if err != nil {
				return nil, err
			}
			var save map[string]any
			if err = json.Unmarshal(data, &save); err != nil {
				return nil, err
			}
			return s.saveWorkflowArgs(caller, save, false, "restore")
		}
	}
	return nil, workflowHistoryNotFound("revision %d was not found in retained workflow history", revision)
}
func (s *Server) handleWorkflowHistory(w http.ResponseWriter, r *http.Request, action string) {
	args := map[string]any{}
	if action == "history" {
		for _, key := range []string{"id", "name", "revision"} {
			if v := r.URL.Query().Get(key); v != "" {
				args[key] = v
			}
		}
	} else {
		var body struct {
			ID       string  `json:"id,omitempty"`
			Name     string  `json:"name,omitempty"`
			Revision *uint64 `json:"revision"`
		}
		if err := decodeJSONStrict(w, r, 4096, &body); err != nil {
			writeError(w, 400, err)
			return
		}
		args = map[string]any{"id": body.ID, "name": body.Name}
		if body.Revision != nil {
			args["revision"] = *body.Revision
		}
	}
	caller := workflowCallerFromRequest(r)
	var out any
	var err error
	if action == "history" {
		out, err = s.WorkflowHistory(caller, args)
	} else {
		out, err = s.RestoreWorkflow(caller, args)
	}
	if err != nil {
		var toolErr controlMCPToolError
		if strings.Contains(err.Error(), errRunStepNeedsOwner.Error()) {
			writeError(w, 403, err)
		} else if errors.As(err, &toolErr) {
			status := toolErr.status
			if status == 0 {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
		} else {
			jobAutomationHTTPError(w, err)
		}
		return
	}
	writeJSON(w, 200, out)
}
func workflowHistoryTools() []controlMCPTool {
	props := map[string]any{"id": controlMCPStringProp("Workflow id, including a deleted workflow.", 0, 200), "name": controlMCPStringProp("Workflow name.", 0, 200), "revision": map[string]any{"type": "integer", "minimum": 1}}
	return []controlMCPTool{
		{Name: "swarm_workflow_history", Title: "Workflow history", Description: "Read the last 100 workflow changes newest first, including after deletion, with changed top-level fields and writer. Supply revision for its portable swarm_workflow definition (or deleted marker). Use swarm_restore_workflow to roll back.", InputSchema: controlMCPObject(nil, props), Annotations: controlMCPReadOnly, call: controlMCPWorkflowHistory},
		{Name: "swarm_restore_workflow", Title: "Restore workflow", Description: "Restore a retained definition as a new revision, recreating a deleted workflow. Uses set validation and permissions; run steps require the owner's full key. History remains readable with swarm_workflow_history.", InputSchema: controlMCPObject([]string{"revision"}, props), Annotations: controlMCPWrites, call: controlMCPRestoreWorkflow},
	}
}
