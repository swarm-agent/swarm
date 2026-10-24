package jobautomation

import (
	"errors"
	"testing"
)

// Purpose: Store.PutChange/DeleteChange must atomically retain account-scoped
// snapshots through deletion, skip no-ops, and bound retention. Real Pebble
// store tests are the narrowest layer proving durable records and unchanged
// state on cross-account reads, rather than just response status.
func TestWorkflowHistoryLifecycleAndCap(t *testing.T) {
	db, s := openTestStore(t)
	a, _, err := s.Put("a", testAutomation(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	first := a
	a.Retries = 1
	a, _, err = s.PutChange("a", a, Change{Source: "import", Who: "key-test"})
	if err != nil {
		t.Fatal(err)
	}
	a, _, err = s.Put("a", a)
	if err != nil || a.Revision != 2 {
		t.Fatalf("noop: %+v %v", a, err)
	}
	if err = s.Delete("a", a.ID); err != nil {
		t.Fatal(err)
	}
	s = NewStore(db) // no in-memory history authority
	h, err := s.History("a", "", a.Name)
	if err != nil || len(h) != 3 || !h[0].Deleted || h[1].Who != "key-test" || h[1].Source != "import" || h[1].ChangedFields[0] != "retries" {
		t.Fatalf("history: %+v %v", h, err)
	}
	for _, lookup := range []struct{ id, name string }{{a.ID, ""}, {"", a.Name}} {
		if _, err = s.History("b", lookup.id, lookup.name); !errors.Is(err, ErrNotFound) {
			t.Fatal("cross-account history", err)
		}
	}
	restored, created, err := s.PutChange("a", *h[2].Definition, Change{Source: "restore", Who: "owner"})
	if err != nil || !created || restored.ID != first.ID || restored.Revision != 4 || len(changedFields(&restored, &first)) != 0 {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	h, err = s.History("a", a.ID, "")
	if err != nil || h[0].Kind != "restored" || h[3].Revision != 1 {
		t.Fatalf("restored history: %+v %v", h, err)
	}
	for i := 0; i < 105; i++ {
		restored.Enabled = !restored.Enabled
		restored, _, err = s.Put("a", restored)
		if err != nil {
			t.Fatal(err)
		}
	}
	h, err = s.History("a", a.ID, "")
	if err != nil || len(h) != 100 || h[0].Revision != 109 || h[99].Revision != 10 {
		t.Fatalf("cap: %d %v", len(h), err)
	}
}

// Purpose: Store.PutChange must compare persisted definitions canonically so
// omitted empty step lists do not consume revisions or evict real history.
// Real Pebble round trips are the narrowest layer proving the no-op decision
// against decoded snapshots; a subsequent real change must still be recorded.
func TestWorkflowHistoryEmptyStepListsAreNoOp(t *testing.T) {
	for _, field := range []string{"args", "if", "what", "all"} {
		t.Run(field, func(t *testing.T) {
			db, store := openTestStore(t)
			input := testAutomation(t.TempDir())
			if field == "args" || field == "all" {
				input.Steps[0].Args = []string{}
			}
			if field == "if" || field == "all" {
				input.Steps[0].If = []Condition{}
			}
			if field == "what" || field == "all" {
				input.Steps[0].What = []string{}
			}
			first, _, err := store.Put("a", input)
			if err != nil {
				t.Fatal(err)
			}
			store = NewStore(db)
			saved, err := store.Get("a", first.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Steps[0].Args != nil || saved.Steps[0].If != nil || saved.Steps[0].What != nil {
				t.Fatal("expected omitted lists to decode as nil")
			}
			repeated, created, err := store.Put("a", input)
			if err != nil || created || repeated.Revision != first.Revision {
				t.Fatalf("repeat: %+v %v %v", repeated, created, err)
			}
			history, err := store.History("a", first.ID, "")
			if err != nil || len(history) != 1 {
				t.Fatalf("no-op history: %+v %v", history, err)
			}
			input.Steps[0].Args = []string{"--check"}
			updated, _, err := store.Put("a", input)
			if err != nil || updated.Revision != first.Revision+1 {
				t.Fatalf("update: %+v %v", updated, err)
			}
			history, err = store.History("a", first.ID, "")
			if err != nil || len(history) != 2 || len(history[0].ChangedFields) != 1 || history[0].ChangedFields[0] != "steps" {
				t.Fatalf("real change history: %+v %v", history, err)
			}
		})
	}
}
