package pebblestore

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Purpose: ApplyV3SessionMutation must serialize cross-session recovery and
// ordinary adoption at the durable batch, not a tool preflight. A losing claim
// must publish neither an event nor a session root. The store layer is the
// narrowest hermetic layer that observes both postconditions.
func TestWorktreeRecoveryExclusivePublication(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	path := filepath.Join(t.TempDir(), "lane")
	createRecoverySession(t, s, "owner", path)
	createRecoverySession(t, s, "first", "")
	createRecoverySession(t, s, "second", "")
	if err := s.DeleteSession("owner"); err != nil { t.Fatal(err) }
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := s.ApplyV3SessionMutation(recoveryInput(id, "reserve", path, 1, 1))
			if err == nil { results <- id } else if !errors.Is(err, ErrWorktreeRecoveryConflict) { t.Error(err) }
		}(id)
	}
	wg.Wait()
	close(results)
	winner := ""
	for id := range results { if winner != "" { t.Fatal("two winners") }; winner = id }
	if winner == "" { t.Fatal("no winner") }
	loser := "first"
	if winner == loser { loser = "second" }
	loserSession, _, err := s.GetSession(loser)
	if err != nil || loserSession.WorktreeRootPath != "" { t.Fatalf("loser mutated: %+v %v", loserSession, err) }
	events, err := s.ListV3SessionEvents(loser, 0, 10)
	if err != nil || len(events) != 1 { t.Fatalf("loser events: %+v %v", events, err) }
	loserSession.WorktreeEnabled, loserSession.WorktreeRootPath = true, path
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID:loser, UserID:"user", AccountScopeID:"account", PayloadHash:"test-payload", IdempotencyKey:"adopt", Kind:V3SessionMutationUpdateSettings, Session:&loserSession})
	if !errors.Is(err, ErrWorktreeRecoveryConflict) { t.Fatalf("ordinary adoption bypass: %v", err) }
	input := recoveryInput(winner, "publish", path, 2, 2)
	current, _, _ := s.GetSession(winner)
	current.WorktreeEnabled, current.WorktreeRootPath, current.WorkspacePath = true, path, path
	input.Session = &current
	published, err := s.ApplyV3SessionMutation(input)
	if err != nil { t.Fatal(err) }
	replayed, err := s.ApplyV3SessionMutation(input)
	if err != nil || !replayed.Replayed || replayed.LastSeq != published.LastSeq { t.Fatalf("replay: %+v %v", replayed, err) }
	records, err := s.InspectWorktreeOwnership("account", "user", []string{path})
	if err != nil || len(records) != 1 || records[0].OwnerSessionID != winner || len(records[0].PreviousOwners) != 1 || records[0].PreviousOwners[0] != "owner" || records[0].OperationState != "published" { t.Fatalf("ownership: %+v %v", records, err) }
}

// Purpose: deleted attribution is necessary but archive, foreign principal,
// absent evidence and stale references cannot authorize a grant. Observe the
// durable ownership revision and claimant session after every rejected mutation.
func TestWorktreeRecoveryRejectsUnprovenAdmission(t *testing.T) {
	for _, scenario := range []string{"archive", "foreign", "missing", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			path := filepath.Join(t.TempDir(), "lane")
			createRecoverySession(t, s, "owner", path)
			createRecoverySession(t, s, "receiver", "")
			if scenario == "archive" { if err := s.ArchiveSession("owner"); err != nil { t.Fatal(err) } } else { if err := s.DeleteSession("owner"); err != nil { t.Fatal(err) } }
			input := recoveryInput("receiver", "reserve", path, 1, 1)
			switch scenario {
			case "foreign": input.AccountScopeID = "other"
			case "missing": input.WorktreeRecovery.Path = filepath.Join(t.TempDir(), "unknown")
			case "stale": input.WorktreeRecovery.ExpectedRevision = 9
			}
			if _, err := s.ApplyV3SessionMutation(input); err == nil { t.Fatal("accepted invalid admission") }
			records, err := s.InspectWorktreeOwnership("account", "user", []string{path})
			if err != nil || records[0].Revision != 1 || records[0].ClaimantSessionID != "" { t.Fatalf("partial claim: %+v %v", records, err) }
			if foreign, err := s.InspectWorktreeOwnership("other", "user", []string{path}); err == nil || foreign != nil { t.Fatalf("foreign attribution leak: %+v %v", foreign, err) }
			events, err := s.ListV3SessionEvents("receiver", 0, 10)
			if err != nil || len(events) != 1 { t.Fatalf("partial event: %+v %v", events, err) }
		})
	}
}

func createRecoverySession(t *testing.T, s *SessionStore, id, path string) {
	t.Helper()
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID:id, UserID:"user", AccountScopeID:"account", PayloadHash:"test-payload", IdempotencyKey:"create", Kind:V3SessionMutationCreateSession, Session:&SessionSnapshot{ID:id, WorktreeEnabled:path != "", WorktreeRootPath:path, WorkspacePath:path}})
	if err != nil { t.Fatal(err) }
}

func recoveryInput(id, action, path string, revision, seq uint64) V3SessionMutationInput {
	return V3SessionMutationInput{SessionID:id, UserID:"user", AccountScopeID:"account", PayloadHash:"test-payload", IdempotencyKey:action, Kind:V3SessionMutationUpdateSettings, ExpectedLastEventSeq:&seq, WorktreeRecovery:&WorktreeRecoveryMutation{Action:action, Path:path, OwnerSessionID:"owner", ExpectedRevision:revision, OperationID:"operation-"+id, Evidence:strings.Repeat("a", 64)}}
}
