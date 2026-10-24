package pebblestore

import (
	"reflect"
	"strings"
	"testing"
)

// Purpose: writable Open must settle interrupted direct media without replaying
// generation or billing. A real store close/reopen exercises the startup boundary
// and durable files, signals and receipts; missing files and non-media records
// prove recovery does not manufacture success or change unrelated task state.
func TestProjectMediaRecoveryOnOpen(t *testing.T) {
	for _, state := range []string{"ready", "accepted", "generating", "partial", "empty", "non-media", "terminal"} {
		t.Run(state, func(t *testing.T) {
			path := t.TempDir()
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			s := NewSessionStore(db)
			task := &ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", Title: "media", Agent: "video", Status: "in_progress", Deliverables: []ProjectTaskDeliverable{{ID: "file", Status: "ready", MediaURL: "data:video/mp4;base64,Ynl0ZXM="}}}
			want := "needs_review"
			switch state {
			case "accepted":
				task.Deliverables[0].Status = "accepted"
			case "generating":
				task.Deliverables[0].Status = "generating"
				task.Deliverables[0].MediaURL = ""
				want = "failed"
			case "partial":
				task.Deliverables = append(task.Deliverables, ProjectTaskDeliverable{ID: "missing", Status: "generating"})
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
				s = NewSessionStore(db)
				got, found, err := s.GetProjectTask("account", "project", "task")
				if err != nil || !found || got.Status != want {
					t.Fatalf("recovered=%+v found=%v err=%v", got, found, err)
				}
				if state == "generating" || state == "partial" {
					missing := got.Deliverables[len(got.Deliverables)-1]
					if missing.Status != "failed" || missing.MediaURL != "" {
						t.Fatalf("missing file manufactured: %+v", missing)
					}
				}
				foreign, err := NewSignalStore(db).ListAfter(0, 10, SignalFilter{Account: "other-account"})
				if err != nil || len(foreign.Signals) != 0 {
					t.Fatalf("cross-account signal: %+v %v", foreign, err)
				}
				if want == "failed" && !strings.Contains(got.LastError, "daemon restarted during generation") {
					t.Fatalf("missing restart reason: %s", got.LastError)
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
