package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
