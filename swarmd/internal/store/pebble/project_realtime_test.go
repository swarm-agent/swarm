package pebblestore

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestProjectRealtime_StoreAtomicBatchOutbox(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: Every mutation to projects and project tasks (PutProject,
	//   UpdateProject, DeleteProject, PutProjectTask, UpdateProjectTask, DeleteProjectTask)
	//   must commit an atomic V3RealtimeOutboxRecord in the exact same Pebble batch as
	//   the domain data, guaranteeing zero emit-after-write loss window.
	// - Boundary/authority: projectRealtimeMutation, commitProjectRealtime, and SessionStore methods
	//   in swarmd/internal/store/pebble/project_realtime.go and project_store.go.
	// - Threat/regression: Lost invalidation events causing frontend stale state, polling fallbacks,
	//   or cross-account event pollution.

	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	sessionStore := NewSessionStore(db)
	accountID := "acct_test_proj_realtime"
	foreignAccountID := "acct_foreign"

	var published []V3RealtimeOutboxRecord
	var pubMu sync.Mutex
	wakes := 0
	db.SetProjectPublisher(func(rec V3RealtimeOutboxRecord) {
		wakes++
		// Requirement: post-commit callbacks run after releasing domain and canonical mutation locks.
		// TryLock proves release without leaving a hung goroutine on regression.
		if !db.projectsMu.TryLock() {
			t.Fatal("publisher holds projectsMu lock")
		}
		db.projectsMu.Unlock()

		// Requirement: synthetic session is not exposed as a real user session.
		if _, found, err := sessionStore.GetSession(rec.SessionID); err != nil || found {
			t.Fatalf("synthetic session exposed as real session: %v %v", found, err)
		}

		// Reentrancy: verify domain read authority can be safely called from callback without deadlock
		if rec.AccountScopeID != "" {
			_, _, _ = sessionStore.GetProject(rec.AccountScopeID, "reentrancy_probe")
		}

		pubMu.Lock()
		defer pubMu.Unlock()
		published = append(published, rec)
	})

	// 1. PutProject commits domain data and outbox atomically
	proj := &ProjectRecord{
		Name:        "Realtime Project",
		Description: "Testing atomic batch outbox",
	}
	if err := sessionStore.PutProject(accountID, proj); err != nil {
		t.Fatalf("PutProject failed: %v", err)
	}
	if proj.ID == "" {
		t.Fatal("expected project ID generated")
	}

	pubMu.Lock()
	if len(published) != 1 {
		t.Fatalf("expected 1 published outbox record, got %d", len(published))
	}
	rec1 := published[0]
	pubMu.Unlock()

	if rec1.AccountScopeID != accountID {
		t.Fatalf("expected outbox account %q, got %q", accountID, rec1.AccountScopeID)
	}
	expectedSessionID := "__project__:" + strings.ToLower(accountID) + ":" + strings.ToLower(proj.ID)
	if rec1.SessionID != expectedSessionID {
		t.Fatalf("expected session ID %q, got %q", expectedSessionID, rec1.SessionID)
	}
	if rec1.Event.EventType != ProjectUpdatedEventType {
		t.Fatalf("expected event type %q, got %q", ProjectUpdatedEventType, rec1.Event.EventType)
	}
	var payload1 map[string]any
	if err := json.Unmarshal(rec1.Event.Payload, &payload1); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload1["project_id"] != proj.ID || payload1["action"] != "project_created" {
		t.Fatalf("unexpected payload1: %+v", payload1)
	}

	// 2. UpdateProject emits outbox record with action project_updated
	_, err = sessionStore.UpdateProject(accountID, proj.ID, func(p *ProjectRecord) error {
		p.Description = "Updated description"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateProject failed: %v", err)
	}

	pubMu.Lock()
	if len(published) != 2 {
		t.Fatalf("expected 2 published records, got %d", len(published))
	}
	rec2 := published[1]
	pubMu.Unlock()

	var payload2 map[string]any
	_ = json.Unmarshal(rec2.Event.Payload, &payload2)
	if payload2["action"] != "project_updated" || payload2["project_id"] != proj.ID {
		t.Fatalf("unexpected payload2: %+v", payload2)
	}

	// 3. PutProjectTask emits outbox record with action task_created
	task := &ProjectTaskRecord{
		ProjectID: proj.ID,
		Title:     "Initial Task",
		Agent:     "coder",
	}
	if err := sessionStore.PutProjectTask(accountID, task); err != nil {
		t.Fatalf("PutProjectTask failed: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected task ID generated")
	}

	pubMu.Lock()
	if len(published) != 3 {
		t.Fatalf("expected 3 published records, got %d", len(published))
	}
	rec3 := published[2]
	pubMu.Unlock()

	var payload3 map[string]any
	_ = json.Unmarshal(rec3.Event.Payload, &payload3)
	if payload3["action"] != "task_created" || payload3["project_id"] != proj.ID || payload3["task_id"] != task.ID {
		t.Fatalf("unexpected payload3: %+v", payload3)
	}

	// 4. UpdateProjectTask emits outbox record with action task_updated
	_, err = sessionStore.UpdateProjectTask(accountID, proj.ID, task.ID, func(t *ProjectTaskRecord) error {
		t.Status = "needs_review"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateProjectTask failed: %v", err)
	}

	pubMu.Lock()
	if len(published) != 4 {
		t.Fatalf("expected 4 published records, got %d", len(published))
	}
	rec4 := published[3]
	pubMu.Unlock()

	var payload4 map[string]any
	_ = json.Unmarshal(rec4.Event.Payload, &payload4)
	if payload4["action"] != "task_updated" || payload4["task_id"] != task.ID {
		t.Fatalf("unexpected payload4: %+v", payload4)
	}

	// 5. DeleteProjectTask emits outbox record with action task_deleted
	if err := sessionStore.DeleteProjectTask(accountID, proj.ID, task.ID); err != nil {
		t.Fatalf("DeleteProjectTask failed: %v", err)
	}

	pubMu.Lock()
	if len(published) != 5 {
		t.Fatalf("expected 5 published records, got %d", len(published))
	}
	rec5 := published[4]
	pubMu.Unlock()

	var payload5 map[string]any
	_ = json.Unmarshal(rec5.Event.Payload, &payload5)
	if payload5["action"] != "task_deleted" || payload5["task_id"] != task.ID {
		t.Fatalf("unexpected payload5: %+v", payload5)
	}

	// 6. DeleteProject emits outbox record with action project_deleted
	if err := sessionStore.DeleteProject(accountID, proj.ID); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
	}

	pubMu.Lock()
	if len(published) != 6 {
		t.Fatalf("expected 6 published records, got %d", len(published))
	}
	rec6 := published[5]
	pubMu.Unlock()

	var payload6 map[string]any
	_ = json.Unmarshal(rec6.Event.Payload, &payload6)
	if payload6["action"] != "project_deleted" || payload6["project_id"] != proj.ID {
		t.Fatalf("unexpected payload6: %+v", payload6)
	}

	// 7. Durable replay verification: ListV3RealtimeOutboxForAuthScopeAfter returns all 6 records
	replayed, err := sessionStore.ListV3RealtimeOutboxForAuthScopeAfter(accountID, "desktop", 0, 100)
	if err != nil {
		t.Fatalf("replay outbox failed: %v", err)
	}
	if len(replayed) != 6 {
		t.Fatalf("expected 6 replayed records for account, got %d", len(replayed))
	}
	for i, r := range replayed {
		if r.EndpointSeq != published[i].EndpointSeq {
			t.Fatalf("replay sequence mismatch at %d: got %d, want %d", i, r.EndpointSeq, published[i].EndpointSeq)
		}
	}

	// 8. Account isolation verification: foreign account sees 0 records
	foreignReplayed, err := sessionStore.ListV3RealtimeOutboxForAuthScopeAfter(foreignAccountID, "desktop", 0, 100)
	if err != nil {
		t.Fatalf("replay foreign outbox failed: %v", err)
	}
	if len(foreignReplayed) != 0 {
		t.Fatalf("expected 0 replayed records for foreign account, got %d", len(foreignReplayed))
	}
	if wakes != 6 {
		t.Fatalf("expected 6 wakes, got %d", wakes)
	}

	// 9. Database reopen replay: outbox records survive restart and replay cleanly
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen Open failed: %v", err)
	}
	defer db.Close()
	sessionStore = NewSessionStore(db)

	replayedAfterReopen, err := sessionStore.ListV3RealtimeOutboxForAuthScopeAfter(accountID, "desktop", 0, 100)
	if err != nil {
		t.Fatalf("reopened replay outbox failed: %v", err)
	}
	if len(replayedAfterReopen) != 6 {
		t.Fatalf("expected 6 replayed records after reopen, got %d", len(replayedAfterReopen))
	}
	for i, r := range replayedAfterReopen {
		if r.EndpointSeq != published[i].EndpointSeq {
			t.Fatalf("reopen sequence mismatch at %d: got %d, want %d", i, r.EndpointSeq, published[i].EndpointSeq)
		}
	}
	foreignReopened, err := sessionStore.ListV3RealtimeOutboxForAuthScopeAfter(foreignAccountID, "desktop", 0, 100)
	if err != nil {
		t.Fatalf("foreign replay after reopen failed: %v", err)
	}
	if len(foreignReopened) != 0 {
		t.Fatalf("expected 0 replayed records for foreign account after reopen, got %d", len(foreignReopened))
	}
}

func TestProjectRealtime_NegativeFailures(t *testing.T) {
	// Written Purpose:
	// - Product Requirement: setProjectRealtimeMutationInBatch must reject invalid mutations,
	//   mismatched account IDs, empty payload/keys, or disallowed key prefixes.
	// - Boundary/authority: setProjectRealtimeMutationInBatch and isAllowedProjectKey in project_realtime.go.
	// - Threat/regression: Rogue writes to arbitrary Pebble keys or cross-account write injection.

	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	batch := db.db.NewBatch()
	defer batch.Close()

	// Negative 1: nil mutation
	if err := setProjectRealtimeMutationInBatch(batch, "acct1", nil); err != ErrProjectInvalid {
		t.Fatalf("expected ErrProjectInvalid on nil, got %v", err)
	}

	// Negative 2: account mismatch
	mutMismatched := &projectRealtimeMutation{
		accountScopeID: "acct_other",
		projectID:      "proj1",
		writes:         map[string][]byte{"k": []byte(`{}`)},
	}
	if err := setProjectRealtimeMutationInBatch(batch, "acct1", mutMismatched); err != ErrProjectInvalid {
		t.Fatalf("expected ErrProjectInvalid on account mismatch, got %v", err)
	}

	// Negative 3: empty project ID
	mutEmptyProj := &projectRealtimeMutation{
		accountScopeID: "acct1",
		projectID:      "",
		writes:         map[string][]byte{"k": []byte(`{}`)},
	}
	if err := setProjectRealtimeMutationInBatch(batch, "acct1", mutEmptyProj); err != ErrProjectInvalid {
		t.Fatalf("expected ErrProjectInvalid on empty project ID, got %v", err)
	}

	// Negative 4: empty writes and deletes
	mutEmpty := &projectRealtimeMutation{
		accountScopeID: "acct1",
		projectID:      "proj1",
	}
	if err := setProjectRealtimeMutationInBatch(batch, "acct1", mutEmpty); err != ErrProjectInvalid {
		t.Fatalf("expected ErrProjectInvalid on empty writes/deletes, got %v", err)
	}

	// Negative 5: disallowed key prefix (e.g. attempting to overwrite session or system keys)
	mutDisallowed := &projectRealtimeMutation{
		accountScopeID: "acct1",
		projectID:      "proj1",
		writes: map[string][]byte{
			"session/by_id/some_session": []byte(`{}`),
		},
	}
	if err := setProjectRealtimeMutationInBatch(batch, "acct1", mutDisallowed); err != ErrProjectInvalid {
		t.Fatalf("expected ErrProjectInvalid on disallowed key, got %v", err)
	}

	// Negative 6: foreign account key path injection
	mutForeignPath := &projectRealtimeMutation{
		accountScopeID: "acct1",
		projectID:      "proj1",
		writes: map[string][]byte{
			KeyProject("acct_victim", "proj_victim"): []byte(`{"id":"proj_victim","name":"hacked"}`),
		},
	}
	if err := setProjectRealtimeMutationInBatch(batch, "acct1", mutForeignPath); err != ErrProjectInvalid {
		t.Fatalf("expected ErrProjectInvalid on foreign key path injection, got %v", err)
	}

	sessionStore := NewSessionStore(db)

	// Negative 7: commitProjectRealtime failure rollback and no persisted partial state
	mutBatchFail := &projectRealtimeMutation{
		accountScopeID: "acct1",
		projectID:      "proj_fail",
		writes: map[string][]byte{
			KeyProject("acct1", "proj_fail"): []byte(`{"id":"proj_fail","name":"partial"}`),
			"unauthorized/system/key":        []byte(`{}`),
		},
	}
	if err := db.commitProjectRealtime(mutBatchFail); err == nil {
		t.Fatal("expected commitProjectRealtime to reject mutation with disallowed key")
	}
	if mutBatchFail.outbox != nil {
		t.Fatal("rejected mutation must not retain outbox record")
	}
	// Assert no persisted partial state in Pebble
	var leakedProj any
	if found, err := db.GetJSON(KeyProject("acct1", "proj_fail"), &leakedProj); err != nil || found {
		t.Fatalf("partial project leaked into store after failed batch: found=%v", found)
	}
	var leakedDisallowed any
	if found, err := db.GetJSON("unauthorized/system/key", &leakedDisallowed); err != nil || found {
		t.Fatalf("disallowed key leaked into store after failed batch: found=%v", found)
	}

	// Negative 8: PutProject and PutProjectTask validate domain constraints and persist no partial state
	badTask := &ProjectTaskRecord{
		ProjectID: "",
		Title:     "Invalid task without project",
	}
	if err := sessionStore.PutProjectTask("acct1", badTask); err == nil {
		t.Fatal("expected PutProjectTask to fail on empty project ID")
	}
	if badTask.ID != "" {
		if _, found, _ := sessionStore.GetProjectTask("acct1", "", badTask.ID); found {
			t.Fatal("partial task persisted after validation failure")
		}
	}

	badProj := &ProjectRecord{
		ID:   "",
		Name: "",
	}
	if err := sessionStore.PutProject("acct1", badProj); err == nil {
		t.Fatal("expected PutProject to fail on empty name")
	}
	if badProj.ID != "" {
		if _, found, _ := sessionStore.GetProject("acct1", badProj.ID); found {
			t.Fatal("partial project persisted after validation failure")
		}
	}
}
