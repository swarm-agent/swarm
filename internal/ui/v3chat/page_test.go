package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

func TestPageHeaderAndLiveOverlayRenderFromStore(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s", Title: "before"}, SnapshotEndpointCursor: "cursor"}})
	runtime := NewRuntime(&fakeTransport{}, store, nil)
	page := NewPage(runtime, testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 18)

	payload, _ := json.Marshal(map[string]any{"title": "renamed live"})
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 1, EventType: "session.title.updated", Payload: payload}}})
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "live.patch", Live: &client.V3RealtimeLivePatch{SessionID: "s", RunID: "run", StreamID: "assistant:run", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: 9, Text: "streaming"}}})
	page.Draw(screen)
	screen.Show()
	drawn := simulationText(screen, 80, 18)
	if !strings.Contains(drawn, "renamed live") {
		t.Fatalf("header did not render reduced title:\n%s", drawn)
	}
	if !strings.Contains(drawn, "streaming") {
		t.Fatalf("live assistant overlay missing:\n%s", drawn)
	}
}

func TestPageRendersComposerAboveCanonicalHomeFooterWithDesktopContext(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:                client.SessionSummary{ID: "s", Title: "chat", Mode: "auto"},
		Preference:             client.ModelPreference{Provider: "codex", Model: "gpt-test", Thinking: "high", ServiceTier: "fast"},
		ContextWindow:          200000,
		UsageSummary:           &client.SessionUsageSummary{ContextWindow: 200000, RemainingTokens: 125000, TotalTokens: 75000},
		SnapshotEndpointCursor: "cursor",
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	page.SetRouteLabel("Primary Desk")
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'i', tcell.ModNone))
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 18)
	page.Draw(screen)
	screen.Show()
	drawn := simulationText(screen, 80, 18)
	if !strings.Contains(drawn, "> hi") {
		t.Fatalf("composer input missing:\n%s", drawn)
	}
	if !strings.Contains(drawn, "Primary Desk") || !strings.Contains(drawn, "Plan: off") || !strings.Contains(drawn, "[gpt-test · high · fast]") {
		t.Fatalf("canonical home footer tokens missing:\n%s", drawn)
	}
	for _, redundant := range []string{"Agent", "model default", "[a:", "[m:", "[t:"} {
		if strings.Contains(drawn, redundant) {
			t.Fatalf("redundant footer label %q remains:\n%s", redundant, drawn)
		}
	}
	if !strings.Contains(drawn, "125k / 200k ctx") {
		t.Fatalf("desktop-style conversation context missing:\n%s", drawn)
	}
	if got := conversationContextFacts(SelectUsage(store.Snapshot()), 0); len(got) != 1 || got[0] != "125k / 200k ctx" {
		t.Fatalf("context facts = %#v", got)
	}
	composerRow, footerSeparatorRow, footerRow := simulationRow(screen, 80, 14), simulationRow(screen, 80, 16), simulationRow(screen, 80, 17)
	if !strings.Contains(composerRow, "> hi") || !strings.Contains(footerSeparatorRow, "─") || !strings.Contains(footerRow, "Primary Desk") {
		t.Fatalf("composer/footer vertical layout mismatch: composer=%q separator=%q footer=%q", composerRow, footerSeparatorRow, footerRow)
	}
	if strings.Contains(drawn, "F2 models") || strings.Contains(drawn, "thinking high") || strings.Contains(drawn, "Enter send") || strings.Contains(drawn, "PgUp/PgDn") || strings.Contains(drawn, "Esc home") || strings.Contains(drawn, "No messages yet") {
		t.Fatalf("invented bottom bar/help text remains:\n%s", drawn)
	}
}

func TestPagePreservesHomeProfileUntilBackendModeShiftResolvesProfile(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:    client.SessionSummary{ID: "s", Title: "chat", Mode: "auto"},
		Preference: client.ModelPreference{Provider: "codex", Model: "gpt-test", Thinking: "high"},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	page.SetRouteLabel("Primary Desk")
	page.SetProfileLabel("Focused work")

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(90, 18)
	page.Draw(screen)
	screen.Show()
	if footer := simulationRow(screen, 90, 17); !strings.Contains(footer, "[Focused work · gpt-test · high]") {
		t.Fatalf("home profile did not persist into chat footer: %q", footer)
	}

	store.Dispatch(ModeAction{Resolved: client.SessionV3ModeResult{
		Mode:             "plan",
		Preference:       client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "medium"},
		AgentModelPolicy: client.SessionV3AgentModelPolicy{ProfileName: "Planning", ProfileSource: "saved"},
	}})
	page.Draw(screen)
	screen.Show()
	footer := simulationRow(screen, 90, 17)
	if !strings.Contains(footer, "[Planning · plan-model · medium]") || strings.Contains(footer, "Focused work") {
		t.Fatalf("backend mode shift did not replace carried profile: %q", footer)
	}
}

func TestShiftTabCyclesModeThroughBackendResolvedState(t *testing.T) {
	transport := &fakeTransport{mode: client.SessionV3ModeResult{
		Mode:             "plan",
		Preference:       client.ModelPreference{Provider: "codex", Model: "plan-model"},
		AgentModelPolicy: client.SessionV3AgentModelPolicy{ProfileName: "Planning", ProfileSource: "saved"},
	}}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s", Mode: "auto"}, Preference: client.ModelPreference{Provider: "codex", Model: "auto-model"}}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
	page.HandleKey(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModShift))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if store.Snapshot().Session.Mode == "plan" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	state := store.Snapshot()
	if state.Session.Mode != "plan" || state.Model.Preference.Model != "plan-model" || state.Model.ProfileName != "Planning" {
		t.Fatalf("Shift+Tab did not commit backend-resolved plan/model state: %#v", state)
	}
	transport.mu.Lock()
	modeRequest := transport.modeRequest
	transport.mu.Unlock()
	if modeRequest != "plan" {
		t.Fatalf("mode request = %q, want plan", modeRequest)
	}
}

func TestCanonicalFooterFallsBackToLocalOnlyWithoutResolvedRouteIdentity(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 18)
	page.Draw(screen)
	screen.Show()
	if footerRow := simulationRow(screen, 80, 17); !strings.Contains(footerRow, "Local") {
		t.Fatalf("footer fallback missing when route identity is unavailable: %q", footerRow)
	}
}

func TestCanonicalFooterUsesContextWindowUntilUsageArrives(t *testing.T) {
	got := conversationContextFacts(UsageState{}, 200000)
	if len(got) != 1 || got[0] != "200k ctx" {
		t.Fatalf("context facts = %#v", got)
	}
}

func TestPageDurableAssistantReplacesLiveOverlay(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s", Title: "chat"}, SnapshotEndpointCursor: "cursor"}})
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "live.patch", Live: &client.V3RealtimeLivePatch{SessionID: "s", RunID: "run", StreamID: "assistant:run", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: 5, Text: "hello"}}})
	payload, _ := json.Marshal(map[string]any{"message": client.SessionMessage{ID: "m", SessionID: "s", Role: "assistant", Content: "hello", Metadata: map[string]any{"run_id": "run"}}})
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 1, EventType: "message.appended", Payload: payload}}})
	state := store.Snapshot()
	if len(SelectLiveSegments(state)) != 0 || len(SelectMessages(state)) != 1 {
		t.Fatalf("durable handoff duplicated state: %#v", state)
	}
}

func TestPageRowCacheIsBounded(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), testPageStyles())
	for i := 0; i < maxRowCacheItems+1; i++ {
		page.cachedWrap(fmt.Sprintf("message:%d", i), "cached text", 40)
	}
	if got := len(page.rowCache); got > maxRowCacheItems {
		t.Fatalf("row cache size = %d, want <= %d", got, maxRowCacheItems)
	}
}

func testPageStyles() PageStyles {
	return PageStyles{Background: tcell.StyleDefault, Panel: tcell.StyleDefault, Border: tcell.StyleDefault, Text: tcell.StyleDefault, Muted: tcell.StyleDefault, Primary: tcell.StyleDefault, Accent: tcell.StyleDefault, Secondary: tcell.StyleDefault, Success: tcell.StyleDefault, Warning: tcell.StyleDefault, Error: tcell.StyleDefault, Prompt: tcell.StyleDefault, Cursor: tcell.StyleDefault.Reverse(true)}
}

func simulationRow(screen tcell.SimulationScreen, width, row int) string {
	cells, _, _ := screen.GetContents()
	var b strings.Builder
	for x := 0; x < width; x++ {
		cell := cells[row*width+x]
		if len(cell.Runes) == 0 {
			b.WriteRune(' ')
		} else {
			b.WriteRune(cell.Runes[0])
		}
	}
	return b.String()
}

func simulationText(screen tcell.SimulationScreen, width, height int) string {
	cells, _, _ := screen.GetContents()
	var b strings.Builder
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			cell := cells[y*width+x]
			if len(cell.Runes) == 0 {
				b.WriteRune(' ')
			} else {
				b.WriteRune(cell.Runes[0])
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
