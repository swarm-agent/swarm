package pebblestore

import (
	"path/filepath"
	"testing"
)

// Purpose: a follow-up (message_task to a finished task, reopen) is a message
// to the same task, so the reservation keeps the task's agent and records it
// as the new attempt's role. Swarm's own session agents (swarm, coder,
// finder, designer) keep theirs; agents that cannot run a follow-up session
// themselves (media, reviewer, plan) are continued by Swarm as before.
// Threat: a read-only follow-up to a finder task turning it into a swarm
// task, which fires the workflows scoped to swarm tasks on a job with no
// commits. A refused reservation (stale revision) changes nothing.
// Authority: SessionStore.ReserveTaskFollowupWithRepositories over a real
// temporary store, the narrowest layer that owns the task record write.
func TestReserveTaskFollowupKeepsTheTasksAgent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := NewSessionStore(store)
	if err := db.PutProject("account", &ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	for agent, want := range map[string]string{
		"finder": "finder", "coder": "coder", "designer": "designer", "swarm": "swarm",
		"image": "swarm", "reviewer": "swarm", "plan": "swarm",
	} {
		id := "task_" + agent
		task := &ProjectTaskRecord{ID: id, ProjectID: "project", AccountID: "account", Title: "Look around", Agent: agent, Status: "needs_review"}
		if err := db.PutProjectTask("account", task); err != nil {
			t.Fatal(err)
		}
		stored, _, err := db.GetProjectTask("account", "project", id)
		if err != nil {
			t.Fatal(err)
		}
		// A stale revision is refused without touching the task.
		if _, err := db.ReserveTaskFollowup("account", "project", id, "user", "stale-"+id, "Where is the limit set?", stored.Revision+5, 1); err == nil {
			t.Fatalf("%s: stale reservation accepted", id)
		}
		if after, _, _ := db.GetProjectTask("account", "project", id); after.Agent != agent || after.Revision != stored.Revision || len(after.Attempts) != len(stored.Attempts) {
			t.Fatalf("%s: refused reservation changed the task: %+v", id, after)
		}
		reserved, err := db.ReserveTaskFollowup("account", "project", id, "user", "followup-"+id, "Where is the limit set?", stored.Revision, 1)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		active := reserved.ActiveAttempt()
		if reserved.Agent != want || active == nil || active.Role != want || active.ID == "initial" {
			t.Fatalf("%s: follow-up agent %q role %+v, want %q", id, reserved.Agent, active, want)
		}
		read, _, err := db.GetProjectTask("account", "project", id)
		if err != nil || read.Agent != want {
			t.Fatalf("%s: stored follow-up agent %q %v, want %q", id, read.Agent, err, want)
		}
	}
}
