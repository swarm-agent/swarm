package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

const streamSpikeSessionID = "session-stream-spike"
const streamSpikeRunID = "run-stream-spike"

const extendedMarkdownHeavyStreamDuration = 5 * time.Second
const extendedMarkdownHeavyTokenInterval = 20 * time.Millisecond
const extendedMarkdownHeavyDrawInterval = 33 * time.Millisecond

func newStreamSpikeChatPage() *ChatPage {
	page := NewChatPage(ChatPageOptions{
		SessionID:      streamSpikeSessionID,
		SessionMode:    "auto",
		AuthConfigured: true,
		ShowHeader:     true,
		SwarmName:      "swarm",
	})
	page.historyLoaded = true
	page.busy = true
	page.streamingRun = true
	page.runStarted = time.Now().Add(-2 * time.Second)
	page.lifecycle = &ChatSessionLifecycle{
		SessionID: streamSpikeSessionID,
		RunID:     streamSpikeRunID,
		Active:    true,
		Phase:     "running",
		StartedAt: time.Now().Add(-2 * time.Second).UnixMilli(),
	}
	page.ownedRunID = streamSpikeRunID
	page.appendMessage("user", "please inspect the tui streaming spike and explain the hot path", time.Now().Add(-3*time.Second).UnixMilli())
	return page
}

func realisticStreamSpikeEvents() []ChatRunStreamEvent {
	chunks := realisticAssistantStreamChunks()
	events := make([]ChatRunStreamEvent, 0, len(chunks)+16)
	events = append(events,
		ChatRunStreamEvent{Type: "turn.started", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, Agent: "swarm"},
		ChatRunStreamEvent{Type: "reasoning.started", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID},
	)
	for i := 0; i < 4; i++ {
		events = append(events, ChatRunStreamEvent{Type: "reasoning.delta", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, Delta: fmt.Sprintf("checking render cache and websocket event burst phase %d", i+1)})
	}
	events = append(events,
		ChatRunStreamEvent{Type: "reasoning.completed", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, Summary: "render cache and full redraw path identified"},
		ChatRunStreamEvent{Type: "tool.started", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, ToolName: "search", CallID: "call-search", Arguments: `{"query":"cachedLiveAssistantLines"}`},
	)
	for i := 0; i < 8; i++ {
		events = append(events, ChatRunStreamEvent{Type: "tool.delta", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, ToolName: "search", CallID: "call-search", Output: fmt.Sprintf("match %02d internal/ui/chat_timeline_cache.go cachedLiveAssistantLines\n", i+1)})
	}
	events = append(events,
		ChatRunStreamEvent{Type: "tool.completed", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, ToolName: "search", CallID: "call-search", Output: "found live assistant cache and markdown parse path", RawOutput: "found live assistant cache and markdown parse path", DurationMS: 84},
	)
	for _, chunk := range chunks {
		events = append(events, ChatRunStreamEvent{Type: "assistant.delta", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, Delta: chunk})
	}
	return events
}

func realisticAssistantStreamChunks() []string {
	sections := []string{
		"The spike is reproducible when the TUI receives a dense stream of assistant deltas and redraws the chat page for each burst.\n\n",
		"## What is happening\n\nThe live assistant buffer keeps growing while the screen is redrawn. Every visible frame has to rebuild timeline blocks, wrap markdown rows, and append the spinner to the final row.\n\n",
		"```go\nfunc hotPath(page *ChatPage, screen tcell.Screen) {\n    page.applyRunStreamEvent(event, time.Now().UnixMilli())\n    page.Draw(screen)\n}\n```\n\n",
		"The expensive part is not a single token; it is the combination of repeated parsing plus the size of the accumulated message. The test should keep the same growing-buffer shape as a real response.\n\n",
		"- stream websocket events\n- coalesce interrupts\n- drain queued events\n- draw a full simulation screen\n- include markdown, code fences, bullets, and tool deltas\n\n",
		"A good benchmark reports both apply-only and apply+draw costs so we can tell whether the spike is event handling, markdown rendering, or terminal painting.\n",
	}
	return chunkStreamingText(strings.Repeat(strings.Join(sections, ""), 8), 42)
}

func TestRealisticStreamingReplayKeepsLiveAssistantVisible(t *testing.T) {
	page := newStreamSpikeChatPage()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 36)

	for _, event := range realisticStreamSpikeEvents() {
		page.applyRunStreamEvent(event, time.Now().UnixMilli())
		page.Draw(screen)
	}

	if got := strings.TrimSpace(page.liveAssistant); got == "" {
		t.Fatal("expected live assistant text after replay")
	}
	text := dumpScreenText(screen, 120, 36)
	if !strings.Contains(text, "good benchmark reports") {
		t.Fatalf("expected streamed final paragraph to be visible, got:\n%s", text)
	}
}

func TestMarkdownHeavyStreamingReplaySpansFiveSeconds(t *testing.T) {
	scenario := markdownHeavyStreamingScenario()
	if scenario.Duration != extendedMarkdownHeavyStreamDuration {
		t.Fatalf("duration = %s, want %s", scenario.Duration, extendedMarkdownHeavyStreamDuration)
	}
	if got, want := len(scenario.Events), scenario.ExpectedTokenCount+scenario.NonAssistantEventCount; got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
	if len(scenario.Markdown) < 40_000 {
		t.Fatalf("markdown fixture too small: %d bytes", len(scenario.Markdown))
	}

	page := newStreamSpikeChatPage()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(132, 42)

	stats := replayStreamingScenario(page, screen, scenario, false)
	if stats.SimulatedDuration != extendedMarkdownHeavyStreamDuration {
		t.Fatalf("simulated duration = %s, want %s", stats.SimulatedDuration, extendedMarkdownHeavyStreamDuration)
	}
	if stats.AssistantDeltas != scenario.ExpectedTokenCount {
		t.Fatalf("assistant deltas = %d, want %d", stats.AssistantDeltas, scenario.ExpectedTokenCount)
	}
	if stats.Draws < 145 || stats.Draws > 155 {
		t.Fatalf("draws = %d, want roughly a 33ms cadence over 5s", stats.Draws)
	}
	if got := strings.TrimSpace(page.liveAssistant); got != scenario.Markdown {
		t.Fatalf("live assistant length = %d, want %d", len(got), len(scenario.Markdown))
	}
	if stats.LiveMarkdownParses < 2 {
		t.Fatalf("expected repeated live markdown reparses during replay, got %d", stats.LiveMarkdownParses)
	}
}

func chunkStreamingText(text string, chunkSize int) []string {
	if chunkSize <= 0 {
		chunkSize = 32
	}
	chunks := make([]string, 0, len(text)/chunkSize+1)
	for len(text) > 0 {
		end := chunkSize
		if end > len(text) {
			end = len(text)
		}
		chunks = append(chunks, text[:end])
		text = text[end:]
	}
	return chunks
}

type markdownHeavyStreamScenario struct {
	Events                 []ChatRunStreamEvent
	EventOffsets           []time.Duration
	Markdown               string
	Duration               time.Duration
	ExpectedTokenCount     int
	NonAssistantEventCount int
}

type streamReplayStats struct {
	Events             int
	AssistantDeltas    int
	Draws              int
	LiveMarkdownParses int
	SimulatedDuration  time.Duration
}

func markdownHeavyStreamingScenario() markdownHeavyStreamScenario {
	const tokenCount = int(extendedMarkdownHeavyStreamDuration/extendedMarkdownHeavyTokenInterval) + 1
	chunks := buildMarkdownHeavyStreamingChunks(tokenCount)
	markdown := strings.Join(chunks, "")

	events := make([]ChatRunStreamEvent, 0, len(chunks)+16)
	offsets := make([]time.Duration, 0, len(chunks)+16)
	appendEvent := func(offset time.Duration, event ChatRunStreamEvent) {
		events = append(events, event)
		offsets = append(offsets, offset)
	}

	appendEvent(0, ChatRunStreamEvent{Type: "turn.started", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, Agent: "swarm"})
	appendEvent(0, ChatRunStreamEvent{Type: "reasoning.started", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID})
	for i := 0; i < 5; i++ {
		appendEvent(0, ChatRunStreamEvent{Type: "reasoning.delta", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, Delta: fmt.Sprintf("profiling markdown-heavy streaming frame %d", i+1)})
	}
	appendEvent(0, ChatRunStreamEvent{Type: "reasoning.completed", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, Summary: "markdown-heavy draw replay ready"})
	appendEvent(0, ChatRunStreamEvent{Type: "tool.started", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, ToolName: "read", CallID: "call-read", Arguments: `{"path":"internal/ui/chat_page.go"}`})
	for i := 0; i < 6; i++ {
		appendEvent(0, ChatRunStreamEvent{Type: "tool.delta", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, ToolName: "read", CallID: "call-read", Output: fmt.Sprintf("%04d: drawTimelineComponent renders live assistant markdown rows\n", 1700+i)})
	}
	appendEvent(0, ChatRunStreamEvent{Type: "tool.completed", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, ToolName: "read", CallID: "call-read", Output: "confirmed live markdown render path", RawOutput: "confirmed live markdown render path", DurationMS: 160})

	for i, chunk := range chunks {
		offset := time.Duration(i) * extendedMarkdownHeavyTokenInterval
		appendEvent(offset, ChatRunStreamEvent{Type: "assistant.delta", SessionID: streamSpikeSessionID, RunID: streamSpikeRunID, Delta: chunk})
	}

	return markdownHeavyStreamScenario{
		Events:                 events,
		EventOffsets:           offsets,
		Markdown:               markdown,
		Duration:               extendedMarkdownHeavyStreamDuration,
		ExpectedTokenCount:     len(chunks),
		NonAssistantEventCount: len(events) - len(chunks),
	}
}

func replayStreamingScenario(page *ChatPage, screen tcell.Screen, scenario markdownHeavyStreamScenario, drawEveryEvent bool) streamReplayStats {
	var stats streamReplayStats
	if page == nil || screen == nil {
		return stats
	}
	start := time.Now().Add(-scenario.Duration)
	nextDrawAt := time.Duration(0)
	for i, event := range scenario.Events {
		offset := time.Duration(0)
		if i < len(scenario.EventOffsets) {
			offset = scenario.EventOffsets[i]
		}
		beforeParse := page.liveAssistantRenderCache.LastParseAt
		page.applyRunStreamEvent(event, start.Add(offset).UnixMilli())
		stats.Events++
		if event.Type == "assistant.delta" {
			stats.AssistantDeltas++
		}
		if drawEveryEvent || offset >= nextDrawAt || i == len(scenario.Events)-1 {
			page.Draw(screen)
			screen.Show()
			stats.Draws++
			if afterParse := page.liveAssistantRenderCache.LastParseAt; !afterParse.IsZero() && !afterParse.Equal(beforeParse) {
				stats.LiveMarkdownParses++
			}
			if !drawEveryEvent {
				for nextDrawAt <= offset {
					nextDrawAt += extendedMarkdownHeavyDrawInterval
				}
			}
		}
	}
	stats.SimulatedDuration = scenario.Duration
	return stats
}

func buildMarkdownHeavyStreamingChunks(targetChunks int) []string {
	chunks := make([]string, 0, targetChunks)
	for i := 0; i < targetChunks; i++ {
		var out strings.Builder
		if i == 0 {
			out.WriteString("# Extended markdown-heavy streaming replay\n\n")
			out.WriteString("This fixture simulates five seconds of dense assistant output with the same growing live markdown buffer shape that the TUI receives from websocket run events.\n\n")
		}
		fmt.Fprintf(&out, "## Frame %03d render/cache checkpoint\n\n", i+1)
		fmt.Fprintf(&out, "The assistant delta at frame `%03d` adds prose with **bold emphasis**, _italic notes_, inline code `cachedLiveAssistantLines`, and links like [chat_page.go](internal/ui/chat_page.go).\n\n", i+1)
		fmt.Fprintf(&out, "- accumulated markdown bytes are growing before draw %03d\n- wrapping width stays stable so cache invalidation is text-driven\n- the visible viewport still forces timeline rebuild work\n\n", i+1)
		fmt.Fprintf(&out, "> quoted diagnostic: frame %03d keeps reparsing the live assistant block when the throttle interval has elapsed.\n\n", i+1)
		fmt.Fprintf(&out, "```go\n// frame %03d\npage.applyRunStreamEvent(event, now)\npage.Draw(screen)\n_ = page.cachedLiveAssistantLines(width)\n```\n\n", i+1)
		fmt.Fprintf(&out, "| phase | frame | expected hot path |\n| --- | ---: | --- |\n| stream | %03d | markdown parse + wrap + timeline draw |\n\n", i+1)
		if i == targetChunks-1 {
			out.WriteString("Final diagnostic: markdown-heavy five-second stream completed with a full live assistant buffer.")
		}
		chunks = append(chunks, out.String())
	}
	return chunks
}

func BenchmarkChatPageRealisticStreamApplyOnly(b *testing.B) {
	events := realisticStreamSpikeEvents()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		for _, event := range events {
			page.applyRunStreamEvent(event, int64(i))
		}
	}
}

func BenchmarkChatPageRealisticStreamApplyAndDrawEveryDelta(b *testing.B) {
	events := realisticStreamSpikeEvents()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		b.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 36)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		for _, event := range events {
			page.applyRunStreamEvent(event, int64(i))
			page.Draw(screen)
			screen.Show()
		}
	}
}

func BenchmarkChatPageRealisticStreamApplyAndDrawCoalesced(b *testing.B) {
	events := realisticStreamSpikeEvents()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		b.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 36)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		for idx, event := range events {
			page.applyRunStreamEvent(event, int64(i))
			if idx%12 == 0 || idx == len(events)-1 {
				page.Draw(screen)
				screen.Show()
			}
		}
	}
}

func BenchmarkChatPageRealisticLiveMarkdownRender(b *testing.B) {
	chunks := realisticAssistantStreamChunks()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		var text strings.Builder
		for _, chunk := range chunks {
			text.WriteString(chunk)
			page.liveAssistant = text.String()
			page.liveAssistantRenderCache.LastParseAt = time.Time{}
			_ = page.cachedLiveAssistantLines(116)
		}
	}
}

func BenchmarkChatPageMarkdownHeavyFiveSecondStreamDrawCadence(b *testing.B) {
	scenario := markdownHeavyStreamingScenario()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		b.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(132, 42)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		stats := replayStreamingScenario(page, screen, scenario, false)
		if stats.AssistantDeltas != scenario.ExpectedTokenCount {
			b.Fatalf("assistant deltas = %d, want %d", stats.AssistantDeltas, scenario.ExpectedTokenCount)
		}
	}
}

func BenchmarkChatPageMarkdownHeavyFiveSecondStreamDrawEveryDelta(b *testing.B) {
	scenario := markdownHeavyStreamingScenario()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		b.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(132, 42)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		stats := replayStreamingScenario(page, screen, scenario, true)
		if stats.AssistantDeltas != scenario.ExpectedTokenCount {
			b.Fatalf("assistant deltas = %d, want %d", stats.AssistantDeltas, scenario.ExpectedTokenCount)
		}
	}
}

const loremBashStreamDuration = 5 * time.Second
const loremBashChunkInterval = 10 * time.Millisecond
const loremBashDrawInterval = 33 * time.Millisecond

type bashLoremStreamScenario struct {
	Events              []ChatRunStreamEvent
	EventOffsets        []time.Duration
	Output              string
	Duration            time.Duration
	ExpectedChunkCount  int
	NonOutputEventCount int
}

type bashLoremReplayStats struct {
	Events            int
	ToolDeltas        int
	Draws             int
	OutputBytes       int
	ViewportLineCount int
	SimulatedDuration time.Duration
}

func bashLoremIpsumStreamingScenario() bashLoremStreamScenario {
	chunks := buildBashLoremIpsumChunks(int(loremBashStreamDuration/loremBashChunkInterval) + 1)
	output := strings.Join(chunks, "")

	events := make([]ChatRunStreamEvent, 0, len(chunks)+2)
	offsets := make([]time.Duration, 0, len(chunks)+2)
	appendEvent := func(offset time.Duration, event ChatRunStreamEvent) {
		events = append(events, event)
		offsets = append(offsets, offset)
	}

	appendEvent(0, ChatRunStreamEvent{
		Type:      "tool.started",
		SessionID: streamSpikeSessionID,
		RunID:     streamSpikeRunID,
		ToolName:  "bash",
		CallID:    "call-bash-lorem",
		Arguments: `{"command":"bash -lc 'for i in $(seq 1 500); do printf %s lorem; done'"}`,
	})
	for i, chunk := range chunks {
		appendEvent(time.Duration(i)*loremBashChunkInterval, ChatRunStreamEvent{
			Type:      "tool.delta",
			SessionID: streamSpikeSessionID,
			RunID:     streamSpikeRunID,
			ToolName:  "bash",
			CallID:    "call-bash-lorem",
			Output:    chunk,
		})
	}
	appendEvent(loremBashStreamDuration, ChatRunStreamEvent{
		Type:       "tool.completed",
		SessionID:  streamSpikeSessionID,
		RunID:      streamSpikeRunID,
		ToolName:   "bash",
		CallID:     "call-bash-lorem",
		Output:     output,
		RawOutput:  output,
		DurationMS: loremBashStreamDuration.Milliseconds(),
	})

	return bashLoremStreamScenario{
		Events:              events,
		EventOffsets:        offsets,
		Output:              output,
		Duration:            loremBashStreamDuration,
		ExpectedChunkCount:  len(chunks),
		NonOutputEventCount: len(events) - len(chunks),
	}
}

func replayBashLoremStreamingScenario(page *ChatPage, screen tcell.Screen, scenario bashLoremStreamScenario, drawEveryEvent bool, expanded bool) bashLoremReplayStats {
	var stats bashLoremReplayStats
	if page == nil || screen == nil {
		return stats
	}
	start := time.Now().Add(-scenario.Duration)
	nextDrawAt := time.Duration(0)
	for i, event := range scenario.Events {
		offset := time.Duration(0)
		if i < len(scenario.EventOffsets) {
			offset = scenario.EventOffsets[i]
		}
		page.applyRunStreamEvent(event, start.Add(offset).UnixMilli())
		if expanded && page.bashOutputAvailable() {
			page.bashOutput.Visible = true
			page.bashOutput.Expanded = true
		}
		stats.Events++
		if event.Type == "tool.delta" {
			stats.ToolDeltas++
		}
		if drawEveryEvent || offset >= nextDrawAt || i == len(scenario.Events)-1 {
			page.Draw(screen)
			screen.Show()
			stats.Draws++
			if !drawEveryEvent {
				for nextDrawAt <= offset {
					nextDrawAt += loremBashDrawInterval
				}
			}
		}
	}
	stats.OutputBytes = len(page.bashOutput.Output)
	stats.ViewportLineCount = len(page.bashOutputViewportLines(100, 12))
	stats.SimulatedDuration = scenario.Duration
	return stats
}

func buildBashLoremIpsumChunks(targetChunks int) []string {
	paragraphs := []string{
		"Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed non risus suspendisse lectus tortor dignissim sit amet adipiscing nec ultricies sed dolor.\n\n",
		"Cras elementum ultrices diam. Maecenas ligula massa, varius a semper congue, euismod non mi. Proin porttitor, orci nec nonummy molestie, enim est eleifend mi.\n\n",
		"Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident.\n\n",
		"Integer in mauris eu nibh euismod gravida. Duis ac tellus et risus vulputate vehicula. Donec lobortis risus a elit etiam tempor ut ullamcorper leo.\n\n",
	}
	chunks := make([]string, 0, targetChunks)
	for i := 0; i < targetChunks; i++ {
		var out strings.Builder
		fmt.Fprintf(&out, "[lorem-bash:%03d] ", i+1)
		out.WriteString(paragraphs[i%len(paragraphs)])
		if i%25 == 24 {
			fmt.Fprintf(&out, "--- burst boundary %03d ---\n\n", i+1)
		}
		chunks = append(chunks, out.String())
	}
	return chunks
}

func TestBashLoremIpsumStreamingReplaySpansFiveSeconds(t *testing.T) {
	scenario := bashLoremIpsumStreamingScenario()
	if scenario.Duration != loremBashStreamDuration {
		t.Fatalf("duration = %s, want %s", scenario.Duration, loremBashStreamDuration)
	}
	if got, want := len(scenario.Events), scenario.ExpectedChunkCount+scenario.NonOutputEventCount; got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
	if len(scenario.Output) < 70_000 {
		t.Fatalf("bash lorem fixture too small: %d bytes", len(scenario.Output))
	}

	page := newStreamSpikeChatPage()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(132, 42)

	stats := replayBashLoremStreamingScenario(page, screen, scenario, false, true)
	if stats.SimulatedDuration != loremBashStreamDuration {
		t.Fatalf("simulated duration = %s, want %s", stats.SimulatedDuration, loremBashStreamDuration)
	}
	if stats.ToolDeltas != scenario.ExpectedChunkCount {
		t.Fatalf("tool deltas = %d, want %d", stats.ToolDeltas, scenario.ExpectedChunkCount)
	}
	if stats.Draws < 145 || stats.Draws > 155 {
		t.Fatalf("draws = %d, want roughly a 33ms cadence over 5s", stats.Draws)
	}
	if got := strings.TrimSpace(page.bashOutput.Output); got == "" || !strings.Contains(got, "lorem-bash:500") {
		t.Fatalf("expected final lorem bash output, got length=%d", len(got))
	}
	if !page.bashOutputAvailable() {
		t.Fatal("expected inline bash output to be available during replay")
	}
}

func BenchmarkChatPageBashLoremIpsumStreamApplyOnly(b *testing.B) {
	scenario := bashLoremIpsumStreamingScenario()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		for idx, event := range scenario.Events {
			offset := time.Duration(0)
			if idx < len(scenario.EventOffsets) {
				offset = scenario.EventOffsets[idx]
			}
			page.applyRunStreamEvent(event, int64(offset/time.Millisecond))
		}
	}
}

func BenchmarkChatPageBashLoremIpsumStreamDrawCadenceCollapsed(b *testing.B) {
	scenario := bashLoremIpsumStreamingScenario()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		b.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(132, 42)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		stats := replayBashLoremStreamingScenario(page, screen, scenario, false, false)
		if stats.ToolDeltas != scenario.ExpectedChunkCount {
			b.Fatalf("tool deltas = %d, want %d", stats.ToolDeltas, scenario.ExpectedChunkCount)
		}
	}
}

func BenchmarkChatPageBashLoremIpsumStreamDrawCadenceExpanded(b *testing.B) {
	scenario := bashLoremIpsumStreamingScenario()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		b.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(132, 42)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		stats := replayBashLoremStreamingScenario(page, screen, scenario, false, true)
		if stats.ToolDeltas != scenario.ExpectedChunkCount {
			b.Fatalf("tool deltas = %d, want %d", stats.ToolDeltas, scenario.ExpectedChunkCount)
		}
	}
}

func BenchmarkChatPageBashLoremIpsumStreamDrawEveryDeltaExpanded(b *testing.B) {
	scenario := bashLoremIpsumStreamingScenario()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		b.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(132, 42)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := newStreamSpikeChatPage()
		stats := replayBashLoremStreamingScenario(page, screen, scenario, true, true)
		if stats.ToolDeltas != scenario.ExpectedChunkCount {
			b.Fatalf("tool deltas = %d, want %d", stats.ToolDeltas, scenario.ExpectedChunkCount)
		}
	}
}

func BenchmarkChatPageBashLoremIpsumViewportWrap(b *testing.B) {
	scenario := bashLoremIpsumStreamingScenario()
	page := newStreamSpikeChatPage()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		b.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(132, 42)
	replayBashLoremStreamingScenario(page, screen, scenario, false, true)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if lines := page.bashOutputViewportLines(100, 12); len(lines) == 0 {
			b.Fatal("expected viewport lines")
		}
	}
}

func parseToolJSONBeforeFastGuardForBenchmark(raw string) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	return payload
}

func BenchmarkParseToolJSONBeforeFastGuardOnBashLoremOutput(b *testing.B) {
	scenario := bashLoremIpsumStreamingScenario()
	samples := make([]string, 0, scenario.ExpectedChunkCount)
	for _, event := range scenario.Events {
		if event.Type == "tool.delta" {
			samples = append(samples, event.Output)
		}
	}
	if len(samples) == 0 {
		b.Fatal("expected bash lorem output samples")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, sample := range samples {
			if payload := parseToolJSONBeforeFastGuardForBenchmark(sample); payload != nil {
				b.Fatalf("expected non-JSON bash output, got %#v", payload)
			}
		}
	}
}

func BenchmarkParseToolJSONAfterFastGuardOnBashLoremOutput(b *testing.B) {
	scenario := bashLoremIpsumStreamingScenario()
	samples := make([]string, 0, scenario.ExpectedChunkCount)
	for _, event := range scenario.Events {
		if event.Type == "tool.delta" {
			samples = append(samples, event.Output)
		}
	}
	if len(samples) == 0 {
		b.Fatal("expected bash lorem output samples")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, sample := range samples {
			if payload := parseToolJSON(sample); payload != nil {
				b.Fatalf("expected non-JSON bash output, got %#v", payload)
			}
		}
	}
}

func BenchmarkParseToolJSONAfterFastGuardOnValidToolJSON(b *testing.B) {
	samples := []string{
		`{"command":"go test ./internal/ui","exit_code":0,"output":"ok"}`,
		` {"path":"internal/ui/chat_page.go","line_start":1,"max_lines":120} `,
		`{"items":[{"name":"first"},{"name":"second"}]}`,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, sample := range samples {
			_ = parseToolJSON(sample)
		}
	}
}
