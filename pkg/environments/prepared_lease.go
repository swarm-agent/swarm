package environments

// PreparedDeploymentSource is an exact attachment target, not permission to use it.
// CreatedAt and runtime identity fence replacement; Build fences immutable source,
// catalog generations and image provenance. Legacy deployments cannot be shared.
type PreparedDeploymentSource struct {
	AccountScopeID string `json:"account_scope_id"`
	WorkspaceID string `json:"workspace_id"`
	EnvironmentID string `json:"environment_id"`
	DeploymentID string `json:"deployment_id"`
	CreatedAt int64 `json:"created_at"`
	ContainerID string `json:"container_id"`
	Build ImageBuildResult `json:"build"`
}

func (s PreparedDeploymentSource) Matches(d Deployment) bool {
	return s.AccountScopeID != "" && s.WorkspaceID != "" && s.EnvironmentID != "" && s.DeploymentID != "" &&
		s.CreatedAt > 0 && s.ContainerID != "" && s.Build.OperationID != "" && ValidBuildImageID(s.Build.ImageID) &&
		s.Build.ConnectionID == d.ConnectionID && s.Build.DefinitionDigest != "" && s.Build.ContextDigest != "" &&
		s.Build.Product.WorkspaceID != "" && s.Build.Recipe.WorkspaceID != "" &&
		s.Build.Product.WorkspaceGeneration > 0 && s.Build.Recipe.WorkspaceGeneration > 0 &&
		exactCommitPattern.MatchString(s.Build.Product.Commit) && exactCommitPattern.MatchString(s.Build.Recipe.Commit) &&
		s.AccountScopeID == d.AccountScopeID && s.WorkspaceID == d.WorkspaceID && s.EnvironmentID == d.EnvironmentID &&
		s.DeploymentID == d.ID && s.CreatedAt == d.CreatedAt && s.ContainerID == d.Runtime.ContainerID && d.Build != nil && s.Build == *d.Build && d.IsUsable()
}
