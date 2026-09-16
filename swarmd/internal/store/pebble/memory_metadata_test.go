package pebblestore

import "testing"

// Purpose: exercise prepareMutation at the durable store boundary. Prevent
// forged origin, invalid purpose and foreign source writes from changing any
// live/history state; prove purpose is independent of trust kind and origin.
func TestMemoryMetadataRoundTripAndRejection(t *testing.T) {
	_, s, d := memoryTest(t)
	m := memoryPut(d, "context", "learned", "Operational fact explicitly supplied")
	m.Entry.Purpose = "operational_context"
	m.Entry.Subject = "Build environment"
	d = memoryApply(t, s, m)
	e := d.Entries[0]
	if e.Origin != "user" || e.Purpose != "operational_context" || e.Subject != "Build environment" {
		t.Fatalf("metadata lost: %+v", e)
	}
	for _, field := range []string{"origin", "purpose", "subject", "source", "foreign_user", "stale"} {
		bad := memoryPut(d, e.ID, e.Kind, e.Content)
		bad.Entry = e
		switch field {
		case "origin":
			bad.Entry.Origin = "learned"
		case "purpose":
			bad.Entry.Purpose = "permission"
		case "subject":
			bad.Entry.Subject = "invalid\x00subject"
		case "source":
			bad.Entry.Sources = []MemorySource{{SessionID: "foreign"}}
		case "foreign_user":
			bad.Actor.ID = "other-user"
			bad.Entry.Sources = []MemorySource{{SessionID: "session"}}
		case "stale":
			bad.ExpectedRevision--
		}
		memoryUnchanged(t, s, d, bad)
	}
	m = memoryPut(d, e.ID, e.Kind, "Edited fact")
	m.Entry = e
	m.Entry.Content = "Edited fact"
	d = memoryApply(t, s, m)
	loaded, err := NewMemoryStore(s.store).GetForAccount("a")
	if err != nil || loaded.Entries[0].Origin != "user" || loaded.Entries[0].Purpose != e.Purpose || loaded.Entries[0].CreatedAt != e.CreatedAt {
		t.Fatalf("reload: %+v %v", loaded, err)
	}
	d = memoryApply(t, s, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "user", ID: "user"}, Reason: "Restore metadata", Operation: "restore", EntryID: e.ID, RestoreRevision: 2})
	if d.Entries[0].Content != e.Content || d.Entries[0].Purpose != e.Purpose {
		t.Fatal("restore lost metadata")
	}
}

// Purpose: legacy documents have no trustworthy creation origin. Editing them
// must not fabricate one; read/migration must not rewrite their metadata.
func TestMemoryMetadataLegacyOriginStaysUnknown(t *testing.T) {
	_, s, d := memoryTest(t)
	d.Entries = []MemoryEntry{{ID: "legacy", Kind: "rule", Content: "Legacy", Revision: 1, CreatedAt: 1, UpdatedAt: 1}}
	if err := s.persist(d); err != nil {
		t.Fatal(err)
	}
	m := memoryPut(d, "legacy", "rule", "Changed")
	got := memoryApply(t, s, m)
	if got.Entries[0].Origin != "" {
		t.Fatal("invented legacy origin")
	}
}
