package pebblestore

import (
	"fmt"
	"strings"
	"testing"
)

// Purpose: project summaries must remain account/user/project scoped and bounded
// independently of unrelated full session bodies. Canonical create/update/delete
// index batches are the narrowest owner of membership and ordering correctness.
func TestProjectConversationSummaryIndex(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	for i := 0; i < 40; i++ {
		project := "unrelated"
		if i < 3 {
			project = "wanted"
		}
		row := SessionSnapshot{ID: fmt.Sprintf("session-%02d", i), AccountScopeID: "account", UserID: "user", Title: "Title", CreatedAt: int64(i + 1), Metadata: map[string]any{"project_id": project, "agent_name": "system-orchestrator", "private_payload": strings.Repeat("x", 10000)}}
		if err := s.CreateSession(row); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListProjectConversations("account", "user", "wanted", 2)
	if err != nil || len(rows) != 2 || rows[0].ID != "session-02" || rows[1].ID != "session-01" {
		t.Fatalf("ordered rows=%+v err=%v", rows, err)
	}
	if rows[0].Metadata["private_payload"] != nil {
		t.Fatal("full body leaked")
	}
	for _, pair := range [][2]string{{"foreign", "user"}, {"account", "other"}} {
		rows, err := s.ListProjectConversations(pair[0], pair[1], "wanted", 200)
		if err != nil || len(rows) != 0 {
			t.Fatal("scope isolation failed")
		}
	}
	original, _, _ := s.GetSession("session-02")
	original.Metadata["project_id"] = "moved"
	if err := s.UpdateSession(original); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListProjectConversations("account", "user", "wanted", 200)
	if err != nil || len(rows) != 2 || rows[0].ID != "session-01" {
		t.Fatal("old membership retained")
	}
	// Corrupt an unrelated canonical body after indexing; scoped reads cannot decode it.
	if err := db.PutBytes(KeySession("session-39"), []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListProjectConversations("account", "user", "wanted", 200); err != nil {
		t.Fatal("read unrelated body", err)
	}
}

// Purpose: a genuinely absent legacy index must backfill across multiple durable
// batches on Open. Interrupted cursor state must not omit later project rows.
func TestProjectConversationSummaryLegacyStartup(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Remove only the migration marker in this isolated pre-index fixture.
	if err := db.Delete("project_conversation_migration/v1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		row := SessionSnapshot{ID: fmt.Sprintf("legacy-%02d", i), AccountScopeID: "account", UserID: "user", CreatedAt: int64(i + 1), Title: "Legacy", Metadata: map[string]any{"project_id": "project", "agent_name": "system-orchestrator", "body": strings.Repeat("detail", 1000)}}
		if err := db.PutJSON(KeySession(row.ID), row); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := NewSessionStore(db).ListProjectConversations("account", "user", "project", 200)
	if err != nil || len(rows) != 70 || rows[0].ID != "legacy-69" || rows[0].Metadata["body"] != nil {
		t.Fatalf("legacy summaries count=%d err=%v", len(rows), err)
	}
}
