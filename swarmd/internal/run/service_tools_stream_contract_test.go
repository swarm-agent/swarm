package run

import (
	"encoding/json"
	"testing"
)

func TestBuildTaskStreamPatchPayloadDesktopSubagentSchema(t *testing.T) {
	payload := buildTaskStreamPatchPayload("parent-session", "call-task", "spawn", "map repo", 3, taskLaunchOutcome{
		LaunchIndex:         2,
		RequestedSubagent:   "finder",
		ResolvedSubagent:    "finder-v2",
		MetaPrompt:          "map backend files",
		AssignmentLabel:     "Backend map",
		SubagentProvider:    "test-provider",
		SubagentModel:       "test-model",
		ChildSessionID:      "child-session-2",
		ChildMode:           "auto",
		WorkspacePath:       "/workspace/project",
		WorkspaceName:       "project",
		WorktreeEnabled:     true,
		WorktreeRootPath:    "/workspace/project",
		WorktreeBranch:      "agent/child-session-2",
		LaunchStartedAtMS:   123000,
		CurrentTool:         "search",
		CurrentToolIdentity: "search",
		CurrentToolRunCount: 3,
		CurrentToolDisplay:  "search x3",
		CurrentToolStarted:  124000,
		CurrentPreviewKind:  "tool",
		CurrentPreviewText:  "matched service_tools.go",
		ToolStarted:         1,
		ToolCompleted:       0,
		ToolFailed:          0,
		ToolOrder:           []string{"search"},
	}, "tool.delta", "")

	wantTop := map[string]any{
		"tool":              "task",
		"action":            "spawn",
		"status":            "running",
		"phase":             "tool.delta",
		"launch_count":      3,
		"description":       "map repo",
		"goal":              "map repo",
		"parent_session_id": "parent-session",
		"task_call_id":      "call-task",
		"path_id":           "tool.task.stream.v2",
		"stream_version":    2,
		"event":             "launch.patch",
		"launch_index":      2,
		"launch_key":        "child-session-2",
		"child_session_id":  "child-session-2",
		"details_truncated": false,
	}
	for key, want := range wantTop {
		if got := payload[key]; got != want {
			t.Fatalf("payload[%q] = %#v, want %#v", key, got, want)
		}
	}
	if got := payload["summary"]; got != "subagent finder-v2 running" {
		t.Fatalf("summary = %#v, want default running summary", got)
	}
	if _, ok := payload["launches"]; ok {
		t.Fatalf("payload includes aggregate launches snapshot: %#v", payload["launches"])
	}

	launch, ok := payload["launch"].(map[string]any)
	if !ok {
		t.Fatalf("launch = %#v, want launch patch map", payload["launch"])
	}
	wantLaunch := map[string]any{
		"launch_index":               2,
		"launch_key":                 "child-session-2",
		"status":                     "running",
		"phase":                      "tool.delta",
		"requested_subagent":         "finder",
		"subagent":                   "finder-v2",
		"agent_type":                 "finder-v2",
		"assignment_label":           "Backend map",
		"subagent_provider":          "test-provider",
		"subagent_model":             "test-model",
		"child_session_id":           "child-session-2",
		"child_mode":                 "auto",
		"launch_started_at_ms":       int64(123000),
		"current_tool":               "search",
		"current_tool_identity":      "search",
		"current_tool_run_count":     3,
		"current_tool_display":       "search x3",
		"current_tool_started_at_ms": int64(124000),
		"current_tool_ms":            int64(0),
		"elapsed_ms":                 int64(0),
		"tool_started":               1,
		"tool_completed":             0,
		"tool_failed":                0,
		"terminal":                   false,
	}
	for key, want := range wantLaunch {
		if got := launch[key]; got != want {
			t.Fatalf("launch[%q] = %#v, want %#v", key, got, want)
		}
	}
	for _, forbidden := range []string{"meta_prompt", "workspace_name", "worktree_enabled", "worktree_root_path", "tool_order", "current_preview_kind", "current_preview_text"} {
		if _, ok := launch[forbidden]; ok {
			t.Fatalf("launch patch includes forbidden field %q: %#v", forbidden, launch[forbidden])
		}
	}
}

func TestToolProgressionStateGroupsNormalizedConsecutiveToolsAndResets(t *testing.T) {
	state := &ToolProgressionState{}
	got := []ToolProgression{
		state.Observe("read"),
		state.Observe("READ"),
		state.Observe(" read "),
		state.Observe("search"),
		state.Observe("search"),
	}
	wantDisplays := []string{"read", "read x2", "read x3", "search", "search x2"}
	for i, want := range wantDisplays {
		if got[i].Display != want {
			t.Fatalf("progression[%d].Display = %q, want %q", i, got[i].Display, want)
		}
	}
	if got[3].RunCount != 1 || got[3].Identity != "search" {
		t.Fatalf("reset progression = %#v, want search count 1", got[3])
	}
}

func TestTaskLaunchProgressionPersistsAcrossCompletionUntilNextStart(t *testing.T) {
	outcome := taskLaunchOutcome{}
	for _, event := range []StreamEvent{
		{Type: StreamEventToolStarted, ToolName: "read"},
		{Type: StreamEventToolCompleted, ToolName: "read"},
		{Type: StreamEventToolStarted, ToolName: "READ"},
	} {
		if event.Type == StreamEventToolStarted {
			progression := providerToolProgressionFromEvent(event, outcome)
			outcome.CurrentTool = emptyToolName(event.ToolName)
			outcome.CurrentToolIdentity = progression.Identity
			outcome.CurrentToolRunCount = progression.RunCount
			outcome.CurrentToolDisplay = progression.Display
		}
		if outcome.CurrentTool == "" {
			t.Fatalf("current tool cleared at lifecycle boundary %q", event.Type)
		}
	}
	if outcome.CurrentToolDisplay != "read x2" || outcome.CurrentToolRunCount != 2 {
		t.Fatalf("progression = %#v, want read x2", outcome)
	}
}

func TestBuildTaskStreamPatchPayloadAcknowledgesCancelledChild(t *testing.T) {
	payload := buildTaskStreamPatchPayload("parent", "call-task", "spawn", "inspect", 2, taskLaunchOutcome{
		LaunchIndex:      2,
		ResolvedSubagent: "finder",
		ChildSessionID:   "child-cancelled",
		Phase:            "cancelled",
		Summary:          "launch 2 subagent finder cancelled (session child-cancelled): user stopped subagent",
		Reason:           "user stopped subagent",
	}, "cancelled", "")

	if got := payload["status"]; got != "error" {
		t.Fatalf("status = %#v, want error", got)
	}
	if got := payload["phase"]; got != "cancelled" {
		t.Fatalf("phase = %#v, want cancelled", got)
	}
	launch, ok := payload["launch"].(map[string]any)
	if !ok {
		t.Fatalf("launch = %#v, want object", payload["launch"])
	}
	if got := launch["child_session_id"]; got != "child-cancelled" {
		t.Fatalf("child_session_id = %#v, want child-cancelled", got)
	}
	if got := launch["phase"]; got != "cancelled" {
		t.Fatalf("launch phase = %#v, want cancelled", got)
	}
	if got := launch["reason"]; got != "user stopped subagent" {
		t.Fatalf("reason = %#v, want user stopped subagent", got)
	}
	if got := launch["error"]; got != "" {
		t.Fatalf("error = %#v, want empty cancellation error", got)
	}
	if got := launch["terminal"]; got != true {
		t.Fatalf("terminal = %#v, want true", got)
	}
}

func TestTaskLaunchOutcomePayloadDistinguishesSuccessFailureAndCancellation(t *testing.T) {
	launches := []struct {
		outcome taskLaunchOutcome
		status  string
		phase   string
	}{
		{outcome: taskLaunchOutcome{LaunchIndex: 1, ChildSessionID: "child-ok", Summary: "completed"}, status: "ok", phase: "completed"},
		{outcome: taskLaunchOutcome{LaunchIndex: 2, ChildSessionID: "child-failed", Summary: "failed", Error: "provider unavailable", Reason: "provider unavailable"}, status: "error", phase: "failed"},
		{outcome: taskLaunchOutcome{LaunchIndex: 3, ChildSessionID: "child-cancelled", Summary: "cancelled", Reason: "user stopped subagent"}, status: "cancelled", phase: "cancelled"},
	}

	for _, tc := range launches {
		row := buildTaskStreamLaunchPayload(tc.outcome, tc.status, tc.phase, true)
		if got := row["child_session_id"]; got != tc.outcome.ChildSessionID {
			t.Fatalf("child_session_id = %#v, want %q", got, tc.outcome.ChildSessionID)
		}
		if got := row["status"]; got != tc.status {
			t.Fatalf("session %s status = %#v, want %q", tc.outcome.ChildSessionID, got, tc.status)
		}
		if got := row["phase"]; got != tc.phase {
			t.Fatalf("session %s phase = %#v, want %q", tc.outcome.ChildSessionID, got, tc.phase)
		}
		if got := row["reason"]; got != tc.outcome.Reason {
			t.Fatalf("session %s reason = %#v, want %q", tc.outcome.ChildSessionID, got, tc.outcome.Reason)
		}
	}
}

func TestBuildTaskStreamPatchPayloadDoesNotExposeAssistantOrReasoningPreviewText(t *testing.T) {
	tests := []struct {
		name string
		kind string
	}{
		{name: "assistant", kind: "assistant"},
		{name: "reasoning", kind: "reasoning"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := buildTaskStreamPatchPayload("parent", "call-task", "spawn", "inspect", 1, taskLaunchOutcome{
				LaunchIndex:        1,
				ResolvedSubagent:   "finder",
				ChildSessionID:     "child-session-1",
				CurrentPreviewKind: tc.kind,
				CurrentPreviewText: "private model text",
			}, tc.kind+".delta", "")

			launch, ok := payload["launch"].(map[string]any)
			if !ok {
				t.Fatalf("launch = %#v, want launch patch map", payload["launch"])
			}
			if _, ok := launch["current_preview_kind"]; ok {
				t.Fatalf("current_preview_kind leaked into parent patch: %#v", launch["current_preview_kind"])
			}
			if _, ok := launch["current_preview_text"]; ok {
				t.Fatalf("current_preview_text leaked into parent patch: %#v", launch["current_preview_text"])
			}
		})
	}
}

func TestEmitTaskStreamDeltaEmitsSingleLaunchPatchNotAggregate(t *testing.T) {
	var outputs []string
	emit := func(event StreamEvent) {
		if event.Type != StreamEventToolDelta {
			t.Fatalf("event type = %q, want %q", event.Type, StreamEventToolDelta)
		}
		outputs = append(outputs, event.Output)
	}
	first := taskLaunchOutcome{
		LaunchIndex:      1,
		ResolvedSubagent: "finder",
		ChildSessionID:   "child-1",
		Phase:            "completed",
		ToolStarted:      2,
		ToolCompleted:    2,
		ReportRef: &taskReportRef{
			SessionID: "child-1",
			MessageID: "msg-child-1",
			GlobalSeq: 12,
			Role:      "assistant",
			Source:    "child_session_transcript",
		},
		Summary: "backend mapped",
	}
	second := taskLaunchOutcome{
		LaunchIndex:        2,
		ResolvedSubagent:   "parallel",
		ChildSessionID:     "child-2",
		Phase:              "assistant.delta",
		CurrentPreviewKind: "assistant",
		CurrentPreviewText: "private child assistant text",
		ToolStarted:        1,
	}

	emitTaskStreamDelta("parent", emit, 3, "task", "call-task", "spawn", "map repo", 2, first, "completed", "backend mapped")
	emitTaskStreamDelta("parent", emit, 3, "task", "call-task", "spawn", "map repo", 2, second, "assistant.delta", "")

	if len(outputs) != 2 {
		t.Fatalf("outputs = %d, want 2", len(outputs))
	}
	var decoded []map[string]any
	for _, output := range outputs {
		var payload map[string]any
		if err := json.Unmarshal([]byte(output), &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if got := payload["path_id"]; got != "tool.task.stream.v2" {
			t.Fatalf("path_id = %#v, want tool.task.stream.v2", got)
		}
		if _, ok := payload["launches"]; ok {
			t.Fatalf("payload includes aggregate launches snapshot: %#v", payload["launches"])
		}
		if _, ok := payload["launch"].(map[string]any); !ok {
			t.Fatalf("launch = %#v, want launch patch object", payload["launch"])
		}
		decoded = append(decoded, payload)
	}

	firstPatch := decoded[0]["launch"].(map[string]any)
	if got := firstPatch["child_session_id"]; got != "child-1" {
		t.Fatalf("first child_session_id = %#v, want child-1", got)
	}
	if got := firstPatch["status"]; got != "ok" {
		t.Fatalf("first status = %#v, want ok", got)
	}
	if got := firstPatch["report_persisted"]; got != true {
		t.Fatalf("first report_persisted = %#v, want true", got)
	}
	reportRef, ok := firstPatch["report_ref"].(map[string]any)
	if !ok {
		t.Fatalf("first report_ref = %#v, want object", firstPatch["report_ref"])
	}
	if got := reportRef["source"]; got != "child_session_transcript" {
		t.Fatalf("report_ref.source = %#v, want child_session_transcript", got)
	}
	secondPatch := decoded[1]["launch"].(map[string]any)
	if got := secondPatch["child_session_id"]; got != "child-2" {
		t.Fatalf("second child_session_id = %#v, want child-2", got)
	}
	if _, ok := secondPatch["current_preview_text"]; ok {
		t.Fatalf("assistant preview leaked into parent progress: %#v", secondPatch["current_preview_text"])
	}
}
