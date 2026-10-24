package pebblestore_test

import (
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/notification"
	. "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: startup observer installation must settle interrupted media, including
// real durable notifications exactly once across reopen, without replaying
// generation or billing. A real store close/reopen exercises the startup boundary
// and durable files, signals and receipts; missing files and non-media records
// prove recovery does not manufacture success or change unrelated task state.
// Existing provider failures must retain their diagnostics rather than being
// falsely attributed to restart; queued, empty and unknown file states must be
// failed as interrupted. This durable test checks both reopen cycles.
func TestProjectMediaRecoveryOnOpen(t *testing.T) {
	for _, state := range []string{"ready", "accepted", "generating", "pending", "queued", "empty-status", "unknown", "partial", "ready-plus-queued", "already-failed", "all-provider-failed", "empty", "non-media", "terminal"} {
		t.Run(state, func(t *testing.T) {
			path := t.TempDir()
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			s := NewSessionStore(db)
			task := &ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", Title: "media", Agent: "video", Status: "in_progress", Deliverables: []ProjectTaskDeliverable{{ID: "file", Status: "ready", MediaURL: "data:video/mp4;base64,Ynl0ZXM="}}}
			want := "needs_review"
			interrupted := false
			switch state {
			case "accepted":
				task.Deliverables[0].Status = "accepted"
			case "generating", "pending", "queued", "empty-status", "unknown":
				interrupted = true
				task.Agent = "image"
				task.Deliverables[0].Status = state
				if state == "empty-status" {
					task.Deliverables[0].Status = ""
				}
				task.Deliverables[0].MediaURL = ""
				want = "failed"
			case "partial", "ready-plus-queued":
				interrupted = true
				status := "generating"
				if state == "ready-plus-queued" {
					task.Agent = "image"
					status = "queued"
				}
				task.Deliverables = append(task.Deliverables, ProjectTaskDeliverable{ID: "missing", Status: status})
				want = "failed"
			case "already-failed":
				task.Agent = "image"
				task.Deliverables = append(task.Deliverables, ProjectTaskDeliverable{ID: "failed", Status: "failed", Description: "Image generation failed: provider rejected request"})
			case "all-provider-failed":
				task.Deliverables = []ProjectTaskDeliverable{{ID: "failed", Status: "failed", Description: "Video generation failed: provider rejected request"}}
				want = "failed"
			case "empty":
				task.Deliverables = nil
				want = "failed"
			case "non-media":
				task.Agent = "finder"
				want = "in_progress"
			case "terminal":
				task.Status = "completed"
				want = "completed"
			}
			if err := s.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			if err := s.PutMediaUsage(SessionMediaUsageRecord{ID: "receipt", AccountScopeID: "account", UserID: "user", ProjectID: "project", TaskID: "task", Kind: "video", CostUSD: 0.25, CreatedAt: 1}); err != nil {
				t.Fatal(err)
			}
			before, err := s.ListMediaUsage("account", 10)
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				db, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
				notifications := NewNotificationStore(db)
				service := notification.NewService(notifications, nil, nil)
				service.SetLocalSwarmIDResolver(func() string { return "swarm" })
				db.SetProjectTaskStatusObserver(service.ObserveProjectTaskStatus)
				records, err := notifications.ListNotificationsForAccount("account", "swarm", 10)
				if err != nil {
					t.Fatal(err)
				}
				if state == "non-media" || state == "terminal" {
					if len(records) != 0 {
						t.Fatal("unrelated notification")
					}
				} else {
					title := "Task ready for review: media"
					if want == "failed" {
						title = "Task failed: media"
					}
					if len(records) != 1 || records[0].Title != title {
						t.Fatalf("notifications: %+v", records)
					}
				}
				s = NewSessionStore(db)
				got, found, err := s.GetProjectTask("account", "project", "task")
				if err != nil || !found || got.Status != want {
					t.Fatalf("recovered=%+v found=%v err=%v", got, found, err)
				}
				if state == "already-failed" && !reflect.DeepEqual(got.Deliverables[1], task.Deliverables[1]) {
					t.Fatalf("existing failure changed: %+v", got.Deliverables[1])
				}
				if interrupted {
					missing := got.Deliverables[len(got.Deliverables)-1]
					if missing.Status != "failed" || missing.MediaURL != "" || missing.Description != "daemon restarted during generation; file did not finish" {
						t.Fatalf("missing file manufactured: %+v", missing)
					}
				}
				foreign, err := NewSignalStore(db).ListAfter(0, 10, SignalFilter{Account: "other-account"})
				if err != nil || len(foreign.Signals) != 0 {
					t.Fatalf("cross-account signal: %+v %v", foreign, err)
				}
				if strings.Contains(got.LastError, "daemon restarted during generation") != interrupted {
					t.Fatalf("incorrect restart attribution: %s", got.LastError)
				}
				if state == "already-failed" && (got.LastError != "1 of 2 deliverables failed generation" || got.ActionNeeded != "Action Needed: 1 of 2 image variation(s) ready for review (1 failed).") {
					t.Fatalf("incorrect partial finish: %+v", got)
				}
				if state == "all-provider-failed" && (got.LastError != task.Deliverables[0].Description || !reflect.DeepEqual(got.Deliverables, task.Deliverables) || !reflect.DeepEqual(got.WhatNotDone, []string{task.Deliverables[0].Description})) {
					t.Fatalf("provider failure lost: %+v", got)
				}
				if len(task.Deliverables) > 0 && task.Deliverables[0].MediaURL != "" && got.Deliverables[0].MediaURL != task.Deliverables[0].MediaURL {
					t.Fatal("ready file changed")
				}
				after, err := s.ListMediaUsage("account", 10)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("billing changed: %+v %v", after, err)
				}
				page, err := NewSignalStore(db).ListAfter(0, 10, SignalFilter{Account: "account"})
				if err != nil {
					t.Fatal(err)
				}
				if state == "non-media" || state == "terminal" {
					if len(page.Signals) != 0 {
						t.Fatal("unrelated signal")
					}
				} else {
					kind := "media.job.succeeded"
					if want == "failed" {
						kind = "media.job.failed"
					}
					if len(page.Signals) != 1 || page.Signals[0].Kind != kind {
						t.Fatalf("signals: %+v", page.Signals)
					}
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
