package pebblestore

import (
	"encoding/json"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: task attachment indexes and environment transitions must commit
// durable invalidation identities, survive restart, and remove detached targets.
// Real store/outbox payloads are the narrowest transaction-level evidence.
func TestTaskEnvironmentEventIndexRestart(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s := NewSessionStore(db)
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "task", Status: "needs_review", SessionID: "session", Agent: "swarm", Revision: 1}
	if err := s.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	a := taskEnvironmentFixture("attachment")
	task, err = s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: 1, AttachmentID: a.ID, Attachment: &a})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dep := environments.Deployment{ID: "deployment", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", ConnectionID: "local", Name: "Review", Status: environments.DeploymentStatusReady}
	m := &environmentRealtimeMutation{accountScopeID: "account", workspaceID: "workspace"}
	if err := m.put(KeyDeploymentForAccount("account", "workspace", dep.ID), dep); err != nil {
		t.Fatal(err)
	}
	if err := db.commitEnvironmentRealtime(m); err != nil {
		t.Fatal(err)
	}
	if m.outbox == nil {
		t.Fatal("no durable outbox")
	}
	raw := m.outbox.Event.Payload
	var payload struct {
		Targets []taskEnvironmentTarget `json:"task_environment_targets"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Targets) != 1 || payload.Targets[0].TaskID != "task" || payload.Targets[0].AttachmentID != a.ID || payload.Targets[0].Revision != 1 {
		t.Fatalf("lost targets: %s", raw)
	}
	s = NewSessionStore(db)
	if _, err := s.MutateProjectTaskEnvironment("account", "project", "task", TaskEnvironmentMutation{ExpectedTaskRevision: task.Revision, ExpectedAttachmentRevision: 1, AttachmentID: a.ID}); err != nil {
		t.Fatal(err)
	}
	raw, err = db.taskEnvironmentEventPayload(m)
	if err != nil {
		t.Fatal(err)
	}
	payload.Targets = nil
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Targets) != 0 {
		t.Fatalf("detached target retained: %s", raw)
	}
}

// Purpose: LeaseStore acquisition must enforce finite review retention for every
// caller, not just task tools; deadline and receipt clamp survive wrapper restart.
// Rejection must leave no new receipt or ownership mutation.
func TestReviewLeaseDeadline(t *testing.T) {
	db := openEphemeralStore(t)
	ds := NewDeploymentStore(db)
	now := time.Now().UnixMilli()
	dep, err := ds.Save(environments.Deployment{ID: "deployment", AccountScopeID: "account", WorkspaceID: "workspace", EnvironmentID: "environment", ConnectionID: "local", Name: "Review", Status: environments.DeploymentStatusReady, Build: &environments.ImageBuildResult{}, CreatedAt: now, ReviewDeadline: now + 60000})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ds.AcquireLease(environments.DeploymentLease{AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, EnvironmentID: dep.EnvironmentID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "owner"})
	if err != nil || lease.ExpiresAt != dep.ReviewDeadline {
		t.Fatalf("unbounded receipt: %+v %v", lease, err)
	}
	dep.ReviewDeadline += 10000
	dep, err = NewDeploymentStore(db).Save(dep)
	if err != nil || dep.ReviewDeadline != lease.ExpiresAt {
		t.Fatal("save renewed review hold")
	}
	if _, err := ds.ReleaseLease("account", "workspace", lease.ID, "done"); err != nil {
		t.Fatal(err)
	}
	// Advance the persisted clock fixture, not wall time or sleep.
	dep.CreatedAt, dep.ReviewDeadline = now-120000, now-60000
	if err := db.PutJSON(KeyDeploymentForAccount("account", "workspace", dep.ID), dep); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.AcquireLease(environments.DeploymentLease{AccountScopeID: "account", WorkspaceID: "workspace", DeploymentID: dep.ID, EnvironmentID: dep.EnvironmentID, ConsumerType: environments.ConsumerTypeSession, ConsumerID: "new"}); err == nil {
		t.Fatal("expired hold acquired")
	}
	all, err := ds.Leases().List("account", "workspace", 10)
	if err != nil || len(all) != 1 || all[0].Active {
		t.Fatalf("failed acquisition mutated receipts: %+v %v", all, err)
	}
}
