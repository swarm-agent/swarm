package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/identity"
)

// TaskEnvironmentRequest accepts references only; provenance and consumer identity
// are resolved by the service. An attachment is never implicit selection.
type TaskEnvironmentRequest struct {
	Action string `json:"action"`
	ProjectID string `json:"project_id"`
	TaskID string `json:"task_id"`
	AttemptID string `json:"attempt_id,omitempty"`
	AttachmentID string `json:"attachment_id,omitempty"`
	ExpectedTaskRevision int `json:"expected_task_revision,omitempty"`
	ExpectedAttachmentRevision int `json:"expected_attachment_revision,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	DeploymentID string `json:"deployment_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	BuildOperationID string `json:"build_operation_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	ExpiresAt int64 `json:"expires_at,omitempty"`
	TTLMillis int64 `json:"ttl_millis,omitempty"`
	LeaseID string `json:"lease_id,omitempty"`
	Command []string `json:"command,omitempty"`
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	MaxOutput int `json:"max_output,omitempty"`
}

type TaskEnvironmentResult struct {
	TaskRevision int `json:"task_revision"`
	Attachments []environments.TaskEnvironmentAttachment `json:"attachments"`
	Lease *environments.DeploymentLease `json:"lease,omitempty"`
	Operation *environments.EnvironmentOperation `json:"operation,omitempty"`
	Deployment *TaskDeploymentView `json:"deployment,omitempty"`
	FailureReasons map[string]string `json:"failure_reasons,omitempty"`
}

// TaskDeploymentView intentionally excludes runtime metadata, credentials and leases.
type TaskDeploymentView struct {
	ID string `json:"id"`
	EnvironmentID string `json:"environment_id"`
	Status environments.DeploymentStatus `json:"status"`
	Health environments.HealthStatus `json:"health"`
	CreatedAt int64 `json:"created_at"`
	Endpoints []string `json:"endpoints,omitempty"`
}

type taskEnvironmentService interface {
	ManageTaskEnvironment(context.Context, identity.Principal, string, TaskEnvironmentRequest) (TaskEnvironmentResult, error)
}

func (r *Runtime) executeTaskEnvironment(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	service, ok := r.projectTaskLifecycle.(taskEnvironmentService)
	if !ok {
		return "", errors.New("task environment service unavailable")
	}
	raw, err := json.Marshal(args)
	if err != nil { return "", err }
	var req TaskEnvironmentRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil { return "", err }
	result, err := service.ManageTaskEnvironment(ctx, scope.Principal, scope.SessionID, req)
	if err != nil { return "", err }
	raw, err = json.Marshal(result)
	return string(raw), err
}
