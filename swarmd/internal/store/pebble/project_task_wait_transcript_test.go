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
	f := newLocalWatchFixture(t, "t1", "t2")
	f.apply(localWatch("run-1", "t1", "t2"))
	return f
}

// Purpose: the person sees in the conversation itself what it watches, and
// when a watch fires or is stopped, even when the Orchestrator says nothing.
// Requirement: registering a watch (local or on a linked machine) commits, in
// the same mutation, a durable system line "Watching N tasks[ on machine]:
// titles." whose metadata names the watch, count, titles and machine (source
// task_wait_notice); a watch firing records one "Tasks settled: titles." line;
// reconciling again adds no second line; the person's message, the next run
// and an ordinary run end add none; a stop records "Stopped watching". Threat:
// the watch being visible only to the AI in tool output, a line lost on
// reload, a duplicated end line, or a message treated as ending the watch.
// Authority: prepareProjectTaskWatchRegistration, prepareProjectTaskWatchWake
// with the post-commit recordProjectTaskWaitEndNotice, and
// prepareProjectTaskWatchCancel through ApplyV3SessionMutation; a temporary
// real store is the narrowest layer that proves message history together with
// the watch records.
func TestProjectTaskWaitTranscriptLines(t *testing.T) {
	t.Run("settled", func(t *testing.T) {
		f := localTaskWaitTranscriptFixture(t)
		lines := taskWaitNoticeLines(t, f.db, "parent")
		if len(lines) != 1 {
			t.Fatalf("start lines = %d", len(lines))
		}
		start := lines[0]
		watch := f.onlyWatch("parent")
		if start.Content != watch.Wait.Notice || start.Content != "Watching 2 tasks: Task t1, Task t2." {
			t.Fatalf("start line %q, notice %q", start.Content, watch.Wait.Notice)
		}
		if start.Metadata["task_wait_notice"] != ProjectTaskWaitNoticeWatching || start.Metadata["task_watch_id"] != watch.WatchID || !jsonEqual(start.Metadata["task_count"], 2) || !jsonEqual(start.Metadata["task_titles"], []string{"Task t1", "Task t2"}) {
			t.Fatalf("start metadata %v", start.Metadata)
		}
		if _, ok := start.Metadata["machine"]; ok {
			t.Fatalf("local start line names a machine: %v", start.Metadata)
		}
		f.settle("t1", "t2")
		f.endRun("parent", "run-1")
		if woke := f.reconcileCount(); woke != 1 {
			t.Fatalf("wake: %d", woke)
		}
		// Reconciling again fires nothing and records no second end line.
		if woke := f.reconcileCount(); woke != 0 {
			t.Fatalf("second reconcile: %d", woke)
		}
		lines = taskWaitNoticeLines(t, f.db, "parent")
		if len(lines) != 2 || lines[1].Content != "Tasks settled: Task t1, Task t2." || lines[1].Metadata["task_wait_notice"] != ProjectTaskWaitNoticeEnded || lines[1].Metadata["ended_by"] != ProjectTaskWaitEndedByTasks || !jsonEqual(lines[1].Metadata["task_count"], 2) {
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
				t.Fatalf("a watch line was stored as a user turn: %+v", m)
			}
		}
		if outcome != 1 {
			t.Fatalf("outcome messages = %d", outcome)
		}
	})
	t.Run("message", func(t *testing.T) {
		f := localTaskWaitTranscriptFixture(t)
		f.endRun("parent", "run-1")
		f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "msg-2", Message: &MessageSnapshot{ID: "m2", Role: "user", Content: "change of plan"}, RunIntent: &V3SessionRunIntent{RunID: "run-2", Status: V3RunIntentPendingExecutor}})
		f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "run-2-running", RunIntent: &V3SessionRunIntent{RunID: "run-2", Status: V3RunIntentRunning}})
		f.endRun("parent", "run-2")
		if n := len(taskWaitNoticeLines(t, f.db, "parent")); n != 1 {
			t.Fatalf("lines after a message and a run = %d", n)
		}
		f.onlyWatch("parent")
	})
	t.Run("stop", func(t *testing.T) {
		f := localTaskWaitTranscriptFixture(t)
		f.apply(V3SessionMutationInput{SessionID: "parent", Kind: V3SessionMutationAppendMessage, ClientRequestID: "stop-watches", TaskWait: &V3ProjectTaskWaitMutation{Cancel: true, CancelledBy: ProjectTaskWaitEndedByStop}})
		lines := taskWaitNoticeLines(t, f.db, "parent")
		if len(lines) != 2 || lines[1].Content != "Stopped watching: Task t1, Task t2." || lines[1].Metadata["ended_by"] != ProjectTaskWaitEndedByStop {
			t.Fatalf("after stop: %+v", lines)
		}
	})
	t.Run("linked machine", func(t *testing.T) {
		f := newTaskWaitFixture(t, "home", map[string]any{})
		in := remoteTaskWaitRegistration(V3ProjectTaskWaitTarget{TaskID: "t1", AttemptID: "a1", SessionID: "rs1"}, V3ProjectTaskWaitTarget{TaskID: "t2", AttemptID: "a2", SessionID: "rs2"})
		in.TaskWait.RemoteTitles = []string{"Remote one", "Remote two"}
		f.apply(in)
		lines := taskWaitNoticeLines(t, f.db, "home")
		if len(lines) != 1 || lines[0].Content != "Watching 2 tasks on box: Remote one, Remote two." || lines[0].Metadata["machine"] != "box" || lines[0].Metadata["untrusted_content"] != true || !jsonEqual(lines[0].Metadata["task_count"], 2) {
			t.Fatalf("linked start line: %+v", lines)
		}
	})
}
