package pebblestore

import (
	"bytes"
	"testing"

	"github.com/cockroachdb/pebble"
)

// Purpose: recovery uses compact summaries and isolates bad candidates rather
// than blocking startup or decoding unrelated inline media. Real corrupt bodies
// exercise GetProjectTask failures; reopen proves healthy tasks still settle and
// rejected bodies remain unchanged for repair, without false success signals.
func TestProjectMediaRecoveryIsolatesCorruptTasks(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	for _, id := range []string{"broken", "unrelated", "healthy"} {
		agent := "video"
		if id == "unrelated" {
			agent = "finder"
		}
		task := &ProjectTaskRecord{ID: id, AccountID: "account", ProjectID: "project", Agent: agent, Status: "in_progress", Title: id, Deliverables: []ProjectTaskDeliverable{{ID: "file", Status: "ready", MediaURL: "data:video/mp4;base64,Ynl0ZXM="}}}
		if err := s.PutProjectTask("account", task); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"broken", "unrelated"} {
		if err := db.db.Set([]byte(KeyProjectTask("account", "project", id)), []byte("invalid json"), pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		transitions := 0
		db.SetProjectTaskStatusObserver(func(tr ProjectTaskStatusTransition) {
			transitions++
			if tr.Task.ID != "healthy" {
				t.Fatalf("unexpected transition: %+v", tr)
			}
		})
		want := 1
		if attempt > 0 {
			want = 0
		}
		if transitions != want {
			t.Fatalf("transitions=%d want=%d", transitions, want)
		}
		s = NewSessionStore(db)
		task, found, err := s.GetProjectTask("account", "project", "healthy")
		if err != nil || !found || task.Status != "needs_review" {
			t.Fatalf("healthy task: %+v %v", task, err)
		}
		for _, id := range []string{"broken", "unrelated"} {
			value, closer, err := db.db.Get([]byte(KeyProjectTask("account", "project", id)))
			if err != nil {
				t.Fatal(err)
			}
			unchanged := bytes.Equal(value, []byte("invalid json"))
			closer.Close()
			if !unchanged {
				t.Fatal("corrupt body mutated")
			}
		}
		page, err := NewSignalStore(db).ListAfter(0, 10, SignalFilter{Account: "account"})
		if err != nil || len(page.Signals) != 1 || page.Signals[0].Refs["task_id"] != "healthy" {
			t.Fatalf("signals: %+v %v", page, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
