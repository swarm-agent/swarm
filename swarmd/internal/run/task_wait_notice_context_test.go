package run

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the visible task wait lines are for the person, never model
// context. Requirement: the run service's provider input (buildInput) and the
// delegated-task transcript skip task_wait_notice system messages while other
// system messages still pass. Threat: the wait line reaching a model as a
// "[system]" user turn. Authority: buildInput and
// formatTaskDelegationTranscriptMessage with
// pebblestore.IsProjectTaskWaitNoticeMessage; pure builders are the narrowest
// layer that decides what a model reads.
func TestTaskWaitNoticesStayOutOfModelContext(t *testing.T) {
	notice := pebblestore.MessageSnapshot{Role: "system", Content: "Wait ended by your message.", Metadata: map[string]any{"source": pebblestore.ProjectTaskWaitNoticeSource}}
	other := pebblestore.MessageSnapshot{Role: "system", Content: "Delegated project task outcomes:\n{}"}
	raw, _ := json.Marshal(buildInput([]pebblestore.MessageSnapshot{notice, other}))
	if strings.Contains(string(raw), "Wait ended") || !strings.Contains(string(raw), "Delegated project task outcomes") {
		t.Fatalf("buildInput: %s", raw)
	}
	if got := formatTaskDelegationTranscriptMessage(notice); got != "" {
		t.Fatalf("delegation transcript kept the notice: %q", got)
	}
	if got := formatTaskDelegationTranscriptMessage(other); got == "" {
		t.Fatal("delegation transcript dropped an ordinary system message")
	}
}
