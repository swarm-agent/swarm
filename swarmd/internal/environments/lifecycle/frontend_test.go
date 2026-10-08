package lifecycle

import (
	"context"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: both provisioning entrypoints must persist deployed HTTP intent before
// provider execution, and subsequent definition edits must not rewrite it. Real
// temporary stores plus the existing provider fixture isolate this lifecycle
// persistence boundary without requiring a container engine.
func TestDeploymentFrontendPersistence(t *testing.T) {
	for _, ensure := range []bool{false, true} {
		t.Run(map[bool]string{false: "deploy", true: "ensure"}[ensure], func(t *testing.T) {
			h := setupTestHarness(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn := createTestConnection(t, h.connections, "account", "workspace", "local", "Local")
			env := createTestEnvironment(t, h.environments, "account", "workspace", "web", conn.ID, false, 2, environments.ReleaseBehaviorNone)
			env.Container.ExposedPorts = []environments.PortMapping{{ContainerPort: 8080}}
			env.FrontendEndpoints = []environments.FrontendEndpoint{{ID: "web", Name: "Web", ContainerPort: 8080, Scheme: "http", Path: "/app", HealthPath: "/health"}}
			if _, err := h.environments.Save(env); err != nil {
				t.Fatal(err)
			}
			var dep environments.Deployment
			if ensure {
				result, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "consumer", WorkspacePath: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				dep = result.Deployment
			} else {
				result, err := h.manager.DeployDeployment(ctx, DeployDeploymentRequest{AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID, WorkspacePath: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				dep = result.Deployment
			}
			env.FrontendEndpoints[0].Path = "/edited"
			if _, err := h.environments.Save(env); err != nil {
				t.Fatal(err)
			}
			stored, found, err := h.deployments.Get("account", "workspace", dep.ID)
			if err != nil || !found || stored.Frontend == nil || len(stored.Frontend.Endpoints) != 1 || stored.Frontend.Endpoints[0].Path != "/app" || stored.Frontend.Ports[0].ContainerPort != 8080 {
				t.Fatalf("deployed intent lost or rewritten: %+v %v", stored, err)
			}
		})
	}
}
