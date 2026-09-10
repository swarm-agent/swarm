package runtime

import (
	"context"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type automationWorkspaceCatalog interface {
	GetByWorkspaceIDForPrincipal(identity.Principal, string) (store.WorkspaceEntry, bool, error)
}

// automationAccess deliberately exposes reads only until a canonical approval,
// occurrence ownership and V3 execution adapter is composed. Persisted policy,
// retrieved context and account membership alone cannot grant execution.
// The domain has no goroutines: its lifetime is the daemon's shared store lifetime.
type automationAccess struct { workspaces automationWorkspaceCatalog }

func (a automationAccess) Workspace(_ context.Context, p automation.Principal, scope store.AutomationScope, action string) error {
	if a.workspaces == nil || p.AccountID == "" || p.AccountID != scope.AccountID || p.SubjectID == "" { return automation.ErrDenied }
	if action != "read" { return automation.ErrDenied }
	_, found, err := a.workspaces.GetByWorkspaceIDForPrincipal(identity.Principal{Type: "user", UserID: p.SubjectID, AccountScopeID: p.AccountID}, scope.WorkspaceID)
	if err != nil { return err }; if !found { return automation.ErrDenied }; return nil
}
func (automationAccess) PlanSession(context.Context, automation.Principal, store.AutomationScope, string) error { return automation.ErrDenied }
func (automationAccess) Execution(context.Context, automation.Principal, store.AutomationScope, store.AutomationDefinition, string) error { return automation.ErrDenied }
func (automationAccess) OccurrenceSession(context.Context, automation.Principal, store.AutomationScope, string) error { return automation.ErrDenied }
