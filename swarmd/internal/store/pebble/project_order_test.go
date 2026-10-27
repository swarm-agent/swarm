package pebblestore

import (
	"reflect"
	"testing"
)

// Purpose: SetProjectOrder/ListProjects own durable account order. This real-store
// test is the narrowest boundary proving updates cannot reshuffle rows and that
// foreign, duplicate and missing IDs cannot partially replace the saved order.
func TestProjectOrder(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	put := func(account, id string, created int64) {
		t.Helper()
		if err := s.PutProject(account, &ProjectRecord{ID: id, Name: id, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}
	assertOrder := func(want ...string) {
		t.Helper()
		records, err := s.ListProjects("account", 100)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, p := range records {
			got = append(got, p.ID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("order %v; want %v", got, want)
		}
	}
	put("account", "a", 1)
	put("account", "b", 2)
	put("other", "foreign", 3)
	assertOrder("b", "a")
	if err := s.SetProjectOrder("account", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	assertOrder("a", "b")
	put("account", "b", 2)
	assertOrder("a", "b")
	for _, ids := range [][]string{{"b", "missing"}, {"b", "foreign"}, {"b", "b"}} {
		if err := s.SetProjectOrder("account", ids); err == nil {
			t.Fatalf("accepted %v", ids)
		}
		assertOrder("a", "b")
	}
	put("account", "c", 4)
	assertOrder("c", "a", "b")
}
