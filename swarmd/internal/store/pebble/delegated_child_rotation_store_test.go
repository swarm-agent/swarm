package pebblestore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func openDelegatedChildRotationTestStore(t *testing.T) (*Store, *SessionStore) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "delegated-child-rotation.pebble"))
	if err != nil {
		t.Fatalf("open delegated child rotation store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close delegated child rotation store: %v", err)
		}
	})
	return store, NewSessionStore(store)
}

func createDelegatedChildLineageForTest(t *testing.T, sessions *SessionStore, workspacePath string) DelegatedChildLineageRecord {
	t.Helper()
	lineage, changed, err := sessions.CreateDelegatedChildLineage(
		DelegatedChildLineageRecord{
			AccountScopeID: "account-1",
			LogicalTaskID:  "logical-task-1",
			ProgramID:      "program-1",
			JobID:          "job-1",
		},
		DelegatedChildGenerationRecord{
			SessionID:             "session-1",
			ParentSessionID:       "parent-session",
			RunID:                 "run-1",
			ParentRunID:           "parent-run",
			AttemptID:             "attempt-1",
			PermissionPrincipalID: "principal-1",
			PermissionScopeID:     "scope-1",
			ReservationSessionID:  "reservation-session",
			ReservationRunID:      "reservation-run",
			ReservationCallID:     "reservation-call",
			WorkspacePath:         workspacePath,
			WorktreeBranch:        "agent/context-rotation",
			ParentBranch:          "dev",
			ImmutableBaseCommit:   "base-commit",
		},
		"create-1",
	)
	if err != nil || !changed {
		t.Fatalf("create delegated child lineage: changed=%t err=%v", changed, err)
	}
	return lineage
}

func rotateDelegatedChildForTest(t *testing.T, sessions *SessionStore, lineage DelegatedChildLineageRecord, successorSessionID, mutationID string, handoff DelegatedChildTargetedHandoff) DelegatedChildLineageRecord {
	t.Helper()
	predecessor, ok, err := sessions.GetDelegatedChildGeneration(lineage.AccountScopeID, lineage.LogicalTaskID, lineage.CurrentGeneration)
	if err != nil || !ok {
		t.Fatalf("get predecessor generation %d: ok=%t err=%v", lineage.CurrentGeneration, ok, err)
	}
	lease, ok, err := sessions.GetDelegatedWorktreeOwner(lineage.AccountScopeID, predecessor.WorkspacePath)
	if err != nil || !ok {
		t.Fatalf("get predecessor lease: ok=%t err=%v", ok, err)
	}
	rotated, changed, err := sessions.RotateDelegatedChild(RotateDelegatedChildInput{
		AccountScopeID:              lineage.AccountScopeID,
		LogicalTaskID:               lineage.LogicalTaskID,
		ExpectedLineageRevision:     lineage.Revision,
		ExpectedPredecessorRevision: predecessor.Revision,
		ExpectedLeaseRevision:       lease.Revision,
		PredecessorGeneration:       predecessor.Generation,
		PredecessorSessionID:        predecessor.SessionID,
		MutationID:                  mutationID,
		Successor: DelegatedChildGenerationRecord{
			SessionID:             successorSessionID,
			ParentSessionID:       predecessor.ParentSessionID,
			RunID:                 fmt.Sprintf("run-%d", predecessor.Generation+1),
			ParentRunID:           predecessor.ParentRunID,
			AttemptID:             fmt.Sprintf("attempt-%d", predecessor.Generation+1),
			PermissionPrincipalID: predecessor.PermissionPrincipalID,
			PermissionScopeID:     predecessor.PermissionScopeID,
			ReservationSessionID:  predecessor.ReservationSessionID,
			ReservationRunID:      predecessor.ReservationRunID,
			ReservationCallID:     predecessor.ReservationCallID,
		},
		Handoff: handoff,
	})
	if err != nil || !changed {
		t.Fatalf("rotate to %s: changed=%t err=%v", successorSessionID, changed, err)
	}
	return rotated
}

func TestDelegatedChildRotationPersistsTargetedHandoffAcrossReopen(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "rotation.pebble")
	workspacePath := filepath.Join(root, "managed-worktree")
	if err := os.MkdirAll(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(store)
	lineage := createDelegatedChildLineageForTest(t, sessions, workspacePath)

	rows := make([]string, maxDelegatedChildHandoffRows+7)
	for i := range rows {
		rows[i] = fmt.Sprintf("completed-%03d", i)
	}
	longObjective := strings.Repeat("界", maxDelegatedChildHandoffTextRunes+9)
	lineage = rotateDelegatedChildForTest(t, sessions, lineage, "session-2", "rotate-1", DelegatedChildTargetedHandoff{
		Objective:     longObjective,
		Completed:     rows,
		NextActions:   []string{"continue from current files", "run focused validation"},
		Decisions:     []string{"use bounded successor context only"},
		Constraints:   []string{"preserve the exact worktree"},
		RelevantFiles: []string{"swarmd/internal/run/context_rotation.go"},
		Validation:    []string{"store transition committed"},
	})
	if err := store.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	restarted := NewSessionStore(reopened)

	persistedLineage, ok, err := restarted.GetDelegatedChildLineage("account-1", "logical-task-1")
	if err != nil || !ok {
		t.Fatalf("get lineage after reopen: ok=%t err=%v", ok, err)
	}
	if persistedLineage.CurrentGeneration != 2 || persistedLineage.CurrentSessionID != "session-2" || persistedLineage.Revision != lineage.Revision {
		t.Fatalf("persisted lineage = %+v", persistedLineage)
	}
	predecessor, ok, err := restarted.GetDelegatedChildGeneration("account-1", "logical-task-1", 1)
	if err != nil || !ok || predecessor.State != DelegatedChildGenerationRetired || predecessor.SuccessorSessionID != "session-2" {
		t.Fatalf("persisted predecessor = %+v ok=%t err=%v", predecessor, ok, err)
	}
	successor, ok, err := restarted.GetDelegatedChildGeneration("account-1", "logical-task-1", 2)
	if err != nil || !ok || successor.State != DelegatedChildGenerationActive || successor.PredecessorSessionID != "session-1" {
		t.Fatalf("persisted successor = %+v ok=%t err=%v", successor, ok, err)
	}
	if successor.PermissionPrincipalID != predecessor.PermissionPrincipalID || successor.ReservationCallID != predecessor.ReservationCallID || successor.WorkspacePath != predecessor.WorkspacePath || successor.WorktreeBranch != predecessor.WorktreeBranch {
		t.Fatalf("successor did not preserve delegated identity: predecessor=%+v successor=%+v", predecessor, successor)
	}
	handoff, ok, err := restarted.GetDelegatedChildHandoff("account-1", "logical-task-1", 1)
	if err != nil || !ok {
		t.Fatalf("get handoff after reopen: ok=%t err=%v", ok, err)
	}
	if got := len([]rune(handoff.Objective)); got != maxDelegatedChildHandoffTextRunes {
		t.Fatalf("objective rune count = %d, want %d", got, maxDelegatedChildHandoffTextRunes)
	}
	if len(handoff.Completed) != maxDelegatedChildHandoffRows || handoff.Completed[0] != "completed-000" || handoff.Completed[len(handoff.Completed)-1] != "completed-127" {
		t.Fatalf("bounded completed rows = %#v", handoff.Completed)
	}
	if handoff.PredecessorSessionID != "session-1" || handoff.SuccessorSessionID != "session-2" || handoff.PredecessorGeneration != 1 || handoff.SuccessorGeneration != 2 {
		t.Fatalf("handoff lineage = %+v", handoff)
	}
	lease, ok, err := restarted.GetDelegatedWorktreeOwner("account-1", workspacePath)
	if err != nil || !ok || lease.Generation != 2 || lease.SessionID != "session-2" || lease.Revision != 2 {
		t.Fatalf("persisted lease = %+v ok=%t err=%v", lease, ok, err)
	}

	raw, ok, err := reopened.GetBytes(KeyDelegatedChildHandoff("account-1", "logical-task-1", 1))
	if err != nil || !ok {
		t.Fatalf("get raw targeted handoff: ok=%t err=%v", ok, err)
	}
	var persistedFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &persistedFields); err != nil {
		t.Fatalf("decode targeted handoff: %v", err)
	}
	allowedFields := map[string]bool{
		"account_scope_id": true, "logical_task_id": true,
		"predecessor_generation": true, "successor_generation": true,
		"predecessor_session_id": true, "successor_session_id": true,
		"objective": true, "completed": true, "next_actions": true,
		"decisions": true, "constraints": true, "relevant_files": true,
		"validation": true, "created_at": true,
	}
	for field := range persistedFields {
		if !allowedFields[field] {
			t.Errorf("targeted handoff persisted unexpected transcript-like field %q: %s", field, raw)
		}
	}
	for _, forbidden := range []string{"transcript", "messages", "conversation", "tool_output", "provider_request"} {
		if bytes.Contains(bytes.ToLower(raw), []byte(forbidden)) {
			t.Errorf("targeted handoff persisted forbidden transcript field %q: %s", forbidden, raw)
		}
	}
}

func TestDelegatedChildRotationTransfersLeaseWithoutMutatingWorktree(t *testing.T) {
	_, sessions := openDelegatedChildRotationTestStore(t)
	workspacePath := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(filepath.Join(workspacePath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"tracked-dirty.txt":           []byte("locally modified tracked contents\n"),
		"untracked.txt":               []byte("untracked contents\n"),
		filepath.Join(".git", "HEAD"): []byte("ref: refs/heads/agent/context-rotation\n"),
	}
	for name, content := range files {
		path := filepath.Join(workspacePath, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	lineage := createDelegatedChildLineageForTest(t, sessions, workspacePath)
	if _, changed, err := sessions.CreateDelegatedChildLineage(
		DelegatedChildLineageRecord{AccountScopeID: "account-1", LogicalTaskID: "competing-task"},
		DelegatedChildGenerationRecord{SessionID: "competing-session", WorkspacePath: workspacePath, WorktreeBranch: "other-branch"},
		"competing-create",
	); err == nil || changed {
		t.Fatalf("competing worktree ownership: changed=%t err=%v", changed, err)
	}

	lineage = rotateDelegatedChildForTest(t, sessions, lineage, "session-2", "rotate-1", DelegatedChildTargetedHandoff{Objective: "continue generation two", NextActions: []string{"continue"}})
	lineage = rotateDelegatedChildForTest(t, sessions, lineage, "session-3", "rotate-2", DelegatedChildTargetedHandoff{Objective: "continue generation three", NextActions: []string{"continue"}})

	if lineage.CurrentGeneration != 3 || lineage.CurrentSessionID != "session-3" || len(lineage.GenerationHistory) != 3 {
		t.Fatalf("repeated rotation lineage = %+v", lineage)
	}
	for generation, want := range []struct {
		state       string
		sessionID   string
		predecessor string
		successor   string
	}{
		{DelegatedChildGenerationRetired, "session-1", "", "session-2"},
		{DelegatedChildGenerationRetired, "session-2", "session-1", "session-3"},
		{DelegatedChildGenerationActive, "session-3", "session-2", ""},
	} {
		record, ok, err := sessions.GetDelegatedChildGeneration("account-1", "logical-task-1", generation+1)
		if err != nil || !ok || record.State != want.state || record.SessionID != want.sessionID || record.PredecessorSessionID != want.predecessor || record.SuccessorSessionID != want.successor {
			t.Errorf("generation %d = %+v ok=%t err=%v, want %+v", generation+1, record, ok, err, want)
		}
	}
	lease, ok, err := sessions.GetDelegatedWorktreeOwner("account-1", workspacePath)
	if err != nil || !ok || lease.Generation != 3 || lease.SessionID != "session-3" || lease.Revision != 3 || lease.WorkspacePath != filepath.Clean(workspacePath) || lease.WorktreeBranch != "agent/context-rotation" {
		t.Fatalf("current owner lease = %+v ok=%t err=%v", lease, ok, err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(workspacePath, name))
		if err != nil {
			t.Errorf("read preserved %s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("worktree file %s mutated: got %q want %q", name, got, want)
		}
	}
}

func TestDelegatedChildRotationRejectsIncompleteHandoffAndPreservesManagedArtifactIdentity(t *testing.T) {
	_, sessions := openDelegatedChildRotationTestStore(t)
	lineage := createDelegatedChildLineageForTest(t, sessions, filepath.Join(t.TempDir(), "worktree"))
	generation, ok, err := sessions.GetDelegatedChildGeneration("account-1", "logical-task-1", 1)
	if err != nil || !ok {
		t.Fatalf("get generation one: ok=%t err=%v", ok, err)
	}
	generation.ManagedArtifactParentSessionID = "parent-session"
	generation.ManagedArtifactCollectionID = "collection-1"
	generation.ManagedArtifactVariantID = "variant-1"
	generation.ManagedArtifactTaskCallID = "reservation-call"
	generation.ManagedArtifactProgramID = "program-1"
	generation.ManagedArtifactProgramJobID = "job-1"
	if err := sessions.store.PutJSON(KeyDelegatedChildGeneration("account-1", "logical-task-1", 1), generation); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := sessions.GetDelegatedWorktreeOwner("account-1", generation.WorkspacePath)
	if err != nil || !ok {
		t.Fatalf("get lease: ok=%t err=%v", ok, err)
	}
	input := RotateDelegatedChildInput{
		AccountScopeID: "account-1", LogicalTaskID: "logical-task-1",
		ExpectedLineageRevision: lineage.Revision, ExpectedPredecessorRevision: generation.Revision,
		ExpectedLeaseRevision: lease.Revision, PredecessorGeneration: 1, PredecessorSessionID: "session-1",
		MutationID: "rotate-managed", Successor: DelegatedChildGenerationRecord{SessionID: "session-2"},
		Handoff: DelegatedChildTargetedHandoff{Objective: "continue managed work"},
	}
	if _, changed, err := sessions.RotateDelegatedChild(input); err == nil || changed {
		t.Fatalf("incomplete handoff rotated: changed=%t err=%v", changed, err)
	}
	input.Handoff.NextActions = []string{"finish the same variant"}
	rotated, changed, err := sessions.RotateDelegatedChild(input)
	if err != nil || !changed {
		t.Fatalf("rotate managed generation: changed=%t err=%v", changed, err)
	}
	successor, ok, err := sessions.GetDelegatedChildGenerationBySession("account-1", rotated.CurrentSessionID)
	if err != nil || !ok {
		t.Fatalf("resolve successor by session: ok=%t err=%v", ok, err)
	}
	if successor.ManagedArtifactParentSessionID != generation.ManagedArtifactParentSessionID || successor.ManagedArtifactCollectionID != generation.ManagedArtifactCollectionID || successor.ManagedArtifactVariantID != generation.ManagedArtifactVariantID || successor.ManagedArtifactTaskCallID != generation.ManagedArtifactTaskCallID || successor.ManagedArtifactProgramID != generation.ManagedArtifactProgramID || successor.ManagedArtifactProgramJobID != generation.ManagedArtifactProgramJobID {
		t.Fatalf("managed artifact identity drifted: predecessor=%+v successor=%+v", generation, successor)
	}
}

func TestDelegatedChildRotationConcurrentMutationIsSingleWinnerAndIdempotent(t *testing.T) {
	_, sessions := openDelegatedChildRotationTestStore(t)
	lineage := createDelegatedChildLineageForTest(t, sessions, filepath.Join(t.TempDir(), "worktree"))
	generation, ok, err := sessions.GetDelegatedChildGeneration("account-1", "logical-task-1", 1)
	if err != nil || !ok {
		t.Fatalf("get generation one: ok=%t err=%v", ok, err)
	}
	lease, ok, err := sessions.GetDelegatedWorktreeOwner("account-1", generation.WorkspacePath)
	if err != nil || !ok {
		t.Fatalf("get generation one lease: ok=%t err=%v", ok, err)
	}

	type result struct {
		lineage DelegatedChildLineageRecord
		changed bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	inputs := []RotateDelegatedChildInput{
		{
			AccountScopeID: "account-1", LogicalTaskID: "logical-task-1",
			ExpectedLineageRevision: lineage.Revision, ExpectedPredecessorRevision: generation.Revision,
			ExpectedLeaseRevision: lease.Revision, PredecessorGeneration: 1, PredecessorSessionID: "session-1",
			MutationID: "race-a", Successor: DelegatedChildGenerationRecord{SessionID: "session-a"},
			Handoff: DelegatedChildTargetedHandoff{Objective: "winner a", NextActions: []string{"continue"}},
		},
		{
			AccountScopeID: "account-1", LogicalTaskID: "logical-task-1",
			ExpectedLineageRevision: lineage.Revision, ExpectedPredecessorRevision: generation.Revision,
			ExpectedLeaseRevision: lease.Revision, PredecessorGeneration: 1, PredecessorSessionID: "session-1",
			MutationID: "race-b", Successor: DelegatedChildGenerationRecord{SessionID: "session-b"},
			Handoff: DelegatedChildTargetedHandoff{Objective: "winner b", NextActions: []string{"continue"}},
		},
	}
	var wg sync.WaitGroup
	for _, input := range inputs {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, changed, err := sessions.RotateDelegatedChild(input)
			results <- result{lineage: got, changed: changed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var winner result
	wins, rejections := 0, 0
	for got := range results {
		if got.err == nil && got.changed {
			wins++
			winner = got
		} else if got.err != nil && !got.changed {
			rejections++
		} else {
			t.Errorf("unexpected race result: %+v", got)
		}
	}
	if wins != 1 || rejections != 1 {
		t.Fatalf("race outcomes: wins=%d rejections=%d", wins, rejections)
	}
	if winner.lineage.CurrentGeneration != 2 || (winner.lineage.CurrentSessionID != "session-a" && winner.lineage.CurrentSessionID != "session-b") {
		t.Fatalf("winning lineage = %+v", winner.lineage)
	}
	winningInput := inputs[0]
	if winner.lineage.CurrentSessionID == "session-b" {
		winningInput = inputs[1]
	}
	idempotent, changed, err := sessions.RotateDelegatedChild(winningInput)
	if err != nil || changed || !reflect.DeepEqual(idempotent, winner.lineage) {
		t.Fatalf("duplicate winning mutation = %+v changed=%t err=%v, want %+v", idempotent, changed, err, winner.lineage)
	}
	staleInput := inputs[0]
	if staleInput.MutationID == winningInput.MutationID {
		staleInput = inputs[1]
	}
	if _, changed, err := sessions.RotateDelegatedChild(staleInput); err == nil || changed {
		t.Fatalf("stale losing mutation: changed=%t err=%v", changed, err)
	}
	persisted, ok, err := sessions.GetDelegatedChildLineage("account-1", "logical-task-1")
	if err != nil || !ok || persisted.CurrentSessionID != winner.lineage.CurrentSessionID || persisted.Revision != winner.lineage.Revision || len(persisted.GenerationHistory) != 2 {
		t.Fatalf("persisted winning lineage = %+v ok=%t err=%v", persisted, ok, err)
	}
	owner, ok, err := sessions.GetDelegatedWorktreeOwner("account-1", generation.WorkspacePath)
	if err != nil || !ok || owner.SessionID != winner.lineage.CurrentSessionID || owner.Generation != 2 || owner.Revision != 2 {
		t.Fatalf("winning owner lease = %+v ok=%t err=%v", owner, ok, err)
	}
}
