package notification

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a person must learn when their project task (orchestrated, or the
// task a worker run executes) finishes and needs review, fails or completes;
// before this, GET /v1/notifications only ever held permission requests.
// Invariant: each committed status transition into needs_review/failed/completed
// yields exactly one category "task" notification, scoped to the task's account,
// carrying project/task/session ids. Writes that repeat a status (retries,
// replays, unrelated field updates) and non-terminal transitions yield none.
// Authority: SessionStore.persistProjectTaskMetadataLocked records the transition,
// Store.publishProjectRealtime delivers it post-commit, and
// Service.NotifyProjectTaskStatus persists the notification. The test drives the
// real store write path so it proves the wiring, not only the formatter.
func TestProjectTaskStatusTransitionsNotifyOncePerTransition(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	svc := NewService(pebblestore.NewNotificationStore(db), nil, nil)
	svc.SetLocalSwarmIDResolver(func() string { return "swarm-local" })
	var realtime []RealtimeEvent
	svc.SetRealtimePublisher(func(event RealtimeEvent) { realtime = append(realtime, event) })
	db.SetProjectTaskStatusObserver(svc.ObserveProjectTaskStatus)

	const account, foreign = "acct-task-notify", "acct-foreign"
	sessions := pebblestore.NewSessionStore(db)
	project := &pebblestore.ProjectRecord{Name: "Notify Project"}
	if err := sessions.PutProject(account, project); err != nil {
		t.Fatalf("put project: %v", err)
	}
	task := &pebblestore.ProjectTaskRecord{ProjectID: project.ID, Title: "Add login page", Agent: "coder"}
	if err := sessions.PutProjectTask(account, task); err != nil {
		t.Fatalf("put task: %v", err)
	}
	setStatus := func(status, actionNeeded string) {
		t.Helper()
		if _, err := sessions.UpdateProjectTask(account, project.ID, task.ID, func(r *pebblestore.ProjectTaskRecord) error {
			r.Status, r.ActionNeeded = status, actionNeeded
			return nil
		}); err != nil {
			t.Fatalf("update task to %s: %v", status, err)
		}
	}
	list := func(accountScopeID string) []pebblestore.NotificationRecord {
		t.Helper()
		records, err := svc.ListNotificationsForAccount(accountScopeID, "swarm-local", 100)
		if err != nil {
			t.Fatalf("list notifications: %v", err)
		}
		return records
	}

	setStatus("in_progress", "")
	if got := list(account); len(got) != 0 {
		t.Fatalf("non-terminal transition notified: %#v", got)
	}

	setStatus("needs_review", "Action Needed: review")
	// A retry/replay of the same status and an unrelated field update are not transitions.
	setStatus("needs_review", "Action Needed: review")
	setStatus("needs_review", "Action Needed: review again")
	got := list(account)
	if len(got) != 1 {
		t.Fatalf("needs_review notifications = %d, want 1: %#v", len(got), got)
	}
	review := got[0]
	if review.Category != pebblestore.NotificationCategoryTask || review.Title != "Task ready for review: Add login page" || review.Severity != pebblestore.NotificationSeverityInfo || review.Status != pebblestore.NotificationStatusActive {
		t.Fatalf("review notification = %#v", review)
	}
	if review.Payload["project_id"] != project.ID || review.Payload["task_id"] != task.ID || review.Payload["status"] != "needs_review" {
		t.Fatalf("review payload ids = %#v", review.Payload)
	}
	if len(realtime) != 1 || realtime[0].EventType != EventNotificationCreated {
		t.Fatalf("realtime events = %#v, want one created", realtime)
	}

	// Replaying the committed transition directly keeps the single record.
	stored, found, err := sessions.GetProjectTask(account, project.ID, task.ID)
	if err != nil || !found {
		t.Fatalf("get task: found=%v err=%v", found, err)
	}
	if _, changed, err := svc.NotifyProjectTaskStatus(pebblestore.ProjectTaskStatusTransition{AccountScopeID: account, PriorStatus: "in_progress", Task: *stored}); err != nil || changed {
		t.Fatalf("replayed transition changed=%v err=%v", changed, err)
	}

	setStatus("failed", "Action Needed: retry")
	got = list(account)
	if len(got) != 2 {
		t.Fatalf("after failure notifications = %d, want 2", len(got))
	}
	failedFound := false
	for _, record := range got {
		if record.Title == "Task failed: Add login page" && record.Severity == pebblestore.NotificationSeverityWarning && record.Payload["status"] == "failed" {
			failedFound = true
		}
	}
	if !failedFound {
		t.Fatalf("failed notification missing: %#v", got)
	}
	if foreignRecords := list(foreign); len(foreignRecords) != 0 {
		t.Fatalf("task notification leaked to another account: %#v", foreignRecords)
	}
}
