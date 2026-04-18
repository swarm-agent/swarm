package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestFormatUnifiedToolEntry_SingleLineOmitsCallIDAndStatus(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName:   "read",
		CallID:     "call_123",
		Output:     `{"path":"/tmp/demo.txt","line_start":20,"count":12,"bytes":84,"summary":"read /tmp/demo.txt (84 bytes)"}`,
		State:      "done",
		DurationMS: 245,
	}

	rendered := formatUnifiedToolEntry(entry)
	if strings.Contains(rendered, "call_123") {
		t.Fatalf("rendered entry leaked call id: %q", rendered)
	}
	if strings.Contains(rendered, "\n") {
		t.Fatalf("rendered entry should be single line: %q", rendered)
	}
	if !strings.Contains(rendered, "read /tmp/demo.txt") {
		t.Fatalf("rendered entry missing read summary: %q", rendered)
	}
	if !strings.Contains(rendered, "lines 20-31") {
		t.Fatalf("rendered entry missing line-range summary: %q", rendered)
	}
	if strings.Contains(rendered, "bytes") {
		t.Fatalf("rendered entry should prefer line summary over bytes: %q", rendered)
	}
	if strings.Contains(strings.ToLower(rendered), "ok") {
		t.Fatalf("rendered entry should not contain status 'ok': %q", rendered)
	}
}

func TestToolEntryDurationLabelPrefersStartedAt(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()

	entry := chatToolStreamEntry{
		ToolName:  "bash",
		State:     "running",
		StartedAt: now - 350,
		CreatedAt: now - 20,
	}
	got := page.toolEntryDurationLabel(entry)
	if got == "" || got == "0ms" {
		t.Fatalf("toolEntryDurationLabel = %q, want non-zero elapsed duration", got)
	}
}

func TestParseToolStreamEntry_ParsesDurationAndState(t *testing.T) {
	raw := "tool=bash call_id=call_9 state=error duration_ms=310 error=boom output=hello"
	entry := parseToolStreamEntry(raw, time.Now().UnixMilli())
	if entry.DurationMS != 310 {
		t.Fatalf("entry.DurationMS = %d, want 310", entry.DurationMS)
	}
	if entry.State != "error" {
		t.Fatalf("entry.State = %q, want error", entry.State)
	}
	if entry.CallID != "call_9" {
		t.Fatalf("entry.CallID = %q, want call_9", entry.CallID)
	}
}

func TestLiveToolEntries_PrefersRunningEntries(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now()
	page.busy = true
	page.runStarted = now.Add(-600 * time.Millisecond)

	page.addToolStreamEntry(chatToolStreamEntry{
		ToolName:   "read",
		State:      "done",
		CreatedAt:  now.Add(-500 * time.Millisecond).UnixMilli(),
		DurationMS: 120,
	})
	page.addToolStreamEntry(chatToolStreamEntry{
		ToolName:  "bash",
		State:     "running",
		CreatedAt: now.Add(-250 * time.Millisecond).UnixMilli(),
	})

	live := page.liveToolEntries(2)
	if len(live) != 1 {
		t.Fatalf("len(live) = %d, want 1 running entry", len(live))
	}
	if live[0].ToolName != "bash" {
		t.Fatalf("live[0].ToolName = %q, want bash", live[0].ToolName)
	}
}

func TestLiveToolEntries_NoRunningReturnsEmpty(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now()
	page.busy = true
	page.runStarted = now.Add(-600 * time.Millisecond)

	page.addToolStreamEntry(chatToolStreamEntry{
		ToolName:   "read",
		State:      "done",
		CreatedAt:  now.Add(-300 * time.Millisecond).UnixMilli(),
		DurationMS: 120,
	})

	live := page.liveToolEntries(2)
	if len(live) != 0 {
		t.Fatalf("len(live) = %d, want 0 when no running tools", len(live))
	}
}

func TestFormatUnifiedToolEntry_UsesInformativePreviewOverOk(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "read",
		Output:   "ok\nread /workspace/swarm/AGENTS.md (lines 1-220)",
		State:    "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if rendered != "read /workspace/swarm/AGENTS.md (lines 1-220)" {
		t.Fatalf("rendered = %q, want informative preview line", rendered)
	}
}

func TestSanitizeCommandSnippetPreview_RemovesDanglingQuoteWhenTruncated(t *testing.T) {
	input := "go test ./internal/ui -run 'TestRenderToolMessageLines_ReadSummaryHighlightsPath..."
	got := sanitizeCommandSnippetPreview(input)
	if strings.Contains(got, "'TestRenderToolMessageLines") {
		t.Fatalf("sanitizeCommandSnippetPreview should remove dangling quote marker, got %q", got)
	}
	if got == "" {
		t.Fatalf("sanitizeCommandSnippetPreview returned empty result")
	}
}

func TestSanitizeCommandSnippetPreview_PreservesBalancedQuotes(t *testing.T) {
	input := "go test ./internal/ui -run 'TestRenderToolMessageLines'"
	got := sanitizeCommandSnippetPreview(input)
	if got != input {
		t.Fatalf("sanitizeCommandSnippetPreview = %q, want %q", got, input)
	}
}

func TestFormatUnifiedToolEntry_AskUserRendersResponseTable(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "ask-user",
		Output: `{
			"tool":"ask_user",
			"status":"answered",
			"questions":[
				{"id":"q_mode","question":"Which mode?"},
				{"id":"q_apply","question":"Apply now?"}
			],
			"answers":{
				"q_mode":"auto",
				"q_apply":"yes"
			}
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "ask-user responses") {
		t.Fatalf("rendered missing ask-user summary header: %q", rendered)
	}
	if !strings.Contains(rendered, "| Question | Response |") {
		t.Fatalf("rendered missing table header: %q", rendered)
	}
	if !strings.Contains(rendered, "Which mode?") || !strings.Contains(rendered, "auto") {
		t.Fatalf("rendered missing first response row: %q", rendered)
	}
	if !strings.Contains(rendered, "Apply now?") || !strings.Contains(rendered, "yes") {
		t.Fatalf("rendered missing second response row: %q", rendered)
	}
}

func TestStyleToolSummaryLine_AskUserUsesPlainTextRendering(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	base := page.theme.Accent
	line := page.styleToolSummaryLine("ask-user responses", "ask-user", base)
	if len(line.Spans) != 0 {
		t.Fatalf("ask-user summary should not be token-highlighted, spans=%#v", line.Spans)
	}
	if line.Text != "ask-user responses" {
		t.Fatalf("line.Text = %q, want %q", line.Text, "ask-user responses")
	}
	if !stylesEqual(line.Style, base) {
		t.Fatalf("line.Style should match base style")
	}
}

func TestStyleToolSummaryLine_ExitPlanModeUsesPlainTextRendering(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	base := page.theme.Accent
	line := page.styleToolSummaryLine("exit_plan_mode plan_123", "exit_plan_mode", base)
	if len(line.Spans) != 0 {
		t.Fatalf("exit_plan_mode summary should not be token-highlighted, spans=%#v", line.Spans)
	}
	if line.Text != "exit_plan_mode plan_123" {
		t.Fatalf("line.Text = %q, want %q", line.Text, "exit_plan_mode plan_123")
	}
	if !stylesEqual(line.Style, base) {
		t.Fatalf("line.Style should match base style")
	}
}

func TestFormatUnifiedToolEntry_EditPayloadRendersPatchLikeBlock(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "edit",
		Output:   `{"tool":"edit","path":"/tmp/demo.txt","replacements":1,"replace_all":false,"old_string_preview":"foo()","new_string_preview":"bar()","old_string_truncated":false,"new_string_truncated":false}`,
		State:    "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if strings.Contains(rendered, "```") {
		t.Fatalf("rendered edit entry should not include markdown fences: %q", rendered)
	}
	if !strings.Contains(rendered, "-foo()") || !strings.Contains(rendered, "+bar()") {
		t.Fatalf("rendered edit entry missing old/new previews: %q", rendered)
	}
	if !strings.Contains(rendered, "edit /tmp/demo.txt") {
		t.Fatalf("rendered edit entry missing path header: %q", rendered)
	}
}

func TestFormatUnifiedToolEntry_MultiEditPayloadRendersAllDiffPairs(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "edit",
		Output:   `{"tool":"edit","path":"/tmp/demo.txt","edit_count":2,"replacements":2,"edits":[{"index":1,"old_string_preview":"foo()","new_string_preview":"bar()","old_string_truncated":false,"new_string_truncated":false},{"index":2,"old_string_preview":"baz()","new_string_preview":"qux()","old_string_truncated":false,"new_string_truncated":false}]}`,
		State:    "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "edit /tmp/demo.txt") {
		t.Fatalf("rendered multi-edit entry missing path header: %q", rendered)
	}
	if !strings.Contains(rendered, "-foo()") || !strings.Contains(rendered, "+bar()") {
		t.Fatalf("rendered multi-edit entry missing first diff pair: %q", rendered)
	}
	if !strings.Contains(rendered, "-baz()") || !strings.Contains(rendered, "+qux()") {
		t.Fatalf("rendered multi-edit entry missing second diff pair: %q", rendered)
	}
}

func TestFormatUnifiedToolEntry_EditDoesNotUseRawArgumentsFallback(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "edit",
		Output:   "edit /tmp/demo.txt replacements=1",
		Raw:      `{"path":"/tmp/demo.txt","old_string":"test","new_string":"edited test","replace_all":true}`,
		State:    "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "edit /tmp/demo.txt") {
		t.Fatalf("rendered edit entry missing path header: %q", rendered)
	}
	if strings.Contains(rendered, "replace_all") {
		t.Fatalf("rendered edit entry should not include raw-args flags: %q", rendered)
	}
	if strings.Contains(rendered, "-test") || strings.Contains(rendered, "+edited test") {
		t.Fatalf("rendered edit entry should not invent diff from raw args: %q", rendered)
	}
}

func TestParseToolStreamEntry_EditHistoryPayloadRendersDiff(t *testing.T) {
	raw := `tool=edit call_id=call_1 output={"tool":"edit","path":"/tmp/demo.txt","replacements":1,"replace_all":false,"old_string_preview":"initial","new_string_preview":"updated","old_string_truncated":false,"new_string_truncated":false}`
	entry := parseToolStreamEntry(raw, time.Now().UnixMilli())

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "edit /tmp/demo.txt") {
		t.Fatalf("rendered edit entry missing path header: %q", rendered)
	}
	if !strings.Contains(rendered, "-initial") || !strings.Contains(rendered, "+updated") {
		t.Fatalf("rendered edit entry missing old/new diff lines: %q", rendered)
	}
}

func TestFormatUnifiedToolEntry_TaskMultiLaunchIncludesAllLaunches(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "task",
		Output: `{
			"tool":"task",
			"goal":"Write poem variants",
			"description":"Write poem variants",
			"status":"ok",
			"success_count":3,
			"launch_count":3,
			"path_id":"tool.task.v1",
			"launches":[
				{"launch_index":1,"subagent":"parallel","meta_prompt":"haiku poem type","status":"ok","phase":"completed"},
				{"launch_index":2,"subagent":"parallel","meta_prompt":"sonnet poem type","status":"ok","phase":"completed"},
				{"launch_index":3,"subagent":"parallel","meta_prompt":"free verse poem type","status":"ok","phase":"completed"}
			]
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if got := strings.TrimSpace(rendered); got != "task Write poem variants (3 launches) (3 ok)" {
		t.Fatalf("rendered entry = %q", got)
	}
}

func TestTaskLaunchPreviewLineUsesMetaPhaseOnly(t *testing.T) {
	line := taskLaunchPreviewLine(map[string]any{
		"launch_index": 1,
		"subagent":     "explorer",
		"meta_prompt":  "map repo",
		"status":       "running",
		"phase":        "running grep",
		"summary":      "should not appear",
	}, 200)
	if got := strings.TrimSpace(line); got != "1. explorer — map repo [running] · running grep" {
		t.Fatalf("line = %q", got)
	}
	if strings.Contains(line, "should not appear") {
		t.Fatalf("task launch preview leaked summary: %q", line)
	}
}

func TestBuildToolStreamLines_TaskMultiLaunchShowsAllLaunches(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.addToolStreamEntry(chatToolStreamEntry{
		ToolName: "task",
		Output: `{
			"tool":"task",
			"goal":"Write poem variants",
			"description":"Write poem variants",
			"status":"ok",
			"success_count":3,
			"launch_count":3,
			"path_id":"tool.task.v1",
			"launches":[
				{"launch_index":1,"subagent":"parallel","meta_prompt":"haiku poem type","status":"ok","phase":"completed"},
				{"launch_index":2,"subagent":"parallel","meta_prompt":"sonnet poem type","status":"ok","phase":"completed"},
				{"launch_index":3,"subagent":"parallel","meta_prompt":"free verse poem type","status":"ok","phase":"completed"}
			]
		}`,
		State: "done",
	})

	lines := page.buildToolStreamLines(160)
	if len(lines) < 4 {
		t.Fatalf("len(lines) = %d, want at least 4", len(lines))
	}
	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		joined = append(joined, line.Text)
	}
	body := strings.Join(joined, "\n")
	if !strings.Contains(body, "1. parallel — haiku poem type [ok] · completed") {
		t.Fatalf("toolstream missing launch 1: %q", body)
	}
	if !strings.Contains(body, "2. parallel — sonnet poem type [ok] · completed") {
		t.Fatalf("toolstream missing launch 2: %q", body)
	}
	if !strings.Contains(body, "3. parallel — free verse poem type [ok] · completed") {
		t.Fatalf("toolstream missing launch 3: %q", body)
	}
}

func TestRenderLiveToolEntryLines_TaskShowsAllLaunches(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	entry := chatToolStreamEntry{
		ToolName: "task",
		State:    "running",
		Output: `{
			"tool":"task",
			"goal":"Run explorers",
			"description":"Run explorers",
			"status":"running",
			"launch_count":3,
			"path_id":"tool.task.stream.v1",
			"launches":[
				{"launch_index":1,"subagent":"explorer","meta_prompt":"map auth","status":"ok","phase":"completed"},
				{"launch_index":2,"subagent":"explorer","meta_prompt":"map tui","status":"running","phase":"running grep"},
				{"launch_index":3,"subagent":"explorer","meta_prompt":"map api","status":"running","phase":"running read"}
			]
		}`,
	}

	lines := page.renderLiveToolEntryLines(entry, 160)
	if len(lines) < 4 {
		t.Fatalf("len(lines) = %d, want at least 4", len(lines))
	}
	body := make([]string, 0, len(lines))
	for _, line := range lines {
		body = append(body, line.Text)
	}
	joined := strings.Join(body, "\n")
	if !strings.Contains(joined, "1. explorer — map auth [ok] · completed") {
		t.Fatalf("live entry missing launch 1: %q", joined)
	}
	if !strings.Contains(joined, "2. explorer — map tui [running] · running grep") {
		t.Fatalf("live entry missing launch 2: %q", joined)
	}
	if !strings.Contains(joined, "3. explorer — map api [running] · running read") {
		t.Fatalf("live entry missing launch 3: %q", joined)
	}
}

func TestMergeTaskLaunchPayloads_SortsByLaunchIndex(t *testing.T) {
	existing := []map[string]any{{
		"launch_index": 2,
		"subagent":     "explorer",
		"status":       "running",
		"phase":        "running grep",
	}}
	incoming := []map[string]any{{
		"launch_index": 1,
		"subagent":     "explorer",
		"status":       "ok",
		"phase":        "completed",
	}, {
		"launch_index": 3,
		"subagent":     "explorer",
		"status":       "running",
		"phase":        "running read",
	}}

	merged := mergeTaskLaunchPayloads(existing, incoming)
	if len(merged) != 3 {
		t.Fatalf("len(merged) = %d, want 3", len(merged))
	}
	for i, want := range []int{1, 2, 3} {
		if got := jsonInt(merged[i], "launch_index"); got != want {
			t.Fatalf("merged[%d] launch_index = %d, want %d", i, got, want)
		}
	}
}

func TestToolPreviewLineLimit_TaskUsesLaunchCount(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "task",
		Output: `{
			"tool":"task",
			"path_id":"tool.task.stream.v1",
			"launch_count":20,
			"launches":[
				{"launch_index":1,"subagent":"explorer","status":"running","phase":"grep"},
				{"launch_index":2,"subagent":"explorer","status":"running","phase":"read"}
			]
		}`,
	}
	if got := toolPreviewLineLimit(entry); got != 20 {
		t.Fatalf("toolPreviewLineLimit(task) = %d, want 20", got)
	}
}

func TestToolPreviewLineLimit_TaskUsesVisibleLaunchesWhenCountMissing(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "task",
		Output: `{
			"tool":"task",
			"path_id":"tool.task.stream.v1",
			"launches":[
				{"launch_index":1,"subagent":"explorer","status":"running","phase":"grep"},
				{"launch_index":2,"subagent":"explorer","status":"running","phase":"read"},
				{"launch_index":3,"subagent":"explorer","status":"ok","phase":"completed"}
			]
		}`,
	}
	if got := toolPreviewLineLimit(entry); got != 3 {
		t.Fatalf("toolPreviewLineLimit(task) = %d, want 3", got)
	}
}

func TestToolPreviewLines_TaskLaunchesStayOrderedAndMetaOnly(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "task",
		Output: `{
			"tool":"task",
			"path_id":"tool.task.stream.v1",
			"launch_count":3,
			"launches":[
				{"launch_index":1,"subagent":"explorer","meta_prompt":"first","status":"ok","phase":"completed","summary":"hide me"},
				{"launch_index":2,"subagent":"explorer","meta_prompt":"second","status":"running","phase":"running grep","report_excerpt":"hide me too"},
				{"launch_index":3,"subagent":"explorer","meta_prompt":"third","status":"running","phase":"running read"}
			]
		}`,
	}

	lines := toolPreviewLines(entry, 200, toolPreviewLineLimit(entry))
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}
	if got := strings.TrimSpace(lines[0]); got != "1. explorer — first [ok] · completed" {
		t.Fatalf("lines[0] = %q", got)
	}
	if got := strings.TrimSpace(lines[1]); got != "2. explorer — second [running] · running grep" {
		t.Fatalf("lines[1] = %q", got)
	}
	if got := strings.TrimSpace(lines[2]); got != "3. explorer — third [running] · running read" {
		t.Fatalf("lines[2] = %q", got)
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "hide me") || strings.Contains(joined, "report_excerpt") {
		t.Fatalf("task preview leaked delegated text: %q", joined)
	}
}

func TestTaskToolLaunchRow_ReasoningMapsToThinkingToolWithoutPreview(t *testing.T) {
	row := taskToolLaunchRow(map[string]any{
		"launch_index":               1,
		"subagent":                   "explorer",
		"status":                     "running",
		"current_preview_kind":       "reasoning",
		"current_preview_text":       "<reasoning>Inspecting files</reasoning>",
		"reasoning_summary":          "Inspecting files before edit",
		"current_tool":               "",
		"current_tool_started_at_ms": int64(123),
	})
	if got := row.Tool; got != "thinking" {
		t.Fatalf("row.Tool = %q, want thinking", got)
	}
	if got := row.PreviewKind; got != "thinking" {
		t.Fatalf("row.PreviewKind = %q, want thinking", got)
	}
	if got := row.PreviewText; got != "" {
		t.Fatalf("row.PreviewText = %q, want empty", got)
	}
}

func TestTaskToolLaunchRow_AssistantPreviewIsHidden(t *testing.T) {
	row := taskToolLaunchRow(map[string]any{
		"launch_index":               1,
		"subagent":                   "clone",
		"status":                     "running",
		"current_preview_kind":       "assistant",
		"current_preview_text":       "No Shore Between",
		"current_tool":               "",
		"current_tool_started_at_ms": int64(123),
	})
	if got := row.PreviewKind; got != "assistant" {
		t.Fatalf("row.PreviewKind = %q, want assistant", got)
	}
	if got := row.PreviewText; got != "" {
		t.Fatalf("row.PreviewText = %q, want empty", got)
	}
}

func TestToolPreviewLines_EditShowsDiffLines(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "edit",
		Output:   `{"path":"/tmp/demo.txt","old_string_preview":"test","new_string_preview":"edited test","old_string_truncated":false,"new_string_truncated":false}`,
	}

	lines := toolPreviewLines(entry, 180, 2)
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	if lines[0] != "-test" {
		t.Fatalf("lines[0] = %q, want -test", lines[0])
	}
	if lines[1] != "+edited test" {
		t.Fatalf("lines[1] = %q, want +edited test", lines[1])
	}
}

func TestToolPreviewLineLimit_EditUsesFullExpandedLineCount(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "edit",
		Output:   `{"path":"/tmp/code-only-test.txt","old_string_preview":"PLACEHOLDER\\n","new_string_preview":"package main\\n\\nimport \"fmt\"\\n","old_string_truncated":false,"new_string_truncated":false}`,
	}

	if got := toolPreviewLineLimit(entry); got != 4 {
		t.Fatalf("toolPreviewLineLimit(edit) = %d, want 4", got)
	}
}

func TestToolPreviewLines_EditDoesNotUseRawArgumentsFallback(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "edit",
		Output:   "edit /tmp/demo.txt replacements=1",
		Raw:      `{"path":"/tmp/demo.txt","old_string":"test","new_string":"edited test"}`,
	}

	lines := toolPreviewLines(entry, 180, 2)
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(lines))
	}
	if lines[0] != "edit /tmp/demo.txt replacements=1" {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestFormatUnifiedToolEntry_EditPayloadExpandsEscapedNewlines(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "edit",
		Output:   `{"tool":"edit","path":"/tmp/code-only-test.txt","replacements":1,"replace_all":false,"old_string_preview":"PLACEHOLDER\\n","new_string_preview":"package main\\n\\nimport \"fmt\"\\n\\nfunc main() {\\n\\tfmt.Println(\"code-only test\")\\n}\\n","old_string_truncated":false,"new_string_truncated":false}`,
		State:    "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if strings.Contains(rendered, `\n`) {
		t.Fatalf("rendered edit entry should expand escaped newlines, got: %q", rendered)
	}
	if !strings.Contains(rendered, "\n+package main") || !strings.Contains(rendered, "\n+import \"fmt\"") {
		t.Fatalf("rendered edit entry missing expanded code lines: %q", rendered)
	}
}

func TestToolPreviewLines_EditShowsFullEscapedMultilinePreview(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "edit",
		Output:   `{"path":"/tmp/code-only-test.txt","old_string_preview":"PLACEHOLDER\\n","new_string_preview":"package main\\n\\nimport \"fmt\"\\n","old_string_truncated":false,"new_string_truncated":false}`,
	}

	lines := toolPreviewLines(entry, 180, 4)
	if len(lines) != 4 {
		t.Fatalf("len(lines) = %d, want 4", len(lines))
	}
	if lines[0] != "-PLACEHOLDER" {
		t.Fatalf("lines[0] = %q, want -PLACEHOLDER", lines[0])
	}
	if lines[1] != "+package main" {
		t.Fatalf("lines[1] = %q, want +package main", lines[1])
	}
	if lines[2] != "+" {
		t.Fatalf("lines[2] = %q, want +", lines[2])
	}
	if lines[3] != "+import \"fmt\"" {
		t.Fatalf("lines[3] = %q, want +import \"fmt\"", lines[3])
	}
}

func TestRenderToolMessageLines_EditPreviewUsesDiffStyles(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Text:      "edit /tmp/demo.txt replacements=1\n-test\n+edited test",
	}

	lines := page.renderToolMessageLines(message, 120)
	if len(lines) < 3 {
		t.Fatalf("len(lines) = %d, want at least 3", len(lines))
	}
	if !stylesEqual(lines[1].Style, page.theme.Error) {
		t.Fatalf("line[1] should use error style for removed text")
	}
	if !stylesEqual(lines[2].Style, page.theme.Success) {
		t.Fatalf("line[2] should use success style for added text")
	}
}

func TestRenderToolMessageLines_ReadSummaryHighlightsPath(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Text:      "read /tmp/demo.go (lines 1-4)",
	}

	lines := page.renderToolMessageLines(message, 120)
	if len(lines) == 0 {
		t.Fatalf("len(lines) = 0, want at least 1")
	}
	if len(lines[0].Spans) == 0 {
		t.Fatalf("expected token spans for read summary line")
	}

	wantToolStyle := styleWithoutBackground(page.theme.Accent.Bold(true))
	wantPathStyle := page.toolTokenPathStyleForBase(page.theme.Accent)

	var hasTool bool
	var hasPath bool
	for _, span := range lines[0].Spans {
		switch {
		case strings.TrimSpace(span.Text) == "read" && stylesEqual(span.Style, wantToolStyle):
			hasTool = true
		case strings.Contains(span.Text, "/tmp/demo.go") && stylesEqual(span.Style, wantPathStyle):
			hasPath = true
		}
	}

	if !hasTool {
		t.Fatalf("expected 'read' token in accent style")
	}
	if !hasPath {
		t.Fatalf("expected filepath token style for /tmp/demo.go")
	}
}

func TestRenderToolMessageLines_WriteSummaryHighlightsPath(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Text:      "write /tmp/out.md (42 bytes)",
	}

	lines := page.renderToolMessageLines(message, 120)
	if len(lines) == 0 {
		t.Fatalf("len(lines) = 0, want at least 1")
	}
	if len(lines[0].Spans) == 0 {
		t.Fatalf("expected token spans for write summary line")
	}

	wantToolStyle := styleWithoutBackground(page.theme.Accent.Bold(true))
	wantPathStyle := page.toolTokenPathStyleForBase(page.theme.Accent)

	var hasTool bool
	var hasPath bool
	for _, span := range lines[0].Spans {
		switch {
		case strings.TrimSpace(span.Text) == "write" && stylesEqual(span.Style, wantToolStyle):
			hasTool = true
		case strings.Contains(span.Text, "/tmp/out.md") && stylesEqual(span.Style, wantPathStyle):
			hasPath = true
		}
	}

	if !hasTool {
		t.Fatalf("expected 'write' token in accent style")
	}
	if !hasPath {
		t.Fatalf("expected filepath token style for /tmp/out.md")
	}
}

func TestStyleToolSummaryLine_BashSummaryHighlightsCommandFlagAndPath(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	line := page.styleToolSummaryLine("bash go test ./internal/ui -run TestRender (failed)", "bash", page.theme.Accent)
	if len(line.Spans) == 0 {
		t.Fatalf("expected styled spans for bash summary")
	}

	palette := page.chatSyntaxPalette(page.theme.Accent)

	var hasCommand bool
	var hasPath bool
	var hasFlag bool
	for _, span := range line.Spans {
		switch {
		case strings.TrimSpace(span.Text) == "go" && stylesEqual(span.Style, palette.Command):
			hasCommand = true
		case strings.Contains(span.Text, "./internal/ui") && stylesEqual(span.Style, palette.Path):
			hasPath = true
		case strings.TrimSpace(span.Text) == "-run" && stylesEqual(span.Style, palette.Flag):
			hasFlag = true
		}
	}

	if !hasCommand {
		t.Fatalf("expected command-highlighted token in bash summary")
	}
	if !hasPath {
		t.Fatalf("expected path-highlighted token in bash summary")
	}
	if !hasFlag {
		t.Fatalf("expected flag-highlighted token in bash summary")
	}
}

func TestStyleToolSummaryLine_PatternAndPathAreDistinctStyles(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	line := page.styleToolSummaryLine(`grep "^internal/ui/.*_test\\.go$" in internal/ui (2 matches)`, "grep", page.theme.Accent)
	if len(line.Spans) == 0 {
		t.Fatalf("expected styled spans for grep summary")
	}

	palette := page.chatSyntaxPalette(page.theme.Accent)
	if stylesEqual(palette.Path, palette.Pattern) {
		t.Fatalf("path and pattern styles must be distinct")
	}

	var hasPattern bool
	var hasPath bool
	for _, span := range line.Spans {
		switch {
		case strings.Contains(span.Text, "^internal/ui/.*_test\\.go$") && stylesEqual(span.Style, palette.Pattern):
			hasPattern = true
		case strings.TrimSpace(span.Text) == "internal/ui" && stylesEqual(span.Style, palette.Path):
			hasPath = true
		}
	}
	if !hasPattern {
		t.Fatalf("expected regex pattern token style")
	}
	if !hasPath {
		t.Fatalf("expected path token style")
	}
}

func TestStyleToolSummaryLine_PatternWithSpacesInQuotedValue(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	line := page.styleToolSummaryLine(`grep "ask-user|ask_user|exit_plan_mode|plan mode|plan-mode|tool-result|tool result" in internal/ui (4 matches)`, "grep", page.theme.Accent)
	if len(line.Spans) == 0 {
		t.Fatalf("expected styled spans for grep summary with quoted pattern containing spaces")
	}

	palette := page.chatSyntaxPalette(page.theme.Accent)
	if stylesEqual(palette.Path, palette.Pattern) {
		t.Fatalf("path and pattern styles must remain distinct")
	}

	var hasPattern bool
	var hasPath bool
	for _, span := range line.Spans {
		switch {
		case strings.Contains(span.Text, `plan mode|plan-mode|tool-result|tool result`) && stylesEqual(span.Style, palette.Pattern):
			hasPattern = true
		case strings.TrimSpace(span.Text) == "internal/ui" && stylesEqual(span.Style, palette.Path):
			hasPath = true
		}
	}
	if !hasPattern {
		t.Fatalf("expected full quoted pattern (including spaces) to use pattern style")
	}
	if !hasPath {
		t.Fatalf("expected path token style for search root")
	}
}

func TestStyleToolSummaryLine_PathDotInContextHighlightsAsPath(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	line := page.styleToolSummaryLine(`grep "tool result" in .`, "grep", page.theme.Accent)
	if len(line.Spans) == 0 {
		t.Fatalf("expected styled spans for grep summary with dot root path")
	}

	palette := page.chatSyntaxPalette(page.theme.Accent)
	hasDotPath := false
	for _, span := range line.Spans {
		if strings.TrimSpace(span.Text) == "." && stylesEqual(span.Style, palette.Path) {
			hasDotPath = true
			break
		}
	}
	if !hasDotPath {
		t.Fatalf("expected dot root token to be path-highlighted")
	}
}

func TestRenderToolMessageLines_PreservesSummaryWhitespace(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Text:      "read  /tmp/demo.go  (lines 1-4)",
	}

	lines := page.renderToolMessageLines(message, 160)
	if len(lines) == 0 {
		t.Fatalf("len(lines) = 0, want at least 1")
	}
	if !strings.Contains(lines[0].Text, "read  /tmp/demo.go  (lines 1-4)") {
		t.Fatalf("expected tool summary whitespace to be preserved, got %q", lines[0].Text)
	}
}

func TestRenderToolMessageLines_ReadSummaryHighlightsPathWhenPathStyleCollidesWithBase(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.theme.MarkdownCodeFunction = page.theme.Accent
	page.theme.MarkdownCodeString = page.theme.Warning

	resolvedPathStyle := page.toolTokenPathStyleForBase(page.theme.Accent)
	wantFallback := styleWithoutBackground(page.theme.MarkdownCodeString)
	if !stylesEqual(resolvedPathStyle, wantFallback) {
		t.Fatalf("resolved path style should fall back to string style when path style collides with base")
	}

	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Text:      "read /tmp/demo.go (lines 1-4)",
	}
	lines := page.renderToolMessageLines(message, 120)
	if len(lines) == 0 || len(lines[0].Spans) == 0 {
		t.Fatalf("expected styled spans for read summary")
	}

	hasPath := false
	for _, span := range lines[0].Spans {
		if strings.Contains(span.Text, "/tmp/demo.go") && stylesEqual(span.Style, resolvedPathStyle) {
			hasPath = true
			break
		}
	}
	if !hasPath {
		t.Fatalf("expected filepath token style to use collision-safe fallback")
	}
}

func TestRenderToolMessageLines_ReadSummaryWithLeadingSymbolHighlightsPath(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Text:      "\u2713 read /workspace/swarm/internal/ui/chat_tool_syntax.go (lines 160-349, truncated)",
	}

	lines := page.renderToolMessageLines(message, 200)
	if len(lines) == 0 || len(lines[0].Spans) == 0 {
		t.Fatalf("expected styled spans for read summary with leading symbol")
	}

	wantPathStyle := page.toolTokenPathStyleForBase(page.theme.Accent)
	hasPath := false
	for _, span := range lines[0].Spans {
		if strings.Contains(span.Text, "/workspace/swarm/internal/ui/chat_tool_syntax.go") && stylesEqual(span.Style, wantPathStyle) {
			hasPath = true
			break
		}
	}
	if !hasPath {
		t.Fatalf("expected filepath token style in read summary with leading symbol")
	}
}

func TestRenderToolMessageLines_EditPreviewAddsSyntaxHighlight(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Text:      "edit /tmp/demo.go replacements=1\n-func old() int { return 1 }\n+func add(x int) int { return x + 1 }",
	}

	lines := page.renderToolMessageLines(message, 160)
	if len(lines) < 3 {
		t.Fatalf("len(lines) = %d, want >= 3", len(lines))
	}
	if len(lines[2].Spans) == 0 {
		t.Fatalf("expected syntax spans on added diff line")
	}

	wantKeyword := styleWithoutBackground(page.theme.MarkdownCodeKeyword)
	wantNumber := styleWithoutBackground(page.theme.MarkdownCodeNumber)
	wantOperator := styleWithoutBackground(page.theme.MarkdownCodeOperator)

	var hasKeyword bool
	var hasNumber bool
	var hasOperator bool
	for _, span := range lines[2].Spans {
		switch {
		case strings.TrimSpace(span.Text) == "func" && stylesEqual(span.Style, wantKeyword):
			hasKeyword = true
		case strings.TrimSpace(span.Text) == "1" && stylesEqual(span.Style, wantNumber):
			hasNumber = true
		case strings.TrimSpace(span.Text) == "+" && stylesEqual(span.Style, wantOperator):
			hasOperator = true
		}
	}

	if !hasKeyword {
		t.Fatalf("expected keyword-highlighted span in added diff line")
	}
	if !hasNumber {
		t.Fatalf("expected number-highlighted span in added diff line")
	}
	if !hasOperator {
		t.Fatalf("expected operator-highlighted span in added diff line")
	}
}

func TestRenderToolMessageLines_TaskMarkdownBulletsRenderInlineStyles(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Text:      "Commit\n• **`f526435`**\n• **Message:** `ui: suppress read tool.started preview to avoid duplicate 0-lines`",
	}

	lines := page.renderToolMessageLines(message, 200)
	if len(lines) < 3 {
		t.Fatalf("len(lines) = %d, want >= 3", len(lines))
	}
	if strings.Contains(lines[1].Text, "**") || strings.Contains(lines[2].Text, "**") {
		t.Fatalf("markdown strong markers should not leak: %#v", []string{lines[1].Text, lines[2].Text})
	}
	if strings.Contains(lines[1].Text, "`") || strings.Contains(lines[2].Text, "`") {
		t.Fatalf("markdown code markers should not leak: %#v", []string{lines[1].Text, lines[2].Text})
	}
	if len(lines[1].Spans) == 0 || len(lines[2].Spans) == 0 {
		t.Fatalf("expected styled spans on markdown bullet preview lines")
	}

	hasBold := false
	hasCode := false
	hasBoldCodeHash := false
	isCodeStyle := func(style tcell.Style) bool {
		for _, want := range []tcell.Style{
			page.theme.MarkdownCodeFunction,
			page.theme.MarkdownCodeKeyword,
			page.theme.MarkdownCodeString,
			page.theme.MarkdownCodeNumber,
			page.theme.MarkdownCodeOperator,
			page.theme.MarkdownCodeType,
			page.theme.MarkdownCode,
		} {
			if markdownStyleExtendsBase(style, want) {
				return true
			}
		}
		return false
	}
	for _, idx := range []int{1, 2} {
		for _, span := range lines[idx].Spans {
			_, _, attrs := span.Style.Decompose()
			if attrs&tcell.AttrBold != 0 {
				hasBold = true
			}
			if isCodeStyle(span.Style) {
				hasCode = true
				if strings.TrimSpace(span.Text) == "f526435" && attrs&tcell.AttrBold != 0 {
					hasBoldCodeHash = true
				}
			}
		}
	}
	if !hasBold {
		t.Fatalf("expected bold styling from markdown strong in task bullets")
	}
	if !hasCode {
		t.Fatalf("expected code styling from markdown code spans in task bullets")
	}
	if !hasBoldCodeHash {
		t.Fatalf("expected nested strong+code styling on markdown bullet hash token")
	}
}

func TestRenderToolMessageLines_NumberedMarkdownListRendersInlineStyles(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "done",
		Text:      "1. **`f5264357b3a64dd28bac924cea3617ced938b64`**\n**ui: suppress read tool.started preview to avoid duplicate 0-lines**\n• Files changed:\n• `internal/ui/chat_page.go`\n• `internal/ui/chat_stream_status_test.go`\n\n2. **`8e87e092e2e35e232ad14698b38fb6f06451f7e`**\n**docs: add critical next-day worktree and dual-lane priorities**\n• Files changed:\n• `docs/master-fix-roadmap.md`",
	}

	lines := page.renderToolMessageLines(message, 260)
	if len(lines) < 8 {
		t.Fatalf("len(lines) = %d, want >= 8", len(lines))
	}

	first := lines[0].Text
	if !strings.Contains(first, "1. ") {
		t.Fatalf("expected numbered prefix in first line, got %q", first)
	}
	if strings.Contains(first, "**") || strings.Contains(first, "`") {
		t.Fatalf("expected markdown markers removed from first line, got %q", first)
	}
	if len(lines[0].Spans) == 0 {
		t.Fatalf("expected styled spans in first line")
	}

	hasBold := false
	hasCode := false
	for _, line := range lines {
		if strings.Contains(line.Text, "**") || strings.Contains(line.Text, "`") {
			t.Fatalf("markdown markers leaked in rendered line: %q", line.Text)
		}
		for _, span := range line.Spans {
			_, _, attrs := span.Style.Decompose()
			if attrs&tcell.AttrBold != 0 {
				hasBold = true
			}
			if stylesEqual(span.Style, styleWithoutBackground(page.theme.MarkdownCodeFunction)) ||
				stylesEqual(span.Style, styleWithoutBackground(page.theme.MarkdownCodeKeyword)) ||
				stylesEqual(span.Style, styleWithoutBackground(page.theme.MarkdownCodeString)) ||
				stylesEqual(span.Style, styleWithoutBackground(page.theme.MarkdownCodeNumber)) ||
				stylesEqual(span.Style, styleWithoutBackground(page.theme.MarkdownCodeOperator)) ||
				stylesEqual(span.Style, styleWithoutBackground(page.theme.MarkdownCodeType)) {
				hasCode = true
			}
		}
	}
	if !hasBold {
		t.Fatalf("expected bold styling from markdown strong in numbered list preview")
	}
	if !hasCode {
		t.Fatalf("expected code styling from markdown code spans in numbered list preview")
	}
}

func TestRenderToolMessageLines_SearchPayloadShowsMatchTableWithoutCodeDump(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	entry := chatToolStreamEntry{
		ToolName: "search",
		Output: `{
			"path_id":"tool.search.v1",
			"search_mode":"content",
			"path":"internal/ui",
			"query":"executeSearch",
			"queries":["executeSearch","ExecuteBatch"],
			"query_count":2,
			"count":2,
			"total_matched":14,
			"matches":[
				{"query":"executeSearch","relative_path":"tool/runtime.go","line":1103,"text":"func (r *Runtime) executeSearch(...)"},
				{"query":"ExecuteBatch","relative_path":"tool/runtime.go","line":625,"text":"func (r *Runtime) ExecuteBatch(...)"}
			],
			"query_results":[
				{"query":"executeSearch","count":9,"total_matched":9},
				{"query":"ExecuteBatch","count":5,"total_matched":5}
			]
		}`,
		State: "done",
	}
	message := chatMessageItem{
		Role:      "tool",
		Text:      formatUnifiedToolEntry(entry),
		ToolState: "done",
		Metadata:  toolTimelineMetadata(entry),
	}

	lines := page.renderToolMessageLines(message, 120)
	if len(lines) < 4 {
		t.Fatalf("len(lines) = %d, want >= 4", len(lines))
	}
	if !strings.Contains(lines[0].Text, "search") {
		t.Fatalf("missing search summary line: %#v", lines[0])
	}
	if !strings.Contains(lines[1].Text, "Path") || !strings.Contains(lines[1].Text, "Ln") {
		t.Fatalf("missing search table header: %#v", lines[1])
	}
	body := strings.Join([]string{lines[2].Text, lines[3].Text}, "\n")
	if !strings.Contains(body, "tool/runtime.go") {
		t.Fatalf("missing filepath rows: %q", body)
	}
	if !strings.Contains(body, "1103") || !strings.Contains(body, "625") {
		t.Fatalf("missing line numbers in rows: %q", body)
	}
	if strings.Contains(body, "func (r *Runtime)") {
		t.Fatalf("should not dump matched code text in timeline table: %q", body)
	}
}

func TestBuildToolStreamLines_SearchPayloadShowsMatchTableWithoutCodeDump(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.addToolStreamEntry(chatToolStreamEntry{
		ToolName: "search",
		Output: `{
			"path_id":"tool.search.v1",
			"search_mode":"content",
			"path":"internal/ui",
			"query":"executeSearch",
			"queries":["executeSearch","ExecuteBatch"],
			"query_count":2,
			"count":2,
			"total_matched":14,
			"matches":[
				{"query":"executeSearch","relative_path":"tool/runtime.go","line":1103,"text":"func (r *Runtime) executeSearch(...)"},
				{"query":"ExecuteBatch","relative_path":"tool/runtime.go","line":625,"text":"func (r *Runtime) ExecuteBatch(...)"}
			],
			"query_results":[
				{"query":"executeSearch","count":9,"total_matched":9},
				{"query":"ExecuteBatch","count":5,"total_matched":5}
			]
		}`,
		State: "done",
	})

	lines := page.buildToolStreamLines(120)
	if len(lines) < 5 {
		t.Fatalf("len(lines) = %d, want >= 5", len(lines))
	}
	body := make([]string, 0, len(lines))
	for _, line := range lines {
		body = append(body, line.Text)
	}
	joined := strings.Join(body, "\n")
	if !strings.Contains(joined, "Path") || !strings.Contains(joined, "Ln") {
		t.Fatalf("missing toolstream search table header: %q", joined)
	}
	if !strings.Contains(joined, "tool/runtime.go") || !strings.Contains(joined, "1103") {
		t.Fatalf("missing filepath/line rows in toolstream: %q", joined)
	}
	if strings.Contains(joined, "func (r *Runtime)") {
		t.Fatalf("toolstream should not dump matched code text: %q", joined)
	}
}

func TestFormatUnifiedToolEntry_ReadShowsLinePreview(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "read",
		Output: `{
			"path":"/tmp/demo.txt",
			"line_start":20,
			"count":2,
			"lines":[
				{"line":20,"text":"first line"},
				{"line":21,"text":"second line"}
			]
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "read /tmp/demo.txt") {
		t.Fatalf("rendered entry missing read summary: %q", rendered)
	}
	if !strings.Contains(rendered, "\n20: first line") {
		t.Fatalf("rendered entry missing first line preview: %q", rendered)
	}
}

func TestToolHeadline_GrepZeroMatchesUsesUnparenthesizedLabel(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "grep",
		Output:   `{"pattern":"TODO","path":".","count":0,"truncated":false}`,
	}
	headline := toolHeadline(entry, 220)
	if strings.Contains(headline, "(0 matches") {
		t.Fatalf("headline should avoid parenthesized zero match label: %q", headline)
	}
	if !strings.Contains(headline, "0 matches") {
		t.Fatalf("headline missing zero match label: %q", headline)
	}
}

func TestStyleToolSummaryLine_ItalicizesZeroResultLabels(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})

	checkItalic := func(line chatRenderLine, wantWord string) {
		hasItalicNumber := false
		hasItalicWord := false
		for _, span := range line.Spans {
			_, _, attrs := span.Style.Decompose()
			switch strings.TrimSpace(strings.ToLower(span.Text)) {
			case "0":
				if attrs&tcell.AttrItalic != 0 {
					hasItalicNumber = true
				}
			case wantWord:
				if attrs&tcell.AttrItalic != 0 {
					hasItalicWord = true
				}
			}
		}
		if !hasItalicNumber {
			t.Fatalf("expected italic style for zero-count number in %q", line.Text)
		}
		if !hasItalicWord {
			t.Fatalf("expected italic style for zero-count label in %q", line.Text)
		}
	}

	grepLine := page.styleToolSummaryLine(`grep "TODO" in . (0 matches)`, "grep", page.theme.Accent)
	if len(grepLine.Spans) == 0 {
		t.Fatalf("expected styled spans for grep summary")
	}
	checkItalic(grepLine, "matches")

	readLine := page.styleToolSummaryLine(`read /tmp/demo.txt 0 lines`, "read", page.theme.Accent)
	if len(readLine.Spans) == 0 {
		t.Fatalf("expected styled spans for read summary")
	}
	checkItalic(readLine, "lines")
}

func TestFormatUnifiedToolEntry_ExitPlanModeShowsActionTitlePlanAndUserMessage(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "exit_plan_mode",
		Output: `{
			"tool":"exit_plan_mode",
			"status":"approved",
			"title":"Implementation Plan",
			"plan_id":"plan_123",
			"approval_state":"approved",
			"target_mode":"auto",
			"user_message":"Ship it"
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "exit_plan_mode approved · Implementation Plan") {
		t.Fatalf("rendered entry missing exit-plan headline: %q", rendered)
	}
	if !strings.Contains(rendered, "title: Implementation Plan") {
		t.Fatalf("rendered entry missing plan title line: %q", rendered)
	}
	if !strings.Contains(rendered, "plan: plan_123") {
		t.Fatalf("rendered entry missing plan id line: %q", rendered)
	}
	if !strings.Contains(rendered, "next mode: auto") {
		t.Fatalf("rendered entry missing next mode line: %q", rendered)
	}
	if !strings.Contains(rendered, "feedback: Ship it") {
		t.Fatalf("rendered entry missing user feedback line: %q", rendered)
	}
	if strings.Contains(rendered, "\"approval_state\"") {
		t.Fatalf("rendered entry should not expose raw JSON payload: %q", rendered)
	}
}

func TestFormatUnifiedToolEntry_ExitPlanPermissionEnvelopeShowsDecisionTitlePlanAndMessage(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "permission",
		Output: `{
			"permission":{
				"approved":false,
				"status":"denied",
				"reason":"Need updates on rollout risks"
			},
			"tool":{
				"name":"exit_plan_mode",
				"arguments":"{\"title\":\"Deployment Plan\",\"plan_id\":\"plan_456\"}"
			}
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "exit_plan_mode denied · Deployment Plan") {
		t.Fatalf("rendered entry missing exit-plan denied headline: %q", rendered)
	}
	if !strings.Contains(rendered, "title: Deployment Plan") {
		t.Fatalf("rendered entry missing extracted plan title line: %q", rendered)
	}
	if !strings.Contains(rendered, "plan: plan_456") {
		t.Fatalf("rendered entry missing extracted plan id line: %q", rendered)
	}
	if !strings.Contains(rendered, "feedback: Need updates on rollout risks") {
		t.Fatalf("rendered entry missing extracted user feedback line: %q", rendered)
	}
	if strings.Contains(rendered, "\"permission\"") {
		t.Fatalf("rendered entry should not expose raw permission envelope JSON: %q", rendered)
	}
}

func TestFormatUnifiedToolEntry_ExitPlanPermissionEnvelopeOmitsDefaultReason(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "permission",
		Output: `{
			"permission":{
				"approved":true,
				"status":"approved",
				"reason":"approved by user"
			},
			"tool":{
				"name":"exit_plan_mode",
				"arguments":"{\"title\":\"Implementation Plan\"}"
			}
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "exit_plan_mode approved · Implementation Plan") {
		t.Fatalf("rendered entry missing exit-plan approved headline: %q", rendered)
	}
	if strings.Contains(rendered, "message:") {
		t.Fatalf("rendered entry should omit default approval reason line: %q", rendered)
	}
}

func TestBuildToolStreamLines_ExitPlanShowsExpandedMetadata(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.addToolStreamEntry(chatToolStreamEntry{
		ToolName: "exit_plan_mode",
		Output: `{
			"tool":"exit_plan_mode",
			"status":"approved",
			"title":"Implementation Plan",
			"plan_id":"plan_123",
			"target_mode":"auto",
			"user_message":"Ship it"
		}`,
		State: "done",
	})

	lines := page.buildToolStreamLines(120)
	joined := joinRenderLines(lines)
	if !strings.Contains(joined, "plan: plan_123") {
		t.Fatalf("toolstream missing plan id line: %q", joined)
	}
	if !strings.Contains(joined, "next mode: auto") {
		t.Fatalf("toolstream missing next mode line: %q", joined)
	}
	if !strings.Contains(joined, "feedback: Ship it") {
		t.Fatalf("toolstream missing feedback line: %q", joined)
	}
}

func TestFormatUnifiedToolEntry_ManageTodosListShowsTodoItems(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "manage_todos",
		Output: `{
			"tool":"manage_todos",
			"action":"list",
			"owner_kind":"user",
			"items":[
				{"id":"todo_1","text":"Ship desktop todo rendering","done":false,"in_progress":true,"priority":"high","group":"ux","tags":["desktop"]},
				{"id":"todo_2","text":"Ship tui todo rendering","done":true,"priority":"medium","tags":["tui"]}
			]
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "manage_todos [user] list") {
		t.Fatalf("rendered entry missing manage_todos headline: %q", rendered)
	}
	if !strings.Contains(rendered, "ux  ·  #desktop\n> [ ] Ship desktop todo rendering  ·  high") {
		t.Fatalf("rendered entry missing first todo preview: %q", rendered)
	}
	if !strings.Contains(rendered, "[x] Ship tui todo rendering  ·  medium") {
		t.Fatalf("rendered entry missing second todo preview: %q", rendered)
	}
	if strings.Contains(rendered, `"items"`) {
		t.Fatalf("rendered entry should not expose raw todo JSON: %q", rendered)
	}
}

func TestBuildToolStreamLines_ManageTodosShowsHumanReadableTodos(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.addToolStreamEntry(chatToolStreamEntry{
		ToolName: "manage_todos",
		Output: `{
			"tool":"manage_todos",
			"action":"summary",
			"summary":{
				"task_count":5,
				"open_count":3,
				"in_progress_count":1,
				"user":{"task_count":2,"open_count":1,"in_progress_count":0},
				"agent":{"task_count":3,"open_count":2,"in_progress_count":1}
			}
		}`,
		State: "done",
	})

	lines := page.buildToolStreamLines(140)
	joined := joinRenderLines(lines)
	if !strings.Contains(joined, "manage_todos summary (3 open · 5 total, 1 in progress)") {
		t.Fatalf("toolstream missing manage_todos headline with counts: %q", joined)
	}
	if !strings.Contains(joined, "All Todos: 3 open · 5 total · 1 in progress") {
		t.Fatalf("toolstream missing overall todo counts: %q", joined)
	}
	if !strings.Contains(joined, "Agent Checklist: 2 open · 3 total · 1 in progress") {
		t.Fatalf("toolstream missing agent todo counts: %q", joined)
	}
}

func TestBuildToolStreamLines_ManageTodosBatchShowsOnlyChangedItems(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.addToolStreamEntry(chatToolStreamEntry{
		ToolName: "manage_todos",
		Output: `{
			"tool":"manage_todos",
			"action":"batch",
			"operation_count":2,
			"operations":[
				{"action":"update","id":"todo_focus"},
				{"action":"update","id":"todo_done"}
			],
			"summary":{"task_count":6,"open_count":4,"in_progress_count":2},
			"results":[
				{"index":0,"action":"update","id":"todo_focus","item":{"id":"todo_focus","text":"Focused changed item","done":false,"in_progress":true,"priority":"high","group":"flow","tags":["focus"]}},
				{"index":1,"action":"update","id":"todo_done","item":{"id":"todo_done","text":"Completed changed item","done":true,"priority":"low","tags":["done"]}}
			],
			"items":[
				{"id":"todo_old","text":"Old top item","done":false,"priority":"urgent"},
				{"id":"todo_focus","text":"Focused changed item","done":false,"in_progress":true,"priority":"high","group":"flow","tags":["focus"]},
				{"id":"todo_done","text":"Completed changed item","done":true,"priority":"low","tags":["done"]}
			]
		}`,
		State: "done",
	})

	lines := page.buildToolStreamLines(140)
	joined := joinRenderLines(lines)
	if !strings.Contains(joined, "manage_todos batch (2 ops, 4 open · 6 total, 2 in progress)") {
		t.Fatalf("toolstream missing batch summary counts: %q", joined)
	}
	if !strings.Contains(joined, "Focused changed item") {
		t.Fatalf("toolstream missing changed in-progress item: %q", joined)
	}
	if !strings.Contains(joined, "[x] Completed changed item  ·  low") {
		t.Fatalf("toolstream missing completed changed item: %q", joined)
	}
	if strings.Contains(joined, "Old top item") {
		t.Fatalf("toolstream should not show unrelated todos for batch previews: %q", joined)
	}
	if strings.Contains(joined, "All Todos: 4 open · 6 total · 2 in progress") {
		t.Fatalf("toolstream should not expand batch previews into global summary lines: %q", joined)
	}
}

func TestRenderTaskToolTableLines_ReasoningMapsToThinkingWithoutLeak(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "running",
		Metadata: map[string]any{
			chatToolTimelineObjectMetadataKey:    true,
			chatToolTimelinePayloadMetadataKey:   `{"tool":"task","status":"running","launches":[{"launch_index":1,"subagent":"explorer","status":"running","current_preview_kind":"reasoning","current_preview_text":"<reasoning>Inspecting files</reasoning>","reasoning_summary":"Inspecting files before edit"}]}`,
			chatToolTimelineStartedAtMetadataKey: int64(100),
		},
	}
	payload, ok := toolTimelinePayload(message)
	if !ok {
		t.Fatalf("expected managed tool payload")
	}
	lines := page.renderTaskToolTableLines(message, payload, 120)
	joinedParts := make([]string, 0, len(lines))
	for _, line := range lines {
		joinedParts = append(joinedParts, line.Text)
	}
	joined := strings.Join(joinedParts, "\n")
	if !strings.Contains(joined, "thinking") {
		t.Fatalf("task rows should show thinking label/tool: %q", joined)
	}
	if strings.Contains(joined, "reasoning:") || strings.Contains(joined, "Inspecting files before edit") || strings.Contains(joined, "<reasoning>") {
		t.Fatalf("task rows leaked reasoning preview: %q", joined)
	}
}

func TestRenderTaskToolTableLines_AssistantPreviewHidden(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	message := chatMessageItem{
		Role:      "tool",
		ToolState: "running",
		Metadata: map[string]any{
			chatToolTimelineObjectMetadataKey:    true,
			chatToolTimelinePayloadMetadataKey:   `{"tool":"task","status":"running","launches":[{"launch_index":1,"subagent":"clone","status":"running","current_preview_kind":"assistant","current_preview_text":"No Shore Between"}]}`,
			chatToolTimelineStartedAtMetadataKey: int64(100),
		},
	}
	payload, ok := toolTimelinePayload(message)
	if !ok {
		t.Fatalf("expected managed tool payload")
	}
	lines := page.renderTaskToolTableLines(message, payload, 120)
	joinedParts := make([]string, 0, len(lines))
	for _, line := range lines {
		joinedParts = append(joinedParts, line.Text)
	}
	joined := strings.Join(joinedParts, "\n")
	if strings.Contains(joined, "No Shore Between") || strings.Contains(joined, "assistant:") {
		t.Fatalf("task rows leaked assistant preview: %q", joined)
	}
}

func TestFormatUnifiedToolEntry_ManageTodosAgentListShowsOnlyCurrentSession(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "manage_todos",
		Output: `{
			"tool":"manage_todos",
			"action":"list",
			"owner_kind":"agent",
			"session_id":"session-1",
			"items":[
				{"id":"todo_local","text":"Local agent item","done":false,"session_id":"session-1"},
				{"id":"todo_other","text":"Other session item","done":false,"session_id":"session-2"}
			]
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "Local agent item") {
		t.Fatalf("rendered entry missing current-session todo: %q", rendered)
	}
	if strings.Contains(rendered, "Other session item") {
		t.Fatalf("rendered entry should not show other-session todos: %q", rendered)
	}
}

func TestShouldSuppressHistoricalToolEntry_PermissionGateResult(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "bash",
		Output: `{
			"permission":{"approved":false,"status":"denied","reason":"no"},
			"tool":{"name":"bash","arguments":"{\"command\":\"rm -rf /tmp/demo\"}"}
		}`,
	}
	if !shouldSuppressHistoricalToolEntry(entry) {
		t.Fatalf("expected handled permission gate history to be suppressed")
	}
}

func TestShouldSuppressHistoricalToolEntry_NonPermissionToolResultNotSuppressed(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "read",
		Output:   `{"path":"/tmp/demo.txt","count":3,"summary":"read /tmp/demo.txt"}`,
	}
	if shouldSuppressHistoricalToolEntry(entry) {
		t.Fatalf("expected ordinary tool history to remain visible")
	}
}

func TestIngestMessageRecord_SuppressesHistoricalPermissionGateToolMessage(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.ingestMessageRecord(ChatMessageRecord{
		Role: "tool",
		Content: `{
			"path_id":"run.tool-history.v2",
			"tool":"bash",
			"call_id":"call_perm_1",
			"completed_output":"{\"permission\":{\"approved\":false,\"status\":\"denied\",\"reason\":\"no\"},\"tool\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"rm -rf /tmp/demo\\\"}\"}}"
		}`,
	})
	if len(page.timeline) != 0 {
		t.Fatalf("expected suppressed historical permission gate to avoid timeline replay, got %d messages", len(page.timeline))
	}
	if len(page.toolStream) != 0 {
		t.Fatalf("expected suppressed historical permission gate to avoid toolstream replay, got %d entries", len(page.toolStream))
	}
}

func TestIngestMessageRecord_PrefersLiveToolCompletionOverHistoricalReplay(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	atUnix := time.Now().UnixMilli()
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:       "tool.completed",
		ToolName:   "manage_todos",
		CallID:     "call_manage_1",
		Output:     `{"tool":"manage_todos","action":"update","summary":{"total":1}}`,
		RawOutput:  `{"tool":"manage_todos","action":"update","summary":{"total":1}}`,
		DurationMS: 42,
	}, atUnix)

	if len(page.timeline) != 1 {
		t.Fatalf("expected live tool completion to append one timeline item, got %d", len(page.timeline))
	}

	page.ingestMessageRecord(ChatMessageRecord{
		Role: "tool",
		Content: `{
			"path_id":"run.tool-history.v2",
			"tool":"manage_todos",
			"call_id":"call_manage_1",
			"completed_output":"{\"tool\":\"manage_todos\",\"action\":\"update\",\"summary\":{\"total\":1}}"
		}`,
		CreatedAt: atUnix + 1,
	})

	if len(page.timeline) != 1 {
		t.Fatalf("expected historical replay for completed live tool to be suppressed, got %d timeline items", len(page.timeline))
	}
	if len(page.toolStream) != 1 {
		t.Fatalf("expected historical replay to merge into existing tool stream entry, got %d entries", len(page.toolStream))
	}
}

func TestIngestMessageRecord_RestoresHistoricalBashOutputViewer(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	fullOutput := strings.Repeat("restored line\n", 120)
	escapedOutput := strings.ReplaceAll(fullOutput, "\n", "\\n")

	page.ingestMessageRecord(ChatMessageRecord{
		Role: "tool",
		Content: `{
			"path_id":"run.tool-history.v2",
			"tool":"bash",
			"call_id":"call_bash_restore",
			"arguments":"{\"command\":\"printf restored\"}",
			"output":"` + escapedOutput + `",
			"completed_output":"bash printf restored (output in timeline)"
		}`,
	})

	if got := strings.TrimSpace(page.bashOutput.Output); got != strings.TrimSpace(fullOutput) {
		t.Fatalf("restored bash output = %q, want persisted full output", got)
	}
	if !page.ToggleInlineBashOutputExpanded() {
		t.Fatalf("expected restored /output viewer to open")
	}
	if got := strings.TrimSpace(page.bashOutput.Command); got != "printf restored" {
		t.Fatalf("restored bash command = %q", got)
	}
}

func TestRenderToolMessageLines_SearchPayloadShowsGroupedMatchTable(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	entry := chatToolStreamEntry{
		ToolName: "search",
		Output: `{
			"path_id":"tool.search.v1",
			"search_mode":"content",
			"path":"internal/ui",
			"queries":["executeSearch","ExecuteBatch","toolCountLabel"],
			"query_count":3,
			"count":5,
			"total_matched":5,
			"matches":[
				{"query":"executeSearch","relative_path":"tool/runtime.go","line":1103,"text":"func (r *Runtime) executeSearch(...)"},
				{"query":"ExecuteBatch","relative_path":"tool/runtime.go","line":625,"text":"func (r *Runtime) ExecuteBatch(...)"},
				{"query":"toolCountLabel","relative_path":"tool/runtime.go","line":1400,"text":"toolCountLabel(...)"},
				{"query":"toolCountLabel","relative_path":"tool/runtime.go","line":1408,"text":"toolCountLabel(...)"},
				{"query":"executeSearch","relative_path":"tool/other.go","line":50,"text":"executeSearch(...)"}
			],
			"query_results":[
				{"query":"executeSearch","count":2,"total_matched":2},
				{"query":"ExecuteBatch","count":1,"total_matched":1},
				{"query":"toolCountLabel","count":2,"total_matched":2}
			]
		}`,
		State: "done",
	}
	message := chatMessageItem{
		Role:      "tool",
		Text:      formatUnifiedToolEntry(entry),
		ToolState: "done",
		Metadata:  toolTimelineMetadata(entry),
	}

	lines := page.renderToolMessageLines(message, 140)
	if len(lines) < 4 {
		t.Fatalf("len(lines) = %d, want >= 4", len(lines))
	}
	body := strings.Join([]string{lines[2].Text, lines[3].Text}, "\n")
	if strings.Count(body, "tool/runtime.go") != 1 {
		t.Fatalf("expected grouped single runtime.go row, got %q", body)
	}
	if !strings.Contains(body, "executeSearch") || !strings.Contains(body, "ExecuteBatch") || !strings.Contains(body, "toolCountLabel") {
		t.Fatalf("missing grouped query labels: %q", body)
	}
	if !strings.Contains(body, "1103") || !strings.Contains(body, "625") || !strings.Contains(body, "1400") {
		t.Fatalf("missing grouped line summary: %q", body)
	}
	if strings.Contains(body, "func (r *Runtime)") {
		t.Fatalf("should not dump matched code text in grouped timeline table: %q", body)
	}
}

func TestBuildToolStreamLines_SearchPayloadShowsGroupedMatchTable(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.addToolStreamEntry(chatToolStreamEntry{
		ToolName: "search",
		Output: `{
			"path_id":"tool.search.v1",
			"search_mode":"content",
			"path":"internal/ui",
			"queries":["executeSearch","ExecuteBatch","toolCountLabel"],
			"query_count":3,
			"count":5,
			"total_matched":5,
			"matches":[
				{"query":"executeSearch","relative_path":"tool/runtime.go","line":1103,"text":"func (r *Runtime) executeSearch(...)"},
				{"query":"ExecuteBatch","relative_path":"tool/runtime.go","line":625,"text":"func (r *Runtime) ExecuteBatch(...)"},
				{"query":"toolCountLabel","relative_path":"tool/runtime.go","line":1400,"text":"toolCountLabel(...)"},
				{"query":"toolCountLabel","relative_path":"tool/runtime.go","line":1408,"text":"toolCountLabel(...)"},
				{"query":"executeSearch","relative_path":"tool/other.go","line":50,"text":"executeSearch(...)"}
			],
			"query_results":[
				{"query":"executeSearch","count":2,"total_matched":2},
				{"query":"ExecuteBatch","count":1,"total_matched":1},
				{"query":"toolCountLabel","count":2,"total_matched":2}
			]
		}`,
		State: "done",
	})

	lines := page.buildToolStreamLines(140)
	body := make([]string, 0, len(lines))
	for _, line := range lines {
		body = append(body, line.Text)
	}
	joined := strings.Join(body, "\n")
	if strings.Count(joined, "tool/runtime.go") != 1 {
		t.Fatalf("expected grouped single runtime.go row in toolstream, got %q", joined)
	}
	if !strings.Contains(joined, "executeSearch") || !strings.Contains(joined, "ExecuteBatch") || !strings.Contains(joined, "toolCountLabel") {
		t.Fatalf("missing grouped query labels in toolstream: %q", joined)
	}
	if !strings.Contains(joined, "1103") || !strings.Contains(joined, "625") || !strings.Contains(joined, "1400") {
		t.Fatalf("missing grouped line summary in toolstream: %q", joined)
	}
	if strings.Contains(joined, "func (r *Runtime)") {
		t.Fatalf("toolstream should not dump matched code text: %q", joined)
	}
}

func TestFormatUnifiedToolEntry_SearchPayloadUsesSearchLabelAndMetadata(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "search",
		Output: `{
			"path_id":"tool.search.v1",
			"search_mode":"content",
			"path":"/repo",
			"query":"executeSearch",
			"queries":["executeSearch","ExecuteBatch"],
			"query_count":2,
			"count":2,
			"total_matched":14,
			"merge_strategy":"round_robin_by_query",
			"matches":[
				{"query":"executeSearch","relative_path":"tool/runtime.go","line":1103,"text":"func (r *Runtime) executeSearch(...)"},
				{"query":"ExecuteBatch","relative_path":"tool/runtime.go","line":625,"text":"func (r *Runtime) ExecuteBatch(...)"}
			],
			"query_results":[
				{"query":"executeSearch","count":9,"total_matched":9},
				{"query":"ExecuteBatch","count":5,"total_matched":5}
			]
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if strings.Contains(rendered, "websearch") {
		t.Fatalf("search entry rendered as websearch: %q", rendered)
	}
	if !strings.Contains(rendered, "search") {
		t.Fatalf("rendered entry missing search label: %q", rendered)
	}
	if !strings.Contains(rendered, "across 2 queries") {
		t.Fatalf("rendered entry missing aggregate metadata: %q", rendered)
	}
	if !strings.Contains(rendered, "Query") || !strings.Contains(rendered, "Path") || !strings.Contains(rendered, "Ln") {
		t.Fatalf("rendered entry missing table header: %q", rendered)
	}
	if !strings.Contains(rendered, "executeSearch") {
		t.Fatalf("rendered entry missing per-query column value: %q", rendered)
	}
	if !strings.Contains(rendered, "tool/runtime.go") || !strings.Contains(rendered, "1103") {
		t.Fatalf("rendered entry missing filepath/line row: %q", rendered)
	}
	if strings.Contains(rendered, "func (r *Runtime)") {
		t.Fatalf("rendered entry should not dump matched code text: %q", rendered)
	}
}

func TestFormatUnifiedToolEntry_BashStructuredOutputPrefersParsedPayloadOverRawJSONBlob(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "bash",
		Output:   `{"command":"git status --short","output":"M internal/ui/chat_component_toolstream.go\n","exit_code":0,"timed_out":false,"truncated":false}`,
		Raw:      `{"path_id":"run.tool-history.v2","tool":"bash","call_id":"call_bash_1","arguments":"{\"command\":\"git status --short\"}","output":"{\"command\":\"git status --short\",\"output\":\"M internal/ui/chat_component_toolstream.go\\n\",\"exit_code\":0,\"timed_out\":false,\"truncated\":false}","completed_output":"bash git status --short (output in timeline)"}`,
		State:    "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "bash git status --short") {
		t.Fatalf("rendered entry missing bash headline: %q", rendered)
	}
	if !strings.Contains(rendered, "M internal/ui/chat_component_toolstream.go") {
		t.Fatalf("rendered entry missing parsed bash output: %q", rendered)
	}
	if strings.Contains(rendered, `"path_id":"run.tool-history.v2"`) {
		t.Fatalf("rendered entry should not expose raw history JSON blob: %q", rendered)
	}
}

func TestFormatUnifiedToolEntry_BashPermissionEnvelopeRendersFriendlySummary(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "bash",
		Output: `{
			"permission":{"approved":true,"status":"approved","reason":"approved by user"},
			"tool":{"name":"bash","arguments":"{\"command\":\"git status --short\"}"}
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "bash approved · git status --short") {
		t.Fatalf("rendered entry missing friendly bash permission summary: %q", rendered)
	}
	if strings.Contains(rendered, `"permission"`) {
		t.Fatalf("rendered entry should not expose raw permission JSON: %q", rendered)
	}
	if strings.Contains(rendered, "approved by user") {
		t.Fatalf("rendered entry should omit default approval reason: %q", rendered)
	}
}

func TestShouldSuppressHistoricalToolEntry_BashPermissionGateResultRemainsVisible(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "bash",
		Output: `{
			"permission":{"approved":false,"status":"denied","reason":"Need a safer command"},
			"tool":{"name":"bash","arguments":"{\"command\":\"rm -rf /tmp/demo\"}"}
		}`,
	}
	if shouldSuppressHistoricalToolEntry(entry) {
		t.Fatalf("expected bash permission result to stay visible for friendly rendering")
	}
}

func TestFormatUnifiedToolEntry_WebSearchStaysWebSearch(t *testing.T) {
	entry := chatToolStreamEntry{
		ToolName: "websearch",
		Output: `{
			"path_id":"tool.websearch.exa.v1",
			"queries":["exa docs"],
			"query_count":1,
			"total_results":3,
			"results":[
				{
					"query":"exa docs",
					"count":3,
					"results":[{"title":"Docs","url":"https://docs.exa.ai/reference/search","published_date":"2025-01-01"}]
				}
			]
		}`,
		State: "done",
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "websearch") {
		t.Fatalf("rendered entry missing websearch label: %q", rendered)
	}
	if strings.Contains(rendered, "search \"") {
		t.Fatalf("websearch entry should not look like repo search: %q", rendered)
	}
}
