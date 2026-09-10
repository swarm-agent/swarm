package runtime

import (
	"context"
	"errors"
	"log"
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/notification"
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

var errAutomationContinue = errors.New("automation catalog continuation")

type automationSweepPosition struct {
	store.AutomationSchedulerPosition
	At int64 `json:"sweep_at,omitempty"`
}

type automationWindowExecution struct {
	automationScheduleExecution
	at time.Time
}

func (e automationWindowExecution) Tick(ctx context.Context, p automation.Principal, scope store.AutomationScope, id string, revision uint64) error {
	timed, ok := e.automationScheduleExecution.(interface {
		TickAt(context.Context, automation.Principal, store.AutomationScope, string, uint64, time.Time) error
	})
	if !ok { return automation.ErrInvalid }
	return timed.TickAt(ctx, p, scope, id, revision, e.at)
}

// automationSweep performs one bounded definition page per turn. Position writes
// follow effects; crashes replay stable trigger receipts, never skip unfinished pages.
func automationSweep(ctx context.Context, db *store.Store, execution automationScheduleExecution, outcomes ...func(context.Context, automation.Principal, store.AutomationRecord) error) error {
	return automationSweepAt(ctx, db, execution, time.Now(), outcomes...)
}

func automationSweepAt(ctx context.Context, db *store.Store, execution automationScheduleExecution, now time.Time, outcomes ...func(context.Context, automation.Principal, store.AutomationRecord) error) error {
	if err := ctx.Err(); err != nil { return err }
	var pos automationSweepPosition
	if err := db.GetAutomationSchedulerPosition("catalog", &pos); err != nil { return err }
	old := pos
	// Commit the window before effects so interrupted pages retain their due time.
	if pos.At == 0 {
		pos.At = now.UnixMilli()
		if err := db.SaveAutomationSchedulerPosition("catalog", old, pos); err != nil { return err }
		old = pos
	}
	execution = automationWindowExecution{execution, time.UnixMilli(pos.At)}
	save := func() error {
		if err := db.SaveAutomationSchedulerPosition("catalog", old, pos); err != nil { return err }
		if pos.At != 0 { return errAutomationContinue }
		return nil
	}
	if pos.AccountID == "" {
		key, account, _, err := db.SchedulerCatalogNext("", pos.AccountKey)
		if errors.Is(err, store.ErrAutomationInvalid) { pos = automationSweepPosition{}; return errors.Join(err, save()) }
		if err != nil { return err }
		pos.AccountKey, pos.AccountID = key, account.ID
		if key == "" { pos.At = 0; return save() }
	}
	if pos.WorkspaceID == "" {
		key, _, entry, err := db.SchedulerCatalogNext(pos.AccountID, pos.WorkspaceKey)
		if errors.Is(err, store.ErrAutomationInvalid) { pos.WorkspaceKey = ""; return errors.Join(err, save()) }
		if err != nil { return err }
		pos.WorkspaceKey, pos.WorkspaceID = key, entry.WorkspaceID
		if key == "" { pos.AccountID = ""; return save() }
		if pos.WorkspaceID == "" { return save() }
	}
	scope := store.AutomationScope{AccountID: pos.AccountID, WorkspaceID: pos.WorkspaceID}
	rows, next, err := db.SearchAutomationRecords(store.AutomationSearch{Scope: scope, Kind: "definition", Cursor: pos.Definitions, Limit: 10})
	if errors.Is(err, store.ErrAutomationInvalid) && pos.Definitions != "" {
		pos.Definitions = ""
		return errors.Join(err, save())
	}
	if err != nil { return err }
	var failures []error
	for _, row := range rows {
		if err := ctx.Err(); err != nil { return err }
		if row.Definition == nil { continue }
		grant, found, err := db.GetAutomationApproval(scope, row.Definition.Authorization.ApprovalReference)
		if err != nil { failures = append(failures, err); continue }
		if !found { continue }
		trusted, err := automation.BindRuntimeIdentity(ctx, identity.Principal{Type: identity.PrincipalTypeUser, UserID: grant.SubjectID, AccountScopeID: scope.AccountID}, "system", "")
		if err != nil { failures = append(failures, err); continue }
		p, _ := automation.RuntimePrincipal(trusted)
		failures = append(failures, automationRunDefinition(trusted, db, execution, p, row))
		for _, reconcile := range outcomes { failures = append(failures, reconcile(trusted, p, row)) }
	}
	pos.Definitions = next
	if next == "" { pos.WorkspaceID = "" }
	return errors.Join(append(failures, save())...)
}

type automationPositionStore interface {
	GetAutomationSchedulerPosition(string, any) error
	SaveAutomationSchedulerPosition(string, any, any) error
}

func automationRunDefinition(ctx context.Context, db automationPositionStore, execution automationScheduleExecution, p automation.Principal, row store.AutomationRecord) error {
	if err := ctx.Err(); err != nil { return err }
	key := store.AutomationRecoveryPositionKey(row.Scope, row.AutomationID)
	var cursor string
	if err := db.GetAutomationSchedulerPosition(key, &cursor); err != nil { return err }
	// A disabled/revoked definition must not prevent completion of a durable stop.
	var tickErr error
	if row.Definition != nil && row.Definition.Enabled { tickErr = execution.Tick(ctx, p, row.Scope, row.AutomationID, row.Revision) }
	next, err := execution.RecoverPage(ctx, p, row.Scope, row.AutomationID, cursor)
	if errors.Is(err, automation.ErrRecoveryCursor) { next = "" }
	if next == cursor && next != "" && err == nil { err = errors.New("automation recovery cursor did not advance") }
	return errors.Join(tickErr, err, db.SaveAutomationSchedulerPosition(key, cursor, next))
}

// Strip only the progress signal; joined execution failures must remain visible.
func automationSweepFailure(err error) error {
	if err == errAutomationContinue { return nil }
	if joined, ok := err.(interface { Unwrap() []error }); ok {
		var failures []error
		for _, child := range joined.Unwrap() { failures = append(failures, automationSweepFailure(child)) }
		return errors.Join(failures...)
	}
	return err
}

type automationWindowKey struct{}

type automationLoop struct { cancel context.CancelFunc; done chan struct{} }
func startAutomationLoop(parent context.Context, interval time.Duration, sweep func(context.Context) error, report func(error)) *automationLoop {
	ctx, cancel := context.WithCancel(parent)
	l := &automationLoop{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		timer := time.NewTimer(interval)
		timer.Stop()
		window := time.Now()
		defer timer.Stop()
		for {
			if ctx.Err() != nil { return }
			bounded, stop := context.WithTimeout(ctx, 30*time.Second)
			err := sweep(context.WithValue(bounded, automationWindowKey{}, window))
			stop()
			delay := interval
			if errors.Is(err, errAutomationContinue) {
				// Yield between bounded pages; never wait a minute per page.
				delay = 10*time.Millisecond
			} else if err == nil {
				window = window.Add(interval)
				delay = time.Until(window)
				if delay < 10*time.Millisecond { delay = 10*time.Millisecond }
			} else {
				window = time.Now().Add(interval)
			}
			if failure := automationSweepFailure(err); failure != nil && ctx.Err() == nil { report(failure) }
			timer.Reset(delay)
			select { case <-ctx.Done(): return; case <-timer.C: }
		}
	}()
	return l
}
func (l *automationLoop) Close() { l.cancel(); <-l.done }

// StartAutomationScheduling is owned by Run, never construction. Lifecycle
// ownership ensures cancellation completes before DB close.
func (d *Daemon) StartAutomationScheduling(ctx context.Context) error {
	d.automationMu.Lock()
	defer d.automationMu.Unlock()
	if d.automationClosed || d.automationLoop != nil || d.automationExecution == nil { return automation.ErrInvalid }
	d.automationLoop = startAutomationLoop(ctx, time.Minute, func(ctx context.Context) error {
		return automationSweepAt(ctx, d.store, d.automationExecution, ctx.Value(automationWindowKey{}).(time.Time), d.automationOutcomes)
	}, func(error) { log.Print("automation scheduler sweep failed; durable pending work retained") })
	return nil
}


// AutomationServices exposes composed startup authorities to consumer wiring;
// possession does not bypass the trusted-context and current ownership checks.
func (d *Daemon) AutomationServices() (*automation.PolicyApproval, *automation.ExecutionService) {
	return d.automationApproval, d.automationExecution
}

// Outcome recovery has its own cursor and runs even when admission fails. Each
// terminal occurrence is itself a durable delivery source, including a crash
// before the first delivery claim. Delivery never calls Dispatch or Ensure.
func (d *Daemon) automationOutcomes(ctx context.Context, p automation.Principal, definition store.AutomationRecord) error {
	if err := ctx.Err(); err != nil { return err }
	key := "outcomes-" + store.AutomationRecoveryPositionKey(definition.Scope, definition.AutomationID)
	var cursor string
	if err := d.store.GetAutomationSchedulerPosition(key, &cursor); err != nil { return err }
	rows, next, err := d.store.SearchAutomationRecords(store.AutomationSearch{Scope: definition.Scope, AutomationID: definition.AutomationID, Kind: "occurrence", Cursor: cursor, Limit: 25})
	if err != nil {
		if errors.Is(err, store.ErrAutomationInvalid) && cursor != "" { return errors.Join(err, d.store.SaveAutomationSchedulerPosition(key, cursor, "")) }
		return err
	}
	var failures []error
	for _, row := range rows {
		if err := ctx.Err(); err != nil { return err }
		updated, err := d.automationExecution.ReconcileOutcome(ctx, p, row.Scope, row.AutomationID, row.ID)
		failures = append(failures, err)
		// A context failure after a committed outcome must not suppress delivery.
		if updated.Occurrence != nil { row = updated }
		if row.Occurrence == nil { continue }
		switch row.Occurrence.State {
		case "completed", "failed", "cancelled", "skipped":
			node, found, err := store.NewSwarmStore(d.store).GetLocalNode()
			if err != nil { failures = append(failures, err); continue }
			if !found { failures = append(failures, automation.ErrInvalid); continue }
			delivery, err := notification.NewAutomationDeliveryService(d.store, d.notificationService, node.SwarmID)
			if err == nil { err = delivery.Deliver(ctx, store.AutomationDeliveryReference{Scope: row.Scope, AutomationID: row.AutomationID, OccurrenceID: row.ID, Revision: row.Revision}) }
			failures = append(failures, err)
		}
	}
	return errors.Join(append(failures, d.store.SaveAutomationSchedulerPosition(key, cursor, next))...)
}
