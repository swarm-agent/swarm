package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestApplySessionLifecycleActiveDoesNotClearUserAbortFlag(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.runAbort = true
	page.busy = true
	page.ownedRunID = "run-1"

	page.ApplySessionLifecycle(ChatSessionLifecycle{
		SessionID: "session-test",
		RunID:     "run-1",
		Active:    true,
		Phase:     "running",
	})

	if !page.runAbort {
		t.Fatal("active lifecycle update cleared runAbort before stop completed")
	}
}

func TestApplySessionLifecycleCompletedPreservesLiveAssistantUntilRunSuccess(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.liveAssistant = "streamed partial response"
	page.streamingRun = true
	page.busy = true
	page.ownedRunID = "run-1"

	page.ApplySessionLifecycle(ChatSessionLifecycle{
		SessionID: "session-test",
		RunID:     "run-1",
		Active:    false,
		Phase:     "completed",
	})

	if got := page.liveAssistant; got != "streamed partial response" {
		t.Fatalf("live assistant cleared on completed lifecycle: %q", got)
	}
	if got := page.ownedRunID; got != "run-1" {
		t.Fatalf("owned run id cleared too early: %q", got)
	}
}

func TestCachedLiveAssistantLinesReuseRecentParseResult(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.liveAssistant = "Hello **world**"

	first := page.cachedLiveAssistantLines(80)
	if len(first) == 0 {
		t.Fatal("expected rendered live assistant lines")
	}

	entry := page.liveAssistantRenderCache
	entry.LastParseAt = time.Now()
	page.liveAssistantRenderCache = entry

	second := page.cachedLiveAssistantLines(80)
	if len(second) == 0 {
		t.Fatal("expected cached live assistant lines")
	}
	if page.liveAssistantRenderCache.LastParseAt != entry.LastParseAt {
		t.Fatal("expected cached path to avoid reparsing within min interval")
	}
}

func TestV3LiveAssistantPatchesAccumulateAtMessageLevelAndRejectDuplicates(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-test", SessionMode: "auto", AuthConfigured: true})
	page.runCancel = func() {}

	first := ChatRunStreamEvent{Type: "assistant.live.delta", SessionID: "session-test", RunID: "run-1", StreamID: "assistant:run-1:step:1", StreamKind: "assistant_text", Operation: "append", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: 5, Delta: "hello"}
	if !page.ApplySharedStreamEvent(first, 100) {
		t.Fatal("first live patch was not applied")
	}
	if got := page.liveAssistant; got != "hello" {
		t.Fatalf("first live assistant = %q", got)
	}
	if page.ApplySharedStreamEvent(first, 100) {
		t.Fatal("duplicate live patch was reported as applied")
	}
	second := first
	second.LiveSeqStart, second.LiveSeqEnd = 2, 2
	second.OffsetStart, second.OffsetEnd = 5, 11
	second.Delta = " world"
	if !page.ApplySharedStreamEvent(second, 110) {
		t.Fatal("second live patch was not applied")
	}
	if got := page.liveAssistant; got != "hello world" {
		t.Fatalf("accumulated live assistant = %q", got)
	}
	if got := page.ownedRunID; got != "run-1" {
		t.Fatalf("owned run id = %q", got)
	}
}

func TestV3LiveAssistantPatchCompletesReasoningInTimelineOrder(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-test", SessionMode: "auto", AuthConfigured: true})
	page.runCancel = func() {}
	page.startReasoningSegment(100)
	page.updateThinkingTimelineMessage("checking files", 101)

	patch := ChatRunStreamEvent{Type: "assistant.live.delta", SessionID: "session-test", RunID: "run-1", StreamID: "assistant:run-1:step:1", StreamKind: "assistant_text", Operation: "append", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: 2, Delta: "ok"}
	if !page.ApplySharedStreamEvent(patch, 110) {
		t.Fatal("live patch was not applied")
	}
	if page.reasoningActive {
		t.Fatal("reasoning remained active after assistant text began")
	}
	reasoningIndex := -1
	for i := range page.timeline {
		if page.timeline[i].Role == "reasoning" {
			reasoningIndex = i
			break
		}
	}
	if reasoningIndex < 0 || page.timeline[reasoningIndex].ToolState != "done" {
		t.Fatalf("reasoning timeline = %#v", page.timeline)
	}
	if page.liveAssistant != "ok" {
		t.Fatalf("live assistant = %q", page.liveAssistant)
	}
}

func TestLiveAssistantStreamingUsesMarkdownRenderer(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.liveAssistant = "# Streaming Title\n\n- first item\n- second item\n\n```go\nfunc main() {}\n```"
	page.streamingRun = true
	page.busy = true
	page.ownedRunID = "run-1"
	page.lifecycle = &ChatSessionLifecycle{
		SessionID: "session-test",
		RunID:     "run-1",
		Active:    true,
		Phase:     "running",
	}

	lines := page.renderLiveAssistantLines(100)
	if len(lines) == 0 {
		t.Fatal("expected streaming assistant lines")
	}
	rendered := chatRenderLinesText(lines)
	if !containsAll(rendered, []string{"Streaming Title", "first item", "second item", "func main()"}) {
		t.Fatalf("streaming markdown content missing:\n%s", rendered)
	}
	if strings.Contains(rendered, "```go") {
		t.Fatalf("streaming code fence was not parsed as markdown:\n%s", rendered)
	}
	if !renderLinesContainStyle(lines, page.theme.MarkdownHeading) {
		t.Fatalf("streaming heading was not markdown-styled:\n%s", rendered)
	}
	if !renderLinesContainStyle(lines, page.theme.MarkdownList) {
		t.Fatalf("streaming list was not markdown-styled:\n%s", rendered)
	}
}

func TestLiveAssistantStreamingCopyBlocksUseCopyAwareMarkdown(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.liveAssistant = "Here is a block:\n\n<copy label=\"cmd\">swarm status</copy>"
	page.streamingRun = true
	page.busy = true
	page.ownedRunID = "run-1"
	page.lifecycle = &ChatSessionLifecycle{
		SessionID: "session-test",
		RunID:     "run-1",
		Active:    true,
		Phase:     "running",
	}

	lines := page.renderLiveAssistantLines(100)
	rendered := chatRenderLinesText(lines)
	if !strings.Contains(rendered, "/copy 1 · cmd") {
		t.Fatalf("streaming copy block marker missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "swarm status") {
		t.Fatalf("streaming copy block preview missing:\n%s", rendered)
	}
}

func TestStreamingMarkdownDrawRemainsVisibleAcrossCompletedLifecycleAndFinalPersist(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
		ShowHeader:     true,
	})
	page.liveAssistant = "# Title\n\n- item one\n- item two"
	page.streamingRun = true
	page.busy = true
	page.ownedRunID = "run-1"

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 24)

	page.Draw(screen)
	before := dumpScreenText(screen, 100, 24)
	if !containsAll(before, []string{"Title", "item one", "item two"}) {
		t.Fatalf("streamed markdown missing before completion:\n%s", before)
	}

	page.ApplySessionLifecycle(ChatSessionLifecycle{
		SessionID: "session-test",
		RunID:     "run-1",
		Active:    false,
		Phase:     "completed",
	})
	page.Draw(screen)
	mid := dumpScreenText(screen, 100, 24)
	if !containsAll(mid, []string{"Title", "item one", "item two"}) {
		t.Fatalf("streamed markdown missing after completed lifecycle:\n%s", mid)
	}

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:      "message.stored",
		SessionID: "session-test",
		RunID:     "run-1",
		Message: &ChatMessageRecord{
			ID:        "msg-assistant",
			SessionID: "session-test",
			Role:      "assistant",
			Content:   "# Title\n\n- item one\n- item two",
			CreatedAt: time.Now().UnixMilli(),
			Metadata:  map[string]any{"run_id": "run-1"},
		},
	}, time.Now().UnixMilli())
	page.Draw(screen)
	after := dumpScreenText(screen, 100, 24)
	if !containsAll(after, []string{"Title", "item one", "item two"}) {
		t.Fatalf("final markdown missing after success:\n%s", after)
	}
}

func TestCachedLiveAssistantLinesReparseAfterShortStreamingInterval(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		SessionMode:    "auto",
		AuthConfigured: true,
	})
	page.liveAssistant = "First line"

	first := page.cachedLiveAssistantLines(80)
	if len(first) == 0 {
		t.Fatal("expected initial rendered live assistant lines")
	}

	page.liveAssistant = "First line\nSecond line"
	entry := page.liveAssistantRenderCache
	entry.LastParseAt = time.Now().Add(-40 * time.Millisecond)
	page.liveAssistantRenderCache = entry

	second := page.cachedLiveAssistantLines(80)
	if len(second) <= len(first) {
		t.Fatalf("expected reparsed output to grow after short interval: first=%d second=%d", len(first), len(second))
	}
	if page.liveAssistantRenderCache.ParsedText != strings.TrimSpace(page.liveAssistant) {
		t.Fatalf("expected cache parsed text to refresh, got %q", page.liveAssistantRenderCache.ParsedText)
	}
}

func containsAll(text string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}

func renderLinesContainStyle(lines []chatRenderLine, style tcell.Style) bool {
	for _, line := range lines {
		if markdownStylesEqualExact(line.Style, style) {
			return true
		}
		for _, span := range line.Spans {
			if markdownStylesEqualExact(span.Style, style) {
				return true
			}
		}
	}
	return false
}

func TestV3ToolLifecycleMessagesUpdateOneTimelineEntryWithActualResult(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-test"})
	messages := []ChatMessageRecord{
		{
			ID:        "event-started",
			SessionID: "session-test",
			GlobalSeq: 2,
			Role:      "tool",
			Content:   `{"type":"session.tool.started","tool_name":"read","call_id":"call-read","tool_instance_id":"step-1:call-read","arguments":"{\"path\":\"facts.go\"}","status":"started"}`,
			Metadata:  map[string]any{"v3_tool_event": true},
			CreatedAt: 200,
		},
		{
			ID:        "assistant-progress",
			SessionID: "session-test",
			GlobalSeq: 3,
			Role:      "assistant",
			Content:   "checking the result",
			CreatedAt: 300,
		},
		{
			ID:        "event-completed",
			SessionID: "session-test",
			GlobalSeq: 4,
			Role:      "tool",
			Content:   `{"type":"session.tool.completed","tool_name":"read","call_id":"call-read","tool_instance_id":"step-1:call-read","output":"actual result line","raw_output":"actual result line","status":"completed","duration_ms":21}`,
			Metadata:  map[string]any{"v3_tool_event": true},
			CreatedAt: 400,
		},
	}

	page.SetMessages(messages)
	if len(page.toolStream) != 1 {
		t.Fatalf("tool lifecycle should reduce to one stream entry, got %#v", page.toolStream)
	}
	entry := page.toolStream[0]
	if entry.EntryKey != "step-1:call-read" || entry.State != "done" || entry.Output != "actual result line" || entry.DurationMS != 21 {
		t.Fatalf("reduced tool result = %#v", entry)
	}
	managed := make([]chatMessageItem, 0, len(page.timeline))
	for _, item := range page.timeline {
		if isManagedToolTimelineMessage(item) {
			managed = append(managed, item)
		}
	}
	if len(managed) != 1 || managed[0].ToolState != "done" {
		t.Fatalf("managed timeline did not render completed tool state: %#v", managed)
	}
	if len(page.timeline) != 2 || page.timeline[0].Role != "tool" || page.timeline[1].Role != "assistant" {
		t.Fatalf("tool lifecycle lost its authoritative position around assistant text: %#v", page.timeline)
	}
	payload, _ := managed[0].Metadata[chatToolTimelinePayloadMetadataKey].(string)
	if !strings.Contains(payload, "actual result line") {
		t.Fatalf("managed timeline omitted actual result payload: %#v", managed)
	}

	page.SetMessages(messages)
	managed = managed[:0]
	for _, item := range page.timeline {
		if isManagedToolTimelineMessage(item) {
			managed = append(managed, item)
		}
	}
	if len(page.toolStream) != 1 || len(managed) != 1 {
		t.Fatalf("hydrated replacement duplicated tool lifecycle: tools=%#v timeline=%#v", page.toolStream, page.timeline)
	}
}

func TestV3ToolFailedMessageRendersActualError(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-test"})
	page.SetMessages([]ChatMessageRecord{{
		ID:        "event-failed",
		SessionID: "session-test",
		GlobalSeq: 2,
		Role:      "tool",
		Content:   `{"type":"session.tool.failed","tool_name":"read","call_id":"call-read","tool_instance_id":"step-1:call-read","error":"permission denied by provider","status":"failed"}`,
		Metadata:  map[string]any{"v3_tool_event": true},
		CreatedAt: 200,
	}})
	if len(page.toolStream) != 1 || page.toolStream[0].State != "error" || page.toolStream[0].Error != "permission denied by provider" {
		t.Fatalf("failed tool entry = %#v", page.toolStream)
	}
	if len(page.timeline) != 1 || !strings.Contains(page.timeline[0].Text, "permission denied by provider") {
		t.Fatalf("failed tool timeline omitted error: %#v", page.timeline)
	}
}

func TestParseToolStreamEntryMapsV3ProviderToolResult(t *testing.T) {
	content := `{"path_id":"run.v3.provider-tool-result.v1","type":"tool.completed","tool_name":"read","call_id":"call-shared","tool_instance_id":"step-7:call-shared","arguments":"{\"path\":\"facts.go\"}","output":"{\"count\":1,\"lines\":[{\"line\":12,\"text\":\"hello world\"}]}","completed_output":"{\"count\":1,\"lines\":[{\"line\":12,\"text\":\"hello world\"}]}","duration_ms":42}`

	entry, ok := parseToolHistoryStreamEntry(content, 1234)
	if !ok {
		t.Fatal("expected V3 provider tool result to parse as tool history entry")
	}
	if entry.ToolName != "read" {
		t.Fatalf("tool name mismatch: %q", entry.ToolName)
	}
	if entry.CallID != "call-shared" {
		t.Fatalf("call id mismatch: %q", entry.CallID)
	}
	if entry.EntryKey != "step-7:call-shared" {
		t.Fatalf("entry key should preserve tool instance identity: %q", entry.EntryKey)
	}
	if entry.StartedArguments != `{"path":"facts.go"}` || !entry.StartedArgsAreJSON {
		t.Fatalf("arguments not preserved as JSON: %q json=%v", entry.StartedArguments, entry.StartedArgsAreJSON)
	}
	if entry.DurationMS != 42 {
		t.Fatalf("duration mismatch: %d", entry.DurationMS)
	}

	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "read") {
		t.Fatalf("rendered V3 provider result should look like a read tool summary, got %q", rendered)
	}
	if strings.Contains(rendered, "path_id") || strings.Contains(rendered, "run.v3.provider-tool-result.v1") {
		t.Fatalf("rendered V3 provider result leaked raw envelope JSON: %q", rendered)
	}
}

func TestParseToolStreamEntryV3ProviderToolResultPreservesInstanceIdentity(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-test"})

	for i := 1; i <= 2; i++ {
		content := fmt.Sprintf(`{"path_id":"run.v3.provider-tool-result.v1","type":"tool.completed","tool_name":"search","call_id":"call-reused","tool_instance_id":"step-%d:call-reused","arguments":"{\"query\":\"q%d\"}","completed_output":"{\"count\":%d,\"results\":[{\"path\":\"file%d.go\",\"items\":[{\"line\":%d,\"text\":\"match %d\"}]}]}","duration_ms":%d}`, i, i, i, i, i, i, i*10)
		page.ingestMessageRecord(ChatMessageRecord{
			ID:        fmt.Sprintf("msg-tool-%d", i),
			SessionID: "session-test",
			GlobalSeq: uint64(i),
			Role:      "tool",
			Content:   content,
			CreatedAt: int64(1000 + i),
		})
	}

	if len(page.toolStream) != 2 {
		t.Fatalf("expected distinct tool stream entries for reused call id, got %d", len(page.toolStream))
	}
	if page.toolStream[0].EntryKey != "message:msg-tool-1" || page.toolStream[1].EntryKey != "message:msg-tool-2" {
		t.Fatalf("historical entries should keep message identity: %#v", page.toolStream)
	}
	managed := make([]chatMessageItem, 0, len(page.timeline))
	for _, item := range page.timeline {
		if isManagedToolTimelineMessage(item) {
			managed = append(managed, item)
		}
	}
	if len(managed) != 2 {
		t.Fatalf("expected distinct timeline entries for reused call id, got %d: %#v", len(managed), page.timeline)
	}
	if managed[0].Text == managed[1].Text {
		t.Fatalf("timeline entries unexpectedly collapsed to the same text: %#v", managed)
	}
	if strings.Contains(managed[0].Text, "path_id") || strings.Contains(managed[1].Text, "path_id") {
		t.Fatalf("timeline leaked raw V3 provider envelope JSON: %#v", managed)
	}
}

func TestParseToolStreamEntryLegacyToolHistoryStillWorks(t *testing.T) {
	content := `{"path_id":"run.tool-history.v2","tool":"search","call_id":"call-search","tool_instance_id":"legacy-step:call-search","arguments":"{\"query\":\"needle\"}","completed_output":"{\"count\":1,\"results\":[{\"path\":\"file.go\",\"items\":[{\"line\":3,\"text\":\"needle\"}]}]}","duration_ms":7}`

	entry, ok := parseToolHistoryStreamEntry(content, 1234)
	if !ok {
		t.Fatal("expected legacy tool history to parse")
	}
	if entry.ToolName != "search" || entry.CallID != "call-search" {
		t.Fatalf("legacy entry identity mismatch: %#v", entry)
	}
	if entry.EntryKey != "legacy-step:call-search" {
		t.Fatalf("legacy tool instance id should be preserved: %q", entry.EntryKey)
	}
	rendered := formatUnifiedToolEntry(entry)
	if !strings.Contains(rendered, "search") || !strings.Contains(rendered, "file.go") {
		t.Fatalf("legacy rendered entry missing search summary: %q", rendered)
	}
}
