package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func taskReportFixture(t *testing.T) *SessionStore {
	t.Helper()
	s := NewSessionStore(openV3SessionEventTestStore(t))
	taskWaitFixture(t, s)
	project, _, _ := s.GetProject("account", "project")
	project.PrimarySessionID = "parent"
	if err := s.PutProject("account", project); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{V3RunIntentPendingExecutor, V3RunIntentRunning} {
		_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &V3SessionRunIntent{RunID: "child-run", ParentSessionID: "parent", Status: status}})
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func taskReportInput(kind ProjectTaskUpdateKind) V3SessionMutationInput {
	return V3SessionMutationInput{SessionID: "child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationReportTask, ClientRequestID: "report", PayloadHash: "not-authority", TaskReport: &V3ProjectTaskReportMutation{RunID: "child-run", ProjectID: "project", TaskID: "task", Kind: kind, Summary: "A bounded update"}}
}

// Purpose: ApplyV3SessionMutation must persist typed task intent with derived
// ownership and stable identity, without starting a parent or changing task state.
// Store-level assertions are the narrowest proof of atomic events and replay.
func TestProjectTaskReportContract(t *testing.T) {
	for _, kind := range []ProjectTaskUpdateKind{ProjectTaskUpdateProgress, ProjectTaskUpdateAttention, ProjectTaskUpdateWakeRequest} {
		t.Run(string(kind), func(t *testing.T) {
			s := taskReportFixture(t)
			in := taskReportInput(kind)
			result, err := s.ApplyV3SessionMutation(in)
			if err != nil {
				t.Fatal(err)
			}
			var update ProjectTaskUpdate
			if result.RealtimeOutbox == nil {
				t.Fatal("no durable outbox")
			}
			if err := json.Unmarshal(result.RealtimeOutbox.Event.Payload, &update); err != nil {
				t.Fatal(err)
			}
			if update.AccountScopeID != "account" || update.UserID != "user" || update.ParentSessionID != "parent" || update.TaskID != "task" || update.ProjectID != "project" || update.AttemptID != "initial" || update.SessionID != "child" || update.RunID != "child-run" || update.EventID == "" || update.EventSeq != result.PrimarySeq || update.Kind != kind || update.Summary != in.TaskReport.Summary {
				t.Fatalf("bad update: %+v", update)
			}
			replay, err := s.ApplyV3SessionMutation(in)
			if err != nil || !replay.Replayed || replay.PrimarySeq != result.PrimarySeq {
				t.Fatalf("replay: %+v %v", replay, err)
			}
			in.TaskReport.Summary = "changed payload"
			if _, err := s.ApplyV3SessionMutation(in); !errors.Is(err, ErrV3IdempotencyConflict) {
				t.Fatalf("conflict: %v", err)
			}
			seq, _ := s.readV3SessionSequence("child")
			if seq != result.PrimarySeq {
				t.Fatal("duplicate/conflict wrote an event")
			}
			state, _, _ := s.GetV3SessionRunState("parent")
			task, _, _ := s.GetProjectTask("account", "project", "task")
			if state.RunID != "goal" || state.Status != V3RunIntentRunning || task.Status != "in_progress" {
				t.Fatal("report mutated execution")
			}
			messages, _ := s.ListV3SessionMessages("parent", 0, 20)
			if len(messages) != 0 {
				t.Fatal("report leaked raw payload into chat")
			}
		})
	}
}

// Purpose: the canonical mutation boundary must reject forged principals,
// unrelated/stale task attempts and malformed updates before any durable write.
// Rejection plus unchanged event sequences/run state proves non-mutation.
func TestProjectTaskReportRejectsUnauthorized(t *testing.T) {
	cases := map[string]func(*testing.T, *SessionStore, *V3SessionMutationInput){
		"account": func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) { in.AccountScopeID = "foreign" },
		"user":    func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) { in.UserID = "foreign" },
		"session": func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) { in.SessionID = "parent" },
		"project": func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) { in.TaskReport.ProjectID = "foreign" },
		"task":    func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) { in.TaskReport.TaskID = "foreign" },
		"run":     func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) { in.TaskReport.RunID = "stale" },
		"kind":    func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) { in.TaskReport.Kind = "complete" },
		"empty":   func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) { in.TaskReport.Summary = " " },
		"large": func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) {
			in.TaskReport.Summary = strings.Repeat("x", 4001)
		},
		"untyped": func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) { in.TaskReport = nil },
		"message": func(_ *testing.T, _ *SessionStore, in *V3SessionMutationInput) {
			in.Message = &MessageSnapshot{ID: "injected", Role: "user", Content: "wake"}
		},
		"stale-attempt": func(t *testing.T, s *SessionStore, _ *V3SessionMutationInput) {
			_, err := s.UpdateProjectTask("account", "project", "task", func(task *ProjectTaskRecord) error {
				task.SessionID = "replacement"
				task.ActiveAttemptID = "replacement"
				task.Attempts = []ProjectTaskAttempt{{ID: "replacement", SessionID: "replacement"}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		},
		"terminal": func(t *testing.T, s *SessionStore, _ *V3SessionMutationInput) { taskWaitStatus(t, s, "completed") },
		"archived-parent": func(t *testing.T, s *SessionStore, _ *V3SessionMutationInput) {
			_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "stop", PayloadHash: "stop", RunIntent: &V3SessionRunIntent{RunID: "goal", Status: V3RunIntentCancelled}})
			if err != nil {
				t.Fatal(err)
			}
			err = s.ArchiveSessions([]string{"parent"})
			if err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := taskReportFixture(t)
			in := taskReportInput(ProjectTaskUpdateWakeRequest)
			change(t, s, &in)
			before, _ := s.readV3SessionSequence("child")
			parentBefore, _ := s.readV3SessionSequence("parent")
			if _, err := s.ApplyV3SessionMutation(in); err == nil {
				t.Fatal("unauthorized report accepted")
			}
			after, _ := s.readV3SessionSequence("child")
			parentAfter, _ := s.readV3SessionSequence("parent")
			if before != after || parentBefore != parentAfter {
				t.Fatal("rejection wrote session state")
			}
		})
	}
}

// Purpose: task-to-Orchestrator send_message must not bypass report_task even as
// a non-triggering note. The V3 boundary preserves ordinary direct user messages.
func TestProjectTaskReportMessageBypass(t *testing.T) {
	for _, trigger := range []bool{false, true} {
		t.Run(map[bool]string{false: "note", true: "trigger"}[trigger], func(t *testing.T) {
			s := taskReportFixture(t)
			in := V3SessionMutationInput{SessionID: "parent", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationAppendMessage, ClientRequestID: "bypass", PayloadHash: "bypass", Message: &MessageSnapshot{ID: "bypass", Role: "user", Content: "wake", Metadata: map[string]any{"sender_session_id": "child"}}}
			if trigger {
				in.RunIntent = &V3SessionRunIntent{RunID: "bypass-run", Status: V3RunIntentPendingExecutor}
			}
			before, _ := s.readV3SessionSequence("parent")
			if _, err := s.ApplyV3SessionMutation(in); err == nil {
				t.Fatal("bypass accepted")
			}
			after, _ := s.readV3SessionSequence("parent")
			if before != after {
				t.Fatal("bypass wrote state")
			}
			in.Message.Metadata = nil
			in.RunIntent = nil
			if _, err := s.ApplyV3SessionMutation(in); err != nil {
				t.Fatalf("ordinary user message rejected: %v", err)
			}
		})
	}
}

// Purpose: captured deployment lineage must survive checkpoint self-parent runs,
// and changing a project's primary session must never retarget an existing task.
// The store boundary proves server-derived ownership rather than prompt metadata.
func TestProjectTaskReportCapturedLineage(t *testing.T) {
	s := taskReportFixture(t)
	child, _, _ := s.GetSession("child")
	child.Metadata["parent_session_id"] = "parent"
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationUpdateMetadata, ClientRequestID: "lineage", PayloadHash: "lineage", Session: &child}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{V3RunIntentCompleted, V3RunIntentPendingExecutor, V3RunIntentRunning} {
		runID := "checkpoint-run"
		if status == V3RunIntentCompleted {
			runID = "child-run"
		}
		_, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "checkpoint-" + status, PayloadHash: status, RunIntent: &V3SessionRunIntent{RunID: runID, ParentSessionID: "child", Status: status}})
		if err != nil {
			t.Fatal(err)
		}
	}
	project, _, _ := s.GetProject("account", "project")
	project.PrimarySessionID = "different"
	if err := s.PutProject("account", project); err != nil {
		t.Fatal(err)
	}
	in := taskReportInput(ProjectTaskUpdateProgress)
	in.TaskReport.RunID = "checkpoint-run"
	result, err := s.ApplyV3SessionMutation(in)
	if err != nil {
		t.Fatal(err)
	}
	var update ProjectTaskUpdate
	if err := json.Unmarshal(result.RealtimeOutbox.Event.Payload, &update); err != nil {
		t.Fatal(err)
	}
	if update.ParentSessionID != "parent" {
		t.Fatal("report retargeted")
	}
	// Even an identical retained receipt cannot be replayed by a terminal attempt.
	taskWaitStatus(t, s, "completed")
	before, _ := s.readV3SessionSequence("child")
	if _, err := s.ApplyV3SessionMutation(in); err == nil {
		t.Fatal("terminal attempt replay accepted")
	}
	after, _ := s.readV3SessionSequence("child")
	if before != after {
		t.Fatal("terminal replay wrote state")
	}
}

// Purpose: task reports and idempotency must survive Pebble reopen, with no
// duplicate event or parent execution. This real store restart is the narrowest
// durability check; it does not claim provider delivery or acknowledgement.
func TestProjectTaskReportRestart(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	taskWaitFixture(t, s)
	for _, status := range []string{V3RunIntentPendingExecutor, V3RunIntentRunning} {
		_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "child", UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &V3SessionRunIntent{RunID: "child-run", ParentSessionID: "parent", Status: status}})
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	in := taskReportInput(ProjectTaskUpdateWakeRequest)
	result, err := s.ApplyV3SessionMutation(in)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	replay, err := s.ApplyV3SessionMutation(in)
	if err != nil || !replay.Replayed || replay.PrimarySeq != result.PrimarySeq {
		t.Fatalf("restart replay: %+v %v", replay, err)
	}
	records, err := s.ListV3RealtimeOutboxForSessionAfterSeq("child", result.PrimarySeq-1, 1)
	if err != nil || len(records) != 1 {
		t.Fatalf("retained event: %v %v", records, err)
	}
	var update ProjectTaskUpdate
	if err := json.Unmarshal(records[0].Event.Payload, &update); err != nil {
		t.Fatal(err)
	}
	if update.Kind != ProjectTaskUpdateWakeRequest || update.ParentSessionID != "parent" || update.EventSeq != result.PrimarySeq {
		t.Fatal(update)
	}
	state, _, _ := s.GetV3SessionRunState("parent")
	if state.RunID != "goal" {
		t.Fatal("report spawned execution")
	}
}
