package pebblestore

import (
	"errors"
	"testing"
)

// Purpose: DeleteProject's projectsMu boundary must refuse while a task is
// running or about to run, without partial removal, and remove the project and
// its tasks otherwise. A real store is the narrowest layer proving this.
func TestProjectDeleteTasks(t *testing.T) {
	for _, status := range []string{"in_progress", "planning", "running", "queued", "pending", "generating", "needs_review", "pending_approval", "accepted", "rejected", "blocked", "completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			db, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := NewSessionStore(db)
			if err := s.PutProject("account", &ProjectRecord{ID: "project", Name: "Project"}); err != nil {
				t.Fatal(err)
			}
			task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "finder", Status: status}
			if err := s.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			terminal := !map[string]bool{"in_progress": true, "planning": true, "running": true, "queued": true, "pending": true, "generating": true}[status]
			err = s.DeleteProject("account", "project")
			if terminal {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var refusal *ProjectTasksUnsettledError
				if !errors.As(err, &refusal) || refusal.Count != 1 {
					t.Fatalf("refusal: %v", err)
				}
			}
			if _, found, err := s.GetProject("account", "project"); err != nil || found == terminal {
				t.Fatalf("project found=%v err=%v", found, err)
			}
			if _, found, err := s.GetProjectTask("account", "project", "task"); err != nil || found == terminal {
				t.Fatalf("task found=%v err=%v", found, err)
			}
		})
	}
}
