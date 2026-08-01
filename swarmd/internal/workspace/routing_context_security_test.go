package workspace

import (
	"errors"
	"testing"

	"swarm/packages/swarmd/internal/identity"
)

func TestRoutingContextCannotCrossAccountSecurityBoundary(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	service := NewService(store)
	owner := testPrincipal()
	attacker := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-attacker", AccountScopeID: "account-attacker"}

	ownerFirst := addRoutingWorkspace(t, store, owner.AccountScopeID, "/host/owner-first", "Owner First", "owner first definition", true)
	addRoutingWorkspace(t, store, owner.AccountScopeID, "/host/owner-second", "Owner Second", "owner second definition", true)
	attackerWorkspace := addRoutingWorkspace(t, store, attacker.AccountScopeID, "/host/attacker", "Attacker", "attacker definition", true)

	ownerContext, err := service.BuildRoutingContextForPrincipal(owner)
	if err != nil {
		t.Fatalf("build owner routing context: %v", err)
	}
	if _, err := service.ResolveRoutingWorkspaceForPrincipal(attacker, ownerContext, ownerFirst.WorkspaceID); !errors.Is(err, ErrInvalidRoutingWorkspaceSelection) {
		t.Fatalf("attacker resolving owner context error = %v, want %v", err, ErrInvalidRoutingWorkspaceSelection)
	}
	if _, err := service.ResolveRoutingWorkspaceForPrincipal(owner, ownerContext, attackerWorkspace.WorkspaceID); !errors.Is(err, ErrInvalidRoutingWorkspaceSelection) {
		t.Fatalf("unoffered attacker workspace selection error = %v, want %v", err, ErrInvalidRoutingWorkspaceSelection)
	}
}
