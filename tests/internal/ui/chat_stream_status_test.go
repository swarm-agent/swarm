package ui

import (
	"strings"
	"testing"
	"time"
)

func TestChatIsCodexModelUsesProvider(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:     "session-1",
		ModelProvider: "codex",
		ModelName:     "gpt-5.3",
	})
	if !page.isCodexModel() {
		t.Fatalf("isCodexModel() = false, want true when provider is codex")
	}
}

func TestChatRunStreamTurnStartedDoesNotSeedStaticCodexThinkingSummary(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:     "session-1",
		ModelProvider: "codex",
		ModelName:     "gpt-5.3",
	})
	page.busy = true
	page.runPrompt = "Build a tool matrix."
	page.runStarted = time.Now().Add(-350 * time.Millisecond)

	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "turn.started", Agent: "swarm"}, time.Now().UnixMilli())

	if got := strings.TrimSpace(page.thinkingSummary); got != "" {
		t.Fatalf("thinkingSummary = %q, want empty until real reasoning arrives", got)
	}
	if !strings.Contains(page.statusLine, "winding up") {
		t.Fatalf("statusLine = %q, want winding up transition", page.statusLine)
	}
}

func TestChatRunStreamReasoningDeltaUpdatesThinkingSummary(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:     "session-1",
		ModelProvider: "codex",
		ModelName:     "gpt-5.3",
	})
	page.busy = true

	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.started"}, time.Now().UnixMilli())
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:  "reasoning.delta",
		Delta: "**Inspecting current project state**",
	}, time.Now().UnixMilli())

	if got := strings.TrimSpace(page.thinkingSummary); got != "Inspecting current project state" {
		t.Fatalf("thinkingSummary = %q", got)
	}
}

func TestChatThinkingAnchorLineKeepsStaticIndicatorDuringStreaming(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:     "session-1",
		ModelProvider: "codex",
		ModelName:     "gpt-5.3",
	})
	page.busy = true
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:  "reasoning.delta",
		Delta: "<thinking>Thinking through the request and evaluating options.</thinking>",
	}, time.Now().UnixMilli())

	line := page.thinkingAnchorLine(80)
	if !strings.Contains(line, "Swarming") {
		t.Fatalf("thinkingAnchorLine = %q, want static swarming indicator", line)
	}
	if strings.Contains(line, "Thinking through the request and evaluating options.") {
		t.Fatalf("thinkingAnchorLine leaked reasoning text: %q", line)
	}
	if strings.Contains(line, "<thinking>") || strings.Contains(line, "</thinking>") {
		t.Fatalf("thinkingAnchorLine leaked raw tag markup: %q", line)
	}
}

func TestChatRenderLiveThinkingLinesStillShowReasoningTextWhenThinkingTagsEnabled(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:     "session-1",
		ModelProvider: "codex",
		ModelName:     "gpt-5.3",
	})
	page.busy = true
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:  "reasoning.delta",
		Delta: "<thinking>Thinking through the request and evaluating options.</thinking>",
	}, time.Now().UnixMilli())

	lines := page.renderLiveThinkingLines(80)
	if len(lines) == 0 {
		t.Fatalf("expected live thinking lines while thinking tags are enabled")
	}
	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		joined = append(joined, line.Text)
	}
	text := strings.Join(joined, "\n")
	if !strings.Contains(text, "Thinking through the request and evaluating options.") {
		t.Fatalf("live thinking lines = %q, want reasoning text", text)
	}
}

func TestChatRunStreamReasoningDeltaDedupesCumulativeChunks(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:     "session-1",
		ModelProvider: "codex",
		ModelName:     "gpt-5.3",
	})
	page.busy = true

	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.started"}, time.Now().UnixMilli())
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:  "reasoning.delta",
		Delta: "**Inspecting**",
	}, time.Now().UnixMilli())
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:  "reasoning.delta",
		Delta: "**Inspecting current project state**",
	}, time.Now().UnixMilli())

	if got := strings.TrimSpace(page.thinkingSummary); got != "Inspecting current project state" {
		t.Fatalf("thinkingSummary = %q", got)
	}
}

func TestChatRunStreamReasoningSummaryDedupesRepeatedSentence(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:     "session-1",
		ModelProvider: "codex",
		ModelName:     "gpt-5.3",
	})
	page.busy = true

	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.started"}, time.Now().UnixMilli())
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:    "reasoning.summary",
		Summary: "Inspecting current project state. Inspecting current project state.",
	}, time.Now().UnixMilli())

	if got := strings.TrimSpace(page.thinkingSummary); got != "Inspecting current project state." {
		t.Fatalf("thinkingSummary = %q", got)
	}
}

func TestChatRunStreamOwnedRunIgnoresSharedReasoningDuplicate(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", ModelProvider: "codex", ModelName: "gpt-5.3"})
	page.busy = true
	page.runCancel = func() {}
	page.ownedRunID = "run-owned"
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.started", RunID: "run-owned"}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.delta", RunID: "run-owned", Delta: "Inspecting current project state"}, now+1)

	beforeLive := page.liveThinking
	beforeSummary := page.thinkingSummary
	beforeTimeline := len(page.timeline)

	if applied := page.ApplySharedStreamEvent(ChatRunStreamEvent{Type: "reasoning.delta", SessionID: "session-1", RunID: "run-owned", Delta: "Inspecting current project state"}, now+2); applied {
		t.Fatalf("expected shared event for owned run to be ignored")
	}
	if page.liveThinking != beforeLive {
		t.Fatalf("liveThinking changed after ignored shared event: %q -> %q", beforeLive, page.liveThinking)
	}
	if page.thinkingSummary != beforeSummary {
		t.Fatalf("thinkingSummary changed after ignored shared event: %q -> %q", beforeSummary, page.thinkingSummary)
	}
	if len(page.timeline) != beforeTimeline {
		t.Fatalf("timeline len = %d, want %d", len(page.timeline), beforeTimeline)
	}
}

func TestChatRunStreamReasoningEventsDriveTimelineLifecycle(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", ModelProvider: "codex", ModelName: "gpt-5.3"})
	page.busy = true
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.started"}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.delta", Delta: "Inspecting"}, now+1)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.summary", Summary: "Inspecting current project state"}, now+2)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.completed", Summary: "Inspecting current project state"}, now+3)

	if got := strings.TrimSpace(page.thinkingSummary); got != "Inspecting current project state" {
		t.Fatalf("thinkingSummary = %q", got)
	}
	if got := strings.TrimSpace(page.liveThinking); got != "Inspecting current project state" {
		t.Fatalf("liveThinking = %q", got)
	}
	if len(page.timeline) == 0 {
		t.Fatalf("expected reasoning timeline entry")
	}
	last := page.timeline[len(page.timeline)-1]
	if role := strings.ToLower(strings.TrimSpace(last.Role)); role != "reasoning" {
		t.Fatalf("last role = %q", role)
	}
	if state := strings.ToLower(strings.TrimSpace(last.ToolState)); state != "done" {
		t.Fatalf("last reasoning state = %q", state)
	}
}

func TestChatRunStreamAssistantDeltaDedupesCumulativeMarkdownChunks(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.busy = true
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:  "assistant.delta",
		Delta: "## Plan\n- step 1",
	}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:  "assistant.delta",
		Delta: "## Plan\n- step 1\n- step 2",
	}, now+1)

	if got := page.liveAssistant; got != "## Plan\n- step 1\n- step 2" {
		t.Fatalf("liveAssistant = %q", got)
	}
}

func TestChatRunStreamAssistantDeltaPreservesWhitespaceOnlyChunks(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.busy = true
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "assistant.delta", Delta: "Hey,"}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "assistant.delta", Delta: " "}, now+1)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "assistant.delta", Delta: "I'm"}, now+2)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "assistant.delta", Delta: " "}, now+3)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "assistant.delta", Delta: "Claude Opus!"}, now+4)

	if got := page.liveAssistant; got != "Hey, I'm Claude Opus!" {
		t.Fatalf("liveAssistant = %q", got)
	}
}

func TestChatRunStreamStepStartedUpdatesStatus(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:     "session-1",
		ModelProvider: "codex",
		ModelName:     "gpt-5.3",
	})
	page.busy = true
	page.runStarted = time.Now().Add(-200 * time.Millisecond)

	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "step.started", Step: 2}, time.Now().UnixMilli())

	if !strings.Contains(page.statusLine, "thinking step 2") {
		t.Fatalf("statusLine = %q, want step transition", page.statusLine)
	}
}

func TestChatRunStreamUsageUpdatedAppliesContextSummary(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:     "session-1",
		ModelProvider: "codex",
		ModelName:     "gpt-5.3",
		ContextWindow: 1000,
	})

	page.appendMessage("user", strings.Repeat("x", 20_000), time.Now().UnixMilli())
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type: "usage.updated",
		UsageSummary: &ChatUsageSummary{
			ContextWindow:   1000,
			TotalTokens:     500,
			CacheReadTokens: 300,
			RemainingTokens: 800,
		},
	}, time.Now().UnixMilli())

	if got := page.footerContextUsageLabel(); got != "80% left" {
		t.Fatalf("footerContextUsageLabel = %q, want compact cache-adjusted usage chip", got)
	}
}

func TestChatRunStreamToolDeltaAppendsLiveOutput(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.started",
		ToolName: "bash",
		CallID:   "call_1",
	}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.delta",
		ToolName: "bash",
		CallID:   "call_1",
		Output:   "line-1",
	}, now+10)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.delta",
		ToolName: "bash",
		CallID:   "call_1",
		Output:   "line-2",
	}, now+20)

	if len(page.toolStream) != 1 {
		t.Fatalf("expected one tool stream entry, got %d", len(page.toolStream))
	}
	entry := page.toolStream[0]
	if strings.TrimSpace(entry.State) != "running" {
		t.Fatalf("expected running state, got %q", entry.State)
	}
	if entry.StartedAt != now {
		t.Fatalf("entry.StartedAt = %d, want %d", entry.StartedAt, now)
	}
	if !strings.Contains(entry.Output, "line-1") || !strings.Contains(entry.Output, "line-2") {
		t.Fatalf("expected tool delta output to accumulate, got %q", entry.Output)
	}
	if got := strings.TrimSpace(page.bashOutput.Output); got != "line-1\nline-2" {
		t.Fatalf("bash output = %q, want full streamed output", got)
	}
}

func TestChatRunStreamBashCompletionKeepsFullStreamedOutputForOutputViewer(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()
	fullOutput := strings.Repeat("line\n", 200)
	truncatedCompletion := strings.Repeat("tail\n", 20)

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:      "tool.started",
		ToolName:  "bash",
		CallID:    "call_bash_1",
		Arguments: `{"command":"printf test"}`,
	}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.delta",
		ToolName: "bash",
		CallID:   "call_bash_1",
		Output:   fullOutput,
	}, now+10)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:      "tool.completed",
		ToolName:  "bash",
		CallID:    "call_bash_1",
		Output:    truncatedCompletion,
		RawOutput: truncatedCompletion,
	}, now+20)

	if got := strings.TrimSpace(page.bashOutput.Output); got != strings.TrimSpace(fullOutput) {
		t.Fatalf("bash output = %q, want full streamed output", got)
	}
	if !page.ToggleInlineBashOutputExpanded() {
		t.Fatalf("expected /output viewer to open")
	}
	if got := strings.TrimSpace(page.bashOutput.Output); got != strings.TrimSpace(fullOutput) {
		t.Fatalf("expanded bash output = %q, want full streamed output", got)
	}
}

func TestChatRunStreamTaskDeltaMergesLaunchRowsAndKeepsOrder(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.started",
		ToolName: "task",
		CallID:   "task_1",
	}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.delta",
		ToolName: "task",
		CallID:   "task_1",
		Output:   `{"tool":"task","path_id":"tool.task.stream.v1","launch_count":3,"launches":[{"launch_index":2,"subagent":"parallel","meta_prompt":"sonnet","status":"running","phase":"running grep"}]}`,
	}, now+10)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.delta",
		ToolName: "task",
		CallID:   "task_1",
		Output:   `{"tool":"task","path_id":"tool.task.stream.v1","launch_count":3,"launches":[{"launch_index":1,"subagent":"parallel","meta_prompt":"haiku","status":"ok","phase":"completed"}]}`,
	}, now+20)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.delta",
		ToolName: "task",
		CallID:   "task_1",
		Output:   `{"tool":"task","path_id":"tool.task.stream.v1","launch_count":3,"launches":[{"launch_index":3,"subagent":"parallel","meta_prompt":"free verse","status":"running","phase":"running read"}]}`,
	}, now+30)

	if len(page.toolStream) != 1 {
		t.Fatalf("expected one tool stream entry, got %d", len(page.toolStream))
	}
	entry := page.toolStream[0]
	lines := toolPreviewLines(entry, 200, toolPreviewLineLimit(entry))
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}
	if got := strings.TrimSpace(lines[0]); got != "1. parallel — haiku [ok] · completed" {
		t.Fatalf("lines[0] = %q", got)
	}
	if got := strings.TrimSpace(lines[1]); got != "2. parallel — sonnet [running] · running grep" {
		t.Fatalf("lines[1] = %q", got)
	}
	if got := strings.TrimSpace(lines[2]); got != "3. parallel — free verse [running] · running read" {
		t.Fatalf("lines[2] = %q", got)
	}
	if entry.DurationMS != 120 {
		t.Fatalf("entry.DurationMS = %d, want 120", entry.DurationMS)
	}
	if got := page.toolEntryDurationLabel(entry); got == "" {
		t.Fatalf("toolEntryDurationLabel should be non-empty after completion fallback")
	}
	if got := page.toolEntryDurationLabel(chatToolStreamEntry{
		ToolName:  "bash",
		State:     "done",
		StartedAt: now,
		CreatedAt: now + 120,
	}); got != "120ms" {
		t.Fatalf("toolEntryDurationLabel(done) = %q, want 120ms", got)
	}
}

func TestChatRunStreamToolStartedReadSuppressesArgumentPreview(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:      "tool.started",
		ToolName:  "read",
		CallID:    "call_read_1",
		Arguments: `{"path":"/tmp/demo.txt","line_start":1,"max_lines":200}`,
	}, now)

	if len(page.toolStream) != 1 {
		t.Fatalf("expected one tool stream entry, got %d", len(page.toolStream))
	}
	entry := page.toolStream[0]
	if strings.TrimSpace(entry.State) != "running" {
		t.Fatalf("expected running state, got %q", entry.State)
	}
	if strings.TrimSpace(entry.Output) != "" {
		t.Fatalf("expected read tool.started preview to be suppressed, got %q", entry.Output)
	}
	if strings.TrimSpace(entry.Raw) != "" {
		t.Fatalf("expected read tool.started raw preview to be suppressed, got %q", entry.Raw)
	}
}

func TestChatRunStreamToolStartedGlobSuppressesArgumentPreview(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:      "tool.started",
		ToolName:  "glob",
		CallID:    "call_glob_1",
		Arguments: `{"path":".","pattern":"**/*.go","max_results":100}`,
	}, now)

	if len(page.toolStream) != 1 {
		t.Fatalf("expected one tool stream entry, got %d", len(page.toolStream))
	}
	entry := page.toolStream[0]
	if strings.TrimSpace(entry.State) != "running" {
		t.Fatalf("expected running state, got %q", entry.State)
	}
	if strings.TrimSpace(entry.Output) != "" {
		t.Fatalf("expected glob tool.started preview to be suppressed, got %q", entry.Output)
	}
	if strings.TrimSpace(entry.Raw) != "" {
		t.Fatalf("expected glob tool.started raw preview to be suppressed, got %q", entry.Raw)
	}
}

func TestChatRunStreamToolStartedGrepSuppressesArgumentPreview(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:      "tool.started",
		ToolName:  "grep",
		CallID:    "call_grep_1",
		Arguments: `{"path":".","pattern":"TODO","max_results":100}`,
	}, now)

	if len(page.toolStream) != 1 {
		t.Fatalf("expected one tool stream entry, got %d", len(page.toolStream))
	}
	entry := page.toolStream[0]
	if strings.TrimSpace(entry.State) != "running" {
		t.Fatalf("expected running state, got %q", entry.State)
	}
	if strings.TrimSpace(entry.Output) != "" {
		t.Fatalf("expected grep tool.started preview to be suppressed, got %q", entry.Output)
	}
	if strings.TrimSpace(entry.Raw) != "" {
		t.Fatalf("expected grep tool.started raw preview to be suppressed, got %q", entry.Raw)
	}
}

func TestChatRunStreamToolCompletedEditUsesCanonicalOutputForDiffPreview(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.started",
		ToolName: "edit",
		CallID:   "call_edit_1",
	}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:     "tool.completed",
		ToolName: "edit",
		CallID:   "call_edit_1",
		Output:   `{"tool":"edit","path":"/tmp/edit-tool-test.txt","replacements":1,"replace_all":false,"old_string_preview":"test","new_string_preview":"edited test","old_string_truncated":false,"new_string_truncated":false}`,
	}, now+10)

	if len(page.timeline) == 0 {
		t.Fatalf("timeline is empty, expected tool message")
	}
	last := page.timeline[len(page.timeline)-1]
	if strings.ToLower(strings.TrimSpace(last.Role)) != "tool" {
		t.Fatalf("last role = %q, want tool", last.Role)
	}
	if !strings.Contains(last.Text, "-test") || !strings.Contains(last.Text, "+edited test") {
		t.Fatalf("tool message missing edit diff preview: %q", last.Text)
	}
}

func TestChatRunStreamExitPlanToolCompletedUpdatesModeImmediately(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
		},
	})
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type:      "tool.completed",
		ToolName:  "exit_plan_mode",
		CallID:    "call_exit_plan_1",
		RawOutput: `{"tool":"exit_plan_mode","status":"approved","mode_changed":true,"target_mode":"auto"}`,
		Output:    `exit_plan_mode approved · Release Handoff`,
	}, now)

	if got := page.SessionMode(); got != "auto" {
		t.Fatalf("session mode = %q, want auto", got)
	}
	if line := page.footerInfoLine(1000); !strings.Contains(line, "mode auto") {
		t.Fatalf("footerInfoLine() = %q, want mode auto", line)
	}
}

func TestChatRunStreamDefersCodexReasoningTimelineWhileBusy(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ModelProvider:  "codex",
		ModelName:      "gpt-5.3",
		AuthConfigured: true,
	})
	baseTimeline := len(page.timeline)
	page.busy = true
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{
		Type: "message.stored",
		Message: &ChatMessageRecord{
			ID:        "msg_reasoning",
			SessionID: "session-1",
			Role:      "reasoning",
			Content:   "Inspecting current project state before applying changes.",
			CreatedAt: now,
		},
	}, now)

	if got := strings.TrimSpace(page.thinkingSummary); got != "Inspecting current project state before applying changes." {
		t.Fatalf("thinkingSummary = %q", got)
	}
	if got := strings.TrimSpace(page.liveThinking); got != "Inspecting current project state before applying changes." {
		t.Fatalf("liveThinking = %q", got)
	}
	if len(page.timeline) != baseTimeline {
		t.Fatalf("timeline entries = %d, want %d while stream is active", len(page.timeline), baseTimeline)
	}
}

func TestChatRunStreamReasoningCreatesNewSegmentAfterToolActivity(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", ModelProvider: "codex", ModelName: "gpt-5.3"})
	page.busy = true
	now := time.Now().UnixMilli()

	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.started"}, now)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.delta", Delta: "Inspecting files"}, now+1)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.completed", Summary: "Inspecting files"}, now+2)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "tool.started", ToolName: "read", CallID: "call_read_1"}, now+3)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "tool.completed", ToolName: "read", CallID: "call_read_1", Output: "read repo"}, now+4)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.started"}, now+5)
	page.applyRunStreamEvent(ChatRunStreamEvent{Type: "reasoning.delta", Delta: "Planning edit"}, now+6)

	reasoningCount := 0
	for _, item := range page.timeline {
		if strings.EqualFold(strings.TrimSpace(item.Role), "reasoning") {
			reasoningCount++
		}
	}
	if reasoningCount < 2 {
		t.Fatalf("expected at least 2 reasoning timeline entries, got %d", reasoningCount)
	}
	if got := strings.TrimSpace(page.timeline[len(page.timeline)-1].Text); got != "Planning edit" {
		t.Fatalf("last reasoning text = %q", got)
	}
}

func TestChatApplyRunSuccessAddsReasoningTimelineEntry(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ModelProvider:  "google",
		ModelName:      "gemini-2.5-pro",
		AuthConfigured: true,
	})

	page.applyRunSuccess(ChatRunResponse{
		ReasoningSummary: "Inspecting current workspace before applying edits.",
		AssistantMessage: ChatMessageRecord{
			Role:      "assistant",
			Content:   "Patch ready.",
			CreatedAt: time.Now().UnixMilli(),
		},
	})

	if len(page.timeline) < 2 {
		t.Fatalf("expected reasoning + assistant messages, got %d", len(page.timeline))
	}
	if role := strings.ToLower(strings.TrimSpace(page.timeline[len(page.timeline)-2].Role)); role != "reasoning" {
		t.Fatalf("timeline reasoning role missing, got %q", role)
	}
}

func TestChatApplyRunSuccessAppendsTargetedSubagentAssistant(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", AuthConfigured: true})
	now := time.Now().UnixMilli()

	page.applyRunSuccess(ChatRunResponse{
		TargetKind: "subagent",
		AssistantMessage: ChatMessageRecord{
			ID:        "assistant-1",
			Role:      "assistant",
			CreatedAt: now,
			Content:   "workspace scan complete",
			Metadata:  map[string]any{"source": "targeted_subagent", "subagent": "explorer"},
		},
	})

	if len(page.timeline) != 1 {
		t.Fatalf("timeline entries = %d, want 1", len(page.timeline))
	}
	if role := strings.ToLower(strings.TrimSpace(page.timeline[0].Role)); role != "assistant" {
		t.Fatalf("timeline role = %q, want assistant", role)
	}
	if text := strings.TrimSpace(page.timeline[0].Text); text != "workspace scan complete" {
		t.Fatalf("timeline text = %q, want delegated report", text)
	}
	lines := page.renderTimelineMessageLines(page.timeline[0], 80)
	if len(lines) == 0 {
		t.Fatalf("expected rendered lines for delegated report")
	}
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered = append(rendered, strings.TrimSpace(line.Text))
	}
	if got := strings.Join(rendered, "\n"); !strings.Contains(got, "@explorer") {
		t.Fatalf("rendered delegated assistant = %q, want @explorer label", got)
	}
}

func TestRenderAssistantMessageLinesLabelsTargetedSubagent(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", AuthConfigured: true})
	now := time.Now().UnixMilli()

	page.appendStoredMessageWithMetadata("assistant-1", "assistant", "workspace scan complete", map[string]any{
		"source":   "targeted_subagent",
		"subagent": "explorer",
	}, now)
	lines := page.renderTimelineMessageLines(page.timeline[0], 80)
	if len(lines) == 0 {
		t.Fatalf("expected rendered lines")
	}
	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		joined = append(joined, strings.TrimSpace(line.Text))
	}
	if got := strings.Join(joined, "\n"); !strings.Contains(got, "@explorer") {
		t.Fatalf("rendered lines = %q, want @explorer label", got)
	}
}

func TestIngestMessageRecordSkipsToolDBDebugSystemMessages(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()
	base := len(page.timeline)

	page.ingestMessageRecord(ChatMessageRecord{
		Role:      "system",
		Content:   `[tool-db-debug] {"kind":"tool.store","call_id":"call_1"}`,
		CreatedAt: now,
	})
	if len(page.timeline) != base {
		t.Fatalf("timeline should skip tool-db-debug messages, got %d entries (base %d)", len(page.timeline), base)
	}

	page.ingestMessageRecord(ChatMessageRecord{
		Role:      "system",
		Content:   "normal system note",
		CreatedAt: now + 1,
	})
	if len(page.timeline) != base+1 {
		t.Fatalf("timeline should keep normal system messages, got %d entries (base %d)", len(page.timeline), base)
	}
}

func TestIngestMessageRecordKeepsCompactionPlanLabelButNotHiddenPlanBody(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	now := time.Now().UnixMilli()

	page.ingestMessageRecord(ChatMessageRecord{
		Role:    "system",
		Content: "[context-compact] index=2 origin=manual\n\nCompacted recap:\n\nrecap text\n\nAttached plan: Execution Plan (plan_123)",
		Metadata: map[string]any{
			"context_compaction_attached_plan_label": "Execution Plan (plan_123)",
			"context_compaction_attached_plan_text":  "Plan ID: plan_123\nTitle: Execution Plan\n# Plan\n\n- [ ] hidden body",
		},
		CreatedAt: now,
	})
	if len(page.timeline) != 1 {
		t.Fatalf("timeline entries = %d, want 1", len(page.timeline))
	}
	last := page.timeline[len(page.timeline)-1]
	if !strings.Contains(last.Text, "Attached plan: Execution Plan (plan_123)") {
		t.Fatalf("expected attached plan label in timeline text: %q", last.Text)
	}
	if strings.Contains(last.Text, "hidden body") || strings.Contains(last.Text, "# Plan") {
		t.Fatalf("timeline should not expose hidden attached plan body: %q", last.Text)
	}
}

func TestEnqueueRunStreamEventDropsToolDeltaWhenChannelFull(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.runStream = make(chan chatRunStreamResult, 1)
	page.runStream <- chatRunStreamResult{RunID: 1}

	result := page.enqueueRunStreamEvent(chatRunStreamResult{
		RunID: 1,
		Event: ChatRunStreamEvent{Type: StreamEventToolDelta, ToolName: "bash"},
	})
	if result.queued {
		t.Fatalf("expected tool.delta to be dropped when queue is full")
	}
	if !result.drop {
		t.Fatalf("expected drop=true for tool.delta when queue is full")
	}
}

func TestRuntimeModeDescription_IncludesBypassPermissions(t *testing.T) {
	if got := runtimeModeDescription("single", true); got != "local (single-user daemon) · bypass permissions" {
		t.Fatalf("runtimeModeDescription(single,true) = %q", got)
	}
}
