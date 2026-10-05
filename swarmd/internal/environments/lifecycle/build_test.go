package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/provider"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type managedBuildProvider struct {
	*mockProvider
	calls    int
	mismatch bool
}

func (p *managedBuildProvider) BuildImage(_ context.Context, r provider.ImageBuildRequest) (*environments.ImageBuildResult, error) {
	p.calls++
	b := &environments.ImageBuildResult{OperationID: r.OperationID, ConnectionID: r.Connection.ID, DefinitionDigest: r.Definition.Digest(), ImageID: "sha256:" + strings.Repeat("c", 64), ContextDigest: strings.Repeat("d", 64), Product: r.Definition.Product, Recipe: r.Definition.Recipe, RecipeFile: r.Definition.RecipeFile}
	if p.mismatch {
		b.Product.Commit = strings.Repeat("e", 40)
	}
	return b, nil
}
func (p *managedBuildProvider) CleanupBuild(context.Context, string) error { return nil }
func buildLifecycleFixture(t *testing.T) (*supervisedTestHarness, environments.Environment, environments.Connection, *managedBuildProvider) {
	t.Helper()
	h := setupSupervisedHarness(t)
	product, err := h.workspaces.AddForAccount("account", t.TempDir(), "Product")
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := h.workspaces.AddForAccount("account", t.TempDir(), "Recipe")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := h.connections.Save(environments.Connection{ID: "podman", Name: "Podman", AccountScopeID: "account", WorkspaceID: "workspace", Kind: environments.ConnectionKindLocalPodman})
	if err != nil {
		t.Fatal(err)
	}
	env := createTestEnvironment(t, h.environments, "account", "workspace", "build-env", conn.ID, true, 1, environments.ReleaseBehaviorNone)
	env.Container.Image = environments.ManagedBuildImage
	env.Container.RootlessSystemd = &environments.RootlessSystemd{CgroupNamespace: "private", Network: "slirp4netns", PidsLimit: 1024}
	env.Provisioning = environments.WorkspaceProvisioning{Strategy: environments.SourceStrategy{Kind: environments.SourceStrategyKindRegistryImage, RegistryImage: &environments.RegistryImageConfig{Image: environments.ManagedBuildImage, PullPolicy: "never"}}}
	env.Build = &environments.ImageBuildDefinition{Product: environments.CommittedBuildSource{WorkspaceID: product.WorkspaceID, WorkspaceGeneration: product.WorkspaceGeneration, Commit: strings.Repeat("a", 40)}, Recipe: environments.CommittedBuildSource{WorkspaceID: recipe.WorkspaceID, WorkspaceGeneration: recipe.WorkspaceGeneration, Commit: strings.Repeat("b", 40)}, RecipeDirectory: "recipe", RecipeFile: "recipe/Containerfile"}
	env, err = h.environments.Save(env)
	if err != nil {
		t.Fatal(err)
	}
	p := &managedBuildProvider{mockProvider: newMockProvider(environments.ConnectionKindLocalPodman)}
	h.manager.registry.Register(p)
	return h, env, conn, p
}

// Purpose: DeploymentManager build admission must reject foreign/stale sources
// before provider effects, and executeBuild must reject inconsistent provenance.
// Real catalog stores plus an injected builder prove that authority boundary.
func TestManagedBuildCatalogAuthority(t *testing.T) {
	h, env, conn, p := buildLifecycleFixture(t)
	ctx := context.Background()
	for _, account := range []string{"foreign", "account"} {
		bad := env.Clone()
		if account == "account" {
			bad.Build.Product.WorkspaceGeneration++
		}
		if _, err := h.manager.executeBuild(ctx, "op_rejected", SubmitOperationRequest{AccountScopeID: account}, bad, &conn); err == nil {
			t.Fatal("unauthorized source accepted")
		}
	}
	if p.calls != 0 {
		t.Fatal("unauthorized request reached builder")
	}
	p.mismatch = true
	if _, err := h.manager.executeBuild(ctx, "op_mismatch", SubmitOperationRequest{AccountScopeID: "account"}, &env, &conn); err == nil {
		t.Fatal("mismatched product accepted")
	}
	p.mismatch = false
	result, err := h.manager.executeBuild(ctx, "op_result", SubmitOperationRequest{AccountScopeID: "account"}, &env, &conn)
	if err != nil || result.Build.Product != env.Build.Product {
		t.Fatalf("%+v %v", result, err)
	}
	runtime := runtimeBuildEnvironment(env, result.Build)
	if runtime.Container.Image != result.Build.ImageID || runtime.Build != nil || env.Container.Image != environments.ManagedBuildImage || env.Build == nil {
		t.Fatal("runtime substitution mutated saved definition")
	}
}

// Purpose: resolveBuildImage must require an account/workspace-scoped successful
// durable operation with exact environment, connection and source identity.
// Real operation transitions prove rejection without deployment side effects.
func TestManagedBuildResultAdmission(t *testing.T) {
	h, env, conn, _ := buildLifecycleFixture(t)
	ctx := context.Background()
	res, err := h.manager.executeBuild(ctx, "op_receipt", SubmitOperationRequest{AccountScopeID: "account"}, &env, &conn)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	op := environments.EnvironmentOperation{OperationID: "op_receipt", AccountScopeID: "account", WorkspaceID: "workspace", Action: environments.OperationActionBuild, EnvironmentID: env.ID, BuildConnectionID: conn.ID, BuildDefinition: env.Build, Status: environments.OperationStatusQueued, CreatedAt: now, ObservedAt: now, Deadline: now + 60000}
	op, _, err = h.opStore.AdmitOperation(op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.manager.resolveBuildImage(ctx, "account", "workspace", op.OperationID, &env, &conn); err == nil {
		t.Fatal("queued operation accepted")
	}
	op, err = h.opStore.TransitionOperation(pebblestore.OperationTransitionInput{AccountScopeID: "account", WorkspaceID: "workspace", OperationID: op.OperationID, ExpectedRevision: op.Revision, TargetStatus: environments.OperationStatusRunning, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	op, err = h.opStore.TransitionOperation(pebblestore.OperationTransitionInput{AccountScopeID: "account", WorkspaceID: "workspace", OperationID: op.OperationID, ExpectedRevision: op.Revision, TargetStatus: environments.OperationStatusSucceeded, Result: res, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.manager.resolveBuildImage(ctx, "account", "workspace", op.OperationID, &env, &conn); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []string{"foreign", "workspace", "connection", "source", "missing"} {
		t.Run(tc, func(t *testing.T) {
			account, workspace, id := "account", "workspace", op.OperationID
			bad := env.Clone()
			c := conn
			switch tc {
			case "foreign":
				account = "other"
			case "workspace":
				workspace = "other"
			case "connection":
				c.ID = "other"
			case "source":
				bad.Build.Product.Commit = strings.Repeat("e", 40)
			case "missing":
				id = ""
			}
			if _, err := h.manager.resolveBuildImage(ctx, account, workspace, id, bad, &c); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
	deps, err := h.deployments.ListByEnvironment("account", "workspace", env.ID, 10)
	if err != nil || len(deps) != 0 {
		t.Fatal("receipt lookup mutated deployments")
	}
}
