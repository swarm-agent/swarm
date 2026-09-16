package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type automationCatalogFake struct{ calls int }

func (f *automationCatalogFake) GetByWorkspaceIDForPrincipal(p identity.Principal, id string) (store.WorkspaceEntry, bool, error) {
	f.calls++
	return store.WorkspaceEntry{AccountScopeID: p.AccountScopeID, WorkspaceID: id}, true, nil
}

type automationMembersFake struct{ active bool }

func (f *automationMembersFake) GetAccountUser(account, user string) (store.AccountUserRecord, bool, error) {
	return store.AccountUserRecord{AccountScopeID: account, UserID: user, Status: store.AccountUserStatusActive}, f.active, nil
}

type automationSessionsFake map[string]store.SessionSnapshot

func (f automationSessionsFake) GetSession(id string) (store.SessionSnapshot, bool, error) {
	s, ok := f[id]
	return s, ok, nil
}

// Purpose: automationAccess resolves agent subjects through canonical sessions and
// rejects revoked membership/cross-account scope before workspace effects. Fake
// boundaries are the narrowest proof of composition identity, not storage IAM.
func TestAutomationCompositionOwnership(t *testing.T) {
	catalog := &automationCatalogFake{}
	members := &automationMembersFake{active: true}
	sessions := automationSessionsFake{"agent": {UserID: "user", AccountScopeID: "account", WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: "workspace"}}}}
	a := automationAccess{workspaces: catalog, members: members, sessions: sessions}
	p := automation.Principal{AccountID: "account", SubjectID: "agent", Role: "agent"}
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	ctx := context.Background()
	if err := a.OccurrenceSession(ctx, p, scope, "agent"); err != nil {
		t.Fatal(err)
	}
	calls := catalog.calls
	if err := a.OccurrenceSession(ctx, p, scope, "other"); err == nil || catalog.calls != calls {
		t.Fatal("foreign occurrence reached catalog")
	}
	members.active = false
	if err := a.Workspace(ctx, p, scope, "run"); err == nil || catalog.calls != calls {
		t.Fatal("revoked membership reached catalog")
	}
	members.active = true
	scope.AccountID = "foreign"
	if err := a.Workspace(ctx, p, scope, "read"); err == nil || catalog.calls != calls {
		t.Fatal("foreign account reached catalog")
	}
}

type automationExecutionFake struct {
	cursors []string
	tickErr error
	repeat  bool
}

func (f *automationExecutionFake) Tick(context.Context, automation.Principal, store.AutomationScope, string, uint64) error {
	return f.tickErr
}
func (f *automationExecutionFake) RecoverPage(_ context.Context, _ automation.Principal, _ store.AutomationScope, _, cursor string) (string, error) {
	f.cursors = append(f.cursors, cursor)
	if len(f.cursors) == 1 || f.repeat {
		return "opaque-cursor", nil
	}
	return "", nil
}

type automationPositionFake struct{ cursor string }

func (f *automationPositionFake) GetAutomationSchedulerPosition(_ string, out any) error {
	*out.(*string) = f.cursor
	return nil
}
func (f *automationPositionFake) SaveAutomationSchedulerPosition(_ string, old, next any) error {
	if old.(string) != f.cursor {
		return store.ErrAutomationConflict
	}
	f.cursor = next.(string)
	return nil
}

// Purpose: recovery continuation must survive worker replacement, remain bounded
// to one page, and continue after admission denial. Fake execution isolates the
// runtime orchestration boundary from providers and proves shutdown has no effects.
func TestAutomationRecoveryBounds(t *testing.T) {
	db := &automationPositionFake{}
	f := &automationExecutionFake{}
	row := store.AutomationRecord{AutomationID: "definition", Revision: 1, Definition: &store.AutomationDefinition{Enabled: true}}
	if err := automationRunDefinition(context.Background(), db, f, automation.Principal{}, row); err != nil {
		t.Fatal(err)
	}
	if len(f.cursors) != 1 || db.cursor != "opaque-cursor" {
		t.Fatal(f.cursors, db.cursor)
	}
	if err := automationRunDefinition(context.Background(), db, f, automation.Principal{}, row); err != nil {
		t.Fatal(err)
	}
	if len(f.cursors) != 2 || f.cursors[1] != "opaque-cursor" || db.cursor != "" {
		t.Fatal(f.cursors, db.cursor)
	}
	f = &automationExecutionFake{tickErr: errors.New("revoked")}
	if err := automationRunDefinition(context.Background(), db, f, automation.Principal{}, row); err == nil || len(f.cursors) != 1 {
		t.Fatal("admission failure blocked recovery")
	}
	f = &automationExecutionFake{repeat: true}
	if err := automationRunDefinition(context.Background(), db, f, automation.Principal{}, row); err == nil || len(f.cursors) != 1 {
		t.Fatal("repeated cursor unbounded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f = &automationExecutionFake{}
	if err := automationRunDefinition(ctx, db, f, automation.Principal{}, row); !errors.Is(err, context.Canceled) || len(f.cursors) != 0 {
		t.Fatal("cancelled sweep had effects")
	}
}

// Purpose: daemon shutdown must cancel and join the sole scheduler worker before
// store close. Channel synchronization proves joining without sleeps or a daemon.
func TestAutomationLoopShutdown(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	l := startAutomationLoop(context.Background(), time.Hour, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}, func(err error) { t.Errorf("shutdown reported as failure: %v", err) })
	select {
	case <-started:
	case <-time.After(time.Second):
		l.cancel()
		t.Fatal("worker did not start")
	}
	done := make(chan struct{})
	go func() { l.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("close returned before worker stopped")
	}
}

// Purpose: outcome recovery is independently bounded and cancellation-safe even
// without an execution service. Real empty-store pagination proves no admission
// or notification dependency is invoked for absent work or a cancelled turn.
func TestAutomationOutcomeEmptyRecovery(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &Daemon{store: db}
	row := store.AutomationRecord{Scope: store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}, AutomationID: "automation"}
	if err := d.automationOutcomes(context.Background(), automation.Principal{}, row); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.automationOutcomes(ctx, automation.Principal{}, row); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

// Purpose: catalog turns must retain one admission window across more than a
// minute of pagination and a store restart, then stop continuation on wrap.
// Real durable positions and fake timestamps isolate pagination from providers.
func TestAutomationCatalogWindowRestart(t *testing.T) {
	path := t.TempDir()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := db.PutJSON(store.AccountScopePrefix()+id, store.AccountScopeRecord{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	start := time.UnixMilli(240000)
	if err := automationSweepAt(context.Background(), db, &automationExecutionFake{}, start); !errors.Is(err, errAutomationContinue) {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for page := 1; page <= 3; page++ {
		var before automationSweepPosition
		if err := db.GetAutomationSchedulerPosition("catalog", &before); err != nil {
			t.Fatal(err)
		}
		if before.At != start.UnixMilli() {
			t.Fatal("window drifted", before)
		}
		err := automationSweepAt(context.Background(), db, &automationExecutionFake{}, start.Add(time.Duration(page)*time.Minute))
		if page < 3 && !errors.Is(err, errAutomationContinue) {
			t.Fatal("pagination stopped early", err)
		}
		if page == 3 && err != nil {
			t.Fatal("empty catalog spun after wrap", err)
		}
	}
	var after automationSweepPosition
	if err := db.GetAutomationSchedulerPosition("catalog", &after); err != nil || after.At != 0 {
		t.Fatal(after, err)
	}
}
