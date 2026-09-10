package runtime

import (
	"context"
	"errors"
	"log"
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type automationWorkspaceCatalog interface {
	GetByWorkspaceIDForPrincipal(identity.Principal, string) (store.WorkspaceEntry, bool, error)
}
type automationSessionCatalog interface { GetSession(string) (store.SessionSnapshot, bool, error) }
type automationMembership interface { GetAccountUser(string, string) (store.AccountUserRecord, bool, error) }

type automationAccess struct {
	workspaces automationWorkspaceCatalog
	sessions automationSessionCatalog
	members automationMembership
}

func (a automationAccess) owner(p automation.Principal) (identity.Principal, error) {
	user := p.SubjectID
	if p.Role == "agent" {
		if a.sessions == nil { return identity.Principal{}, automation.ErrDenied }
		s, found, err := a.sessions.GetSession(p.SubjectID)
		if err != nil { return identity.Principal{}, err }
		if !found || s.AccountScopeID != p.AccountID { return identity.Principal{}, automation.ErrDenied }
		user = s.UserID
	} else if p.Role != "user" && p.Role != "system" { return identity.Principal{}, automation.ErrDenied }
	if a.members == nil || p.AccountID == "" || user == "" { return identity.Principal{}, automation.ErrDenied }
	m, found, err := a.members.GetAccountUser(p.AccountID, user)
	if err != nil { return identity.Principal{}, err }
	if !found || m.Status != store.AccountUserStatusActive || m.UserID != user || m.AccountScopeID != p.AccountID { return identity.Principal{}, automation.ErrDenied }
	return identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: p.AccountID}, nil
}

func (a automationAccess) Workspace(ctx context.Context, p automation.Principal, scope store.AutomationScope, action string) error {
	if err := ctx.Err(); err != nil { return err }
	if a.workspaces == nil || p.AccountID != scope.AccountID || scope.WorkspaceID == "" { return automation.ErrDenied }
	owner, err := a.owner(p)
	if err != nil { return err }
	entry, found, err := a.workspaces.GetByWorkspaceIDForPrincipal(owner, scope.WorkspaceID)
	if err != nil { return err }
	if !found || entry.AccountScopeID != scope.AccountID || entry.WorkspaceID != scope.WorkspaceID { return automation.ErrDenied }
	return nil
}
func (a automationAccess) PlanSession(ctx context.Context, p automation.Principal, scope store.AutomationScope, id string) error {
	if err := a.Workspace(ctx, p, scope, "read"); err != nil { return err }
	owner, err := a.owner(p)
	if err != nil { return err }
	if a.sessions == nil { return automation.ErrDenied }
	s, found, err := a.sessions.GetSession(id)
	if err != nil { return err }
	if !found || s.AccountScopeID != scope.AccountID || s.UserID != owner.UserID { return automation.ErrDenied }
	for _, grant := range s.WorkspaceGrants {
		if grant.Kind == store.WorkspaceGrantPrimary && grant.WorkspaceID == scope.WorkspaceID && (grant.Available == nil || *grant.Available) { return nil }
	}
	return automation.ErrDenied
}
func (a automationAccess) OccurrenceSession(ctx context.Context, p automation.Principal, scope store.AutomationScope, id string) error {
	if p.Role == "agent" && p.SubjectID != id { return automation.ErrDenied }
	return a.PlanSession(ctx, p, scope, id)
}

type automationScheduleExecution interface {
	Tick(context.Context, automation.Principal, store.AutomationScope, string, uint64) error
	RecoverPage(context.Context, automation.Principal, store.AutomationScope, string, string) (string, error)
}

// automationSweep enumerates only canonical account catalogs. Oversized catalogs
// fail explicitly rather than silently starving entries beyond a truncated list.
// Opaque search cursors are passed unchanged; durable schedule cursors and trigger
// receipts live in ExecutionService's repository, never in this timer loop.
func automationSweep(ctx context.Context, db *store.Store, execution automationScheduleExecution) error {
	var failed bool
	accounts, err := store.NewIdentityStore(db).ListAccountScopes(129)
	if err != nil { return err }
	if len(accounts) > 128 { return errors.New("automation account catalog exceeds bounded sweep") }
	for _, account := range accounts {
		entries, err := store.NewWorkspaceStore(db).ListForAccount(account.ID, 129)
		if err != nil { return err }
		if len(entries) > 128 { return errors.New("automation workspace catalog exceeds bounded sweep") }
		for _, entry := range entries {
			scope := store.AutomationScope{AccountID: account.ID, WorkspaceID: entry.WorkspaceID}
			cursor := ""
			for page := 0; page < 20; page++ {
				if err := ctx.Err(); err != nil { return err }
				rows, next, err := db.SearchAutomationRecords(store.AutomationSearch{Scope: scope, Kind: "definition", Cursor: cursor, Limit: 50})
				if err != nil { return err }
				for _, row := range rows {
					if row.Definition == nil || !row.Definition.Enabled { continue }
					grant, found, err := db.GetAutomationApproval(scope, row.Definition.Authorization.ApprovalReference)
					if err != nil { return err }
					if !found { continue }
					verified := identity.Principal{Type: identity.PrincipalTypeUser, UserID: grant.SubjectID, AccountScopeID: scope.AccountID}
					trusted, err := automation.BindRuntimeIdentity(ctx, verified, "system", "")
					if err != nil { return err }
					p, _ := automation.RuntimePrincipal(trusted)
					if err := automationRunDefinition(trusted, execution, p, row); err != nil { failed = true }
				}
				if next == "" { break }
				if next == cursor || page == 19 { return errors.New("automation definition catalog exceeds bounded sweep") }
				cursor = next
			}
		}
	}
	if failed { return errors.New("one or more automation definitions could not be processed") }
	return nil
}

func automationRunDefinition(ctx context.Context, execution automationScheduleExecution, p automation.Principal, row store.AutomationRecord) error {
	if err := ctx.Err(); err != nil { return err }
	if err := execution.Tick(ctx, p, row.Scope, row.AutomationID, row.Revision); err != nil { return err }
	cursor := ""
	for page := 0; page < 20; page++ {
		if err := ctx.Err(); err != nil { return err }
		next, err := execution.RecoverPage(ctx, p, row.Scope, row.AutomationID, cursor)
		if err != nil { return err }
		if next == "" { return nil }
		if next == cursor { return errors.New("automation recovery cursor did not advance") }
		cursor = next
	}
	return errors.New("automation recovery exceeds bounded sweep")
}

type automationLoop struct { cancel context.CancelFunc; done chan struct{} }
func startAutomationLoop(parent context.Context, interval time.Duration, sweep func(context.Context) error, report func(error)) *automationLoop {
	ctx, cancel := context.WithCancel(parent)
	l := &automationLoop{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		timer := time.NewTicker(interval)
		defer timer.Stop()
		for {
			if ctx.Err() != nil { return }
			bounded, stop := context.WithTimeout(ctx, 30*time.Second)
			err := sweep(bounded)
			stop()
			if err != nil && ctx.Err() == nil { report(err) }
			select { case <-ctx.Done(): return; case <-timer.C: }
		}
	}()
	return l
}
func (l *automationLoop) Close() { l.cancel(); <-l.done }

// StartAutomationScheduling is explicit, not called by NewDaemon or Run during
// development. Lifecycle ownership ensures cancellation completes before DB close.
func (d *Daemon) StartAutomationScheduling(ctx context.Context) error {
	d.automationMu.Lock()
	defer d.automationMu.Unlock()
	if d.automationClosed || d.automationLoop != nil || d.automationExecution == nil { return automation.ErrInvalid }
	d.automationLoop = startAutomationLoop(ctx, time.Minute, func(ctx context.Context) error {
		return automationSweep(ctx, d.store, d.automationExecution)
	}, func(error) { log.Print("automation scheduler sweep failed; durable pending work retained") })
	return nil
}


// AutomationServices exposes composed startup authorities to consumer wiring;
// possession does not bypass the trusted-context and current ownership checks.
func (d *Daemon) AutomationServices() (*automation.PolicyApproval, *automation.ExecutionService) {
	return d.automationApproval, d.automationExecution
}
