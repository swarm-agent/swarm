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

func TestToolPresentationPlanLifecycleUpdateDoesNotRepeatPlanGoal(t *testing.T) {
	goal := "Your AI Command Center Terminal, desktop, mobile. One durable AI server."
	payload := `{"tool":"plan_manage","action":"complete_checkpoint","checkpoint_id":"cp-1","status":"ok","plan":{"title":"Fix Header Astro parse error","status":"approved","update_kind":"complete_checkpoint","update_scope":"cp-1","document":{"title":"Fix Header Astro parse error","status":"approved","info":{"goal":"` + goal + `"},"checkpoints":[{"id":"cp-1","title":"Fix Header Astro parse error","status":"completed"}]}}}`
	presentation := buildToolPresentation(ToolTimelineItem{Name: "plan_manage", Output: payload})
	if presentation.Summary != "plan complete checkpoint · 1 checkpoint · approved" {
		t.Fatalf("plan presentation summary = %q", presentation.Summary)
	}
	joined := presentationText(presentation)
	if strings.Contains(joined, goal) {
		t.Fatalf("lifecycle card repeated the full plan goal:\n%s", joined)
	}
	if !strings.Contains(joined, "Checkpoint completed") {
		t.Fatalf("lifecycle card missing update-specific summary:\n%s", joined)
	}
}

func TestToolPresentationPlanLifecycleUpdatePrefersUpdateSummary(t *testing.T) {
	payload := `{"tool":"plan_manage","action":"mark_blocked","checkpoint_id":"cp-2","status":"ok","plan":{"title":"Release plan","update_summary":"Waiting for deployment credentials","update_kind":"mark_blocked","update_scope":"cp-2","document":{"title":"Release plan","info":{"goal":"Ship the release across all supported platforms."},"checkpoints":[{"id":"cp-1","title":"Build","status":"completed"},{"id":"cp-2","title":"Deploy","status":"blocked"}]}}}`
	presentation := buildToolPresentation(ToolTimelineItem{Name: "plan_manage", Output: payload})
	joined := presentationText(presentation)
	if !strings.Contains(joined, "Waiting for deployment credentials") {
		t.Fatalf("lifecycle card missing persisted update summary:\n%s", joined)
	}
	if strings.Contains(joined, "Ship the release across all supported platforms.") {
		t.Fatalf("lifecycle card repeated the plan goal:\n%s", joined)
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

func TestProviderToolStartPresentationsUseSpecializedActivityCopy(t *testing.T) {
	cases := []struct {
		name string
		tool ToolTimelineItem
		want string
	}{
		{name: "edit", tool: ToolTimelineItem{Name: "edit", Status: "constructing"}, want: "editing…"},
		{name: "plan", tool: ToolTimelineItem{Name: "plan_manage", Status: "constructing"}, want: "planning…"},
		{name: "task", tool: ToolTimelineItem{Name: "task", Status: "constructing"}, want: "launching subagents…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			presentation := buildToolPresentation(tc.tool)
			if presentation.Summary != tc.want {
				t.Fatalf("activity summary = %q, want %q", presentation.Summary, tc.want)
			}
			rows := NewPage(nil, testPageStyles()).renderToolRows(tc.tool, 60, testPageStyles())
			var rendered strings.Builder
			for _, row := range rows {
				rendered.WriteString(row.text)
				rendered.WriteByte('\n')
			}
			if text := rendered.String(); !strings.Contains(strings.ToLower(text), strings.ToLower(tc.want)) {
				t.Fatalf("activity rows missing %q:\n%s", tc.want, text)
			}
		})
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
	if len(presentation.Lines) != maxBashPresentationLines+3 {
		t.Fatalf("bash line count = %d, want command + omission + %d output lines + command hint", len(presentation.Lines), maxBashPresentationLines)
	}
	joined := presentationText(presentation)
	if !strings.Contains(joined, "$ make build") || !strings.Contains(joined, "… 22 earlier lines") || !strings.Contains(joined, "line 30") || !strings.Contains(joined, "use /output to open full output") || strings.Contains(joined, "line 1\n") {
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
	if !strings.Contains(rows[len(rows)-2].text, "use /output") {
		t.Fatalf("bounded bash output missing /output command hint: %#v", rows)
	}
}

func TestToggleLatestBashOutputOpensFullLiveOutput(t *testing.T) {
	store := NewStore()
	state := NewState()
	state.Tools["bash-new"] = ToolTimelineItem{ID: "bash-new", CallID: "bash-new", GlobalSeq: 9, Name: "bash", Status: "running", Output: "new line 1\nnew line 2\n"}
	state.Tools["bash-old"] = ToolTimelineItem{ID: "bash-old", CallID: "bash-old", GlobalSeq: 4, Name: "bash", Status: "completed", Output: "old output"}
	store.mu.Lock()
	store.state = state
	store.mu.Unlock()
	page := NewPage(NewRuntime(nil, store, nil), testPageStyles())

	if !page.ToggleLatestBashOutput() {
		t.Fatal("expected /output to open the latest Bash output")
	}
	if !page.bashOutputModal || page.bashOutputModalTool.CallID != "bash-new" {
		t.Fatalf("bash output modal = %#v", page.bashOutputModalTool)
	}
	if got := bashToolOutputText(page.bashOutputModalTool); got != "new line 1\nnew line 2\n" {
		t.Fatalf("full Bash output = %q", got)
	}
	if !page.ToggleLatestBashOutput() || page.bashOutputModal {
		t.Fatal("second /output command should close the Bash output modal")
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
	firstPatch := `{"tool":"task","path_id":"tool.task.stream.v2","launch_count":2,"launch_key":"child-1","launch":{"launch_index":1,"child_session_id":"child-1","subagent":"finder","assignment_label":"Map backend","status":"running","current_tool":"search","current_tool_display":"search x3","current_preview_kind":"assistant","current_preview_text":"SECRET CHILD RESPONSE"}}`
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
	for _, want := range []string{"SUBAGENT STREAM", "Map backend", "@finder", "current: search x3", "Implement TUI", "@coder", "current: edit"} {
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

func TestTaskSwarmRendersHeightAwareMatrixWithoutChangingRegularTasks(t *testing.T) {
	launches := make([]map[string]any, 0, 100)
	for index := 1; index <= 100; index++ {
		status := "running"
		if index%4 == 0 {
			status = "ok"
		}
		launches = append(launches, map[string]any{
			"launch_index":     index,
			"subagent":         "coder",
			"assignment_label": fmt.Sprintf("Epic agent %d", index),
			"status":           status,
		})
	}
	payload, err := json.Marshal(map[string]any{"tool": "task", "launch_count": len(launches), "launches": launches})
	if err != nil {
		t.Fatal(err)
	}
	swarm := ToolTimelineItem{Name: "task", Arguments: `{"mode":"swarm","count":100}`, Output: string(payload), Status: "running"}
	presentation := buildToolPresentation(swarm)
	if !presentation.TaskSwarm || presentation.TaskSwarmStrategy != "explore" || !strings.HasPrefix(presentation.Summary, "Explore Swarm") {
		t.Fatalf("explicit swarm presentation = %#v", presentation)
	}

	page := NewPage(nil, testPageStyles())
	shortRows := page.renderToolRowsForHeight(swarm, 80, 12, testPageStyles())
	tallRows := page.renderToolRowsForHeight(swarm, 80, 36, testPageStyles())
	shortText := renderTaskPresentationRowsText(shortRows)
	if !strings.Contains(shortText, "EXPLORE SWARM") || !strings.Contains(shortText, "independent alternatives") || !strings.Contains(shortText, "100 AGENTS") || !strings.Contains(shortText, "showing 100/100 agents") {
		t.Fatalf("short swarm matrix missing dashboard details:\n%s", shortText)
	}
	for name, rows := range map[string][]renderRow{"short": shortRows, "tall": tallRows} {
		text := renderTaskPresentationRowsText(rows)
		if !strings.Contains(text, "showing 100/100 agents") || !strings.Contains(text, "✓100") {
			t.Fatalf("%s 100-agent swarm matrix truncated the final agent:\n%s", name, text)
		}
	}
	for _, row := range shortRows {
		if displayWidth(row.text) > 80 {
			t.Fatalf("swarm matrix row exceeds width: %q", row.text)
		}
	}

	regular := ToolTimelineItem{Name: "task", Arguments: `{"mode":"regular"}`, Output: string(payload), Status: "running"}
	regularPresentation := buildToolPresentation(regular)
	if regularPresentation.TaskSwarm {
		t.Fatal("regular task wave must not become swarm mode by count")
	}
	regularText := renderTaskPresentationRowsText(page.renderToolRowsForHeight(regular, 80, 12, testPageStyles()))
	if strings.Contains(regularText, "EXPLORE SWARM") || strings.Contains(regularText, "ASSEMBLY SWARM") || !strings.Contains(regularText, "SUBAGENT STREAM") {
		t.Fatalf("regular task rendering changed:\n%s", regularText)
	}
}

func TestAssemblySwarmShowsPartsAndPendingParentIntegration(t *testing.T) {
	payload := `{"tool":"task","path_id":"tool.task.v1","task_mode":"swarm","swarm_strategy":"assembly","integration_contract":"Combine committed parts into the parent deliverable.","integration_required":true,"integration_status":"pending_parent_assembly","ready_for_dependent_work":false,"launch_count":1,"launches":[{"launch_index":1,"swarm_mode":true,"swarm_strategy":"assembly","assembly_part":{"name":"Backend API","owned_scope":["swarmd/internal/api/**"]},"integration_contract":"Combine committed parts into the parent deliverable.","integration_required":true,"subagent":"coder","status":"ok"}]}`
	tool := ToolTimelineItem{Name: "task", Arguments: `{"mode":"swarm","swarm_strategy":"assembly"}`, Output: payload, Status: "completed"}
	presentation := buildToolPresentation(tool)
	if presentation.TaskSwarmStrategy != "assembly" || presentation.TaskRows[0].Title != "Backend API" || !presentation.TaskIntegrationRequired {
		t.Fatalf("Assembly presentation = %#v", presentation)
	}
	text := renderTaskPresentationRowsText(NewPage(nil, testPageStyles()).renderToolRowsForHeight(tool, 100, 18, testPageStyles()))
	for _, want := range []string{"ASSEMBLY SWARM", "complementary parts", "parent integration required", "contract: Combine committed parts", "Backend API"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Assembly rendering missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ready for dependent work") {
		t.Fatalf("Assembly children falsely imply integrated parent work:\n%s", text)
	}
}

func TestIdeaSwarmUsesSharedModelHeaderAndGenericAgentLabels(t *testing.T) {
	launches := []map[string]any{
		{"launch_index": 1, "subagent": "idea", "assignment_label": "Idea swarm 1", "subagent_provider": "codex", "subagent_model": "gpt-5.6-sol", "status": "running"},
		{"launch_index": 2, "subagent": "idea", "assignment_label": "Idea swarm 2", "subagent_provider": "codex", "subagent_model": "gpt-5.6-sol", "status": "ok"},
	}
	payload, err := json.Marshal(map[string]any{"tool": "task", "task_mode": "swarm", "launch_count": len(launches), "launches": launches})
	if err != nil {
		t.Fatal(err)
	}
	tool := ToolTimelineItem{Name: "task", Arguments: `{"mode":"swarm","agent_type":"idea"}`, Output: string(payload), Status: "running"}
	presentation := buildToolPresentation(tool)
	if presentation.TaskSwarmAgent != "idea" || presentation.TaskSwarmModel != "codex/gpt-5.6-sol" {
		t.Fatalf("Idea swarm metadata = %#v", presentation)
	}
	text := renderTaskPresentationRowsText(NewPage(nil, testPageStyles()).renderToolRowsForHeight(tool, 96, 16, testPageStyles()))
	for _, want := range []string{"IDEA SWARM", "codex/gpt-5.6-sol", "Agent #1", "Agent #2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Idea swarm rendering missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Idea swarm 1") || strings.Contains(text, "Idea swarm 2") {
		t.Fatalf("Idea implementation labels leaked into rows:\n%s", text)
	}
}

func TestTaskSwarmCellKeepsTitleBeforeRightAlignedActivity(t *testing.T) {
	layout := taskSwarmRenderLayout{cellWidth: 36, density: "detail"}
	cell, _ := taskSwarmCell(taskPresentationRow{Index: 1, Status: "running", Title: "Long hydrated worker title", Agent: "coder", Tool: "search x3"}, layout, testPageStyles())
	if displayWidth(cell) != layout.cellWidth || !strings.Contains(cell, "Long hydrated") || !strings.HasSuffix(strings.TrimSpace(cell), "search x3") {
		t.Fatalf("swarm cell did not preserve title and right-side activity: %q", cell)
	}
}

func renderTaskPresentationRowsText(rows []renderRow) string {
	var rendered strings.Builder
	for _, row := range rows {
		rendered.WriteString(row.text)
		rendered.WriteByte('\n')
	}
	return rendered.String()
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
	tool := ToolTimelineItem{Name: "task", Status: "completed", Output: `{"tool":"task","path_id":"tool.task.v1","launch_count":1,"launches":[{"launch_index":1,"subagent":"finder","assignment_label":"Map backend","status":"ok","elapsed_ms":3400,"report":"SECRET FULL REPORT","report_excerpt":"SECRET EXCERPT"}]}`}
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
