package run

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func TestWorkerGCPServiceTargetAndCommand(t *testing.T) {
	_, sessions, service, _ := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool {
		t.Fatal("unexpected dispatch")
		return false
	})
	ws := sessions.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{
		Name: "gcp-worker",
		WorkspaceRequirements: []store.WorkerWorkspaceRequirement{
			{Role: "primary", Required: true},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace"})
	if err != nil {
		t.Fatal(err)
	}

	p := identity.Principal{
		Type:               identity.PrincipalTypeUser,
		AccountScopeID:     "account",
		UserID:             "owner",
		AccountScopeSource: identity.AccountScopeSourceServerState,
	}
	ctx, err := automation.BindRuntimeIdentity(context.Background(), p, "user", "")
	if err != nil {
		t.Fatal(err)
	}

	// Register GCP Target
	target, err := service.RegisterGCPTarget(ctx, store.WorkerGCPRegistration{
		Name:           "gcp-cluster-1",
		RuntimeID:      "gcp_rt_1",
		DesktopURL:     "https://10.0.0.1:5555",
		IdempotencyKey: "gcp_key_1",
	})
	if err != nil {
		t.Fatalf("RegisterGCPTarget failed: %v", err)
	}
	if target.Kind != "gcp" || target.ReferenceID != "gcp_rt_1" || target.ReferenceDigest == "" || target.Capacity != 1 {
		t.Fatalf("unexpected target reference: %+v", target)
	}

	// Propose deployment onto GCP Target
	dep, err := service.ProposeDeployment(ctx, w.ID, store.WorkerDeploymentRequest{
		WorkerRevision:  w.Revision,
		ContextRevision: 0,
		Target:          target,
		Lifecycle:       "persistent",
		IdempotencyKey:  "dep_gcp_1",
	})
	if err != nil {
		t.Fatalf("ProposeDeployment failed: %v", err)
	}
	if dep.Target.Kind != "gcp" || dep.ApprovalState != "pending" || dep.CleanupScope != "owned_runtime" {
		t.Fatalf("unexpected deployment: %+v", dep)
	}

	// Approve deployment
	approved, err := service.ApproveDeployment(ctx, w.ID, dep.ID, dep.Revision, dep.ApprovalDigest)
	if err != nil {
		t.Fatalf("ApproveDeployment failed: %v", err)
	}
	if approved.ApprovalState != "approved" || approved.DesiredState != "running" {
		t.Fatalf("unexpected approved deployment: %+v", approved)
	}

	// Queue a stop command
	cmd, err := service.DeploymentCommand(ctx, w.ID, approved.ID, store.WorkerCommandRequest{
		ExpectedRevision: approved.Revision,
		Generation:       approved.Generation,
		Kind:             "stop",
		IdempotencyKey:   "stop_1",
	})
	if err != nil {
		t.Fatalf("DeploymentCommand failed: %v", err)
	}
	if cmd.Status != "pending" {
		t.Fatalf("expected pending command: %+v", cmd)
	}

	// Acknowledge the command
	ackDigest := strings.Repeat("a", 64)
	acked, err := service.AcknowledgeDeploymentCommand(ctx, w.ID, approved.ID, store.WorkerCommandAcknowledgement{
		CommandID:      cmd.ID,
		Generation:     approved.Generation,
		Status:         "acknowledged",
		EvidenceDigest: ackDigest,
	})
	if err != nil {
		t.Fatalf("AcknowledgeDeploymentCommand failed: %v", err)
	}
	if acked.Status != "acknowledged" {
		t.Fatalf("expected acknowledged command: %+v", acked)
	}
}
