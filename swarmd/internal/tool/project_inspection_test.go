package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type inspectionLifecycle struct {
	ProjectTaskLifecycleService
	target ProjectInspectionTarget
	err    error
}

func (f *inspectionLifecycle) ResolveProjectInspection(context.Context, identity.Principal, string, ProjectInspectionRequest) (ProjectInspectionTarget, error) {
	return f.target, f.err
}

type inspectionEnvironmentStore struct{ manageEnvironmentStore }

type inspectionDeploymentManager struct {
	manageDeploymentLifecycleService
	dep       environments.Deployment
	submitted bool
}

func (f *inspectionDeploymentManager) GetDeployment(string, string, string) (environments.Deployment, bool, error) {
	return f.dep, true, nil
}

func (f *inspectionDeploymentManager) Submit(_ context.Context, req lifecycle.SubmitOperationRequest) (*environments.EnvironmentOperation, error) {
	f.submitted = true
	if req.LeaseID != "lease" || req.Attribution.SessionID != "parent" || req.DeploymentID != "deployment" {
		return nil, errors.New("lease or caller attribution lost")
	}
	return &environments.EnvironmentOperation{OperationID: "operation"}, nil
}

// Purpose: inspect_files uses existing rooted read boundaries, not arbitrary
// execution or persistent ambient scope. Runtime.inspectProjectFiles is the
// narrowest layer proving traversal/symlink and mutation rejection after the
// catalog resolver has selected a tree (catalog ownership is tested in api).
func TestProjectInspectionReadBoundary(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "feature"), []byte("selected tree"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	resolver := &inspectionLifecycle{target: ProjectInspectionTarget{Root: root}}
	r.projectTaskLifecycle = resolver
	scope := WorkspaceScope{SessionID: "parent", RejectScopeExpansion: true}
	for _, name := range []string{"read", "list"} {
		path := "feature"
		if name == "list" {
			path = "."
		}
		args := map[string]any{"inspection": map[string]any{"tool": name, "arguments": map[string]any{"path": path}}}
		if out, err := r.inspectProjectFiles(context.Background(), scope, args); err != nil || !strings.Contains(out, "feature") {
			t.Fatalf("%s: %s %v", name, out, err)
		}
	}
	for _, path := range []string{filepath.Join(outside, "secret"), "escape/secret", "../secret"} {
		if _, err := r.inspectProjectFiles(context.Background(), scope, map[string]any{"inspection": map[string]any{"tool": "read", "arguments": map[string]any{"path": path}}}); err == nil {
			t.Fatalf("escaped selected tree: %s", path)
		}
	}
	for _, name := range []string{"write", "edit", "bash", "git_commit"} {
		if _, err := r.inspectProjectFiles(context.Background(), scope, map[string]any{"inspection": map[string]any{"tool": name, "arguments": map[string]any{"path": "feature", "content": "changed"}}}); err == nil {
			t.Fatalf("mutation accepted: %s", name)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "feature"))
	if string(data) != "selected tree" || scope.PrimaryPath != "" || len(scope.Roots) != 0 {
		t.Fatal("inspection mutated content or ambient scope")
	}
	resolver.err = errors.New("stale identity")
	if _, err := r.inspectProjectFiles(context.Background(), scope, map[string]any{}); err == nil {
		t.Fatal("resolver rejection swallowed")
	}
}

// Purpose: project-result validation must reach supervised Submit with the
// parent's lease identity only for a deployment bound to the exact isolated
// tree. executeManageEnvironments is the narrowest tool boundary; the manager
// remains responsible for actual lease admission and execution permissions.
func TestProjectInspectionValidationBinding(t *testing.T) {
	r := NewRuntime(1)
	root := t.TempDir()
	resolver := &inspectionLifecycle{target: ProjectInspectionTarget{Root: root, Reference: ProjectInspectionRequest{TaskID: "task", AttemptID: "attempt", SessionID: "child", WorkspaceID: "workspace", HeadCommit: "head"}}}
	r.projectTaskLifecycle = resolver
	r.environmentsStore = &inspectionEnvironmentStore{}
	r.sessions = environmentAccessSessions{snapshot: pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user", Metadata: map[string]any{"agent_profile": pebblestore.AgentProfile{Name: "swarm"}}}}
	manager := &inspectionDeploymentManager{dep: environments.Deployment{WorkspacePath: root}}
	r.deploymentManager = manager
	scope := WorkspaceScope{SessionID: "parent", Principal: identity.Principal{Type: "user", UserID: "user", AccountScopeID: "account"}, RejectScopeExpansion: true}
	args := map[string]any{"action": "exec", "project_result": map[string]any{"project_id": "project", "task_id": "task", "attempt_id": "attempt", "source_session_id": "child", "head_commit": "head"}, "deployment_id": "deployment", "lease_id": "lease", "command": []any{"go", "test", "./focused"}}
	if _, err := r.executeManageEnvironments(context.Background(), scope, "call", args); err != nil || !manager.submitted {
		t.Fatalf("validation did not reach supervised admission: %v", err)
	}
	for _, path := range []string{"", t.TempDir()} {
		manager.submitted = false
		manager.dep.WorkspacePath = path
		if _, err := r.executeManageEnvironments(context.Background(), scope, "call", args); err == nil || manager.submitted {
			t.Fatal("unrelated/legacy deployment executed")
		}
	}
	manager.dep.WorkspacePath = root
	resolver.err = errors.New("head changed")
	if _, err := r.executeManageEnvironments(context.Background(), scope, "call", args); err == nil || manager.submitted {
		t.Fatal("stale result executed")
	}
	if scope.PrimaryPath != "" {
		t.Fatal("validation installed ambient workspace")
	}
}

// Purpose: initial and resumed runs use these same canonical definitions. The
// arguments required to select inspection and validation must actually be
// exposed; this schema test complements the behavioral dispatch tests above.
func TestProjectInspectionExposedContracts(t *testing.T) {
	projects := manageProjectsDefinition().Parameters["properties"].(map[string]any)
	for _, key := range []string{"inspection", "attempt_id", "source_session_id", "head_commit", "workspace_id", "workspace_generation"} {
		if projects[key] == nil {
			t.Fatalf("missing inspection field %s", key)
		}
	}
	envs := manageEnvironmentsDefinition().Parameters["properties"].(map[string]any)
	if envs["project_result"] == nil {
		t.Fatal("validation selector not exposed")
	}
}
