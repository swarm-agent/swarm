package run

import "testing"

func TestTaskLaunchProgressDurationsRunningUsesStoredValuesOnly(t *testing.T) {
	launch := taskLaunchOutcome{
		LaunchStartedAtMS:  1,
		CurrentTool:        "search",
		CurrentToolStarted: 1,
	}

	elapsedMS, currentToolMS := taskLaunchProgressDurations(launch, false)
	if elapsedMS != 0 {
		t.Fatalf("elapsedMS = %d, want 0", elapsedMS)
	}
	if currentToolMS != 0 {
		t.Fatalf("currentToolMS = %d, want 0", currentToolMS)
	}
}

func TestBuildTaskStreamPatchPayloadRunningKeepsTimerAnchorsWithoutRecomputedDurations(t *testing.T) {
	payload := buildTaskStreamPatchPayload("parent", "call-task", "spawn", "inspect timers", 1, taskLaunchOutcome{
		LaunchIndex:        1,
		ResolvedSubagent:   "explorer",
		LaunchStartedAtMS:  123000,
		CurrentTool:        "search",
		CurrentToolStarted: 124000,
	}, "tool.delta", "")

	launch, ok := payload["launch"].(map[string]any)
	if !ok {
		t.Fatalf("launch = %#v, want launch patch map", payload["launch"])
	}
	if got := launch["launch_started_at_ms"]; got != int64(123000) {
		t.Fatalf("launch_started_at_ms = %#v, want 123000", got)
	}
	if got := launch["current_tool_started_at_ms"]; got != int64(124000) {
		t.Fatalf("current_tool_started_at_ms = %#v, want 124000", got)
	}
	if got := launch["elapsed_ms"]; got != int64(0) {
		t.Fatalf("elapsed_ms = %#v, want 0 for running stream payload", got)
	}
	if got := launch["current_tool_ms"]; got != int64(0) {
		t.Fatalf("current_tool_ms = %#v, want 0 for running stream payload", got)
	}
}

func TestBuildTaskStreamPatchPayloadTerminalIncludesFinalElapsed(t *testing.T) {
	payload := buildTaskStreamPatchPayload("parent", "call-task", "spawn", "inspect timers", 1, taskLaunchOutcome{
		LaunchIndex:      1,
		ResolvedSubagent: "explorer",
		ElapsedMS:        3400,
	}, "completed", "done")

	launch, ok := payload["launch"].(map[string]any)
	if !ok {
		t.Fatalf("launch = %#v, want launch patch map", payload["launch"])
	}
	if got := launch["elapsed_ms"]; got != int64(3400) {
		t.Fatalf("elapsed_ms = %#v, want 3400", got)
	}
}
