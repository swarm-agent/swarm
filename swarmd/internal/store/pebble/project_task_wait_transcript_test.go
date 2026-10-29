package pebblestore

import (
	"strings"
	"testing"
)

func taskWaitNoticeLines(t *testing.T, db *SessionStore, session string) []MessageSnapshot {
	t.Helper()
	messages, err := db.ListMessages(session, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var lines []MessageSnapshot
	for _, message := range messages {
		if IsProjectTaskWaitNoticeMessage(message) {
			lines = append(lines, message)
		}
	}
	return lines
}

func localTaskWaitTranscriptFixture(t *testing.T) *taskWaitFixture {
	t.Helper()
	f := newTaskWaitFixture(t, "parent", map[string]any{"swarm_v3_project_id": "project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator", "project_id": "project"})
	for _, id := range []string{"t1", "t2"} {
		child := "child-" + id
		f.apply(V3SessionMutationInput{SessionID: child, Kind: V3SessionMutationCreateSession, ClientRequestID: "create-" + child, Session: &SessionSnapshot{ID: child, Mode: "auto", Metadata: map[string]any{"project_id": "project", "task_id": id}}})
		task := &ProjectTaskRecord{ID: id, ProjectID: "project", AccountID: "account", Title: "Task " + id, Agent: "coder", Status: "running", SessionID: child, ActiveAttemptID: "a-" + id, Attempts: []ProjectTaskAttempt{{ID: "a-" + id, SessionID: child, Role: "coder", Status: "running"}}}
		if err := f.db.PutProjectTask("account", task); err != nil {
			t.Fatal(err)
		}
	}
	f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, EventType: "session.run.waiting_tasks", ClientRequestID: "task-wait:run-1", TaskWait: &V3ProjectTaskWaitMutation{RunID: "run-1", ProjectID: "project", TaskIDs: []string{"t1", "t2"}}})
	return f
}

// Purpose: the person sees in the conversation itself that it waits on tasks,
// and when that wait ends, even when the Orchestrator says nothing.
// Requirement: registering a wait (local or on a linked machine) commits, in
// the same mutation, a durable system line whose text is the wait's notice and
// whose metadata names the count, titles and machine (source
// task_wait_notice); ending the wait by its tasks settling, by the person's
// message or by a stop records one matching end line; reconciling again adds
// no second line; a later ordinary run adds none. Threat: the wait being
// visible only to the AI in tool output, a line lost on reload, or a
// duplicated end line. Authority: prepareProjectTaskWait /
// prepareRemoteProjectTaskWait, setProjectTaskWaitTransition and the
// post-commit recordProjectTaskWaitEndNotice through ApplyV3SessionMutation; a
// temporary real store is the narrowest layer that proves message history
// together with the run intent transitions.
func TestProjectTaskWaitTranscriptLines(t *testing.T) {
	t.Run("settled", func(t *testing.T) {
		f := localTaskWaitTranscriptFixture(t)
		lines := taskWaitNoticeLines(t, f.db, "parent")
		if len(lines) != 1 {
			t.Fatalf("start lines = %d", len(lines))
		}
		start := lines[0]
		wait := f.intent("parent", "run-1").TaskWait
		if start.Content != wait.Notice || !strings.Contains(start.Content, "2 task(s): Task t1, Task t2") {
			t.Fatalf("start line %q, notice %q", start.Content, wait.Notice)
		}
		if start.Metadata["task_wait_notice"] != ProjectTaskWaitNoticeWaiting || start.Metadata["task_wait_run_id"] != "run-1" || !jsonEqual(start.Metadata["task_count"], 2) || !jsonEqual(start.Metadata["task_titles"], []string{"Task t1", "Task t2"}) {
			t.Fatalf("start metadata %v", start.Metadata)
		}
		if _, ok := start.Metadata["machine"]; ok {
			t.Fatalf("local start line names a machine: %v", start.Metadata)
		}
		for _, id := range []string{"t1", "t2"} {
			if _, err := f.db.UpdateProjectTask("account", "project", id, func(t *ProjectTaskRecord) error {
				t.Status = "needs_review"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		woke := 0
		if err := f.db.ReconcileProjectTaskWaits("account", "project", func(V3SessionMutationResult) { woke++ }); err != nil || woke != 1 {
			t.Fatalf("wake: %v %d", err, woke)
		}
		// Reconciling again wakes nothing and records no second end line.
		if err := f.db.ReconcileProjectTaskWaits("account", "project", func(V3SessionMutationResult) { woke++ }); err != nil || woke != 1 {
			t.Fatalf("second reconcile: %v %d", err, woke)
		}
		lines = taskWaitNoticeLines(t, f.db, "parent")
		if len(lines) != 2 || lines[1].Content != "Wait ended: tasks settled." || lines[1].Metadata["task_wait_notice"] != ProjectTaskWaitNoticeEnded || lines[1].Metadata["ended_by"] != ProjectTaskWaitEndedByTasks || !jsonEqual(lines[1].Metadata["task_count"], 2) {
			t.Fatalf("after wake: %+v", lines)
		}
		// The outcome message the model reads is still there, and is not a notice.
		messages, _ := f.db.ListMessages("parent", 0, 100)
		outcome := 0
		for _, m := range messages {
			if strings.HasPrefix(m.Content, "Delegated project task outcomes:") {
				outcome++
				if IsProjectTaskWaitNoticeMessage(m) {
					t.Fatal("outcome message classified as a notice")
				}
			}
			if m.Role == "user" {
				t.Fatalf("a wait line was stored as a user turn: %+v", m)
			}
		}
		if outcome != 1 {
			t.Fatalf("outcome messages = %d", outcome)
		}
	})
	t.Run("message", func(t *testing.T) {
		f := localTaskWaitTranscriptFixture(t)
		f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "msg-2", Message: &MessageSnapshot{ID: "m2", Role: "user", Content: "change of plan"}, RunIntent: &V3SessionRunIntent{RunID: "run-2", Status: V3RunIntentPendingExecutor}})
		if got := f.intent("parent", "run-1").Status; got != V3RunIntentCancelled {
			t.Fatalf("wait not superseded: %s", got)
		}
		lines := taskWaitNoticeLines(t, f.db, "parent")
		if len(lines) != 2 || lines[1].Content != "Wait ended by your message." || lines[1].Metadata["ended_by"] != ProjectTaskWaitEndedByMessage {
			t.Fatalf("after message: %+v", lines)
		}
		// The next run starting is not a wait ending: no further line.
		f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "run-2-running", RunIntent: &V3SessionRunIntent{RunID: "run-2", Status: V3RunIntentRunning}})
		f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "run-2-done", RunIntent: &V3SessionRunIntent{RunID: "run-2", Status: V3RunIntentCompleted}})
		if n := len(taskWaitNoticeLines(t, f.db, "parent")); n != 2 {
			t.Fatalf("lines after a normal run = %d", n)
		}
	})
	t.Run("stop", func(t *testing.T) {
		f := localTaskWaitTranscriptFixture(t)
		f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "stop-run-1", RunIntent: &V3SessionRunIntent{RunID: "run-1", Status: V3RunIntentCancelled}})
		lines := taskWaitNoticeLines(t, f.db, "parent")
		if len(lines) != 2 || lines[1].Content != "Wait ended: stopped." || lines[1].Metadata["ended_by"] != ProjectTaskWaitEndedByStop {
			t.Fatalf("after stop: %+v", lines)
		}
	})
	t.Run("linked machine", func(t *testing.T) {
		f := newTaskWaitFixture(t, "home", map[string]any{})
		in := remoteTaskWaitRegistration(V3ProjectTaskWaitTarget{TaskID: "t1", AttemptID: "a1", SessionID: "rs1"}, V3ProjectTaskWaitTarget{TaskID: "t2", AttemptID: "a2", SessionID: "rs2"})
		in.TaskWait.RemoteTitles = []string{"Remote one", "Remote two"}
		f.apply(in)
		lines := taskWaitNoticeLines(t, f.db, "home")
		if len(lines) != 1 || !strings.Contains(lines[0].Content, "2 task(s) on box: Remote one, Remote two") || lines[0].Metadata["machine"] != "box" || lines[0].Metadata["untrusted_content"] != true || !jsonEqual(lines[0].Metadata["task_count"], 2) {
			t.Fatalf("linked start line: %+v", lines)
		}
	})
}

// Purpose: the end line text matches how the wait ended. Pure text function.
func TestProjectTaskWaitEndNoticeText(t *testing.T) {
	for _, c := range []struct{ by, status, want string }{
		{ProjectTaskWaitEndedByTasks, "", "Wait ended: tasks settled."},
		{ProjectTaskWaitEndedByMessage, "", "Wait ended by your message."},
		{ProjectTaskWaitEndedByStop, V3RunIntentCancelled, "Wait ended: stopped."},
		{ProjectTaskWaitEndedByStop, V3RunIntentInterrupted, "Wait ended: run interrupted."},
	} {
		if got := ProjectTaskWaitEndNotice(c.by, c.status); got != c.want {
			t.Fatalf("%s/%s = %q, want %q", c.by, c.status, got, c.want)
		}
	}
}
