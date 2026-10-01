package pebblestore

import (
	"testing"
)

func TestDeliverableStoreCRUD(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	sessionStore := NewSessionStore(db)
	accountID := "acct_test_mailbox"

	// 1. Put deliverable
	deliv1 := &DeliverableRecord{
		Title:       "Launch Thread (3 Tweets)",
		Kind:        "social_post",
		WorkerID:    "worker_social_bot",
		WorkspaceID: "ws_social",
		Status:      "pending_review",
		Payload: map[string]any{
			"posts": []any{
				map[string]any{"text": "1/3 Introducing Swarm V3..."},
				map[string]any{"text": "2/3 Local-first background workers..."},
				map[string]any{"text": "3/3 Built-in Agent Mailbox for approvals."},
			},
		},
		ActionContract: &DeliverableActionContract{
			Action: "publish_x_post",
			Parameters: map[string]any{
				"channel": "twitter",
			},
		},
	}

	if err := sessionStore.PutDeliverable(accountID, deliv1); err != nil {
		t.Fatalf("failed to put deliverable: %v", err)
	}
	if deliv1.ID == "" {
		t.Fatalf("expected deliverable ID to be generated")
	}

	// Put second deliverable (alert kind)
	deliv2 := &DeliverableRecord{
		Title:       "CI Test Failure in Dev Branch",
		Kind:        "alert",
		WorkerID:    "worker_ci_sentinel",
		WorkspaceID: "ws_dev",
		Status:      "pending_review",
		Summary:     "Test timed out on commit abc123",
	}
	if err := sessionStore.PutDeliverable(accountID, deliv2); err != nil {
		t.Fatalf("failed to put second deliverable: %v", err)
	}

	// 2. Get deliverable
	fetched, found, err := sessionStore.GetDeliverable(accountID, deliv1.ID)
	if err != nil || !found {
		t.Fatalf("failed to get deliverable: %v, found: %v", err, found)
	}
	if fetched.Title != deliv1.Title {
		t.Fatalf("expected title %q, got %q", deliv1.Title, fetched.Title)
	}
	if fetched.ActionContract == nil || fetched.ActionContract.Action != "publish_x_post" {
		t.Fatalf("expected ActionContract publish_x_post, got %+v", fetched.ActionContract)
	}

	// 3. List all deliverables
	all, err := sessionStore.ListDeliverables(accountID, DeliverableFilter{})
	if err != nil {
		t.Fatalf("failed to list deliverables: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 deliverables, got %d", len(all))
	}

	// 4. List filtered by worker
	byWorker, err := sessionStore.ListDeliverables(accountID, DeliverableFilter{
		WorkerID: "worker_social_bot",
	})
	if err != nil {
		t.Fatalf("failed to list by worker: %v", err)
	}
	if len(byWorker) != 1 || byWorker[0].ID != deliv1.ID {
		t.Fatalf("expected 1 deliverable for worker_social_bot, got %d", len(byWorker))
	}

	// 5. List filtered by status
	byStatus, err := sessionStore.ListDeliverables(accountID, DeliverableFilter{
		Status: "pending_review",
	})
	if err != nil {
		t.Fatalf("failed to list by status: %v", err)
	}
	if len(byStatus) != 2 {
		t.Fatalf("expected 2 pending deliverables, got %d", len(byStatus))
	}

	// 6. Update deliverable status to approved
	updated, err := sessionStore.UpdateDeliverableStatus(accountID, deliv1.ID, "approved", "user_roy", map[string]any{
		"publication_id": "tweet_123456789",
	})
	if err != nil {
		t.Fatalf("failed to update status: %v", err)
	}
	if updated.Status != "approved" {
		t.Fatalf("expected status approved, got %s", updated.Status)
	}
	if updated.ReviewedBy != "user_roy" {
		t.Fatalf("expected reviewed by user_roy, got %s", updated.ReviewedBy)
	}

	// 7. Verify index updated: pending_review should now have 1, approved should have 1
	pendingList, err := sessionStore.ListDeliverables(accountID, DeliverableFilter{
		Status: "pending_review",
	})
	if err != nil {
		t.Fatalf("failed to list pending: %v", err)
	}
	if len(pendingList) != 1 || pendingList[0].ID != deliv2.ID {
		t.Fatalf("expected 1 pending deliverable (deliv2), got %d", len(pendingList))
	}

	approvedList, err := sessionStore.ListDeliverables(accountID, DeliverableFilter{
		Status: "approved",
	})
	if err != nil {
		t.Fatalf("failed to list approved: %v", err)
	}
	if len(approvedList) != 1 || approvedList[0].ID != deliv1.ID {
		t.Fatalf("expected 1 approved deliverable (deliv1), got %d", len(approvedList))
	}

	// 8. Test RequestChangesDeliverable
	revised, err := sessionStore.RequestChangesDeliverable(accountID, deliv1.ID, "Please tighten the punchline in tweet 2", []string{"Tone", "Length"}, "reviewer_alice")
	if err != nil {
		t.Fatalf("failed to request changes: %v", err)
	}
	if revised.Status != "needs_revision" {
		t.Fatalf("expected status needs_revision, got %s", revised.Status)
	}
	if revised.RevisionFeedback == nil {
		t.Fatalf("expected RevisionFeedback to be populated")
	}
	if revised.RevisionFeedback.Notes != "Please tighten the punchline in tweet 2" {
		t.Fatalf("expected notes to match, got %q", revised.RevisionFeedback.Notes)
	}
	if len(revised.RevisionFeedback.Tags) != 2 || revised.RevisionFeedback.Tags[0] != "Tone" {
		t.Fatalf("expected tags [Tone Length], got %+v", revised.RevisionFeedback.Tags)
	}
	if len(revised.RevisionHistory) != 1 {
		t.Fatalf("expected revision history len 1, got %d", len(revised.RevisionHistory))
	}

	// Verify status index for needs_revision
	needsRevList, err := sessionStore.ListDeliverables(accountID, DeliverableFilter{
		Status: "needs_revision",
	})
	if err != nil {
		t.Fatalf("failed to list needs_revision: %v", err)
	}
	if len(needsRevList) != 1 || needsRevList[0].ID != deliv1.ID {
		t.Fatalf("expected 1 deliverable in needs_revision, got %d", len(needsRevList))
	}

	// 9. Delete deliverable
	if err := sessionStore.DeleteDeliverable(accountID, deliv2.ID); err != nil {
		t.Fatalf("failed to delete deliverable: %v", err)
	}
	remaining, err := sessionStore.ListDeliverables(accountID, DeliverableFilter{})
	if err != nil {
		t.Fatalf("failed to list remaining: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != deliv1.ID {
		t.Fatalf("expected 1 remaining deliverable, got %d", len(remaining))
	}
}
