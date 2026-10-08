package pebblestore

import (
	"fmt"
	"strings"
	"testing"
)

// Purpose: Open must prepare compact archived project rows before first reads,
// resume durable progress after malformed legacy data, and isolate account/user/
// project scopes. Real Pebble startup and canonical archive batches are the
// narrowest boundary proving this; fixture results are not live performance.
func TestProjectArchiveLegacyStartupAndInterruption(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Delete("project_conversation_archive_migration/v1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		id := fmt.Sprintf("legacy-%02d", i)
		project := "wanted"
		if i >= 60 {
			project = "unrelated"
		}
		session := SessionSnapshot{ID: id, AccountScopeID: "account", UserID: "user", Title: "Archived", CreatedAt: int64(i + 1), Metadata: map[string]any{"project_id": project, "agent_name": "system-orchestrator", "private_body": strings.Repeat("x", 10000)}}
		tomb := V3SessionTombstone{SessionID: id, AccountScopeID: "account", UserID: "user", Archived: true, Kind: "archived", UpdatedAt: int64(i + 1), Session: session}
		if err = db.PutJSON(KeyV3SessionTombstone(id), tomb); err != nil {
			t.Fatal(err)
		}
	}
	// A failure in the third batch must retain prior committed progress and bodies.
	if err = db.PutBytes(KeyV3SessionTombstone("legacy-99"), []byte("malformed")); err != nil {
		t.Fatal(err)
	}
	if err = NewSessionStore(db).prepareProjectArchiveIndex(); err == nil {
		t.Fatal("corrupt legacy input accepted")
	}
	var state struct {
		Cursor string
		Ready  bool
	}
	if _, err = db.GetJSON("project_conversation_archive_migration/v1", &state); err != nil || state.Ready || state.Cursor == "" {
		t.Fatalf("lost cursor: %+v %v", state, err)
	}
	if err = db.Delete(KeyV3SessionTombstone("legacy-99")); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	rows, err := s.ListArchivedProjectConversations("account", "user", "wanted", 200)
	if err != nil || len(rows) != 60 || rows[0].SessionID != "legacy-59" || rows[0].Session.Metadata["private_body"] != nil {
		t.Fatalf("first read rows=%d err=%v", len(rows), err)
	}
	for _, scope := range [][3]string{{"foreign", "user", "wanted"}, {"account", "foreign", "wanted"}, {"account", "user", "missing"}} {
		rows, err := s.ListArchivedProjectConversations(scope[0], scope[1], scope[2], 200)
		if err != nil || len(rows) != 0 {
			t.Fatal("scope leak", err)
		}
	}
	// Even matching canonical records can be unreadable: list reads only summaries.
	if err = db.PutBytes(KeyV3SessionTombstone("legacy-59"), []byte("unreadable")); err != nil {
		t.Fatal(err)
	}
	if err = db.PutBytes(KeyV3SessionTombstoneByAccount("account", "legacy-59"), []byte("unreadable")); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListArchivedProjectConversations("account", "user", "wanted", 1)
	if err != nil || len(rows) != 1 || rows[0].SessionID != "legacy-59" {
		t.Fatal("list decoded canonical archive", err)
	}
}

// Purpose: canonical ArchiveSession/UnarchiveSession must move scoped display
// membership atomically, retaining full canonical history only for explicit open.
func TestProjectArchiveCanonicalMembership(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	row := SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "user", CreatedAt: 1, Metadata: map[string]any{"project_id": "project", "agent_name": "system-orchestrator"}}
	if err = s.CreateSession(row); err != nil {
		t.Fatal(err)
	}
	if err = s.ArchiveSession(row.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListArchivedProjectConversations("account", "user", "project", 10)
	if err != nil || len(rows) != 1 {
		t.Fatal("archive missing", err)
	}
	if err = s.ReactivateArchivedSessions([]string{row.ID}, map[string]int64{row.ID: rows[0].UpdatedAt}); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListArchivedProjectConversations("account", "user", "project", 10)
	if err != nil || len(rows) != 0 {
		t.Fatal("restored row remains archived", err)
	}
	active, err := s.ListProjectConversations("account", "user", "project", 10)
	if err != nil || len(active) != 1 {
		t.Fatal("restored row absent", err)
	}
}
