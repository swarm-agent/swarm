package tool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type resultEnvironmentStore struct {
	manageEnvironmentStore
	env environments.Environment
}

func (s *resultEnvironmentStore) Get(a, w, id string) (environments.Environment, bool, error) {
	return *s.env.Clone(), a == s.env.AccountScopeID && w == s.env.WorkspaceID && id == s.env.ID, nil
}

type resultEnvironmentManager struct {
	manageDeploymentLifecycleService
	conn        environments.Connection
	op          environments.EnvironmentOperation
	dep         environments.Deployment
	lease       environments.DeploymentLease
	submissions []lifecycle.SubmitOperationRequest
}

func (m *resultEnvironmentManager) ResolveConnection(_ context.Context, a, w, id string, _ *environments.Environment) (*environments.Connection, error) {
	if a != m.conn.AccountScopeID || (id != "" && id != m.conn.ID) {
		return nil, errors.New("connection mismatch")
	}
	return &m.conn, nil
}
func (m *resultEnvironmentManager) Get(_ context.Context, a, w, id string) (environments.EnvironmentOperation, bool, error) {
	return m.op, a == m.op.AccountScopeID && w == m.op.WorkspaceID && id == m.op.OperationID, nil
}
func (m *resultEnvironmentManager) GetDeployment(a, w, id string) (environments.Deployment, bool, error) {
	return m.dep, a == m.dep.AccountScopeID && w == m.dep.WorkspaceID && id == m.dep.ID, nil
}
func (m *resultEnvironmentManager) GetLease(a, w, id string) (environments.DeploymentLease, bool, error) {
	return m.lease, a == m.lease.AccountScopeID && w == m.lease.WorkspaceID && id == m.lease.ID, nil
}
func (m *resultEnvironmentManager) Submit(_ context.Context, req lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	m.submissions = append(m.submissions, req)
	return &environments.EnvironmentOperation{OperationID: "submitted"}, nil
}

func resultEnvironmentFixture(t *testing.T) (*Runtime, WorkspaceScope, *inspectionLifecycle, *resultEnvironmentStore, *resultEnvironmentManager, map[string]any) {
	t.Helper()
	r := NewRuntime(1)
	ref := ProjectInspectionRequest{ProjectID: "project", TaskID: "task", AttemptID: "attempt", SessionID: "child", HeadCommit: strings.Repeat("e", 40), WorkspaceID: "workspace", WorkspaceGeneration: 1, WorkspacePath: t.TempDir()}
	target := ProjectInspectionTarget{Root: t.TempDir(), Reference: ref, Branch: "agent/result", Base: strings.Repeat("a", 40)}
	resolver := &inspectionLifecycle{target: target}
	r.projectTaskLifecycle = resolver
	store := &resultEnvironmentStore{env: environments.Environment{ID: "env", AccountScopeID: "account", WorkspaceID: "workspace", Build: &environments.ImageBuildDefinition{Product: environments.CommittedBuildSource{WorkspaceID: "workspace", WorkspaceGeneration: 1, Commit: strings.Repeat("a", 40)}, Recipe: environments.CommittedBuildSource{WorkspaceID: "recipe", WorkspaceGeneration: 1, Commit: strings.Repeat("b", 40)}, RecipeDirectory: "recipe", RecipeFile: "recipe/Containerfile"}}}
	r.environmentsStore = store
	definition := *store.env.Build
	definition.Product.Commit = ref.HeadCommit
	build := environments.ImageBuildResult{ProductResult: projectResultBinding(target), OperationID: "build", ConnectionID: "podman", Product: definition.Product, Recipe: definition.Recipe, RecipeFile: definition.RecipeFile, DefinitionDigest: definition.Digest(), ContextDigest: strings.Repeat("c", 64), ImageID: "sha256:" + strings.Repeat("d", 64)}
	manager := &resultEnvironmentManager{conn: environments.Connection{ID: "podman", AccountScopeID: "account", WorkspaceID: "workspace", Kind: environments.ConnectionKindLocalPodman}, op: environments.EnvironmentOperation{OperationID: "build", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "env", Action: "build", Status: environments.OperationStatusSucceeded, Result: environments.OperationResult{Build: &build}}, dep: environments.Deployment{ID: "deployment", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "env", ConnectionID: "podman", Build: &build}, lease: environments.DeploymentLease{ID: "lease", AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: "deployment", EnvironmentID: "env", ConsumerType: environments.ConsumerTypeSession, ConsumerID: "parent"}}
	r.deploymentManager = manager
	r.sessions = environmentAccessSessions{snapshot: pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user", Metadata: map[string]any{"agent_profile": pebblestore.AgentProfile{Name: "swarm"}}}}
	scope := WorkspaceScope{SessionID: "parent", Principal: identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}, RejectScopeExpansion: true}
	reference := map[string]any{"project_id": ref.ProjectID, "task_id": ref.TaskID, "attempt_id": ref.AttemptID, "source_session_id": ref.SessionID, "head_commit": ref.HeadCommit, "workspace_id": ref.WorkspaceID, "workspace_generation": ref.WorkspaceGeneration, "workspace_path": ref.WorkspacePath}
	return r, scope, resolver, store, manager, reference
}

// Purpose: executeManageEnvironments must admit exact-result managed Podman
// build/ensure/deploy/exec/release without inventing mounts, and retain an
// authenticated resolver through asynchronous admission. This tool boundary
// proves wiring; lifecycle tests prove exported source and durable acceptance.
func TestProjectResultManagedRoute(t *testing.T) {
	r, scope, resolver, store, manager, ref := resultEnvironmentFixture(t)
	for _, action := range []string{"build", "ensure", "deploy", "exec", "release"} {
		args := map[string]any{"action": action, "project_result": ref, "environment_id": "env"}
		if action == "ensure" || action == "deploy" {
			args["build_operation_id"] = "build"
		}
		if action == "exec" || action == "release" {
			args["deployment_id"], args["lease_id"] = "deployment", "lease"
		}
		if action == "exec" {
			args["command"] = []string{"true"}
		}
		if _, err := r.executeManageEnvironments(context.Background(), scope, action, args); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		req := manager.submissions[len(manager.submissions)-1]
		if req.WorkspacePath != "" || req.BuildProductResolver == nil || req.Attribution.SessionID != "parent" {
			t.Fatalf("lost image source authority: %+v", req)
		}
		p, err := req.BuildProductResolver(context.Background())
		if err != nil || p.Root != resolver.target.Root || p.Source.Commit != resolver.target.Reference.HeadCommit || p.Source.Commit == store.env.Build.Product.Commit {
			t.Fatalf("configured dev replaced result: %+v %v", p, err)
		}
		resolver.err = errors.New("dirty or advanced task result")
		if _, err := req.BuildProductResolver(context.Background()); err == nil {
			t.Fatal("captured resolver did not revalidate")
		}
		resolver.err = nil
	}
	if _, err := r.executeManageEnvironments(context.Background(), scope, "release-lease", map[string]any{"action": "release", "project_result": ref, "lease_id": "lease"}); err != nil {
		t.Fatal(err)
	}
	if store.env.Build.Product.Commit != strings.Repeat("a", 40) || scope.PrimaryPath != "" {
		t.Fatal("saved definition or ambient scope mutated")
	}
}

// Purpose: tool admission must reject foreign, stale or substituted managed
// provenance/receipts before Submit. Mutating a fake scoped reader's response
// also tests defense against inconsistent service records, not only lookup IDs.
func TestProjectResultManagedRejection(t *testing.T) {
	for _, variant := range []string{"sha", "attempt", "task", "session", "account", "workspace", "connection", "environment", "build-id", "missing-build", "failed", "registry", "dirty", "lease", "lease-account", "lease-deployment", "deployment-result", "mount"} {
		t.Run(variant, func(t *testing.T) {
			r, scope, resolver, store, manager, ref := resultEnvironmentFixture(t)
			args := map[string]any{"action": "ensure", "project_result": ref, "environment_id": "env", "build_operation_id": "build"}
			switch variant {
			case "sha":
				manager.op.Result.Build.Product.Commit = strings.Repeat("f", 40)
			case "attempt":
				resolver.target.Reference.AttemptID = "other"
			case "task":
				resolver.target.Reference.TaskID = "other"
			case "session":
				resolver.target.Reference.SessionID = "other"
			case "account":
				manager.op.AccountScopeID = "other"
			case "workspace":
				manager.op.WorkspaceID = "other"
			case "connection":
				manager.op.Result.Build.ConnectionID = "other"
			case "environment":
				manager.op.EnvironmentID = "other"
			case "build-id":
				args["build_operation_id"] = "other"
			case "missing-build":
				delete(args, "build_operation_id")
			case "failed":
				manager.op.Status = environments.OperationStatusFailed
			case "registry":
				store.env.Build = nil
			case "dirty":
				resolver.err = errors.New("dirty result")
			default:
				args["action"], args["deployment_id"], args["lease_id"], args["command"] = "exec", "deployment", "lease", []string{"true"}
				switch variant {
				case "lease":
					manager.lease.ConsumerID = "other"
				case "lease-account":
					manager.lease.AccountScopeID = "other"
				case "lease-deployment":
					manager.lease.DeploymentID = "other"
				case "deployment-result":
					manager.dep.Build.ProductResult = "other"
				case "mount":
					manager.dep.WorkspacePath = resolver.target.Root
				}
			}
			if _, err := r.executeManageEnvironments(context.Background(), scope, variant, args); err == nil || len(manager.submissions) != 0 {
				t.Fatalf("%s reached mutation: %v", variant, err)
			}
		})
	}
}

// Purpose: extending image validation must preserve real Docker mount identity,
// not permit static dev mounts or Podman pretending to support direct mounts.
// The shared tool guard is the narrowest boundary owning this selection.
func TestProjectResultDockerMountPreserved(t *testing.T) {
	r, scope, resolver, store, manager, ref := resultEnvironmentFixture(t)
	store.env.Build = nil
	store.env.Provisioning.Strategy = environments.SourceStrategy{Kind: environments.SourceStrategyKindLocalMount, LocalMount: &environments.LocalMountConfig{ContainerPath: "/app"}}
	manager.conn.Kind = environments.ConnectionKindLocalDocker
	args := map[string]any{"action": "ensure", "project_result": ref, "environment_id": "env"}
	if _, err := r.executeManageEnvironments(context.Background(), scope, "mount", args); err != nil {
		t.Fatal(err)
	}
	if manager.submissions[0].WorkspacePath != resolver.target.Root || manager.submissions[0].BuildProductResolver != nil {
		t.Fatal("Docker mount was converted to image identity")
	}
	for _, path := range []string{resolver.target.Reference.WorkspacePath, "/unrelated"} {
		store.env.Provisioning.Strategy.LocalMount.HostPath = path
		before := len(manager.submissions)
		if _, err := r.executeManageEnvironments(context.Background(), scope, "bad-mount", args); err == nil || len(manager.submissions) != before {
			t.Fatal("configured source replaced result")
		}
	}
}
