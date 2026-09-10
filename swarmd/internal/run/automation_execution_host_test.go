package run

import (
	"context"
	"errors"
	"strings"
	"testing"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: host replay must not create another attempt/intent, and cancellation
// before creation must fence all recovered starts. This service/store layer is
// the narrowest proof of AutomationExecutionHost and canonical mutation effects;
// it uses no provider, executor goroutine, or ambient workspace.
func TestAutomationHostRecoveredStartAndCancellation(t *testing.T) {
	db, err := store.Open(t.TempDir()); if err != nil { t.Fatal(err) }; defer db.Close()
	repository := store.NewSessionStore(db)
	svc := sessions.NewService(repository, nil)
	key := strings.Repeat("a", 64)
	snapshot := store.SessionSnapshot{ID: "automation-"+key, AccountScopeID: "account", UserID: "user", Mode: sessions.ModeAuto, WorktreeEnabled: true, Metadata: map[string]any{"automation_execution_key": key, "automation_execution_policy": "{}"}}
	if _, err := svc.ApplySessionMutation(sessions.SessionMutationInput{SessionID: snapshot.ID, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID, Kind: sessions.SessionMutationCreateSession, Session: &snapshot, ClientRequestID: "create", IdempotencyKey: "create", PayloadHash: "create", RequestHash: "create"}); err != nil { t.Fatal(err) }
	request := "existing-start"
	if _, err := svc.ApplySessionMutation(sessions.SessionMutationInput{SessionID: snapshot.ID, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID, Kind: sessions.SessionMutationRecordRunIntent, RunIntent: &store.V3SessionRunIntent{RunID: "automation-run:"+key, Status: sessions.RunIntentPendingExecutor}, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request}); err != nil { t.Fatal(err) }
	writes := 0
	apply := func(in sessions.SessionMutationInput) (sessions.SessionMutationResult, error) { writes++; return svc.ApplySessionMutation(in) }
	for i := 0; i < 2; i++ {
		host, err := NewAutomationExecutionHost(&Service{sessions: svc}, repository, apply); if err != nil { t.Fatal(err) }
		if err := host.Start(context.Background(), snapshot, key); err != nil { t.Fatal(err) }
	}
	if writes != 0 { t.Fatalf("replay wrote %d mutations", writes) }
	host, _ := NewAutomationExecutionHost(&Service{sessions: svc}, repository, apply)
	otherKey := strings.Repeat("b", 64)
	absent := store.SessionSnapshot{ID: "automation-"+otherKey, AccountScopeID: "account"}
	if err := host.Cancel(context.Background(), absent, otherKey); err != nil { t.Fatal(err) }
	if err := host.Start(context.Background(), absent, otherKey); !errors.Is(err, store.ErrAutomationExecutionCancelled) { t.Fatalf("start after cancel: %v", err) }
	if writes != 0 { t.Fatal("cancel-before-create wrote session state") }
}
