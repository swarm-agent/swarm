package pebblestore

import (
	"encoding/json"
	"strings"
	"testing"
)

// Purpose: ProjectTaskRecord serialization protects new durable failures and
// legacy board/export presentation without mutating canonical in-memory history.
// A temporary real store proves reload and account-scoped lookup behavior.
func TestProjectMediaErrorPrivacy(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(db)
	key := "fake-retained-credential"
	tainted := "provider failed https://example.invalid/interactions?key=" + key
	task := &ProjectTaskRecord{ID: "task-privacy", ProjectID: "project-privacy", Title: "test", Status: "failed", LastError: tainted, Deliverables: []ProjectTaskDeliverable{{ID: "output", Status: "failed", Description: tainted}}}
	if err := store.PutProjectTask("account-a", task); err != nil {
		db.Close()
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
	store = NewSessionStore(db)
	got, found, err := store.GetProjectTask("account-a", task.ProjectID, task.ID)
	if err != nil || !found {
		t.Fatal("failed to reload task")
	}
	if strings.Contains(got.LastError, key) || strings.Contains(got.Deliverables[0].Description, key) || got.Status != "failed" {
		t.Fatal("unsafe durable failure")
	}
	if _, found, err := store.GetProjectTask("account-b", task.ProjectID, task.ID); err != nil || found {
		t.Fatal("cross-account access")
	}
	// Simulate decoded historical data, bypassing the new-write serializer only.
	var legacy ProjectTaskRecord
	if err := json.Unmarshal([]byte(`{"id":"legacy","status":"failed","last_error":"https://example.invalid/?key=fake-retained-credential"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	before := legacy.LastError
	encoded, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(encoded), key) || legacy.LastError != before {
		t.Fatal("legacy presentation leaked or mutated history")
	}
}
