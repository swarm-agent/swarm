package lifecycle

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: executeBuild must export the authenticated isolated result, not the
// configured catalog dev tree; a changed/dirty result after provider execution
// must trigger owned cleanup and no admission. Service plus injected provider
// is the narrowest layer proving source selection and failure postconditions.
func TestProjectResultBuildSourceAndStaleness(t *testing.T) {
	for _, stale := range []bool{false, true} {
		h, env, conn, p := buildLifecycleFixture(t)
		saved := *env.Build
		product := ResolvedBuildProduct{Source: saved.Product, Root: t.TempDir(), Binding: strings.Repeat("f", 64)}
		product.Source.Commit = strings.Repeat("e", 40)
		changed := false
		resolver := func(context.Context) (ResolvedBuildProduct, error) {
			if changed {
				return ResolvedBuildProduct{}, errors.New("task attempt changed or dirty")
			}
			return product, nil
		}
		if stale {
			p.afterBuild = func() { changed = true }
		}
		ctx := withBuildProduct(context.Background(), resolver)
		out, err := h.manager.executeBuild(ctx, "op_result_source", SubmitOperationRequest{AccountScopeID: "account"}, &env, &conn)
		if p.lastBuild.ProductRoot != product.Root || p.lastBuild.Definition.Product != product.Source || p.lastBuild.Definition.Recipe != saved.Recipe {
			t.Fatalf("wrong exported source: %+v", p.lastBuild)
		}
		if stale {
			if err == nil || out != nil || p.cleanupCalls != 1 {
				t.Fatalf("stale build admitted: %+v %v cleanup=%d", out, err, p.cleanupCalls)
			}
		} else if err != nil || out == nil || out.Build.ProductResult != product.Binding || out.Build.Product != product.Source {
			t.Fatalf("result provenance lost: %+v %v", out, err)
		}
		stored, found, err := h.environments.Get("account", "workspace", env.ID)
		if err != nil || !found || *stored.Build != saved {
			t.Fatal("saved environment mutated")
		}
	}
}

// Purpose: durable successful build provenance is scoped to result identity in
// addition to SHA/recipe/connection; configured dev cannot consume the result
// without the authenticated resolver. Real operation transitions prove exact
// successful admission and mismatched attempt rejection without deployments.
func TestProjectResultBuildReceiptBinding(t *testing.T) {
	h, env, conn, _ := buildLifecycleFixture(t)
	product := ResolvedBuildProduct{Source: env.Build.Product, Root: t.TempDir(), Binding: strings.Repeat("f", 64)}
	product.Source.Commit = strings.Repeat("e", 40)
	ctx := withBuildProduct(context.Background(), func(context.Context) (ResolvedBuildProduct, error) { return product, nil })
	selected := env.Clone()
	res, err := h.manager.executeBuild(ctx, "op_result_receipt", SubmitOperationRequest{AccountScopeID: "account"}, selected, &conn)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	op, _, err := h.opStore.AdmitOperation(environments.EnvironmentOperation{OperationID: "op_result_receipt", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID, Action: "build", BuildDefinition: selected.Build, BuildConnectionID: conn.ID, CreatedAt: now, ObservedAt: now, Deadline: now + 60000, Status: environments.OperationStatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	op, err = h.opStore.TransitionOperation(pebblestore.OperationTransitionInput{AccountScopeID: "account", WorkspaceID: "workspace", OperationID: op.OperationID, ExpectedRevision: op.Revision, TargetStatus: environments.OperationStatusRunning, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	op, err = h.opStore.TransitionOperation(pebblestore.OperationTransitionInput{AccountScopeID: "account", WorkspaceID: "workspace", OperationID: op.OperationID, ExpectedRevision: op.Revision, TargetStatus: environments.OperationStatusSucceeded, Result: res, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.resolveBuildImage(ctx, "account", "workspace", op.OperationID, env.Clone(), &conn); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"unbound", "attempt", "dirty", "workspace"} {
		badctx := ctx
		switch variant {
		case "unbound":
			badctx = context.Background()
		case "attempt":
			badctx = withBuildProduct(context.Background(), func(context.Context) (ResolvedBuildProduct, error) {
				p := product
				p.Binding = strings.Repeat("a", 64)
				return p, nil
			})
		case "dirty":
			badctx = withBuildProduct(context.Background(), func(context.Context) (ResolvedBuildProduct, error) {
				return ResolvedBuildProduct{}, errors.New("dirty")
			})
		case "workspace":
			badctx = withBuildProduct(context.Background(), func(context.Context) (ResolvedBuildProduct, error) {
				p := product
				p.Source.WorkspaceGeneration++
				return p, nil
			})
		}
		if _, err := h.manager.resolveBuildImage(badctx, "account", "workspace", op.OperationID, env.Clone(), &conn); err == nil {
			t.Fatalf("%s accepted", variant)
		}
	}
	deps, err := h.deployments.ListByEnvironment("account", "workspace", env.ID, 10)
	if err != nil || len(deps) != 0 {
		t.Fatal("receipt validation mutated deployments")
	}
}

// Purpose: supervised result identity must be checked again after admission,
// before exec/release can mutate a provider or lease. executeAction owns that
// asynchronous boundary; injected stale resolver proves zero provider effects.
func TestProjectResultQueuedExecutionRevalidation(t *testing.T) {
	h, env, conn, p := buildLifecycleFixture(t)
	for _, action := range []string{"exec", "release"} {
		req := SubmitOperationRequest{Action: action, AccountScopeID: "account", WorkspaceID: "workspace", BuildProductResolver: func(context.Context) (ResolvedBuildProduct, error) {
			return ResolvedBuildProduct{}, errors.New("task advanced after admission")
		}}
		op := environments.EnvironmentOperation{OperationID: "op_queued_result"}
		var mu sync.Mutex
		progress := time.Now()
		if out, err := h.manager.executeAction(context.Background(), &op, &mu, &progress, req, &env, &conn, nil); err == nil || out != nil {
			t.Fatal("stale queued execution accepted")
		}
	}
	if atomic.LoadInt32(&p.execCalls) != 0 || p.calls != 0 {
		t.Fatal("stale queued execution reached provider")
	}
}

// Purpose: idempotency must distinguish task result identity even when commit
// and recipe match. The shared request hash is the narrowest durable fence;
// legacy requests without a result binding retain their previous hash inputs.
func TestProjectResultIdempotencyBinding(t *testing.T) {
	in := environments.OperationRequestHashInput{Action: "build", BuildDigest: strings.Repeat("a", 64), ProductResult: strings.Repeat("b", 64)}
	first := environments.ComputeOperationRequestHash(in)
	in.ProductResult = strings.Repeat("c", 64)
	if first == environments.ComputeOperationRequestHash(in) {
		t.Fatal("task attempt omitted from request hash")
	}
}

// Purpose: result-bound images must provision without host mounts, then accept
// only the parent's held receipt for real lifecycle exec/release dispatch.
// Real stores and an injected provider prove durable identity propagation;
// this is deterministic contract evidence, not a live Podman test. The fixture
// creates the selected connection kind initially; immutable connection kinds
// must not be changed to make SSH lifecycle tests pass.
func TestProjectResultManagedLifecycle(t *testing.T) {
	t.Run("local", func(t *testing.T) { testResultManagedLifecycle(t, false) })
	t.Run("ssh", func(t *testing.T) { testResultManagedLifecycle(t, true) })
}

func testResultManagedLifecycle(t *testing.T, remote bool) {
	kind := environments.ConnectionKindLocalPodman
	if remote {
		kind = environments.ConnectionKindSSH
	}
	h, env, conn, p := buildLifecycleFixtureKind(t, kind)
	if !remote {
		h.manager.registry.Register(&resultPodmanProvider{p})
	}
	product := ResolvedBuildProduct{Source: env.Build.Product, Root: t.TempDir(), Binding: strings.Repeat("f", 64)}
	product.Source.Commit = strings.Repeat("e", 40)
	resolver := func(context.Context) (ResolvedBuildProduct, error) { return product, nil }
	ctx := withBuildProduct(context.Background(), resolver)
	selected := env.Clone()
	// Both scopes must identify the same initial SSH connection when the
	// post-build drift check reloads it; the mock emits that exact digest.
	res, err := h.manager.executeBuild(ctx, "op_lifecycle_result", SubmitOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace"}, selected, &conn)
	if err != nil {
		t.Fatal(err)
	}
	if res.Build.ConnectionDigest != environments.ConnectionTransportDigest(&conn) {
		t.Fatal("build receipt lost initial connection transport identity")
	}
	now := time.Now().UnixMilli()
	op, _, err := h.opStore.AdmitOperation(environments.EnvironmentOperation{OperationID: "op_lifecycle_result", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID, Action: "build", BuildDefinition: selected.Build, BuildConnectionID: conn.ID, CreatedAt: now, ObservedAt: now, Deadline: now + 60000, Status: environments.OperationStatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	op, err = h.opStore.TransitionOperation(pebblestore.OperationTransitionInput{AccountScopeID: "account", WorkspaceID: "workspace", OperationID: op.OperationID, ExpectedRevision: op.Revision, TargetStatus: environments.OperationStatusRunning, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.opStore.TransitionOperation(pebblestore.OperationTransitionInput{AccountScopeID: "account", WorkspaceID: "workspace", OperationID: op.OperationID, ExpectedRevision: op.Revision, TargetStatus: environments.OperationStatusSucceeded, Result: res, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	ensured, err := h.manager.EnsureDeployment(ctx, EnsureDeploymentRequest{AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: env.ID, ConnectionID: conn.ID, BuildOperationID: op.OperationID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "parent", TTLMillis: 60000})
	if err != nil {
		t.Fatal(err)
	}
	if ensured.Deployment.WorkspacePath != "" || ensured.Deployment.Build == nil || *ensured.Deployment.Build != *res.Build {
		t.Fatal("image acquired fake mount identity or lost provenance")
	}
	for _, action := range []string{"exec", "release"} {
		req := SubmitOperationRequest{Action: action, AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: ensured.Deployment.ID, LeaseID: ensured.Lease.ID, Attribution: environments.OperationAttribution{SessionID: "parent"}, Command: []string{"true"}, BuildProductResolver: resolver}
		bad := req
		bad.Attribution.SessionID = "other"
		before := atomic.LoadInt32(&p.execCalls)
		if _, _, _, err := h.manager.validateAdmission(context.Background(), &bad); err == nil || atomic.LoadInt32(&p.execCalls) != before {
			t.Fatal("foreign consumer reached provider")
		}
		if _, _, _, err := h.manager.validateAdmission(context.Background(), &req); err != nil {
			t.Fatalf("%s admission: %v", action, err)
		}
		var mu sync.Mutex
		progress := time.Now()
		operation := environments.EnvironmentOperation{OperationID: "op_" + action + "_result", ProductResult: product.Binding}
		if _, err := h.manager.executeAction(context.Background(), &operation, &mu, &progress, req, nil, &conn, &ensured.Deployment); err != nil {
			t.Fatalf("%s execution: %v", action, err)
		}
	}
	lease, found, err := h.manager.GetLease("account", "workspace", ensured.Lease.ID)
	if err != nil || !found || lease.Active || atomic.LoadInt32(&p.execCalls) != 1 {
		t.Fatal("exec/release postconditions not observed")
	}
}

// Purpose: executeBuild must re-read the scoped SSH connection after the mock
// emits its captured transport digest. Actual stored transport drift must reject
// the receipt and clean the owned build, not weaken authority to fix a fixture.
// Real connection/deployment stores plus an injected builder prove this boundary.
func TestProjectResultSSHConnectionDrift(t *testing.T) {
	h, env, conn, p := buildLifecycleFixtureKind(t, environments.ConnectionKindSSH)
	initialDigest := environments.ConnectionTransportDigest(&conn)
	p.afterBuild = func() {
		changed := conn
		ssh := *conn.SSH
		ssh.Host = "changed.example.invalid"
		changed.SSH = &ssh
		if _, err := h.connections.Save(changed); err != nil {
			t.Fatal(err)
		}
	}
	out, err := h.manager.executeBuild(context.Background(), "op_drift_result",
		SubmitOperationRequest{AccountScopeID: "account", WorkspaceID: "workspace"}, env.Clone(), &conn)
	if err == nil || !strings.Contains(err.Error(), "SSH connection changed during build") || out != nil {
		t.Fatalf("changed SSH transport accepted: %+v %v", out, err)
	}
	if environments.ConnectionTransportDigest(p.lastBuild.Connection) != initialDigest || p.calls != 1 || p.cleanupCalls != 1 {
		t.Fatal("captured receipt identity or rejected-build cleanup lost")
	}
	deps, err := h.deployments.ListByEnvironment("account", "workspace", env.ID, 10)
	if err != nil || len(deps) != 0 {
		t.Fatal("changed transport allocated deployment")
	}
}

type resultPodmanProvider struct{ *managedBuildProvider }

func (p *resultPodmanProvider) Capabilities(context.Context, *environments.Connection) (environments.ConnectionCapabilities, error) {
	return environments.ConnectionCapabilities{SupportsPodman: true, RootlessSystemd: true}, nil
}
