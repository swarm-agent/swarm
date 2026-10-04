package api

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the actual snapshot reader and shared syncTaskSessionState policy
// must preserve manual approval and terminal review, reject foreign execution,
// and never write a compact record back. This temp-store/API-policy layer is
// narrower than a running server and exercises the production reconciliation.
func TestProjectTaskBoardLifecycle(t *testing.T) {
	for _, tc := range []struct { name, status, account, run, want string }{
		{"review", "in_progress", "account", pebblestore.V3RunIntentCompleted, "needs_review"},
		{"manual", "pending_approval", "account", pebblestore.V3RunIntentRunning, "pending_approval"},
		{"foreign", "in_progress", "foreign", pebblestore.V3RunIntentCompleted, "in_progress"},
		{"live", "needs_review", "account", pebblestore.V3RunIntentRunning, "in_progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := pebblestore.Open(t.TempDir()); if err != nil { t.Fatal(err) }; defer db.Close()
			s := pebblestore.NewSessionStore(db)
			task := pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", SessionID: "session", Title: "Task", Agent: "swarm", Status: tc.status, FullPlanMarkdown: "detail-only"}
			if err := s.PutProjectTask("account", &task); err != nil { t.Fatal(err) }
			if err := db.PutJSON(pebblestore.KeySession("session"), pebblestore.SessionSnapshot{ID: "session", AccountScopeID: tc.account}); err != nil { t.Fatal(err) }
			if err := db.PutJSON(pebblestore.KeyV3SessionRunIntentActive("session"), pebblestore.V3SessionRunState{SessionID: "session", AccountScopeID: tc.account, RunID: task.ExecutionRunID(), Status: tc.run}); err != nil { t.Fatal(err) }
			rows, stats, err := s.ReadProjectTaskBoard("account", "project", false, func(rows []pebblestore.ProjectTaskRecord, reader *pebblestore.ProjectTaskBoardReader) {
				for i := range rows { reader.BindTask(&rows[i]); syncTaskSessionState(&rows[i], reader) }
			})
			if err != nil || len(rows) != 1 { t.Fatalf("rows=%v err=%v", rows, err) }
			if rows[0].Status != tc.want || rows[0].FullPlanMarkdown != "" { t.Fatalf("row=%+v", rows[0]) }
			if stats.RelatedRecordReads > 4 { t.Fatalf("duplicate reads: %+v", stats) }
			canonical, _, err := s.GetProjectTask("account", "project", "task")
			if err != nil || canonical.Status != tc.status || canonical.FullPlanMarkdown != "detail-only" { t.Fatalf("read mutated canonical: %+v %v", canonical, err) }
		})
	}
}

// Purpose: writeProjectTaskBoard exposes bounded numeric attribution, with exact
// serialized bytes and no sensitive content in headers. The writer is the
// narrowest layer proving the on-wire instrumentation contract.
func TestProjectTaskBoardTiming(t *testing.T) {
	w := httptest.NewRecorder()
	writeProjectTaskBoard(w, []projectTaskBoardRow{{ProjectTaskRecord: pebblestore.ProjectTaskRecord{ID: "private-task"}}}, pebblestore.ProjectTaskReadStats{ScannedRows: 1}, time.Now(), time.Millisecond)
	if w.Code != 200 || w.Header().Get("X-Task-Scanned-Rows") != "1" || w.Header().Get("X-Task-Response-Bytes") != strconv.Itoa(w.Body.Len()) { t.Fatalf("headers=%v", w.Header()) }
	if !strings.Contains(w.Header().Get("Server-Timing"), "task_encode;dur=") || strings.Contains(w.Header().Get("Server-Timing"), "private-task") { t.Fatal("invalid timing contract") }
}

// Purpose: canonical program execution policy must honor current child run
// identity even after parent completion, while a stale generation cannot make
// the board running. Snapshot-backed policy is the narrow production boundary.
func TestProjectTaskBoardChildExecution(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir()); if err != nil { t.Fatal(err) }; defer db.Close()
	s := pebblestore.NewSessionStore(db)
	task := pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", SessionID: "parent", Title: "Task", Agent: "swarm", Status: "needs_review", TaskProgramID: "program"}
	if err := s.PutProjectTask("account", &task); err != nil { t.Fatal(err) }
	for _, sid := range []string{"parent", "child"} {
		if err := db.PutJSON(pebblestore.KeySession(sid), pebblestore.SessionSnapshot{ID: sid, AccountScopeID: "account"}); err != nil { t.Fatal(err) }
	}
	if err := db.PutJSON(pebblestore.KeyV3SessionRunIntentActive("parent"), pebblestore.V3SessionRunState{SessionID: "parent", AccountScopeID: "account", RunID: task.ExecutionRunID(), Status: pebblestore.V3RunIntentCompleted}); err != nil { t.Fatal(err) }
	program := pebblestore.TaskProgramRecord{ParentSessionID: "parent", ProgramID: "program", ReservationRunID: task.ExecutionRunID(), State: pebblestore.TaskProgramStateCompleted, Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "job", CurrentSessionID: "child", CurrentRunID: "current"}}}
	if err := db.PutJSON(pebblestore.KeyTaskProgram("parent", "program"), program); err != nil { t.Fatal(err) }
	for _, run := range []string{"current", "stale"} {
		if err := db.PutJSON(pebblestore.KeyV3SessionRunIntentActive("child"), pebblestore.V3SessionRunState{SessionID: "child", AccountScopeID: "account", RunID: run, Status: pebblestore.V3RunIntentRunning}); err != nil { t.Fatal(err) }
		rows, _, err := s.ReadProjectTaskBoard("account", "project", false, func(rows []pebblestore.ProjectTaskRecord, reader *pebblestore.ProjectTaskBoardReader) {
			reader.BindTask(&rows[0]); syncTaskSessionState(&rows[0], reader)
			summary := projectTaskBoardSummary(&rows[0], reader)
			if summary.Program == nil || len(summary.Program.Jobs) != 1 || summary.Program.Jobs[0].SessionID != "child" { t.Fatal("lost permission hydration identity") }
		})
		want := "needs_review"; if run == "current" { want = "in_progress" }
		if err != nil || len(rows) != 1 || rows[0].Status != want { t.Fatalf("run=%s rows=%+v err=%v", run, rows, err) }
	}
}
