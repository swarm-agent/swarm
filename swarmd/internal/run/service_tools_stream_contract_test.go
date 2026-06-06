package run

import "testing"

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
