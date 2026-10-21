package api

import (
	"errors"
	"net/http"
	"strings"
)

// swarm_design: N standalone HTML designs (or an edit of one HTML file) in a
// project folder through POST /v3/projects/{id}/design-jobs. The job is read
// with swarm_get_job (design/<project>/<task>) and stopped with
// swarm_stop_job; its files land in the folder, so there is nothing to merge.

const controlMCPDesignNext = "the designs are files in the project folder; nothing to merge or deliver"

func controlMCPDesign(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("sessions:write"); err != nil {
		return nil, err
	}
	brief := strings.TrimSpace(controlMCPString(args, "brief"))
	if brief == "" {
		return nil, controlMCPToolFailure("brief is required: what to design, for whom, and what it must contain")
	}
	place, err := c.place(controlMCPString(args, "project"), controlMCPString(args, "workspace"))
	if err != nil {
		return nil, err
	}
	body := map[string]any{"workspace": place.path, "brief": brief}
	for _, key := range []string{"style", "batch_name", "folder", "edit_path"} {
		if value := strings.TrimSpace(controlMCPString(args, key)); value != "" {
			body[key] = value
		}
	}
	if count := controlMCPInt(args, "count", 0); count > 0 {
		body["count"] = count
	}
	if raw, ok := args["variations"]; ok && raw != nil {
		variations := []string{}
		for _, item := range controlMCPList(raw) {
			text, ok := item.(string)
			if !ok {
				return nil, controlMCPToolFailure("variations must be strings")
			}
			if text = strings.TrimSpace(text); text != "" {
				variations = append(variations, text)
			}
		}
		body["variations"] = variations
	}
	created, err := c.dispatch(http.MethodPost, ProjectsPath+"/"+place.projectID+"/design-jobs", nil, body)
	if err != nil {
		return nil, err
	}
	taskID, _ := controlMCPMap(created["task"])["id"].(string)
	if !controlMCPSafeID.MatchString(taskID) {
		return nil, controlMCPToolFailure("Swarm accepted the design job without a usable task id")
	}
	out, err := c.designJobView(place.projectID, taskID, controlMCPInt(args, "wait_seconds", 0))
	if err != nil {
		return nil, err
	}
	out["project"] = map[string]any{"id": place.projectID, "name": place.projectName}
	out["workspace"] = place.path
	return out, nil
}

// designJobView is swarm_get_job for a design job: its status and each
// design's outcome from the durable job record.
func (c *controlMCPCall) designJobView(projectID, taskID string, wait int) (map[string]any, error) {
	state, _, waited, err := c.waitForJob(controlMCPJobDesign, projectID, taskID, wait)
	if err != nil {
		return nil, err
	}
	task, err := c.task(projectID, taskID)
	if err != nil {
		return nil, err
	}
	job := controlMCPMap(task["design"])
	if job == nil {
		return nil, controlMCPToolFailure("%s is not a design job", controlMCPJobID(controlMCPJobDesign, projectID, taskID))
	}
	status, _ := state["status"].(string)
	out := map[string]any{
		"job_id":    controlMCPJobID(controlMCPJobDesign, projectID, taskID),
		"status":    status,
		"mode":      job["mode"],
		"model":     controlMCPPick(job, "provider", "model", "thinking"),
		"counts":    job["counts"],
		"totals":    controlMCPPick(job, "calls", "tokens", "cost_usd", "cost_complete"),
		"requested": job["count"],
	}
	for _, key := range []string{"folder", "results_path", "edit_path", "error", "results_error", "edit_base_sha256", "edit_final_sha256"} {
		if value, _ := job[key].(string); value != "" {
			out[key] = value
		}
	}
	if sessionID, _ := task["session_id"].(string); sessionID != "" {
		out["session_id"] = sessionID
	}
	if wait > 0 {
		out["waited"] = waited
	}
	items := []map[string]any{}
	for _, raw := range controlMCPList(job["items"]) {
		item := controlMCPMap(raw)
		if item == nil {
			continue
		}
		view := controlMCPPick(item, "n", "path", "status", "tokens", "cost_usd", "repair_edits")
		for _, key := range []string{"error", "repair_refused", "variation"} {
			if text, _ := item[key].(string); text != "" {
				view[key] = truncateRunes(text, 200)
			}
		}
		if diag := controlMCPDesignDiagnostic(item, "check", "diagnostics"); diag != nil {
			view["check"] = diag
		}
		if diag := controlMCPDesignDiagnostic(item, "check_after_repair", "diagnostics_after_repair"); diag != nil {
			view["check_after_repair"] = diag
		}
		items = append(items, view)
	}
	out["items"] = items
	switch status {
	case "completed":
		out["next"] = controlMCPDesignNext
	case "failed", "cancelled":
		out["next"] = "the job did not finish: see error and items; " + controlMCPDesignNext + " for what was written; start a new swarm_design for what is missing"
	default:
		out["next"] = "the designs are being made: call swarm_get_job with wait_seconds to wait"
	}
	return out, nil
}

// controlMCPDesignDiagnostic is one check's verdict and its first finding.
func controlMCPDesignDiagnostic(item map[string]any, statusKey, diagKey string) map[string]any {
	status, _ := item[statusKey].(string)
	if status == "" {
		return nil
	}
	out := map[string]any{"status": status}
	kinds := []string{}
	for _, raw := range controlMCPList(item[diagKey]) {
		if kind, _ := controlMCPMap(raw)["kind"].(string); kind != "" {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) > 0 {
		out["kinds"] = kinds
		if message, _ := controlMCPMap(controlMCPList(item[diagKey])[0])["message"].(string); message != "" {
			out["message"] = truncateRunes(message, 200)
		}
	}
	return out
}

// stopDesignJob cancels a running design job; one that already ended says so.
func (c *controlMCPCall) stopDesignJob(projectID, taskID string) (any, error) {
	jobID := controlMCPJobID(controlMCPJobDesign, projectID, taskID)
	response, err := c.dispatch(http.MethodPost, ProjectsPath+"/"+projectID+"/design-jobs/"+taskID+"/cancel", nil, map[string]any{})
	if err != nil {
		var refused controlMCPToolError
		if errors.As(err, &refused) && refused.status == http.StatusConflict {
			if state, _, stateErr := c.jobState(controlMCPJobDesign, projectID, taskID); stateErr == nil {
				if status, _ := state["status"].(string); status != "in_progress" {
					return map[string]any{"job_id": jobID, "status": status, "stopped": false, "note": "the job already ended; nothing to stop"}, nil
				}
			}
		}
		return nil, controlMCPNotFound(err, "job", jobID, "swarm_list_tasks")
	}
	return map[string]any{"job_id": jobID, "status": response["status"], "next": "the job stops after the designs in progress; swarm_get_job with wait_seconds shows what was written"}, nil
}

// controlMCPDesignRefused is the answer of job tools that do not apply to a
// design job.
func controlMCPDesignRefused(tool string) error {
	return controlMCPToolFailure("%s does not apply to a design job: %s (read it with swarm_get_job, stop it with swarm_stop_job, or start a new swarm_design)", tool, controlMCPDesignNext)
}
