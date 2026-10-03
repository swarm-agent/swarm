package pebblestore

import (
	"reflect"
	"testing"
)

// Purpose: adding non-triggering session feedback must not turn reopen_task into
// a second execution or approval authority. ReserveTaskFollowup is the narrowest
// atomic boundary proving stale revisions and active/review states reject with
// exactly unchanged task records.
func TestFeedbackRetainsFollowupGuards(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	for _, status := range []string{"in_progress", "planning", "rejected"} {
		t.Run(status, func(t *testing.T) {
			task := &ProjectTaskRecord{ID: status, ProjectID: "project", Title: "work", Agent: "coder", Status: status, SessionID: "retained", Revision: 7}
			if err := s.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			before, _, err := s.GetProjectTask("account", "project", status)
			if err != nil {
				t.Fatal(err)
			}
			for _, revision := range []int{6, 7} {
				if _, err := s.ReserveTaskFollowup("account", "project", status, "owner", "feedback", "note", revision, 100); err == nil {
					t.Fatal("feedback bypassed start/approval guard")
				}
				after, _, err := s.GetProjectTask("account", "project", status)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("rejection mutated task: %+v %v", after, err)
				}
			}
		})
	}
}
