package api

import (
	"encoding/json"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a wait on a linked machine's tasks must tell the person what the
// conversation waits on, like a local wait. Requirement: the wait_tasks result
// with machine carries a notice naming the machine and each task by the title
// the machine reported, the durable wait stores it, and the committed
// session.run.waiting_tasks event carries status waiting_tasks and the notice.
// Threat: a silent pend on remote work, or a notice that drops the machine.
// Owners: tool executeFleetWaitTasks (RemoteTitles), SessionStore
// prepareRemoteProjectTaskWait (ProjectTaskWaitNotice) and v3EventPayload,
// over two in-process daemons linked like production: the narrowest layer
// where the titles really come through the link.
func TestRemoteWaitTasksNoticeNamesMachineAndTasks(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1", "t2")
	out, err := h.wait(t, remoteWaitGoal, "t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	const want = "This conversation is now waiting on 2 task(s) on box: Remote t1, Remote t2. It wakes when they settle; a new message ends the wait."
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "waiting_tasks" || result["machine"] != "box" || result["notice"] != want || result["untrusted_content"] != true {
		t.Fatalf("wait_tasks result: %s", out)
	}
	waiting, _ := h.intent(t, "run-1")
	if waiting.Status != pebblestore.V3RunIntentWaitingTasks || waiting.TaskWait == nil || waiting.TaskWait.Notice != want {
		t.Fatalf("durable wait: %+v", waiting)
	}
	events, err := h.home.s.sessions.Store().ListV3SessionEvents(remoteWaitGoal, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, event := range events {
		if event.EventType != "session.run.waiting_tasks" {
			continue
		}
		found++
		var payload struct {
			Status    string `json:"status"`
			Notice    string `json:"notice"`
			RunIntent struct {
				TaskWait struct {
					Notice string `json:"notice"`
				} `json:"task_wait"`
			} `json:"run_intent"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Status != "waiting_tasks" || payload.Notice != want || payload.RunIntent.TaskWait.Notice != want {
			t.Fatalf("waiting event payload: %s", event.Payload)
		}
	}
	if found != 1 {
		t.Fatalf("waiting events: %d", found)
	}
}
