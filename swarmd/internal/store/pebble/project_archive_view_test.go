package pebblestore

import "testing"

// Purpose: project task management must show the full persisted active/archive partition
// and only delete archived, unlaunched records at their exact revision. Threats are a
// truncated board hiding selectable tasks, stale/foreign deletion, and orphaning work.
// ListProjectTasksByArchive and DeleteProjectTaskIfRevision are the narrowest durable authority.
func TestProjectTaskArchiveViewAndGuardedDelete(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer db.Close()
	s := NewSessionStore(db)
	for _, id := range []string{"a", "b"} {
		if err := s.PutProject("account-a", &ProjectRecord{ID:id, Name:id}); err != nil { t.Fatal(err) }
	}
	for i := 0; i < 110; i++ {
		id := string(rune('a'+i/26)) + string(rune('a'+i%26))
		if err := s.PutProjectTask("account-a", &ProjectTaskRecord{ID:id, ProjectID:"a", Title:id, Agent:"coder", Status:"blocked", Revision:1}); err != nil { t.Fatal(err) }
	}
	active, err := s.ListProjectTasksByArchive("account-a", "a", false)
	if err != nil || len(active) != 110 { t.Fatalf("active view truncated: %d %v", len(active), err) }
	id := active[0].ID
	if err := s.DeleteProjectTaskIfRevision("account-a", "a", id, 1); err == nil { t.Fatal("unarchived record deleted") }
	if _, err := s.ArchiveProjectTaskIfRevision("account-a", "a", id, 1); err != nil { t.Fatal(err) }
	active, err = s.ListProjectTasksByArchive("account-a", "a", false)
	if err != nil || len(active) != 109 { t.Fatalf("archived task on active board: %d %v", len(active), err) }
	archived, err := s.ListProjectTasksByArchive("account-a", "a", true)
	if err != nil || len(archived) != 1 || archived[0].ID != id || archived[0].Revision != 2 { t.Fatalf("archive view: %+v %v", archived, err) }
	if foreign, err := s.ListProjectTasksByArchive("account-b", "a", true); err != nil || len(foreign) != 0 { t.Fatalf("foreign archive visible: %+v %v", foreign, err) }
	if err := s.DeleteProjectTaskIfRevision("account-b", "a", id, 2); err == nil { t.Fatal("foreign account deleted task") }
	if err := s.DeleteProjectTaskIfRevision("account-a", "b", id, 2); err == nil { t.Fatal("foreign project deleted task") }
	if err := s.DeleteProjectTaskIfRevision("account-a", "a", id, 1); err == nil { t.Fatal("stale revision deleted task") }
	if _, found, err := s.GetProjectTask("account-a", "a", id); err != nil || !found { t.Fatalf("rejection changed record: %v %v", found, err) }
	if err := s.DeleteProjectTaskIfRevision("account-a", "a", id, 2); err != nil { t.Fatal(err) }
	if _, found, err := s.GetProjectTask("account-a", "a", id); err != nil || found { t.Fatalf("delete not persisted: %v %v", found, err) }
}
