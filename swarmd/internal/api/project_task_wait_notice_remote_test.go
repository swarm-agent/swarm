package api

import (
	"encoding/json"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a watch on a linked machine's tasks tells the person and the AI
// what the conversation watches, like a local watch. Requirement: the
// wait_tasks result with machine has status watching and a notice naming the
// machine and each task by the title the machine reported, telling the AI to
// reply and end its turn; the run keeps running (no waiting_tasks event); the
// committed session.task_watch.started event carries the transcript line and
// the conversation's task_watches. Threat: a silent pend on remote work, or a
// notice that drops the machine. Owners: tool executeFleetWaitTasks
// (RemoteTitles), registerProjectTaskWatch, SessionStore
// prepareProjectTaskWatchRegistration and v3EventPayload, over two in-process
// daemons linked like production: the narrowest layer where the titles really
// come through the link.
func TestRemoteWaitTasksNoticeNamesMachineAndTasks(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1", "t2")
	out, err := h.wait(t, remoteWaitGoal, "t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	const want = "Watching 2 task(s) on box: Remote t1, Remote t2. Reply to the person now and end your turn; a new turn starts with the outcomes when they settle."
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	// The envelope says where and that the content is untrusted; its data
	// is the watch.
	data := envelopeDataMap(result)
	if result["state"] != "watching" || data["status"] != "watching" || result["machine"] != "box" || data["notice"] != want || result["untrusted_content"] != true {
		t.Fatalf("wait_tasks result: %s", out)
	}
	if run, _ := h.intent(t, "run-1"); run.Status != pebblestore.V3RunIntentRunning || run.TaskWait != nil {
		t.Fatalf("watch changed the run: %+v", run)
	}
	events, err := h.home.s.sessions.Store().ListV3SessionEvents(remoteWaitGoal, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, event := range events {
		if event.EventType == "session.run.waiting_tasks" {
			t.Fatalf("waiting event emitted: %s", event.Payload)
		}
		if event.EventType != "session.task_watch.started" {
			continue
		}
		found++
		var payload struct {
			Notice      string `json:"notice"`
			TaskWatches []struct {
				Machine string   `json:"machine"`
				Titles  []string `json:"titles"`
			} `json:"task_watches"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Notice != "Watching 2 tasks on box: Remote t1, Remote t2." || len(payload.TaskWatches) != 1 || payload.TaskWatches[0].Machine != "box" || len(payload.TaskWatches[0].Titles) != 2 {
			t.Fatalf("watch event payload: %s", event.Payload)
		}
	}
	if found != 1 {
		t.Fatalf("watch events: %d", found)
	}
}
