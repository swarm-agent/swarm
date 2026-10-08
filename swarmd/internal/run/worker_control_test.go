package run

import (
	"context"
	"errors"
	"testing"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: shared control-plane services deny missing/agent/system identity
// for explicit-user writes. Threat: bypassing HTTP via direct service invocation.
// Real service/temp-store fixtures prove rejection without persistent mutation.
func TestWorkerControlServiceIdentity(t *testing.T) {
	_, sessions, execution, _ := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { t.Fatal("unexpected dispatch"); return false })
	ws := sessions.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "identity"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner", AccountScopeSource: identity.AccountScopeSourceServerState}
	for _, origin := range []string{"missing", "agent", "system"} {
		t.Run(origin, func(t *testing.T) {
			ctx := context.Background()
			if origin != "missing" {
				session := ""
				if origin == "agent" {
					session = "untrusted-child"
				}
				var e error
				ctx, e = automation.BindRuntimeIdentity(ctx, p, origin, session)
				if e != nil {
					t.Fatal(e)
				}
			}
			if _, e := execution.UpdateContext(ctx, w.ID, store.WorkerContextUpdate{Text: "forged", Provenance: "forged"}); !errors.Is(e, automation.ErrDenied) {
				t.Fatalf("context bypass: %v", e)
			}
			if _, e := execution.ApproveDeployment(ctx, w.ID, "deployment", 1, "digest"); !errors.Is(e, automation.ErrDenied) {
				t.Fatalf("approval bypass: %v", e)
			}
			if _, e := execution.ProposeDeployment(ctx, w.ID, store.WorkerDeploymentRequest{}); !errors.Is(e, automation.ErrDenied) {
				t.Fatalf("proposal bypass: %v", e)
			}
		})
	}
	c, err := ws.GetWorkerContext("account", w.ID, 0)
	if err != nil || c.Revision != 0 {
		t.Fatalf("unauthorized mutation: %+v %v", c, err)
	}
	ctx, err := automation.BindRuntimeIdentity(context.Background(), p, "user", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = execution.UpdateContext(ctx, w.ID, store.WorkerContextUpdate{Text: "explicit", Provenance: "user"}); err != nil {
		t.Fatal(err)
	}
	if _, err = execution.UpdateContext(ctx, w.ID, store.WorkerContextUpdate{Text: "stale", Provenance: "user"}); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("stale context: %v", err)
	}
	c, err = ws.GetWorkerContext("account", w.ID, 0)
	if err != nil || c.Text != "explicit" || c.Revision != 1 {
		t.Fatalf("stale context overwrote: %+v %v", c, err)
	}
}
