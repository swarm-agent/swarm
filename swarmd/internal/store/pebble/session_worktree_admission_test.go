package pebblestore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// Purpose: first ownership admission in ApplyV3SessionMutation must require
// typed evidence, preserve source identity, and reject event-name forgery with
// no ownership/event publication. Store tests observe the atomic postconditions.
func TestWorktreeAdmissionSuccessor(t *testing.T) {
	s := NewSessionStore(openV3SessionEventTestStore(t))
	source := filepath.Join(t.TempDir(), "source")
	createRecoverySession(t, s, "owner", source)
	current, _, err := s.GetSession("owner")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "successor")
	current.WorktreeRootPath = path
	input := V3SessionMutationInput{SessionID: "owner", UserID: "user", AccountScopeID: "account", PayloadHash: "successor", IdempotencyKey: "successor", Kind: V3SessionMutationUpdateSettings, EventType: "session.worktree.adopted", Session: &current}
	if _, err := s.ApplyV3SessionMutation(input); !errors.Is(err, ErrWorktreeRecoveryConflict) {
		t.Fatalf("event-name admission: %v", err)
	}
	if _, err := s.InspectWorktreeOwnership("account", "user", []string{path}); err == nil {
		t.Fatal("partial claim")
	}
	events, err := s.ListV3SessionEvents("owner", 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("partial events: %v %v", events, err)
	}
	input.WorktreeAdmission = &WorktreeAdmissionEvidence{Kind: "allocated", Path: path, SourcePath: source, OwnerSessionID: "owner", Branch: current.WorktreeBranch}
	if _, err := s.ApplyV3SessionMutation(input); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetSession("owner")
	if err != nil || got.WorkspacePath != source || got.WorktreeRootPath != path {
		t.Fatalf("source/runtime: %+v %v", got, err)
	}
	input.WorktreeAdmission = nil
	input.IdempotencyKey, input.PayloadHash = "same-owner-update", "same-owner-update"
	if _, err := s.ApplyV3SessionMutation(input); err != nil {
		t.Fatalf("same-owner update: %v", err)
	}
}

// Purpose: JSON callers cannot manufacture the trusted in-process admission
// field. This is the narrowest layer proving the transport exclusion contract.
func TestWorktreeAdmissionNotJSONAuthority(t *testing.T) {
	var input V3SessionMutationInput
	if err := json.Unmarshal([]byte(`{"WorktreeAdmission":{"Kind":"allocated"},"worktree_admission":{"Kind":"allocated"}}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.WorktreeAdmission != nil {
		t.Fatal("decoded trusted evidence")
	}
}

// Purpose: migration may use authenticated retained same-owner history, never
// mere record absence. Cross-principal history must defeat even trusted caller
// evidence and rejection must not create a claim.
func TestWorktreeAdmissionLegacyHistory(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-owner", true: "foreign-history"}[foreign], func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			path := filepath.Join(t.TempDir(), "lane")
			createRecoverySession(t, s, "owner", path)
			current, _, err := s.GetSession("owner")
			if err != nil {
				t.Fatal(err)
			}
			// Simulate a pre-ownership-index store with durable canonical history.
			if err := s.store.db.Delete([]byte(worktreeOwnershipKey(path)), nil); err != nil {
				t.Fatal(err)
			}
			if foreign {
				other := current
				other.ID, other.UserID, other.AccountScopeID = "foreign", "foreign-user", "foreign-account"
				batch := s.store.NewBatch()
				defer batch.Close()
				if err := s.retainRepositoryHistoryInBatch(batch, other, false, false); err != nil {
					t.Fatal(err)
				}
				if err := batch.Commit(nil); err != nil {
					t.Fatal(err)
				}
			}
			input := V3SessionMutationInput{SessionID: "owner", UserID: "user", AccountScopeID: "account", PayloadHash: "legacy", IdempotencyKey: "legacy", Kind: V3SessionMutationUpdateSettings, Session: &current, WorktreeAdmission: &WorktreeAdmissionEvidence{Kind: "legacy", Path: path, SourcePath: path, OwnerSessionID: "owner", Branch: current.WorktreeBranch}}
			_, err = s.ApplyV3SessionMutation(input)
			if foreign {
				if !errors.Is(err, ErrWorktreeRecoveryConflict) {
					t.Fatalf("foreign migration: %v", err)
				}
				if _, err := s.InspectWorktreeOwnership("account", "user", []string{path}); err == nil {
					t.Fatal("partial claim")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Purpose: a reserved recovery survives a real close/reopen; stale publication
// after restart leaves the reservation and receiver unchanged, allowing repair.
func TestWorktreeAdmissionRestartReservation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	path := filepath.Join(t.TempDir(), "lane")
	createRecoverySession(t, s, "owner", path)
	createRecoverySession(t, s, "receiver", "")
	if err := s.DeleteSession("owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyV3SessionMutation(recoveryInput("receiver", "reserve", path, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	input := recoveryInput("receiver", "publish", path, 1, 2)
	if _, err := s.ApplyV3SessionMutation(input); !errors.Is(err, ErrWorktreeRecoveryConflict) {
		t.Fatalf("stale publish: %v", err)
	}
	records, err := s.InspectWorktreeOwnership("account", "user", []string{path})
	if err != nil || len(records) != 1 || records[0].Revision != 2 || records[0].ClaimantSessionID != "receiver" {
		t.Fatalf("reservation lost: %+v %v", records, err)
	}
	current, _, err := s.GetSession("receiver")
	if err != nil || current.WorktreeRootPath != "" {
		t.Fatalf("partial switch: %+v %v", current, err)
	}
}

// Purpose: explicit trusted Coder evidence permits runtime WorkspacePath only
// with exact original-source metadata; metadata alone and Finder allocations
// cannot create exclusive ownership. The store boundary observes no partial claim.
func TestWorktreeAdmissionDelegatedRuntime(t *testing.T) {
	for _, scenario := range []string{"coder", "metadata-only", "finder", "wrong-source"} {
		t.Run(scenario, func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			createRecoverySession(t, s, "seed", "")
			root := t.TempDir()
			source, runtime := filepath.Join(root, "source"), filepath.Join(root, "runtime")
			next := SessionSnapshot{ID: "child", WorkspacePath: runtime, WorktreeEnabled: true, WorktreeRootPath: runtime, WorktreeBranch: "agent/child", Metadata: map[string]interface{}{"subagent": "coder", "swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": runtime, "swarm_v3_worktree_owner_session_id": "child"}}
			e := &WorktreeAdmissionEvidence{Kind: "allocated", Path: runtime, SourcePath: source, OwnerSessionID: "child", Branch: next.WorktreeBranch, DelegatedCoder: true}
			switch scenario {
			case "metadata-only": e.DelegatedCoder = false
			case "finder": next.Metadata["subagent"] = "finder"
			case "wrong-source": next.Metadata["swarm_v3_source_workspace_path"] = runtime
			}
			_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationCreateSession, IdempotencyKey: "create", PayloadHash: "create", Session: &next, WorktreeAdmission: e})
			if scenario == "coder" {
				if err != nil { t.Fatal(err) }
			} else {
				if !errors.Is(err, ErrWorktreeRecoveryConflict) { t.Fatalf("admission: %v", err) }
				if _, err := s.InspectWorktreeOwnership("account", "user", []string{runtime}); err == nil { t.Fatal("partial claim") }
			}
		})
	}
}
