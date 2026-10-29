package api

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the visible task wait lines are for the person, never a model turn.
// Requirement: the V3 provider input skips system messages whose metadata
// source is task_wait_notice, while ordinary system messages (the wait's
// outcome result the Orchestrator must read) and user turns still reach the
// model. Threat: the wait line being replayed to the model as "[system] ..."
// user input, changing what it does next. Authority:
// sessionsV3ProviderInputWithOptions with pebblestore.IsProjectTaskWaitNoticeMessage;
// the pure builder is the narrowest layer that decides model context.
func TestProviderInputSkipsTaskWaitNotices(t *testing.T) {
	messages := []pebblestore.MessageSnapshot{
		{ID: "u1", Role: "user", Content: "delegate it"},
		{ID: "n1", Role: "system", Content: "This conversation is now waiting on 1 task(s): Fix login. It wakes when they settle; a new message ends the wait.", Metadata: map[string]any{"source": pebblestore.ProjectTaskWaitNoticeSource, "task_wait_notice": "waiting"}},
		{ID: "r1", Role: "system", Content: "Delegated project task outcomes:\n{}"},
		{ID: "n2", Role: "system", Content: "Wait ended: tasks settled.", Metadata: map[string]any{"source": pebblestore.ProjectTaskWaitNoticeSource, "task_wait_notice": "ended"}},
	}
	input := sessionsV3ProviderInputWithOptions(messages, sessionsV3ProviderInputOptions{})
	raw, _ := json.Marshal(input)
	text := string(raw)
	if strings.Contains(text, "now waiting on") || strings.Contains(text, "Wait ended") {
		t.Fatalf("wait line reached the model: %s", text)
	}
	if !strings.Contains(text, "delegate it") || !strings.Contains(text, "Delegated project task outcomes") {
		t.Fatalf("model context lost real messages: %s", text)
	}
	if len(input) != 2 {
		t.Fatalf("input items = %d: %s", len(input), text)
	}
}
