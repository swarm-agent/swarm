package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolPresentationReadUsesHumanSummaryWithoutRawPayload(t *testing.T) {
	tool := ToolTimelineItem{
		Name:      "read",
		Arguments: `{"path":"README.md","line_start":10,"max_lines":20}`,
		Output:    `{"path":"README.md","line_start":10,"count":3,"bytes":42,"truncated":false,"lines":[{"line":10,"text":"one"}]}`,
	}
	presentation := buildToolPresentation(tool)
	if presentation.Summary != "read README.md · lines 10–12 · 42 B" {
		t.Fatalf("summary = %q", presentation.Summary)
	}
	if len(presentation.Lines) != 0 {
		t.Fatalf("read should not dump file contents into timeline: %#v", presentation.Lines)
	}
}

func TestToolPresentationSearchGroupsFilesAndLineMatches(t *testing.T) {
	tool := ToolTimelineItem{
		Name:      "search",
		Arguments: `{"query":"ToolCall","path":"internal/ui"}`,
		Output: `{
			"search_mode":"content","count":4,"total_matched":4,
			"results":[
				{"path":"internal/ui/a.go","items":[{"line":10,"text":"first"},{"line":20,"text":"second"}]},
				{"path":"internal/ui/b.go","items":[{"line":7,"text":"third"},{"line":8,"text":"fourth"}]}
			]
		}`,
	}
	presentation := buildToolPresentation(tool)
	if !strings.Contains(presentation.Summary, `search "ToolCall"`) || !strings.Contains(presentation.Summary, "4 matches") || !strings.Contains(presentation.Summary, "2 files") {
		t.Fatalf("summary = %q", presentation.Summary)
	}
	joined := presentationText(presentation)
	for _, want := range []string{"internal/ui/a.go · lines 10, 20 · 2 matches", "internal/ui/b.go · lines 7, 8 · 2 matches"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("presentation missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "first") || strings.Contains(joined, "second") {
		t.Fatalf("search presentation should not dump matched code:\n%s", joined)
	}
}

func TestToolPresentationPlanManageUsesStructuredCardSummaryWithoutJSON(t *testing.T) {
	payload := `{"path_id":"tool.plan-new-request.v1","document_operation":"request_new_plan","title":"Two-step completion plan","document":{"id":"plan-1","title":"Two-step completion plan","info":{"goal":"Finish the target work end-to-end."},"checkpoints":[{"id":"cp-1","title":"Verify","status":"pending"},{"id":"cp-2","title":"Finish","status":"pending"}]}}`
	presentation := buildToolPresentation(ToolTimelineItem{Name: "plan_manage", Output: payload})
	if presentation.Kind != "plan" || presentation.Summary != "plan request new plan · 2 checkpoints" {
		t.Fatalf("plan presentation = %#v", presentation)
	}
	joined := presentationText(presentation)
	for _, want := range []string{"Two-step completion plan", "Finish the target work end-to-end."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("plan presentation missing %q:\n%s", want, joined)
		}
	}
	for _, raw := range []string{"path_id", "document_operation", `"checkpoints"`, "acceptance_criteria"} {
		if strings.Contains(joined, raw) {
			t.Fatalf("plan presentation leaked raw payload key %q:\n%s", raw, joined)
		}
	}
}

func TestToolPresentationPlanManageUnwrapsDurableHistoryEnvelope(t *testing.T) {
	planPayload := `{"tool":"plan_manage","action":"save","plan":{"title":"Envelope plan","document":{"title":"Envelope plan","info":{"goal":"Keep the complete document"},"checkpoints":[{"id":"cp-1","title":"One"}]}}}`
	envelope, err := json.Marshal(map[string]any{"path_id": "run.tool-history.v2", "tool": "plan_manage", "completed_output": planPayload})
	if err != nil {
		t.Fatal(err)
	}
	presentation := buildToolPresentation(ToolTimelineItem{Name: "plan_manage", Output: string(envelope)})
	if presentation.Kind != "plan" || !strings.Contains(presentationText(presentation), "Envelope plan") || strings.Contains(presentationText(presentation), "completed_output") {
		t.Fatalf("durable plan envelope presentation = %#v\n%s", presentation, presentationText(presentation))
	}
}

func TestToolPresentationEditShowsBoundedDiff(t *testing.T) {
	tool := ToolTimelineItem{
		Name:   "edit",
		Output: `{"path":"main.go","replacements":1,"old_string_preview":"old line","new_string_preview":"new line"}`,
	}
	presentation := buildToolPresentation(tool)
	if presentation.Summary != "edit main.go · 1 replacement" {
		t.Fatalf("summary = %q", presentation.Summary)
	}
	if got := presentationText(presentation); !strings.Contains(got, "− old line") || !strings.Contains(got, "+ new line") {
		t.Fatalf("edit presentation = %q", got)
	}
}

func TestToolPresentationBashKeepsTailWithinViewportBudget(t *testing.T) {
	var output strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&output, "line %d\n", i)
	}
	payload, _ := json.Marshal(map[string]any{"command": "make build", "exit_code": 0, "output": output.String()})
	presentation := buildToolPresentation(ToolTimelineItem{Name: "bash", Arguments: `{"command":"make build"}`, Output: string(payload)})
	if presentation.Summary != "bash · exit 0" {
		t.Fatalf("summary = %q", presentation.Summary)
	}
	if len(presentation.Lines) != maxBashPresentationLines+2 {
		t.Fatalf("bash line count = %d, want command + omission + %d output lines", len(presentation.Lines), maxBashPresentationLines)
	}
	joined := presentationText(presentation)
	if !strings.Contains(joined, "$ make build") || !strings.Contains(joined, "… 22 earlier lines") || !strings.Contains(joined, "line 30") || strings.Contains(joined, "line 1\n") {
		t.Fatalf("bash presentation did not retain a bounded live tail:\n%s", joined)
	}
}

func TestToolRowsBoundWrappedBashOutput(t *testing.T) {
	page := NewPage(nil, testPageStyles())
	tool := ToolTimelineItem{Name: "bash", Status: "running", Arguments: `{"command":"printf long"}`, Output: strings.Repeat("long output ", 300)}
	rows := page.renderToolRows(tool, 24, testPageStyles())
	if len(rows) > 12 {
		t.Fatalf("bash rows = %d, want header + at most 10 body rows + spacer", len(rows))
	}
	if !strings.Contains(rows[len(rows)-2].text, "output clipped") {
		t.Fatalf("bounded bash output missing clipped marker: %#v", rows)
	}
}

func TestToolDeltaOutputAppendsVerbatimForRealtimeBash(t *testing.T) {
	state := NewState()
	state.Session.ID = "s"
	start, _ := json.Marshal(map[string]any{"call_id": "bash-1", "tool_name": "bash", "arguments": `{"command":"printf hi"}`})
	state = applyToolEvent(state, clientSessionV3Event{Seq: 1, EventType: "session.tool.started"}, rawToolPayload(t, start))
	first, _ := json.Marshal(map[string]any{"call_id": "bash-1", "tool_name": "bash", "output": "first\n"})
	second, _ := json.Marshal(map[string]any{"call_id": "bash-1", "tool_name": "bash", "output": "second\n"})
	state = applyToolEvent(state, clientSessionV3Event{Seq: 2, EventType: "session.tool.delta"}, rawToolPayload(t, first))
	state = applyToolEvent(state, clientSessionV3Event{Seq: 3, EventType: "session.tool.delta"}, rawToolPayload(t, second))
	if got := state.Tools["bash-1"].Output; got != "first\nsecond\n" {
		t.Fatalf("live bash output = %q", got)
	}
}

func TestTaskStreamV2UsesKeyedRowsWithoutRawJSONOrReports(t *testing.T) {
	state := NewState()
	state.Session.ID = "s"
	started, _ := json.Marshal(map[string]any{"call_id": "task-1", "tool_name": "task", "arguments": `{"prompt":"inspect"}`})
	state = applyToolEvent(state, clientSessionV3Event{Seq: 1, EventType: "session.tool.started"}, rawToolPayload(t, started))
	firstPatch := `{"tool":"task","path_id":"tool.task.stream.v2","launch_count":2,"launch_key":"child-1","launch":{"launch_index":1,"child_session_id":"child-1","subagent":"explorer","assignment_label":"Map backend","status":"running","current_tool":"search","current_tool_display":"search x3","current_preview_kind":"assistant","current_preview_text":"SECRET CHILD RESPONSE"}}`
	secondPatch := `{"tool":"task","path_id":"tool.task.stream.v2","launch_count":2,"launch_key":"child-2","launch":{"launch_index":2,"child_session_id":"child-2","subagent":"coder","assignment_label":"Implement TUI","status":"running","current_tool":"edit"}}`
	for seq, patch := range []string{firstPatch, secondPatch} {
		delta, _ := json.Marshal(map[string]any{"call_id": "task-1", "tool_name": "task", "output": patch})
		state = applyToolEvent(state, clientSessionV3Event{Seq: uint64(seq + 2), EventType: "session.tool.delta"}, rawToolPayload(t, delta))
	}
	tool := state.Tools["task-1"]
	if tool.Output != "" || tool.TaskStream == nil || len(tool.TaskStream.LaunchOrder) != 2 {
		t.Fatalf("task stream state = %#v, raw output = %q", tool.TaskStream, tool.Output)
	}
	presentation := buildToolPresentation(tool)
	if presentation.Kind != "task" || len(presentation.TaskRows) != 2 {
		t.Fatalf("task presentation = %#v", presentation)
	}
	rows := NewPage(nil, testPageStyles()).renderToolRows(tool, 34, testPageStyles())
	var rendered strings.Builder
	for _, row := range rows {
		rendered.WriteString(row.text)
		rendered.WriteByte('\n')
	}
	text := rendered.String()
	for _, want := range []string{"SUBAGENT STREAM", "Map backend", "@explorer", "current: search x3", "Implement TUI", "@coder", "current: edit"} {
		if !strings.Contains(text, want) {
			t.Fatalf("task rows missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "┌") != 2 || strings.Count(text, "└") != 2 || strings.Count(text, "│") < 8 {
		t.Fatalf("task rows are not rendered as two cards:\n%s", text)
	}
	for _, row := range rows {
		if utf8.RuneCountInString(row.text) > 34 {
			t.Fatalf("narrow task card row exceeds width: %q", row.text)
		}
	}
	for _, hidden := range []string{"tool.task.stream.v2", "launch_key", "SECRET CHILD RESPONSE"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("task rows leaked %q:\n%s", hidden, text)
		}
	}
}

func TestTaskStreamV2RetainsProgressionWhenLaterPatchHasEmptyCurrentTool(t *testing.T) {
	item := ToolTimelineItem{Name: "task"}
	started := `{"tool":"task","path_id":"tool.task.stream.v2","launch_key":"child-1","launch":{"launch_index":1,"current_tool":"read","current_tool_identity":"read","current_tool_run_count":2,"current_tool_display":"read x2"}}`
	completed := `{"tool":"task","path_id":"tool.task.stream.v2","launch_key":"child-1","launch":{"launch_index":1,"status":"running","phase":"tool.completed","current_tool":""}}`
	if !applyTaskStreamPatch(&item, started) || !applyTaskStreamPatch(&item, completed) {
		t.Fatal("expected task stream patches to apply")
	}
	presentation := buildToolPresentation(item)
	if len(presentation.TaskRows) != 1 || presentation.TaskRows[0].Tool != "read x2" {
		t.Fatalf("retained progression = %#v, want read x2", presentation.TaskRows)
	}
}

func TestTaskTerminalPresentationSuppressesResolvedReportBodies(t *testing.T) {
	tool := ToolTimelineItem{Name: "task", Status: "completed", Output: `{"tool":"task","path_id":"tool.task.v1","launch_count":1,"launches":[{"launch_index":1,"subagent":"explorer","assignment_label":"Map backend","status":"ok","elapsed_ms":3400,"report":"SECRET FULL REPORT","report_excerpt":"SECRET EXCERPT"}]}`}
	presentation := buildToolPresentation(tool)
	if presentation.Kind != "task" || len(presentation.TaskRows) != 1 || presentation.TaskRows[0].Status != "done" {
		t.Fatalf("terminal task presentation = %#v", presentation)
	}
	rows := NewPage(nil, testPageStyles()).renderToolRows(tool, 80, testPageStyles())
	var rendered strings.Builder
	for _, row := range rows {
		rendered.WriteString(row.text)
		rendered.WriteByte('\n')
	}
	if text := rendered.String(); strings.Contains(text, "SECRET FULL REPORT") || strings.Contains(text, "SECRET EXCERPT") || strings.Contains(text, `"report"`) {
		t.Fatalf("terminal task rows exposed subagent response:\n%s", text)
	}
}

func rawToolPayload(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func presentationText(presentation toolPresentation) string {
	parts := []string{presentation.Summary}
	for _, line := range presentation.Lines {
		parts = append(parts, line.Text)
	}
	return strings.Join(parts, "\n")
}
