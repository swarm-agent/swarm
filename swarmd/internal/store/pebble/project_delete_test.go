package pebblestore

import (
	"errors"
	"testing"
)

// Purpose: DeleteProject's projectsMu boundary must reject every non-terminal
// task without partial removal, including unknown states, and reject admission
// after deletion. A real store is the narrowest layer proving these postconditions.
func TestProjectDeleteTasks(t *testing.T) {
	for _, status := range []string{"in_progress", "planning", "running", "queued", "needs_review", "blocked", "unknown", "completed", "failed", "cancelled"} {
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
			terminal := status == "completed" || status == "failed" || status == "cancelled"
			err = s.DeleteProject("account", "project")
			if terminal {
				if err != nil {
					t.Fatal(err)
				}
				if admitted, err := s.ReserveProjectTaskIfAbsent("account", task); admitted || !errors.Is(err, ErrProjectNotFound) {
					t.Fatalf("admitted=%v err=%v", admitted, err)
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

// Purpose: ReserveProjectTaskIfAbsent and DeleteProject must serialize on
// projectsMu: either admission wins and deletion refuses, or deletion wins and
// admission fails. Concurrent real-store calls prove no orphan is committed.
func TestProjectDeleteAdmissionRace(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	if err := s.PutProject("account", &ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	created := make(chan error, 1)
	deleted := make(chan error, 1)
	go func() {
		<-start
		_, err := s.ReserveProjectTaskIfAbsent("account", &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "finder", Status: "planning"})
		created <- err
	}()
	go func() { <-start; deleted <- s.DeleteProject("account", "project") }()
	close(start)
	createErr, deleteErr := <-created, <-deleted
	_, projectFound, err := s.GetProject("account", "project")
	if err != nil {
		t.Fatal(err)
	}
	_, taskFound, err := s.GetProjectTask("account", "project", "task")
	if err != nil {
		t.Fatal(err)
	}
	if projectFound != taskFound {
		t.Fatalf("partial state: project=%v task=%v", projectFound, taskFound)
	}
	if projectFound {
		var refusal *ProjectTasksUnsettledError
		if createErr != nil || !errors.As(deleteErr, &refusal) || refusal.Count != 1 {
			t.Fatalf("create=%v delete=%v", createErr, deleteErr)
		}
	} else if deleteErr != nil || !errors.Is(createErr, ErrProjectNotFound) {
		t.Fatalf("create=%v delete=%v", createErr, deleteErr)
	}
}
