package run

import (
	"encoding/json"
	"testing"
)

func TestBuildTaskStreamPayloadDesktopSubagentSchema(t *testing.T) {
	payload := buildTaskStreamPayload("parent-session", "spawn", "map repo", 3, taskLaunchOutcome{
		LaunchIndex:        2,
		RequestedSubagent:  "explorer",
		ResolvedSubagent:   "explorer-v2",
		MetaPrompt:         "map backend files",
		AssignmentLabel:    "Backend map",
		SubagentProvider:   "test-provider",
		SubagentModel:      "test-model",
		ChildSessionID:     "child-session-2",
		ChildMode:          "auto",
		WorkspacePath:      "/workspace/project",
		WorkspaceName:      "project",
		WorktreeEnabled:    true,
		WorktreeRootPath:   "/workspace/project",
		WorktreeBranch:     "agent/child-session-2",
		LaunchStartedAtMS:  123000,
		CurrentTool:        "search",
		CurrentToolStarted: 124000,
		CurrentPreviewKind: "tool",
		CurrentPreviewText: "matched service_tools.go",
		ToolStarted:        1,
		ToolCompleted:      0,
		ToolFailed:         0,
		ToolOrder:          []string{"search"},
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
		"assignment_label":  "Backend map",
		"subagent_provider": "test-provider",
		"subagent_model":    "test-model",
		"path_id":           "tool.task.stream.v1",
		"details_truncated": false,
	}
	for key, want := range wantTop {
		if got := payload[key]; got != want {
			t.Fatalf("payload[%q] = %#v, want %#v", key, got, want)
		}
	}
	if got := payload["summary"]; got != "subagent explorer-v2 running" {
		t.Fatalf("summary = %#v, want default running summary", got)
	}

	launches, ok := payload["launches"].([]map[string]any)
	if !ok || len(launches) != 1 {
		t.Fatalf("launches = %#v, want one launch map", payload["launches"])
	}
	launch := launches[0]
	wantLaunch := map[string]any{
		"launch_index":               2,
		"status":                     "running",
		"requested_subagent":         "explorer",
		"subagent":                   "explorer-v2",
		"agent_type":                 "explorer-v2",
		"meta_prompt":                "map backend files",
		"assignment_label":           "Backend map",
		"subagent_provider":          "test-provider",
		"subagent_model":             "test-model",
		"child_session_id":           "child-session-2",
		"child_mode":                 "auto",
		"workspace_path":             "/workspace/project",
		"workspace_name":             "project",
		"worktree_enabled":           true,
		"worktree_root_path":         "/workspace/project",
		"worktree_branch":            "agent/child-session-2",
		"phase":                      "tool.delta",
		"launch_started_at_ms":       int64(123000),
		"current_tool":               "search",
		"current_tool_started_at_ms": int64(124000),
		"current_tool_ms":            int64(0),
		"current_preview_kind":       "tool",
		"current_preview_text":       "matched service_tools.go",
		"elapsed_ms":                 int64(0),
		"tool_started":               1,
		"tool_completed":             0,
		"tool_failed":                0,
	}
	for key, want := range wantLaunch {
		if got := launch[key]; got != want {
			t.Fatalf("launch[%q] = %#v, want %#v", key, got, want)
		}
	}
	toolOrder, ok := launch["tool_order"].([]string)
	if !ok || len(toolOrder) != 1 || toolOrder[0] != "search" {
		t.Fatalf("tool_order = %#v, want [search]", launch["tool_order"])
	}
}

func TestBuildTaskStreamPayloadDoesNotExposeAssistantOrReasoningPreviewText(t *testing.T) {
	tests := []struct {
		name string
		kind string
		want string
	}{
		{name: "assistant", kind: "assistant", want: "assistant"},
		{name: "reasoning", kind: "reasoning", want: "reasoning"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := buildTaskStreamPayload("parent", "spawn", "inspect", 1, taskLaunchOutcome{
				LaunchIndex:        1,
				ResolvedSubagent:   "explorer",
				ChildSessionID:     "child-session-1",
				CurrentPreviewKind: tc.kind,
				CurrentPreviewText: "private model text",
			}, tc.kind+".delta", "")

			launches, ok := payload["launches"].([]map[string]any)
			if !ok || len(launches) != 1 {
				t.Fatalf("launches = %#v, want one launch map", payload["launches"])
			}
			launch := launches[0]
			if got := launch["current_preview_kind"]; got != tc.want {
				t.Fatalf("current_preview_kind = %#v, want %#v", got, tc.want)
			}
			if got := launch["current_preview_text"]; got != "" {
				t.Fatalf("current_preview_text = %#v, want empty redacted preview", got)
			}
		})
	}
}

func TestEmitTaskStreamPayloadAggregatesIndependentLaunchProgress(t *testing.T) {
	var outputs []string
	emit := func(event StreamEvent) {
		if event.Type != StreamEventToolDelta {
			t.Fatalf("event type = %q, want %q", event.Type, StreamEventToolDelta)
		}
		outputs = append(outputs, event.Output)
	}
	launches := []taskLaunchOutcome{
		{
			LaunchIndex:       1,
			RequestedSubagent: "explorer",
			ResolvedSubagent:  "explorer",
			AssignmentLabel:   "Map backend files",
			ChildSessionID:    "child-1",
			Phase:             "completed",
			ToolStarted:       2,
			ToolCompleted:     2,
			ReportRef: &taskReportRef{
				SessionID: "child-1",
				MessageID: "msg-child-1",
				GlobalSeq: 12,
				Role:      "assistant",
				Source:    "child_session_transcript",
			},
			Summary: "backend mapped",
		},
		{
			LaunchIndex:        2,
			RequestedSubagent:  "parallel",
			ResolvedSubagent:   "parallel",
			AssignmentLabel:    "Map frontend files",
			ChildSessionID:     "child-2",
			Phase:              "assistant.delta",
			CurrentPreviewKind: "assistant",
			CurrentPreviewText: "private child assistant text",
			ToolStarted:        1,
		},
	}
	rows := make([]map[string]any, 0, len(launches))
	for _, launch := range launches {
		status := taskStreamStatusForPhase(launch.Phase)
		row := buildTaskStreamLaunchPayload(launch, status, launch.Phase, status == "ok" || status == "error")
		if launch.ReportRef != nil {
			row["report_ref"] = map[string]any{
				"session_id": launch.ReportRef.SessionID,
				"message_id": launch.ReportRef.MessageID,
				"global_seq": launch.ReportRef.GlobalSeq,
				"role":       launch.ReportRef.Role,
				"source":     launch.ReportRef.Source,
			}
			row["report_persisted"] = true
		}
		rows = append(rows, row)
	}
	payload := buildTaskStreamPayload("parent", "spawn", "map repo", len(launches), launches[1], "assistant.delta", "2 subagent launch(es) active")
	payload["launches"] = rows
	emitTaskStreamPayload(emit, 3, "task", "call-task", payload)

	if len(outputs) != 1 {
		t.Fatalf("outputs = %d, want 1", len(outputs))
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(outputs[0]), &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	decodedRows, ok := decoded["launches"].([]any)
	if !ok || len(decodedRows) != 2 {
		t.Fatalf("launches = %#v, want two rows", decoded["launches"])
	}
	first, ok := decodedRows[0].(map[string]any)
	if !ok {
		t.Fatalf("first launch row = %#v", decodedRows[0])
	}
	second, ok := decodedRows[1].(map[string]any)
	if !ok {
		t.Fatalf("second launch row = %#v", decodedRows[1])
	}
	if got := first["child_session_id"]; got != "child-1" {
		t.Fatalf("first child_session_id = %#v, want child-1", got)
	}
	if got := first["status"]; got != "ok" {
		t.Fatalf("first status = %#v, want ok", got)
	}
	if got := first["report_persisted"]; got != true {
		t.Fatalf("first report_persisted = %#v, want true", got)
	}
	reportRef, ok := first["report_ref"].(map[string]any)
	if !ok {
		t.Fatalf("first report_ref = %#v, want object", first["report_ref"])
	}
	if got := reportRef["source"]; got != "child_session_transcript" {
		t.Fatalf("report_ref.source = %#v, want child_session_transcript", got)
	}
	if got := second["child_session_id"]; got != "child-2" {
		t.Fatalf("second child_session_id = %#v, want child-2", got)
	}
	if got := second["current_preview_kind"]; got != "assistant" {
		t.Fatalf("second current_preview_kind = %#v, want assistant", got)
	}
	if got := second["current_preview_text"]; got != "" {
		t.Fatalf("assistant preview leaked into parent progress: %#v", got)
	}
}
