package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: AcquirePreparedLease must fence exact prepared source/runtime identity and
// isolate consumers; validateAdmission must reject borrowed receipts before effects.
// This real temporary-store service test is the narrowest layer spanning both boundaries.
func TestPreparedLeaseIsolation(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := createTestConnection(t, h.connections, "account", "workspace", "connection", "Local")
	dep, err := h.deployments.Save(environments.Deployment{
		ID: "prepared", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment",
		ConnectionID: conn.ID, Name: "Prepared", Status: environments.DeploymentStatusReady, Health: environments.HealthStatusHealthy,
		Runtime: environments.RuntimeMetadata{ContainerID: "runtime-generation"},
		Build: &environments.ImageBuildResult{OperationID: "build", ConnectionID: conn.ID, DefinitionDigest: "definition", ContextDigest: "context", ImageID: "sha256:" + strings.Repeat("a", 64),
			Product: environments.CommittedBuildSource{WorkspaceID: "product", WorkspaceGeneration: 1, Commit: strings.Repeat("b", 40)},
			Recipe:  environments.CommittedBuildSource{WorkspaceID: "recipe", WorkspaceGeneration: 1, Commit: strings.Repeat("c", 40)}, RecipeFile: "recipe/Containerfile"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := AcquirePreparedLeaseRequest{Source: environments.PreparedDeploymentSource{AccountScopeID: dep.AccountScopeID, WorkspaceID: dep.WorkspaceID, EnvironmentID: dep.EnvironmentID, DeploymentID: dep.ID, CreatedAt: dep.CreatedAt, ContainerID: dep.Runtime.ContainerID, Build: *dep.Build}, Attribution: environments.OperationAttribution{SessionID: "first"}}
	first, err := h.manager.AcquirePreparedLease(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.manager.AcquirePreparedLease(ctx, req)
	if err != nil || again.ID != first.ID {
		t.Fatalf("retry: %+v %v", again, err)
	}
	req.Attribution.SessionID = "second"
	second, err := h.manager.AcquirePreparedLease(ctx, req)
	if err != nil || second.ID == first.ID {
		t.Fatalf("independent lease: %+v %v", second, err)
	}
	for _, field := range []string{"account", "workspace", "environment", "deployment", "source", "generation", "runtime", "identity"} {
		t.Run(field, func(t *testing.T) {
			bad := req
			switch field {
			case "account":
				bad.Source.AccountScopeID = "foreign"
			case "workspace":
				bad.Source.WorkspaceID = "foreign"
			case "environment":
				bad.Source.EnvironmentID = "foreign"
			case "deployment":
				bad.Source.DeploymentID = "foreign"
			case "source":
				bad.Source.Build.Product.Commit = strings.Repeat("d", 40)
			case "generation":
				bad.Source.Build.Product.WorkspaceGeneration++
			case "runtime":
				bad.Source.ContainerID = "replacement"
			case "identity":
				bad.Attribution = environments.OperationAttribution{}
			}
			if _, err := h.manager.AcquirePreparedLease(ctx, bad); err == nil {
				t.Fatal("accepted stale/foreign source")
			}
		})
	}
	for _, action := range []string{"exec", "release"} {
		_, err := h.manager.Submit(ctx, SubmitOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, LeaseID: first.ID, Action: action, Command: []string{"true"}, ConsumerID: "first", Attribution: environments.OperationAttribution{SessionID: "second"}})
		if err == nil {
			t.Fatalf("accepted borrowed lease for %s", action)
		}
	}
	for _, action := range []string{"stop", "start", "destroy"} {
		if _, err := h.manager.Submit(ctx, SubmitOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, Action: action, Attribution: environments.OperationAttribution{SessionID: "first"}}); err == nil {
			t.Fatalf("accepted destructive %s while shared", action)
		}
	}
	all, err := h.deployments.Leases().ListForDeployment("account", "workspace", dep.ID, 100)
	if err != nil || len(all) != 2 || !all[0].Active || !all[1].Active {
		t.Fatalf("rejections mutated leases: %+v %v", all, err)
	}
	if _, err := h.manager.ReleaseDeployment(ctx, ReleaseDeploymentRequest{AccountScopeID: "account", WorkspaceID: "workspace", LeaseID: first.ID, Attribution: environments.OperationAttribution{SessionID: "first"}}); err != nil {
		t.Fatal(err)
	}
	active, found, err := h.deployments.GetActiveLease("account", "workspace", dep.ID)
	if err != nil || !found || active.ID != second.ID {
		t.Fatalf("lost other consumer: %+v %v", active, err)
	}
	if h.mockProv.execCalls != 0 || h.mockProv.stopCalls != 0 || h.mockProv.destroyCalls != 0 {
		t.Fatal("unexpected provider effects")
	}
}

// Purpose: ordinary exclusive receipts must not become bearer tokens through a
// forged consumer_id. Submit/validateAdmission is the narrowest service boundary
// proving rejection before operation persistence or provider execution.
func TestLeaseAdmissionRejectsForgedConsumer(t *testing.T) {
	h := setupSupervisedHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := createTestConnection(t, h.connections, "account", "workspace", "connection", "Local")
	dep, err := h.deployments.Save(environments.Deployment{ID: "deployment", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", ConnectionID: conn.ID, Name: "Exclusive", Status: environments.DeploymentStatusReady, Health: environments.HealthStatusHealthy})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := h.deployments.AcquireLease(environments.DeploymentLease{AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", DeploymentID: dep.ID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"exec", "release"} {
		for _, explicit := range []bool{false, true} {
			id := ""
			if explicit {
				id = lease.ID
			}
			_, err := h.manager.Submit(ctx, SubmitOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, LeaseID: id, Action: action, Command: []string{"true"}, ConsumerID: "owner", Attribution: environments.OperationAttribution{SessionID: "attacker"}})
			if err == nil {
				t.Fatalf("accepted %s explicit=%v", action, explicit)
			}
		}
	}
	stored, found, err := h.deployments.Leases().Get("account", "workspace", lease.ID)
	if err != nil || !found || !stored.Active {
		t.Fatalf("modified owner lease: %+v %v", stored, err)
	}
	page, err := h.opStore.QueryHistory(environments.OperationHistoryQuery{AccountScopeID: "account", WorkspaceID: "workspace", Limit: 10})
	if err != nil || len(page.Operations) != 0 {
		t.Fatalf("persisted unauthorized operation: %+v %v", page, err)
	}
	if h.mockProv.execCalls != 0 || h.mockProv.stopCalls != 0 || h.mockProv.destroyCalls != 0 {
		t.Fatal("unauthorized provider effects")
	}
}
