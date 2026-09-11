package pebblestore

import (
	"errors"
	"reflect"
	"testing"
)

// Purpose: the canonical mutation must preserve the existing conversation on
// upgrade and reject foreign ownership and stale definition policy without any
// session publication. The temporary store is the narrowest durable boundary.
func TestAutomationSessionUpgradePreservesConversation(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	fixture := automationFixture()
	fixture.Record.Definition.SessionID = "conversation"
	if _, _, err := db.ApplyAutomationMutation(fixture); err != nil {
		t.Fatal(err)
	}
	original := SessionSnapshot{ID: "conversation", UserID: "writer", AccountScopeID: "account-a", Title: "Existing discussion", Mode: "auto", WorktreeEnabled: true, MessageCount: 7, LastMessageAt: 99, CreatedAt: 10, Metadata: map[string]any{"context": "keep"}}
	// Seed a pre-existing owned lane: allocation itself is tested by worktree tests.
	if err := s.CreateSession(original); err != nil {
		t.Fatal(err)
	}
	binding := &SessionAutomationBinding{AutomationID: "check", WorkspaceID: "workspace-a", Policy: fixture.Record.Definition.Authorization}
	input := V3SessionMutationInput{SessionID: original.ID, UserID: original.UserID, AccountScopeID: original.AccountScopeID, Kind: V3SessionMutationUpdateMetadata, AutomationBinding: binding, AutomationDefinitionRevision: 1, NowUnixMs: 100001}
	foreign := input
	foreign.AccountScopeID = "foreign"
	if err := s.guardAutomationSessionMutation(&foreign); !errors.Is(err, ErrAutomationInvalid) {
		t.Fatalf("foreign: %v", err)
	}
	stale := input
	copy := *binding
	copy.Policy.ExpiresAt = 9
	stale.AutomationBinding = &copy
	if err := s.guardAutomationSessionMutation(&stale); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("stale: %v", err)
	}
	unchanged, _, err := s.GetSession(original.ID)
	if err != nil || unchanged.Automation != nil {
		t.Fatalf("rejected mutation changed session: %+v %v", unchanged, err)
	}
	if err := s.guardAutomationSessionMutation(&input); err != nil {
		t.Fatal(err)
	}
	got := *input.Session
	got.Automation = nil
	if !reflect.DeepEqual(got, unchanged) {
		t.Fatalf("upgrade replaced conversation: %+v != %+v", got, unchanged)
	}
}

// Purpose: recovery may observe a reserved session before plan installation.
// Missing evidence must block a user turn and unrelated run, never overwrite the
// reservation. Terminal occurrence cancellation is independently store-fenced.
func TestAutomationSessionReservationRejectsUnrelatedTurn(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	session := SessionSnapshot{ID: "conversation", UserID: "writer", AccountScopeID: "account-a", Automation: &SessionAutomationBinding{AutomationID: "check", WorkspaceID: "workspace-a", ExecutionKey: "key", OccurrenceID: "occurrence", PlanID: "automation-plan:key"}}
	if err := s.CreateSession(session); err != nil {
		t.Fatal(err)
	}
	for _, input := range []V3SessionMutationInput{
		{SessionID: session.ID, Message: &MessageSnapshot{Role: "user", Content: "do not lose this"}},
		{SessionID: session.ID, RunIntent: &V3SessionRunIntent{RunID: "user-run", Status: V3RunIntentPendingExecutor}},
	} {
		if err := s.guardAutomationSessionMutation(&input); !errors.Is(err, ErrAutomationConflict) {
			t.Fatalf("reservation bypass: %v", err)
		}
	}
	got, _, err := s.GetSession(session.ID)
	if err != nil || !reflect.DeepEqual(got.Automation, session.Automation) || got.MessageCount != 0 {
		t.Fatalf("rejection mutated history: %+v %v", got, err)
	}
}

// Purpose: a restart between reservation and plan publication must not admit a
// competing turn or forget the occurrence. A real reopen proves durable recovery
// input, rather than relying on a retained host object or process-local lock.
func TestAutomationSessionReservationSurvivesRestart(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	original := SessionSnapshot{ID: "conversation", UserID: "writer", AccountScopeID: "account-a", Automation: &SessionAutomationBinding{AutomationID: "check", WorkspaceID: "workspace-a", ExecutionKey: "key", OccurrenceID: "occurrence", PlanID: "automation-plan:key"}}
	if err := s.CreateSession(original); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	input := V3SessionMutationInput{SessionID: original.ID, Message: &MessageSnapshot{Role: "user", Content: "retained by client"}}
	if err := s.guardAutomationSessionMutation(&input); !errors.Is(err, ErrAutomationConflict) {
		t.Fatalf("restart lost reservation: %v", err)
	}
	got, found, err := s.GetSession(original.ID)
	if err != nil || !found || !reflect.DeepEqual(got.Automation, original.Automation) {
		t.Fatalf("restart lost identity: %+v %v", got, err)
	}
}
