package pebblestore

import (
	"strings"
	"testing"
)

// Purpose: ReadProjectTaskBoard must deduplicate related reads in one snapshot,
// reject foreign and stale bindings, and never accept an unrelated latest plan.
// Direct temporary-store fixtures are the narrowest layer for these read checks.
func TestProjectTaskBoardSnapshotBindings(t *testing.T) {
	db, err := Open(t.TempDir()); if err != nil { t.Fatal(err) }; defer db.Close()
	s := NewSessionStore(db)
	task := ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", SessionID: "session", Title: "Task", Agent: "swarm", PlanBinding: &ProjectTaskPlanBinding{SessionID: "session", PlanID: "plan", DefinitionRevision: 2}}
	if err := s.PutProjectTask("account", &task); err != nil { t.Fatal(err) }
	if err := s.UpdateSession(SessionSnapshot{ID: "session", AccountScopeID: "account"}); err != nil { t.Fatal(err) }
	if err := s.PutPlan(SessionPlanSnapshot{ID: "plan", SessionID: "session", AccountScopeID: "account", Version: 1}); err != nil { t.Fatal(err) }
	_, stats, err := s.ReadProjectTaskBoard("account", "project", false, func(rows []ProjectTaskRecord, r *ProjectTaskBoardReader) {
		r.BindTask(&rows[0])
		if _, ok, err := r.GetPlan("session", "plan"); ok || err != nil { t.Fatalf("stale plan accepted: %v", err) }
		if err := s.UpdateSession(SessionSnapshot{ID: "session", AccountScopeID: "foreign"}); err != nil { t.Fatal(err) }
		for i := 0; i < 5; i++ {
			v, ok, err := r.GetSession("session")
			if !ok || err != nil || v.AccountScopeID != "account" { t.Fatalf("inconsistent snapshot: %+v %v", v, err) }
		}
	})
	if err != nil || stats.RelatedRecordReads != 4 { t.Fatalf("stats=%+v err=%v", stats, err) }
	_, _, err = s.ReadProjectTaskBoard("account", "project", false, func(rows []ProjectTaskRecord, r *ProjectTaskBoardReader) {
		if _, ok, err := r.GetSession("session"); ok || err != nil { t.Fatalf("foreign session accepted: %v", err) }
	})
	if err != nil { t.Fatal(err) }
}

// Purpose: BackfillProjectTaskSummaries must progress for ordinary media rows
// above the chunk size, while enforcing the configured single-record ceiling.
// Rejection must leave no ready partial board; retry must recover without data loss.
func TestProjectTaskBoardLargeLegacyMigration(t *testing.T) {
	db, err := Open(t.TempDir()); if err != nil { t.Fatal(err) }; defer db.Close()
	s := NewSessionStore(db)
	task := ProjectTaskRecord{ID: "task", AccountID: "account", ProjectID: "project", Title: "Task", FullPlanMarkdown: strings.Repeat("x", (16<<20)+1)}
	if err := db.PutJSON(KeyProjectTask("account", "project", "task"), task); err != nil { t.Fatal(err) }
	t.Setenv("SWARM_PROJECT_MEDIA_MAX_REQUEST_BYTES", "1024")
	if _, _, err := s.ListProjectTaskSummaries("account", "project", false); err == nil { t.Fatal("oversized record accepted") }
	t.Setenv("SWARM_PROJECT_MEDIA_MAX_REQUEST_BYTES", "33554432")
	rows, stats, err := s.ListProjectTaskSummaries("account", "project", false)
	if err != nil || len(rows) != 1 || stats.BackfillRows != 1 || stats.DecodedBytes > 10000 { t.Fatalf("migration did not recover: rows=%d stats=%+v err=%v", len(rows), stats, err) }
	_, stats, err = s.ListProjectTaskSummaries("account", "project", false)
	if err != nil || stats.BackfillBytes != 0 { t.Fatalf("warm read migrated again: %+v %v", stats, err) }
}
