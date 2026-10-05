package environments

import (
	"encoding/json"
	"errors"
	"strings"
)

const MaxTaskEnvironmentAttachments = 16

// TaskEnvironmentAttachment is backend-resolved evidence, never a lease or scope
// grant. Consumers must authorize the task and revalidate Source before acquiring
// their own lease. AttemptID empty means prepared for the task, not yet assigned.
// ExpiresAt is a bounded retention deadline, not an instruction to stop a deployment.
type TaskEnvironmentAttachment struct {
	ID              string                   `json:"id"`
	Revision        int                      `json:"revision"`
	AccountScopeID  string                   `json:"account_scope_id"`
	ProjectID       string                   `json:"project_id"`
	TaskID          string                   `json:"task_id"`
	AttemptID       string                   `json:"attempt_id,omitempty"`
	EnvironmentID   string                   `json:"environment_id"`
	EnvironmentName string                   `json:"environment_name"`
	State           string                   `json:"state"`
	ErrorMessage    string                   `json:"error_message,omitempty"` // sanitized projection only
	OperationID     string                   `json:"operation_id,omitempty"`
	Source          PreparedDeploymentSource `json:"source"`
	ExpiresAt       int64                    `json:"expires_at"`
	RetainForReview bool                     `json:"retain_for_review,omitempty"`
}

func (a TaskEnvironmentAttachment) Validate() error {
	for _, id := range []string{a.ID, a.AccountScopeID, a.ProjectID, a.TaskID, a.EnvironmentID, a.Source.WorkspaceID} {
		if strings.TrimSpace(id) != id || id == "" || len(id) > 256 || strings.ContainsAny(id, "\x00\r\n") {
			return errors.New("attachment requires bounded exact identities")
		}
	}
	if a.Revision < 1 || a.ExpiresAt <= 0 || len(a.AttemptID) > 256 || len(a.OperationID) > 256 || strings.TrimSpace(a.EnvironmentName) == "" || len(a.EnvironmentName) > 256 {
		return errors.New("invalid attachment revision, expiry or metadata")
	}
	if a.Source.AccountScopeID != a.AccountScopeID || a.Source.EnvironmentID != a.EnvironmentID {
		return errors.New("attachment source identity mismatch")
	}
	for _, source := range []CommittedBuildSource{a.Source.Build.Product, a.Source.Build.Recipe} {
		if source.WorkspaceID == "" || len(source.WorkspaceID) > 256 || source.WorkspaceGeneration <= 0 || !exactCommitPattern.MatchString(source.Commit) {
			return errors.New("attachment requires exact product and recipe provenance")
		}
	}
	switch a.State {
	case "preparing", "building":
		if a.OperationID == "" {
			return errors.New("pending attachment requires operation identity")
		}
	case "ready":
		if a.Source.DeploymentID == "" || a.Source.CreatedAt <= 0 || a.Source.ContainerID == "" || a.Source.Build.OperationID == "" || !ValidBuildImageID(a.Source.Build.ImageID) || a.Source.Build.DefinitionDigest == "" || a.Source.Build.ContextDigest == "" {
			return errors.New("ready attachment requires exact deployment generation and build")
		}
	case "failed", "stopped", "stale":
		if a.OperationID == "" && a.Source.DeploymentID == "" {
			return errors.New("attachment requires retained operation or deployment evidence")
		}
	default:
		return errors.New("invalid attachment state")
	}
	// Also bounds nested provenance strings without an unbounded free-form payload.
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > 8192 {
		return errors.New("attachment exceeds 8192 bytes")
	}
	return nil
}

// EffectiveState does not mutate retained source evidence or grant a new attempt access.
func (a TaskEnvironmentAttachment) EffectiveState(attemptID string, now int64) string {
	if (a.AttemptID != "" && a.AttemptID != attemptID) || now >= a.ExpiresAt {
		return "stale"
	}
	return a.State
}
