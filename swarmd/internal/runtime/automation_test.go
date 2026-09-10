package runtime

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type automationCatalogFake struct { calls int }
func (f *automationCatalogFake) GetByWorkspaceIDForPrincipal(p identity.Principal, id string) (store.WorkspaceEntry, bool, error) {
	f.calls++
	return store.WorkspaceEntry{}, p.AccountScopeID == "account" && id == "workspace", nil
}

// Purpose: startup composition permits only live owned-workspace reads while
// missing approval/execution adapters must fail before catalog or runtime effects.
// Fake catalog observes the negative side-effect boundary without a live daemon.
func TestAutomationCompositionFailClosed(t *testing.T) {
	catalog := &automationCatalogFake{}
	a := automationAccess{workspaces: catalog}
	p := automation.Principal{AccountID: "account", SubjectID: "session", Role: "agent"}
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	ctx := context.Background()
	if err := a.Workspace(ctx, p, scope, "read"); err != nil { t.Fatal(err) }
	for _, action := range []string{"run", "manage", "context", "outcome"} {
		if err := a.Workspace(ctx, p, scope, action); err == nil { t.Fatal("missing adapter granted", action) }
	}
	if catalog.calls != 1 { t.Fatal("denied operation reached catalog") }
	scope.AccountID = "foreign"
	if err := a.Workspace(ctx, p, scope, "read"); err == nil || catalog.calls != 1 { t.Fatal("foreign read reached catalog") }
	if err := a.Execution(ctx, p, scope, store.AutomationDefinition{}, "run"); err == nil { t.Fatal("policy data became a grant") }
}
