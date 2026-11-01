package tool

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: an environment answer whose build is still running names the next
// step its caller can take: the Orchestrator's watch (wait_environment,
// which wakes it), or, for a Swarm Control caller that has no turn to wake,
// get_environment's held wait; a build that ended names none. Threat: a
// still-building answer that leaves the caller to poll or forget, or one
// that points Swarm Control at an action it cannot use. Boundary:
// withFolderEnvironmentNextAction (Runtime.executeFolderEnvironments).
// Narrowest layer: the answer function both surfaces share.
func TestFolderEnvironmentStillBuildingNamesTheWatchOrTheHeldWait(t *testing.T) {
	building := json.RawMessage(`{"ok":true,"environment":{"workspace_path":"/srv/app","status":"building"}}`)
	next := func(raw json.RawMessage) string {
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		s, _ := out["next_action"].(string)
		return s
	}
	if got := next(withFolderEnvironmentNextAction(building, false, "box")); !strings.Contains(got, "wait_environment with workspace_path /srv/app and machine box") {
		t.Fatalf("Orchestrator next step: %q", got)
	}
	if got := next(withFolderEnvironmentNextAction(building, true, "")); !strings.Contains(got, "get_environment") || !strings.Contains(got, "wait_seconds") || strings.Contains(got, "wait_environment") {
		t.Fatalf("Swarm Control next step: %q", got)
	}
	ready := json.RawMessage(`{"ok":true,"environment":{"workspace_path":"/srv/app","status":"ready"}}`)
	if got := next(withFolderEnvironmentNextAction(ready, false, "")); got != "" {
		t.Fatalf("an ended build names a next step: %q", got)
	}
}

// Purpose: a watch's outcome carries at most the last EnvironmentWatchLogLines
// lines of the log and says when earlier lines were left out (pointing at the
// paged log); a failed build names its error and no image. Threat: an outcome
// flooding the conversation with the whole log, or hiding that it is a tail.
// Boundary: environmentOutcomeFrom/environmentWatchMessage.
func TestEnvironmentWatchMessageCarriesTheLogTail(t *testing.T) {
	var log strings.Builder
	for i := 0; i < 100; i++ {
		log.WriteString("step output\n")
	}
	log.WriteString("warm-build FAILED\n")
	raw, _ := json.Marshal(map[string]any{"environment": map[string]any{
		"defined": true, "status": "failed", "hash": "abc", "error": "setup command 2 failed", "log": log.String(),
		"log_page":   map[string]any{"offset": 0, "total_bytes": log.Len()},
		"last_build": map[string]any{"started_at": 1000, "finished_at": 61000, "duration_ms": 60000},
	}})
	text := environmentWatchMessage(pebblestore.V3EnvironmentWatchRecord{WorkspacePath: "/srv/app"}, environmentOutcomeFrom(raw), nil)
	body := strings.SplitN(text, "```\n", 2)
	if len(body) != 2 || strings.Count(body[1], "step output") != EnvironmentWatchLogLines-1 || !strings.Contains(body[1], "warm-build FAILED") {
		t.Fatalf("log tail: %s", text)
	}
	for _, want := range []string{"[Environment build ended: failed]", "Duration: 1m0s", "Error: setup command 2 failed", "Last 40 lines", "log_offset"} {
		if !strings.Contains(text, want) {
			t.Fatalf("message lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "Image:") {
		t.Fatalf("a failed build names an image: %s", text)
	}
}
