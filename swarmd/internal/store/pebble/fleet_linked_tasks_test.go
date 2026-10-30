package pebblestore

import (
	"path/filepath"
	"testing"
)

// Purpose: the home's linked-task record is the durable source for listing
// tasks a session/project sent to a linked machine. The store must keep the
// sender immutable across retried sends, update only records it holds from
// observations (a read never invents a record), and keep accounts apart in
// both indexes. FleetStore is the owning boundary; a temp Pebble store is the
// narrowest layer that proves key/index behaviour.
func TestFleetLinkedTaskStore(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "linked"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fleet := NewFleetStore(store)

	if _, err := fleet.PutLinkedTask(FleetLinkedTask{AccountScopeID: "a", Machine: "box", ProjectID: "p", TaskID: "t"}); err == nil {
		t.Fatal("record without sending session accepted")
	}
	first, err := fleet.PutLinkedTask(FleetLinkedTask{AccountScopeID: "a", Machine: "box", ProjectID: "p", TaskID: "t1", Title: "One", HomeSessionID: "s1", HomeProjectID: "hp", Status: "running", CreatedAt: 100})
	if err != nil || first.CreatedAt != 100 {
		t.Fatalf("put: %+v %v", first, err)
	}
	if _, err := fleet.PutLinkedTask(FleetLinkedTask{AccountScopeID: "a", Machine: "box", ProjectID: "p", TaskID: "t2", Title: "Two", HomeSessionID: "s1", HomeProjectID: "hp", CreatedAt: 200}); err != nil {
		t.Fatal(err)
	}
	// A retried send from another session keeps the original sender.
	again, err := fleet.PutLinkedTask(FleetLinkedTask{AccountScopeID: "a", Machine: "box", ProjectID: "p", TaskID: "t1", HomeSessionID: "s-other", HomeProjectID: "other", Status: "queued", CreatedAt: 999})
	if err != nil || again.HomeSessionID != "s1" || again.HomeProjectID != "hp" || again.CreatedAt != 100 {
		t.Fatalf("retried send changed sender: %+v %v", again, err)
	}
	if rows, err := fleet.ListLinkedTasksForSession("a", "s-other"); err != nil || len(rows) != 0 {
		t.Fatalf("retried send indexed another session: %+v %v", rows, err)
	}
	// Another account's identical ids are separate records.
	if _, err := fleet.PutLinkedTask(FleetLinkedTask{AccountScopeID: "b", Machine: "box", ProjectID: "p", TaskID: "t1", HomeSessionID: "s1", HomeProjectID: "hp"}); err != nil {
		t.Fatal(err)
	}

	updated, err := fleet.ObserveLinkedTask("a", "box", "p", FleetLinkedTaskObservation{TaskID: "t1", Status: "needs_review", Progress: "done", RemoteSessionID: "rs"})
	if err != nil || !updated {
		t.Fatalf("observe: %v %v", updated, err)
	}
	if created, err := fleet.ObserveLinkedTask("a", "box", "p", FleetLinkedTaskObservation{TaskID: "unknown", Status: "running"}); err != nil || created {
		t.Fatalf("observation invented a record: %v %v", created, err)
	}
	if _, found, _ := fleet.GetLinkedTask("a", "box", "p", "unknown"); found {
		t.Fatal("observation invented a record")
	}
	if _, err := fleet.ObserveLinkedTask("a", "box", "p", FleetLinkedTaskObservation{TaskID: "t2", Missing: true}); err != nil {
		t.Fatal(err)
	}

	rows, err := fleet.ListLinkedTasksForSession("a", "s1")
	if err != nil || len(rows) != 2 || rows[0].TaskID != "t2" || rows[1].TaskID != "t1" {
		t.Fatalf("session list (newest first): %+v %v", rows, err)
	}
	if rows[1].Status != "needs_review" || rows[1].Progress != "done" || rows[1].RemoteSessionID != "rs" || rows[1].Title != "One" {
		t.Fatalf("observation not kept: %+v", rows[1])
	}
	if !rows[0].Missing || rows[0].Title != "Two" {
		t.Fatalf("missing not kept: %+v", rows[0])
	}
	if project, err := fleet.ListLinkedTasksForProject("a", "hp"); err != nil || len(project) != 2 {
		t.Fatalf("project list: %+v %v", project, err)
	}
	if other, err := fleet.ListLinkedTasksForProject("b", "hp"); err != nil || len(other) != 1 || other[0].Status != "" {
		t.Fatalf("account b saw account a's state: %+v %v", other, err)
	}
	if none, err := fleet.ListLinkedTasksForSession("c", "s1"); err != nil || len(none) != 0 {
		t.Fatalf("unrelated account listed records: %+v %v", none, err)
	}
}

// Purpose: a task a Swarm Control (MCP) caller sends to a linked machine is
// recorded with that caller as its sender (HomeSender, its AI key) instead
// of a session: exactly one sender, listed by its own index and never under
// a session or another account, and kept across a retried send. Threat: a
// record with no sender or two, a fake session, or one caller listing
// another's sends. Boundary: FleetStore PutLinkedTask and
// ListLinkedTasksForSender over a temp Pebble store.
func TestFleetLinkedTaskSenderIsSessionOrControlCaller(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "linked"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fleet := NewFleetStore(store)
	for _, bad := range []FleetLinkedTask{
		{AccountScopeID: "a", Machine: "box", ProjectID: "p", TaskID: "t"},
		{AccountScopeID: "a", Machine: "box", ProjectID: "p", TaskID: "t", HomeSessionID: "s1", HomeSender: "key:k1"},
	} {
		if _, err := fleet.PutLinkedTask(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
		if _, found, _ := fleet.GetLinkedTask("a", "box", "p", "t"); found {
			t.Fatal("a refused record was stored")
		}
	}
	sent, err := fleet.PutLinkedTask(FleetLinkedTask{AccountScopeID: "a", Machine: "box", ProjectID: "p", TaskID: "t1", HomeSender: "key:k1", CreatedAt: 100})
	if err != nil || sent.HomeSender != "key:k1" || sent.HomeSessionID != "" {
		t.Fatalf("put: %+v %v", sent, err)
	}
	if _, err := fleet.PutLinkedTask(FleetLinkedTask{AccountScopeID: "b", Machine: "box", ProjectID: "p", TaskID: "t9", HomeSender: "key:k1"}); err != nil {
		t.Fatal(err)
	}
	// A retried send by another caller keeps the first sender.
	again, err := fleet.PutLinkedTask(FleetLinkedTask{AccountScopeID: "a", Machine: "box", ProjectID: "p", TaskID: "t1", HomeSender: "key:k2"})
	if err != nil || again.HomeSender != "key:k1" {
		t.Fatalf("retried send changed sender: %+v %v", again, err)
	}
	if rows, err := fleet.ListLinkedTasksForSender("a", "key:k1"); err != nil || len(rows) != 1 || rows[0].TaskID != "t1" {
		t.Fatalf("sender list: %+v %v", rows, err)
	}
	for _, other := range []struct{ account, sender string }{{"a", "key:k2"}, {"b", "key:k2"}} {
		if rows, err := fleet.ListLinkedTasksForSender(other.account, other.sender); err != nil || len(rows) != 0 {
			t.Fatalf("%s/%s lists %+v %v", other.account, other.sender, rows, err)
		}
	}
	if rows, err := fleet.ListLinkedTasksForSession("a", ""); err != nil || len(rows) != 0 {
		t.Fatalf("a Swarm Control send is listed under a session: %+v %v", rows, err)
	}
	if _, err := fleet.ListLinkedTasksForSender("a", " "); err == nil {
		t.Fatal("an empty sender listed records")
	}
}
