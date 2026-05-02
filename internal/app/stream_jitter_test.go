package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func newStreamTestApp() *App {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		panic(err)
	}
	cfg := defaultAppConfig()
	cfg.Swarm.Name = "swarm"
	return &App{
		screen:             screen,
		home:               ui.NewHomePage(model.EmptyHome()),
		chat:               ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-test", SessionMode: "auto", AuthConfigured: true, SwarmName: cfg.Swarm.Name}),
		route:              "chat",
		config:             cfg,
		pendingChatRender:  make(chan struct{}, 1),
		pendingStreamReady: make(chan struct{}, 1),
		streamEvents:       make(chan client.StreamEventEnvelope, 256),
	}
}

func TestRequestStreamReadyInterruptCoalescesBurst(t *testing.T) {
	a := newStreamTestApp()
	defer a.screen.Fini()

	for i := 0; i < 20; i++ {
		a.requestStreamReadyInterrupt()
	}
	if got := len(a.pendingStreamReady); got != 1 {
		t.Fatalf("pending stream ready len = %d, want 1", got)
	}

	a.consumePendingStreamReady()
	if got := len(a.pendingStreamReady); got != 0 {
		t.Fatalf("pending stream ready len after consume = %d, want 0", got)
	}
}

func TestAgentStreamEventUpdatesRuntimeState(t *testing.T) {
	a := newStreamTestApp()
	defer a.screen.Fini()

	state := client.AgentState{
		Profiles: []client.AgentProfile{
			{Name: "swarm", Mode: "primary", Enabled: true, ExecutionSetting: "readwrite", ExitPlanModeEnabled: boolPtr(false)},
			{Name: "reviewer", Mode: "subagent", Enabled: true},
		},
		ActivePrimary: "swarm",
		Version:       2,
	}
	raw, err := json.Marshal(map[string]any{"state": state, "version": state.Version})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !a.applySessionStreamEvent(client.StreamEventEnvelope{EventType: "agent.profile.created", Payload: raw}) {
		t.Fatal("expected agent stream event to report changes")
	}
	if got := a.homeModel.ActiveAgentExecutionSetting; got != "readwrite" {
		t.Fatalf("active agent execution = %q, want readwrite", got)
	}
	if a.homeModel.ActiveAgentExitPlanMode {
		t.Fatalf("active agent exit plan mode = true, want false")
	}
	if !sameStringSet(a.homeModel.Subagents, []string{"reviewer"}) {
		t.Fatalf("subagents = %#v, want reviewer", a.homeModel.Subagents)
	}
	if meta := a.chat.Meta(); !sameStringSet(meta.Subagents, []string{"reviewer"}) {
		t.Fatalf("chat meta subagents = %#v, want reviewer", meta.Subagents)
	}

	state.Profiles[1].Mode = "primary"
	state.Version = 3
	raw, err = json.Marshal(map[string]any{"state": state, "version": state.Version})
	if err != nil {
		t.Fatalf("marshal update payload: %v", err)
	}
	if !a.applySessionStreamEvent(client.StreamEventEnvelope{EventType: "agent.profile.updated", Payload: raw}) {
		t.Fatal("expected agent stream update to report changes")
	}
	if len(a.homeModel.Subagents) != 0 {
		t.Fatalf("subagents after mode update = %#v, want none", a.homeModel.Subagents)
	}
	if meta := a.chat.Meta(); len(meta.Subagents) != 0 {
		t.Fatalf("chat meta subagents after mode update = %#v, want none", meta.Subagents)
	}
}

func TestConsumeSessionStreamEventsDrainsBurst(t *testing.T) {
	a := newStreamTestApp()
	defer a.screen.Fini()

	for i := 0; i < 32; i++ {
		a.streamEvents <- client.StreamEventEnvelope{EventType: "session.title.updated", Payload: []byte(`{"session_id":"session-test","title":"burst title"}`)}
	}
	if changed := a.consumeSessionStreamEvents(); !changed {
		t.Fatal("expected burst stream drain to report changes")
	}
	if got := len(a.streamEvents); got != 0 {
		t.Fatalf("expected stream queue drained, remaining=%d", got)
	}
}

func TestConsumeStreamReadyForRenderThrottlesDenseBurst(t *testing.T) {
	a := newStreamTestApp()
	defer a.screen.Fini()
	base := time.Unix(1_700_000_000, 0)
	envelopes := realisticAppStreamEnvelopeBurst()

	for _, envelope := range envelopes[:8] {
		a.streamEvents <- envelope
	}
	if !a.consumeStreamReadyForRender(base, false) {
		t.Fatal("expected first stream burst to request a draw")
	}
	a.noteStreamRenderDrawn(base)

	for _, envelope := range envelopes[8:16] {
		a.streamEvents <- envelope
	}
	if a.consumeStreamReadyForRender(base.Add(streamRenderMinInterval/2), false) {
		t.Fatal("expected dense follow-up burst to be throttled")
	}
	if !a.streamRenderPending {
		t.Fatal("expected throttled stream burst to leave render pending")
	}
	if got := len(a.streamEvents); got != 0 {
		t.Fatalf("expected throttled path to still drain events, remaining=%d", got)
	}

	if !a.consumeStreamReadyForRender(base.Add(streamRenderMinInterval), false) {
		t.Fatal("expected pending stream render after throttle interval")
	}
}

func realisticAppStreamEnvelopeBurst() []client.StreamEventEnvelope {
	chunks := realisticAppAssistantStreamChunks()
	envelopes := make([]client.StreamEventEnvelope, 0, len(chunks)+10)
	now := time.Now().UnixMilli()
	seq := uint64(1)
	appendRunEvent := func(eventType string, payload client.SessionRunStreamEvent) {
		payload.SessionID = "session-test"
		payload.RunID = "run-stream-spike"
		if payload.Type == "" {
			payload.Type = eventType
			if len(payload.Type) >= len("run.") && payload.Type[:len("run.")] == "run." {
				payload.Type = payload.Type[len("run."):]
			}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		envelopes = append(envelopes, client.StreamEventEnvelope{
			GlobalSeq: seq,
			Stream:    "session:session-test",
			EventType: eventType,
			EntityID:  "session-test",
			Payload:   raw,
			TsUnixMs:  now + int64(seq),
		})
		seq++
	}

	appendRunEvent("session.lifecycle.updated", client.SessionRunStreamEvent{Type: "session.lifecycle.updated", Lifecycle: &client.SessionLifecycleSnapshot{SessionID: "session-test", RunID: "run-stream-spike", Active: true, Phase: "running", StartedAt: now}})
	appendRunEvent("run.turn.started", client.SessionRunStreamEvent{Type: "turn.started", Agent: "swarm"})
	appendRunEvent("run.tool.started", client.SessionRunStreamEvent{Type: "tool.started", ToolName: "search", CallID: "call-search", Arguments: `{"query":"stream spike"}`})
	for i := 0; i < 6; i++ {
		appendRunEvent("run.tool.delta", client.SessionRunStreamEvent{Type: "tool.delta", ToolName: "search", CallID: "call-search", Output: fmt.Sprintf("result %02d internal/ui/chat_page.go\n", i+1)})
	}
	appendRunEvent("run.tool.completed", client.SessionRunStreamEvent{Type: "tool.completed", ToolName: "search", CallID: "call-search", Output: "search complete", RawOutput: "search complete", DurationMS: 41})
	for _, chunk := range chunks {
		appendRunEvent("run.assistant.delta", client.SessionRunStreamEvent{Type: "assistant.delta", Delta: chunk})
	}
	appendRunEvent("session.lifecycle.updated", client.SessionRunStreamEvent{Type: "session.lifecycle.updated", Lifecycle: &client.SessionLifecycleSnapshot{SessionID: "session-test", RunID: "run-stream-spike", Active: false, Phase: "completed", StartedAt: now, EndedAt: now + int64(seq)}})
	return envelopes
}

func realisticAppAssistantStreamChunks() []string {
	body := strings.Repeat("Streaming delta text with markdown **formatting**, code `Draw`, and enough accumulated content to exercise the same live assistant redraw path.\n\n", 24)
	chunks := make([]string, 0, len(body)/48+1)
	for len(body) > 0 {
		end := 48
		if end > len(body) {
			end = len(body)
		}
		chunks = append(chunks, body[:end])
		body = body[end:]
	}
	return chunks
}

func BenchmarkAppConsumeRealisticSessionStreamBurst(b *testing.B) {
	envelopes := realisticAppStreamEnvelopeBurst()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := newStreamTestApp()
		for _, envelope := range envelopes {
			if !a.applySessionStreamEvent(envelope) {
				b.Fatalf("stream envelope %q did not apply", envelope.EventType)
			}
		}
		a.screen.Fini()
	}
}

func BenchmarkAppConsumeRealisticSessionStreamBurstAndDraw(b *testing.B) {
	envelopes := realisticAppStreamEnvelopeBurst()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := newStreamTestApp()
		a.screen.SetSize(120, 36)
		for _, envelope := range envelopes {
			if a.applySessionStreamEvent(envelope) {
				a.chat.Draw(a.screen)
				a.screen.Show()
			}
		}
		a.screen.Fini()
	}
}

func BenchmarkAppQueuedRealisticSessionStreamBurstAndDrawCoalesced(b *testing.B) {
	envelopes := realisticAppStreamEnvelopeBurst()
	const batchSize = 12
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := newStreamTestApp()
		a.screen.SetSize(120, 36)
		for start := 0; start < len(envelopes); start += batchSize {
			end := start + batchSize
			if end > len(envelopes) {
				end = len(envelopes)
			}
			for _, envelope := range envelopes[start:end] {
				a.streamEvents <- envelope
			}
			if a.consumeSessionStreamEvents() {
				a.chat.Draw(a.screen)
				a.screen.Show()
			}
		}
		a.screen.Fini()
	}
}

func BenchmarkAppStreamRenderBaselineEveryEnvelope(b *testing.B) {
	envelopes := realisticAppStreamEnvelopeBurst()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := newStreamTestApp()
		a.screen.SetSize(120, 36)
		for _, envelope := range envelopes {
			a.streamEvents <- envelope
			if a.consumeSessionStreamEvents() {
				a.chat.Draw(a.screen)
				a.screen.Show()
			}
		}
		a.screen.Fini()
	}
}

func BenchmarkAppStreamRenderFixedThrottled(b *testing.B) {
	envelopes := realisticAppStreamEnvelopeBurst()
	base := time.Unix(1_700_000_000, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := newStreamTestApp()
		a.screen.SetSize(120, 36)
		for idx, envelope := range envelopes {
			a.streamEvents <- envelope
			now := base.Add(time.Duration(idx) * 20 * time.Millisecond)
			if a.consumeStreamReadyForRender(now, false) {
				a.chat.Draw(a.screen)
				a.screen.Show()
				a.noteStreamRenderDrawn(now)
			}
		}
		finalNow := base.Add(time.Duration(len(envelopes))*20*time.Millisecond + streamRenderMinInterval)
		if a.consumeStreamReadyForRender(finalNow, false) {
			a.chat.Draw(a.screen)
			a.screen.Show()
			a.noteStreamRenderDrawn(finalNow)
		}
		a.screen.Fini()
	}
}
