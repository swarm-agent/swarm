package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
)

// ProjectFlowAction is manage_projects flow: a project's flow over a period
// (today, 7d, 30d), where its tasks' time and tokens went.
const ProjectFlowAction = "flow"

// ProjectTaskFlowService computes flow reports from durable records. The API
// server implements it (project_task_flow.go there) and serves the same
// functions as GET /v3/projects/{p}/tasks/{t}/flow and GET
// /v3/projects/{p}/flow; it is optional so legacy lifecycle adapters fail
// closed.
type ProjectTaskFlowService interface {
	ProjectTaskFlow(ctx context.Context, p identity.Principal, projectID, taskID string) (jobautomation.FlowReport, error)
	ProjectFlowRollup(ctx context.Context, p identity.Principal, projectID, period string) (jobautomation.FlowRollup, error)
}

// executeProjectFlow answers manage_projects flow.
func (r *Runtime) executeProjectFlow(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	projectID := strings.TrimSpace(asString(args["project_id"]))
	if projectID == "" {
		return "", errors.New("manage_projects flow requires project_id")
	}
	service, ok := r.getProjectTaskLifecycleService().(ProjectTaskFlowService)
	if !ok {
		return "", errors.New("project flow report service unavailable")
	}
	rollup, err := service.ProjectFlowRollup(ctx, scope.Principal, projectID, asString(args["period"]))
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{
		"tool": manageProjectsToolName, "action": ProjectFlowAction, "status": "ok", "path_id": toolPathID(manageProjectsToolName),
		"flow": rollup, "flow_markdown": jobautomation.FlowRollupMarkdown(rollup),
	})
	return string(out), err
}
