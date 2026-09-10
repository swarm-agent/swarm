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
	if err := s.DeleteSession("owner"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := s.ApplyV3SessionMutation(recoveryInput(id, "reserve", path, 1, 1))
			if err == nil {
				results <- id
			} else if !errors.Is(err, ErrWorktreeRecoveryConflict) {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	close(results)
	winner := ""
	for id := range results {
		if winner != "" {
			t.Fatal("two winners")
		}
		winner = id
	}
	if winner == "" {
		t.Fatal("no winner")
	}
	loser := "first"
	if winner == loser {
		loser = "second"
	}
	loserSession, _, err := s.GetSession(loser)
	if err != nil || loserSession.WorktreeRootPath != "" {
		t.Fatalf("loser mutated: %+v %v", loserSession, err)
	}
	events, err := s.ListV3SessionEvents(loser, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("loser events: %+v %v", events, err)
	}
	loserSession.WorktreeEnabled, loserSession.WorktreeRootPath, loserSession.WorktreeBranch = true, path, "agent/owner"
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: loser, UserID: "user", AccountScopeID: "account", PayloadHash: "test-payload", IdempotencyKey: "adopt", Kind: V3SessionMutationUpdateSettings, Session: &loserSession})
	if !errors.Is(err, ErrWorktreeRecoveryConflict) {
		t.Fatalf("ordinary adoption bypass: %v", err)
	}
	input := recoveryInput(winner, "publish", path, 2, 2)
	current, _, _ := s.GetSession(winner)
	current.WorktreeEnabled, current.WorktreeRootPath, current.WorktreeBranch = true, path, "agent/owner"
	input.Session = &current
	published, err := s.ApplyV3SessionMutation(input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.ApplyV3SessionMutation(input)
	if err != nil || !replayed.Replayed || replayed.LastSeq != published.LastSeq {
		t.Fatalf("replay: %+v %v", replayed, err)
	}
	records, err := s.InspectWorktreeOwnership("account", "user", []string{path})
	if err != nil || len(records) != 1 || records[0].OwnerSessionID != winner || len(records[0].PreviousOwners) != 1 || records[0].PreviousOwners[0] != "owner" || records[0].OperationState != "published" {
		t.Fatalf("ownership: %+v %v", records, err)
	}
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
			if scenario == "archive" {
				if err := s.ArchiveSession("owner"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.DeleteSession("owner"); err != nil {
					t.Fatal(err)
				}
			}
			input := recoveryInput("receiver", "reserve", path, 1, 1)
			switch scenario {
			case "foreign":
				input.AccountScopeID = "other"
			case "missing":
				input.WorktreeRecovery.Path = filepath.Join(t.TempDir(), "unknown")
			case "stale":
				input.WorktreeRecovery.ExpectedRevision = 9
			}
			if _, err := s.ApplyV3SessionMutation(input); err == nil {
				t.Fatal("accepted invalid admission")
			}
			records, err := s.InspectWorktreeOwnership("account", "user", []string{path})
			if err != nil || records[0].Revision != 1 || records[0].ClaimantSessionID != "" {
				t.Fatalf("partial claim: %+v %v", records, err)
			}
			if foreign, err := s.InspectWorktreeOwnership("other", "user", []string{path}); err == nil || foreign != nil {
				t.Fatalf("foreign attribution leak: %+v %v", foreign, err)
			}
			events, err := s.ListV3SessionEvents("receiver", 0, 10)
			if err != nil || len(events) != 1 {
				t.Fatalf("partial event: %+v %v", events, err)
			}
		})
	}
}

func createRecoverySession(t *testing.T, s *SessionStore, id, path string) {
	t.Helper()
	for i := 0; i < 10; i++ {
		done, err := s.BackfillRepositoryHistory(100)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
		if i == 9 {
			t.Fatal("history backfill exceeded fixture bound")
		}
	}
	_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{WorktreeAdmission: &WorktreeAdmissionEvidence{Kind: "allocated", Path: path, SourcePath: path, OwnerSessionID: id, Branch: "agent/" + id}, SessionID: id, UserID: "user", AccountScopeID: "account", PayloadHash: "test-payload", IdempotencyKey: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: id, WorktreeEnabled: path != "", WorktreeRootPath: path, WorktreeBranch: "agent/" + id, WorkspacePath: path}})
	if err != nil {
		t.Fatal(err)
	}
}

func recoveryInput(id, action, path string, revision, seq uint64) V3SessionMutationInput {
	return V3SessionMutationInput{SessionID: id, UserID: "user", AccountScopeID: "account", PayloadHash: "test-payload", IdempotencyKey: action, Kind: V3SessionMutationUpdateSettings, ExpectedLastEventSeq: &seq, WorktreeRecovery: &WorktreeRecoveryMutation{Action: action, Path: path, OwnerSessionID: "owner", ExpectedRevision: revision, OperationID: "operation-" + id, Evidence: strings.Repeat("a", 64)}}
}

// Purpose: publication through prepareWorktreeOwnership must preserve the saved
// source workspace, and a stale claim must leave both ownership and events intact.
// This store-level test observes atomic postconditions without filesystem mocks.
func TestWorktreeRecoveryPublicationPreservesSource(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	path := filepath.Join(t.TempDir(), "lane")
	source := filepath.Join(t.TempDir(), "source")
	createRecoverySession(t, s, "owner", path)
	createRecoverySession(t, s, "receiver", "")
	current, _, err := s.GetSession("receiver")
	if err != nil {
		t.Fatal(err)
	}
	current.WorkspacePath = source
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "receiver", UserID: "user", AccountScopeID: "account", PayloadHash: "source", IdempotencyKey: "source", Kind: V3SessionMutationUpdateSettings, Session: &current})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession("owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyV3SessionMutation(recoveryInput("receiver", "reserve", path, 1, 2)); err != nil {
		t.Fatal(err)
	}
	current, _, err = s.GetSession("receiver")
	if err != nil {
		t.Fatal(err)
	}
	current.WorktreeEnabled, current.WorktreeRootPath, current.WorktreeBranch = true, path, "agent/owner"
	input := recoveryInput("receiver", "publish", path, 1, 3)
	input.Session = &current
	if _, err := s.ApplyV3SessionMutation(input); !errors.Is(err, ErrWorktreeRecoveryConflict) {
		t.Fatalf("stale publication: %v", err)
	}
	records, err := s.InspectWorktreeOwnership("account", "user", []string{path})
	if err != nil || len(records) != 1 || records[0].Revision != 2 || records[0].OwnerSessionID != "owner" || records[0].ClaimantSessionID != "receiver" {
		t.Fatalf("partial publication: %+v %v", records, err)
	}
	events, err := s.ListV3SessionEvents("receiver", 0, 10)
	if err != nil || len(events) != 3 {
		t.Fatalf("partial event: %+v %v", events, err)
	}
	input.WorktreeRecovery.ExpectedRevision = 2
	if _, err := s.ApplyV3SessionMutation(input); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetSession("receiver")
	if err != nil || !ok || got.WorkspacePath != source || got.WorktreeRootPath != path {
		t.Fatalf("source/runtime identity lost: %+v %v", got, err)
	}
}

// Purpose: copy publication at ApplyV3SessionMutation must atomically release
// the source reservation without transferring its owner, admit only a newly
// allocated destination, and retain exact source evidence. Wrong evidence must
// leave both claims and session events unchanged. This is the narrowest batch test.
func TestWorktreeRecoveryCopyPublication(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	root := t.TempDir()
	path, destination := filepath.Join(root, "lane"), filepath.Join(root, "copy")
	createRecoverySession(t, s, "owner", path)
	createRecoverySession(t, s, "receiver", "")
	reserve := recoveryInput("receiver", "reserve_copy", path, 1, 1)
	if _, err := s.ApplyV3SessionMutation(reserve); err != nil {
		t.Fatal(err)
	}
	owner, _, _ := s.GetSession("owner")
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "owner", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationUpdateSettings, IdempotencyKey: "write", PayloadHash: "write", Session: &owner}); !errors.Is(err, ErrWorktreeRecoveryConflict) {
		t.Fatalf("reserved writer: %v", err)
	}
	current, _, _ := s.GetSession("receiver")
	current.WorktreeEnabled, current.WorktreeRootPath, current.WorktreeBranch = true, destination, "agent/copy"
	// A real source identity is distinct from the allocated runtime root.
	current.WorkspacePath = filepath.Join(root, "source")
	// Establish source identity before reserve in normal callers; this fixture
	// uses a delegated runtime snapshot only in the separate admission test.
	input := recoveryInput("receiver", "publish_copy", path, 2, 2)
	input.Session = &current
	input.WorktreeAdmission = &WorktreeAdmissionEvidence{Kind: "allocated", Path: destination, SourcePath: current.WorkspacePath, OwnerSessionID: "receiver", Branch: current.WorktreeBranch}
	if _, err := s.ApplyV3SessionMutation(input); !errors.Is(err, ErrWorktreeRecoveryConflict) {
		t.Fatalf("changed source accepted: %v", err)
	}
	// Keep receiver's original source immutable, then supply a valid source in
	// the initial session fixture for the successful publication below.
	if _, err := s.ApplyV3SessionMutation(recoveryInput("receiver", "release", path, 2, 2)); err != nil {
		t.Fatal(err)
	}
	released, err := s.InspectWorktreeOwnership("account", "user", []string{path})
	if err != nil || released[0].OwnerSessionID != "owner" || released[0].ClaimantSessionID != "" {
		t.Fatalf("release: %+v %v", released, err)
	}
	current, _, _ = s.GetSession("receiver")
	current.WorkspacePath = filepath.Join(root, "source")
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "receiver", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationUpdateSettings, IdempotencyKey: "source", PayloadHash: "source", Session: &current}); err != nil {
		t.Fatal(err)
	}
	reserve = recoveryInput("receiver", "reserve_copy", path, 3, 4)
	reserve.IdempotencyKey, reserve.WorktreeRecovery.OperationID = "copy-again", "copy-again"
	if _, err := s.ApplyV3SessionMutation(reserve); err != nil {
		t.Fatal(err)
	}
	journal := recoveryInput("receiver", "journal_copy", path, 4, 5)
	journal.WorktreeRecovery.OperationID = "copy-again"
	journal.WorktreeRecovery.DestinationPath, journal.WorktreeRecovery.DestinationBranch, journal.WorktreeRecovery.DestinationBase = destination, "agent/copy", strings.Repeat("a", 40)
	if _, err := s.ApplyV3SessionMutation(journal); err != nil {
		t.Fatal(err)
	}
	current.Metadata = map[string]any{"swarm_v3_worktree_base_commit": strings.Repeat("a", 40)}
	input = recoveryInput("receiver", "publish_copy", path, 5, 6)
	input.WorktreeRecovery.OperationID = "copy-again"
	current.WorktreeEnabled, current.WorktreeRootPath, current.WorktreeBranch = true, destination, "agent/copy"
	input.Session = &current
	input.WorktreeAdmission = &WorktreeAdmissionEvidence{Kind: "allocated", Path: destination, SourcePath: current.WorkspacePath, OwnerSessionID: "receiver", Branch: current.WorktreeBranch}
	input.WorktreeRecovery.Evidence = strings.Repeat("b", 64)
	if _, err := s.ApplyV3SessionMutation(input); !errors.Is(err, ErrWorktreeRecoveryConflict) {
		t.Fatalf("wrong evidence: %v", err)
	}
	if claims, err := s.InspectWorktreeOwnership("account", "user", []string{destination}); err != nil || claims[0].OperationState != "allocating_copy" || claims[0].ClaimantSessionID != "receiver" {
		t.Fatalf("failed publication changed destination fence: %+v %v", claims, err)
	}
	input.WorktreeRecovery.Evidence = strings.Repeat("a", 64)
	if _, err := s.ApplyV3SessionMutation(input); err != nil {
		t.Fatal(err)
	}
	records, err := s.InspectWorktreeOwnership("account", "user", []string{path, destination})
	if err != nil {
		t.Fatal(err)
	}
	if records[0].OwnerSessionID != "owner" || records[0].ClaimantSessionID != "" || records[0].Revision != 6 || records[1].OwnerSessionID != "receiver" || records[1].CopySource == nil || records[1].CopySource.Path != path || records[1].CopySource.Revision != 5 {
		t.Fatalf("copy claims: %+v", records)
	}
	if result, err := s.ApplyV3SessionMutation(input); err != nil || !result.Replayed {
		t.Fatalf("copy replay: %+v %v", result, err)
	}
}

// Purpose: the narrow ownership preparation guard must reject a newly queued
// writer while a copy reservation survives restart, without blocking terminal
// run reconciliation. The persisted reservation must remain unchanged.
func TestWorktreeRecoveryCopyRestartWriterGuard(t *testing.T) {
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	path := filepath.Join(root, "lane")
	createRecoverySession(t, s, "owner", path)
	createRecoverySession(t, s, "receiver", "")
	if _, err := s.ApplyV3SessionMutation(recoveryInput("receiver", "reserve_copy", path, 1, 1)); err != nil {
		t.Fatal(err)
	}
	journal := recoveryInput("receiver", "journal_copy", path, 2, 2)
	journal.WorktreeRecovery.DestinationPath = filepath.Join(root, "retained")
	journal.WorktreeRecovery.DestinationBranch = "agent/retained"
	journal.WorktreeRecovery.DestinationBase = strings.Repeat("a", 40)
	if _, err := s.ApplyV3SessionMutation(journal); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	// R16/R22: program admission after restart must share the reservation
	// fence, not merely the ordinary session adoption lock.
	program := taskProgramStoreFixture("owner", "fenced", "fenced-hash")
	if _, _, err := s.CreateTaskProgram(program); !errors.Is(err, ErrWorktreeRecoveryConflict) {
		t.Fatalf("program bypassed fence: %v", err)
	}
	if _, found, err := s.GetTaskProgram("owner", "fenced"); err != nil || found {
		t.Fatal("rejected program persisted")
	}
	for _, status := range []string{V3RunIntentPendingExecutor, V3RunIntentRunning, V3RunIntentCompleted} {
		_, err := s.prepareWorktreeOwnership(V3SessionMutationInput{SessionID: "owner", RunIntent: &V3SessionRunIntent{Status: status}}, SessionSnapshot{})
		if status == V3RunIntentCompleted {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrWorktreeRecoveryConflict) {
			t.Fatalf("writer admitted: %s %v", status, err)
		}
	}
	records, err := s.InspectWorktreeOwnership("account", "user", []string{path})
	if err != nil || records[0].Revision != 3 || records[0].DestinationPath != filepath.Join(root, "retained") || records[0].OperationState != "reserved_copy" || records[0].OwnerSessionID != "owner" {
		t.Fatalf("reservation changed: %+v %v", records, err)
	}
}
