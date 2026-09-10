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

type automationCatalogFake struct { calls int }
func (f *automationCatalogFake) GetByWorkspaceIDForPrincipal(p identity.Principal, id string) (store.WorkspaceEntry, bool, error) {
	f.calls++
	return store.WorkspaceEntry{AccountScopeID: p.AccountScopeID, WorkspaceID: id}, true, nil
}
type automationMembersFake struct { active bool }
func (f *automationMembersFake) GetAccountUser(account, user string) (store.AccountUserRecord, bool, error) {
	return store.AccountUserRecord{AccountScopeID: account, UserID: user, Status: store.AccountUserStatusActive}, f.active, nil
}
type automationSessionsFake map[string]store.SessionSnapshot
func (f automationSessionsFake) GetSession(id string) (store.SessionSnapshot, bool, error) { s, ok := f[id]; return s, ok, nil }

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
	if err := a.OccurrenceSession(ctx, p, scope, "agent"); err != nil { t.Fatal(err) }
	calls := catalog.calls
	if err := a.OccurrenceSession(ctx, p, scope, "other"); err == nil || catalog.calls != calls { t.Fatal("foreign occurrence reached catalog") }
	members.active = false
	if err := a.Workspace(ctx, p, scope, "run"); err == nil || catalog.calls != calls { t.Fatal("revoked membership reached catalog") }
	members.active = true
	scope.AccountID = "foreign"
	if err := a.Workspace(ctx, p, scope, "read"); err == nil || catalog.calls != calls { t.Fatal("foreign account reached catalog") }
}

type automationExecutionFake struct { cursors []string; tickErr error; repeat bool }
func (f *automationExecutionFake) Tick(context.Context, automation.Principal, store.AutomationScope, string, uint64) error { return f.tickErr }
func (f *automationExecutionFake) RecoverPage(_ context.Context, _ automation.Principal, _ store.AutomationScope, _, cursor string) (string, error) {
	f.cursors = append(f.cursors, cursor)
	if len(f.cursors) == 1 || f.repeat { return "opaque-cursor", nil }
	return "", nil
}

// Purpose: the daemon recovery adapter must preserve opaque cursors, stop on a
// failed admission, reject nonadvancing pages, and honor shutdown before effects.
// Fake execution isolates lifecycle orchestration from provider/Git execution.
func TestAutomationRecoveryBounds(t *testing.T) {
	f := &automationExecutionFake{}
	row := store.AutomationRecord{AutomationID: "definition", Revision: 1}
	if err := automationRunDefinition(context.Background(), f, automation.Principal{}, row); err != nil { t.Fatal(err) }
	if len(f.cursors) != 2 || f.cursors[0] != "" || f.cursors[1] != "opaque-cursor" { t.Fatal(f.cursors) }
	f = &automationExecutionFake{tickErr: errors.New("revoked")}
	if err := automationRunDefinition(context.Background(), f, automation.Principal{}, row); err == nil || len(f.cursors) != 0 { t.Fatal("failed admission recovered") }
	f = &automationExecutionFake{repeat: true}
	if err := automationRunDefinition(context.Background(), f, automation.Principal{}, row); err == nil || len(f.cursors) != 2 { t.Fatal("repeated cursor unbounded") }
	ctx, cancel := context.WithCancel(context.Background()); cancel()
	f = &automationExecutionFake{}
	if err := automationRunDefinition(ctx, f, automation.Principal{}, row); !errors.Is(err, context.Canceled) || len(f.cursors) != 0 { t.Fatal("cancelled sweep had effects") }
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
	select { case <-started: case <-time.After(time.Second): l.cancel(); t.Fatal("worker did not start") }
	done := make(chan struct{})
	go func() { l.Close(); close(done) }()
	select { case <-done: case <-time.After(time.Second): t.Fatal("shutdown did not join") }
	select { case <-stopped: default: t.Fatal("close returned before worker stopped") }
}
