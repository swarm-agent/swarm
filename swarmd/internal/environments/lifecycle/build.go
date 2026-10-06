package lifecycle

import (
	"context"
	"errors"
	"path/filepath"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type buildCatalog interface {
	GetByWorkspaceIDForAccount(string, string) (pebblestore.WorkspaceEntry, bool, error)
}

func (m *DeploymentManager) buildSourceRoot(account string, source environments.CommittedBuildSource) (string, error) {
	catalog, ok := m.workspaces.(buildCatalog)
	if !ok {
		return "", errors.New("authorized build source catalog is unavailable")
	}
	entry, found, err := catalog.GetByWorkspaceIDForAccount(account, source.WorkspaceID)
	if err != nil || !found || entry.WorkspaceGeneration != source.WorkspaceGeneration || entry.AccountScopeID != account || entry.WorkspaceID != source.WorkspaceID || !filepath.IsAbs(entry.Path) {
		return "", errors.New("build source is unauthorized, missing or stale")
	}
	return entry.Path, nil
}

func (m *DeploymentManager) validateBuild(ctx context.Context, account string, env *environments.Environment, conn *environments.Connection) error {
	if env == nil || env.Build == nil {
		return errors.New("saved environment has no managed build definition")
	}
	if err := env.Validate(); err != nil {
		return err
	}
	if conn == nil || conn.Kind != environments.ConnectionKindLocalPodman {
		return errors.New("managed build requires local_podman connection")
	}
	if _, err := m.buildSourceRoot(account, env.Build.Product); err != nil {
		return err
	}
	if _, err := m.buildSourceRoot(account, env.Build.Recipe); err != nil {
		return err
	}
	return ctx.Err()
}

func (m *DeploymentManager) executeBuild(ctx context.Context, opID string, req SubmitOperationRequest, env *environments.Environment, conn *environments.Connection) (out *environments.OperationResult, retErr error) {
	if err := m.validateBuild(ctx, req.AccountScopeID, env, conn); err != nil {
		return nil, err
	}
	product, err := m.buildSourceRoot(req.AccountScopeID, env.Build.Product)
	if err != nil {
		return nil, err
	}
	recipe, err := m.buildSourceRoot(req.AccountScopeID, env.Build.Recipe)
	if err != nil {
		return nil, err
	}
	prov, ok := m.registry.Get(conn.Kind)
	if !ok {
		return nil, ErrProviderNotRegistered
	}
	builder, ok := prov.(provider.ImageBuilder)
	if !ok {
		return nil, errors.New("provider does not implement managed builds")
	}
	accepted := false
	defer func() {
		// Provider success is not admission success. A stale catalog or mismatched
		// receipt must not leave an imported image behind as an accepted build.
		if !accepted {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), m.cleanupTimeout)
			defer cancel()
			if err := builder.CleanupBuild(cleanupCtx, opID); err != nil {
				out = nil
				retErr = provider.BuildCleanupError(retErr, err)
			}
		}
	}()
	result, err := builder.BuildImage(ctx, provider.ImageBuildRequest{OperationID: opID, Connection: conn, Definition: *env.Build, ProductRoot: product, RecipeRoot: recipe})
	if err != nil {
		return nil, err
	}
	if result == nil || !environments.ValidBuildImageID(result.ImageID) || result.DefinitionDigest != env.Build.Digest() || result.OperationID != opID || result.ConnectionID != conn.ID || result.Product != env.Build.Product || result.Recipe != env.Build.Recipe || result.RecipeFile != env.Build.RecipeFile || len(result.ContextDigest) != 64 {
		return nil, errors.New("provider returned mismatched build provenance")
	}
	if err := m.validateBuild(ctx, req.AccountScopeID, env, conn); err != nil {
		return nil, err
	}
	accepted = true
	return &environments.OperationResult{Build: result, Summary: "Managed image built from exact committed inputs"}, nil
}

func (m *DeploymentManager) resolveBuildImage(ctx context.Context, account, workspace, operationID string, env *environments.Environment, conn *environments.Connection) (*environments.ImageBuildResult, error) {
	if env.Build == nil {
		if operationID != "" {
			return nil, errors.New("build operation supplied for an environment without managed build inputs")
		}
		return nil, nil
	}
	if err := m.validateBuild(ctx, account, env, conn); err != nil {
		return nil, err
	}
	if m.operations == nil || operationID == "" {
		return nil, errors.New("managed deployment requires an exact successful build_operation_id")
	}
	op, found, err := m.operations.Get(account, workspace, operationID)
	if err != nil || !found || op.Status != environments.OperationStatusSucceeded || op.Action != environments.OperationActionBuild || op.EnvironmentID != env.ID || op.Result.Build == nil {
		return nil, errors.New("build operation is not an authorized successful result for this environment")
	}
	b := *op.Result.Build
	if b.OperationID != operationID || b.ConnectionID != conn.ID || b.DefinitionDigest != env.Build.Digest() || b.Product != env.Build.Product || b.Recipe != env.Build.Recipe || b.RecipeFile != env.Build.RecipeFile || len(b.ContextDigest) != 64 || !environments.ValidBuildImageID(b.ImageID) {
		return nil, errors.New("build result source or connection is stale or mismatched")
	}
	return &b, nil
}

func runtimeBuildEnvironment(env environments.Environment, b *environments.ImageBuildResult) environments.Environment {
	copy := env.Clone()
	if b != nil {
		copy.Build = nil
		copy.Container.Image = b.ImageID
		copy.Provisioning.Strategy.RegistryImage.Image = b.ImageID
	}
	return *copy
}
