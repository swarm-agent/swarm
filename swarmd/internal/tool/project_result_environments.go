package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
)

func projectResultBinding(target ProjectInspectionTarget) string {
	raw, _ := json.Marshal(target.Reference)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r *Runtime) projectBuildProduct(scope WorkspaceScope, target ProjectInspectionTarget) lifecycle.BuildProductResolver {
	// Capture the authenticated canonical reference, never caller paths or mutable dev.
	ref := target.Reference
	reference := map[string]any{"project_id": ref.ProjectID, "task_id": ref.TaskID, "attempt_id": ref.AttemptID, "source_session_id": ref.SessionID, "head_commit": ref.HeadCommit, "workspace_id": ref.WorkspaceID, "workspace_generation": ref.WorkspaceGeneration, "workspace_path": ref.WorkspacePath}
	return func(ctx context.Context) (lifecycle.ResolvedBuildProduct, error) {
		current, err := r.resolveProjectInspection(ctx, scope, reference)
		if err != nil {
			return lifecycle.ResolvedBuildProduct{}, err
		}
		if current != target {
			return lifecycle.ResolvedBuildProduct{}, errors.New("selected task result changed")
		}
		return lifecycle.ResolvedBuildProduct{Root: current.Root, Binding: projectResultBinding(current), Source: environments.CommittedBuildSource{WorkspaceID: current.Reference.WorkspaceID, WorkspaceGeneration: current.Reference.WorkspaceGeneration, Commit: current.Reference.HeadCommit}}, nil
	}
}

// A managed image is bound by build provenance, not an invented WorkspacePath.
// Docker retains its existing direct-mount contract; registry images without a
// managed build cannot establish selected-result identity.
func (r *Runtime) validateProjectResultEnvironment(ctx context.Context, scope WorkspaceScope, target ProjectInspectionTarget, req *lifecycle.SubmitOperationRequest) error {
	env, found, err := r.environmentsStore.Get(req.AccountScopeID, req.WorkspaceID, req.EnvironmentID)
	if err != nil || !found || env.AccountScopeID != req.AccountScopeID || env.WorkspaceID != req.WorkspaceID || env.ID != req.EnvironmentID {
		return errors.New("validation environment unavailable")
	}
	resolver, ok := r.deploymentManager.(interface {
		ResolveConnection(context.Context, string, string, string, *environments.Environment) (*environments.Connection, error)
	})
	if !ok {
		return errors.New("validation connection resolver unavailable")
	}
	conn, err := resolver.ResolveConnection(ctx, req.AccountScopeID, req.WorkspaceID, req.ConnectionID, &env)
	if err != nil || conn == nil || conn.AccountScopeID != req.AccountScopeID {
		return errors.New("validation connection unavailable or unauthorized")
	}
	req.ConnectionID = conn.ID
	if env.Build != nil {
		if conn.Kind != environments.ConnectionKindLocalPodman || env.Build.Product.WorkspaceID != target.Reference.WorkspaceID || env.Build.Product.WorkspaceGeneration != target.Reference.WorkspaceGeneration {
			return errors.New("managed task-result validation requires local Podman and the exact catalog source")
		}
		req.BuildProductResolver = r.projectBuildProduct(scope, target)
		req.WorkspacePath = ""
		if req.Action == "build" {
			return nil
		}
		if req.BuildOperationID == "" {
			return errors.New("selected task result requires an exact successful build_operation_id; build with project_result first")
		}
		op, found, err := r.deploymentManager.Get(ctx, req.AccountScopeID, req.WorkspaceID, req.BuildOperationID)
		b := env.Build
		definition := *b
		definition.Product.Commit = target.Reference.HeadCommit
		if err != nil || !found || op.AccountScopeID != req.AccountScopeID || op.WorkspaceID != req.WorkspaceID || op.EnvironmentID != env.ID || op.Action != environments.OperationActionBuild || op.Status != environments.OperationStatusSucceeded || op.Result.Build == nil {
			return errors.New("selected task result requires an authorized successful managed build")
		}
		result := op.Result.Build
		if result.OperationID != req.BuildOperationID || result.ConnectionID != conn.ID || result.ProductResult != projectResultBinding(target) || result.Product != definition.Product || result.Recipe != definition.Recipe || result.RecipeFile != definition.RecipeFile || result.DefinitionDigest != definition.Digest() || !environments.ValidBuildImageID(result.ImageID) || len(result.ContextDigest) != 64 {
			return errors.New("managed build does not match selected task result, recipe or connection")
		}
		return nil
	}
	if req.Action == "build" || conn.Kind != environments.ConnectionKindLocalDocker || env.Provisioning.Strategy.Kind != environments.SourceStrategyKindLocalMount || env.Provisioning.Strategy.LocalMount == nil || (env.Provisioning.Strategy.LocalMount.HostPath != "" && env.Provisioning.Strategy.LocalMount.HostPath != target.Root) {
		return errors.New("project result validation requires a managed local Podman build or local Docker mount of this exact result")
	}
	return nil
}

func (r *Runtime) validateProjectResultDeployment(ctx context.Context, scope WorkspaceScope, target ProjectInspectionTarget, req *lifecycle.SubmitOperationRequest) error {
	if req.DeploymentID == "" && req.Action == "release" && req.LeaseID != "" {
		reader, ok := r.deploymentManager.(interface {
			GetLease(string, string, string) (environments.DeploymentLease, bool, error)
		})
		if !ok {
			return errors.New("validation receipt reader unavailable")
		}
		lease, found, err := reader.GetLease(req.AccountScopeID, req.WorkspaceID, req.LeaseID)
		if err != nil || !found {
			return errors.New("validation receipt unavailable")
		}
		req.DeploymentID = lease.DeploymentID
	}
	dep, found, err := r.deploymentManager.GetDeployment(req.AccountScopeID, req.WorkspaceID, req.DeploymentID)
	if err != nil || !found {
		return errors.New("validation deployment unavailable")
	}
	if dep.Build == nil {
		if dep.WorkspacePath != target.Root {
			return errors.New("validation deployment does not mount the selected isolated result")
		}
		return nil // Existing Docker route retains lifecycle-owned lease admission.
	}
	if dep.AccountScopeID != req.AccountScopeID || dep.WorkspaceID != req.WorkspaceID || dep.Build.ProductResult != projectResultBinding(target) || dep.Build.Product.Commit != target.Reference.HeadCommit || dep.Build.Product.WorkspaceID != target.Reference.WorkspaceID || dep.Build.Product.WorkspaceGeneration != target.Reference.WorkspaceGeneration || dep.Build.ConnectionID != dep.ConnectionID || dep.WorkspacePath != "" {
		return errors.New("validation image deployment belongs to another task result")
	}
	if (req.EnvironmentID != "" && req.EnvironmentID != dep.EnvironmentID) || (req.ConnectionID != "" && req.ConnectionID != dep.ConnectionID) || (req.BuildOperationID != "" && req.BuildOperationID != dep.Build.OperationID) {
		return errors.New("validation deployment reference mismatch")
	}
	reader, ok := r.deploymentManager.(interface {
		GetLease(string, string, string) (environments.DeploymentLease, bool, error)
	})
	if !ok || req.LeaseID == "" {
		return errors.New("own explicit validation receipt required")
	}
	lease, found, err := reader.GetLease(req.AccountScopeID, req.WorkspaceID, req.LeaseID)
	if err != nil || !found || lease.AccountScopeID != req.AccountScopeID || lease.WorkspaceID != req.WorkspaceID || lease.DeploymentID != dep.ID || lease.EnvironmentID != dep.EnvironmentID || lease.ConsumerType != environments.ConsumerTypeSession || lease.ConsumerID != scope.SessionID || lease.Shared || lease.TaskBinding != nil {
		return errors.New("validation receipt belongs to another deployment or consumer")
	}
	req.EnvironmentID, req.ConnectionID, req.BuildOperationID = dep.EnvironmentID, dep.ConnectionID, dep.Build.OperationID
	if err := r.validateProjectResultEnvironment(ctx, scope, target, req); err != nil {
		return err
	}
	op, found, err := r.deploymentManager.Get(ctx, req.AccountScopeID, req.WorkspaceID, dep.Build.OperationID)
	if err != nil || !found || op.Result.Build == nil || *op.Result.Build != *dep.Build {
		return errors.New("validation deployment build receipt changed")
	}
	return nil
}
