package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
)

const ManagedExecutionMetadataKey = "managed_execution"

// ManagedExecutionBinding contains references, never provider credentials. The
// receipt and current durable task/session remain the authority on every call.
type ManagedExecutionBinding struct {
	WorkspaceID string `json:"workspace_id"`
	LeaseID     string `json:"lease_id"`
}

func (r *Runtime) managedExecutionBinding(scope WorkspaceScope) (*ManagedExecutionBinding, error) {
	if r.sessions == nil || scope.SessionID == "" {
		return nil, nil
	}
	s, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("execution session unavailable")
	}
	raw, selected := s.Metadata[ManagedExecutionMetadataKey]
	if !selected {
		return nil, nil
	}
	if !scope.Principal.Valid() || s.ID != scope.SessionID || s.AccountScopeID != scope.Principal.AccountScopeID || s.UserID != scope.Principal.UserID || (scope.Principal.SessionID != "" && scope.Principal.SessionID != s.ID) {
		return nil, errors.New("managed execution session ownership mismatch")
	}
	encoded, err := json.Marshal(raw)
	var binding ManagedExecutionBinding
	if err != nil || json.Unmarshal(encoded, &binding) != nil || binding.WorkspaceID == "" || binding.LeaseID == "" {
		return nil, errors.New("managed execution binding is invalid; local fallback forbidden")
	}
	return &binding, nil
}

func (r *Runtime) executeManagedBash(ctx context.Context, scope WorkspaceScope, binding ManagedExecutionBinding, args map[string]any, onDelta func(string)) (string, error) {
	for _, key := range []string{"workspace_path", "committed_source", "lease_id", "deployment_id", "working_dir"} {
		if _, present := args[key]; present {
			return "", fmt.Errorf("managed Bash does not accept %s overrides", key)
		}
	}
	if scope.ReadOnlyBash || scope.ReviewCheckout != "" {
		return "", errors.New("managed Bash does not support reviewer checkout execution")
	}
	command, err := validateBashArguments(args)
	if err != nil {
		return "", err
	}
	service, ok := r.deploymentManager.(interface {
		GetLease(string, string, string) (environments.DeploymentLease, bool, error)
		ExecTaskLease(context.Context, string, string, string, environments.OperationAttribution, provider.ExecRequest) (*provider.ExecResult, error)
	})
	if !ok {
		return "", errors.New("managed execution service unavailable; local fallback forbidden")
	}
	lease, found, err := service.GetLease(scope.Principal.AccountScopeID, binding.WorkspaceID, binding.LeaseID)
	if err != nil {
		return "", err
	}
	if !found || lease.ID != binding.LeaseID || lease.AccountScopeID != scope.Principal.AccountScopeID || lease.WorkspaceID != binding.WorkspaceID || lease.ConsumerType != environments.ConsumerTypeSession || lease.ConsumerID != scope.SessionID || lease.TaskBinding == nil || lease.PreparedSource == nil || lease.TaskBinding.UserID != scope.Principal.UserID || !lease.IsHeld(time.Now().UnixMilli()) {
		return "", errors.New("managed execution receipt does not belong to current session")
	}
	// Do not rely on the selector metadata for task/attempt/source authority.
	s, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil || !found {
		return "", errors.New("managed execution session unavailable")
	}
	b := lease.TaskBinding
	if asString(s.Metadata["project_id"]) != b.ProjectID || asString(s.Metadata["task_id"]) != b.TaskID || asString(s.Metadata["task_attempt_id"]) != b.AttemptID {
		return "", errors.New("managed execution task attempt mismatch")
	}
	timeout := time.Duration(asInt(args["timeout_ms"], int(defaultBashTimeout.Milliseconds()))) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultBashTimeout
	}
	if timeout > maxBashTimeout {
		timeout = maxBashTimeout
	}
	capture := newCappedBuffer(maxBashOutputViewerBytes)
	stream := newBashStreamWriter(capture, maxBashOutputViewerBytes, onDelta)
	result, runErr := service.ExecTaskLease(ctx, scope.Principal.AccountScopeID, binding.WorkspaceID, binding.LeaseID, environments.OperationAttribution{Actor: scope.Principal.UserID, SessionID: scope.SessionID}, provider.ExecRequest{
		Command: []string{"bash", "-c", sandboxBashScript, "swarm-managed-bash", command}, Timeout: timeout, MaxOutput: maxBashOutputViewerBytes,
	})
	if result != nil {
		_, _ = stream.Write([]byte(result.CombinedOutput()))
	}
	stream.Flush()
	if result == nil {
		if runErr == nil {
			runErr = errors.New("managed provider returned no execution result")
		}
		return "", runErr
	}
	timedOut := errors.Is(runErr, provider.ErrOperationTimedOut)
	if runErr != nil && !timedOut {
		return "", fmt.Errorf("managed Bash execution failed: %w", runErr)
	}
	output, err := bashResponse(command, capture, stream, result.ExitCode, timedOut, nil)
	if err != nil {
		return "", err
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return "", err
	}
	response["execution_mode"] = "managed"
	response["operation_id"] = result.OperationID
	if result.Truncated {
		response["truncated"], response["details_truncated"] = true, true
	}
	response["lease_id"], response["deployment_id"], response["workspace_id"] = lease.ID, lease.DeploymentID, lease.WorkspaceID
	response["source_commit"] = lease.PreparedSource.Build.Product.Commit
	response["source_consistency"] = "prepared_committed_checkout; host file edits are not synchronized"
	encoded, err := json.Marshal(response)
	return strings.TrimSpace(string(encoded)), err
}
