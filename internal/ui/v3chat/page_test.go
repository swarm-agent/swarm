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
	if !strings.Contains(simulationRow(screen, 80, 0), "renamed live") {
		t.Fatalf("header did not render live session title:\n%s", drawn)
	}
	if !strings.Contains(drawn, "streaming") {
		t.Fatalf("live assistant overlay missing:\n%s", drawn)
	}
}

func TestRunTimerWakeIsOneShotAndReschedulesWithoutHeartbeat(t *testing.T) {
	wake := make(chan struct{}, 3)
	runtime := NewRuntime(&fakeTransport{}, NewStore(), func() { wake <- struct{}{} })
	page := NewPage(runtime, testPageStyles())
	defer page.Close()

	waitForWake := func(label string) {
		t.Helper()
		select {
		case <-wake:
		case <-time.After(1500 * time.Millisecond):
			t.Fatalf("timed out waiting for %s timer wake", label)
		}
	}

	page.scheduleRunTimer(true)
	waitForWake("first")
	page.scheduleRunTimer(true)
	waitForWake("second")
	select {
	case <-wake:
		t.Fatal("run timer became a recurring heartbeat without another render")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPageCanonicalHeaderUsesLivePlanStateWithoutTimerOrConnectedChrome(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "s", Title: "Canonical title"},
		ActiveRunIntent: &client.SessionV3RunIntent{
			RunID: "run", Status: "running", StartedAt: 120_000, CumulativeDurationMS: 90_000,
		},
		HasActivePlan: true,
		ActivePlan:    activePlanFixture("running", "in_progress", "cp-1", "Wire live plan state"),
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	defer page.Close()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 18)
	page.DrawAt(screen, time.UnixMilli(125_000))
	screen.Show()
	header := simulationRow(screen, 100, 0)
	if !strings.HasPrefix(header, "Canonical title") || !strings.Contains(header, "In Progress") || !strings.Contains(header, "cp-1 Wire live plan state") {
		t.Fatalf("canonical header missing live title/checkpoint state: %q", header)
	}
	if strings.Contains(header, "0:05") || strings.Contains(header, "1:35") {
		t.Fatalf("canonical header retained timer chrome: %q", header)
	}
	if strings.Contains(simulationText(screen, 100, 18), "Connected") || strings.Contains(simulationText(screen, 100, 18), "connected") || strings.Contains(header, "Swarm") {
		t.Fatalf("redundant connection/header chrome remains:\n%s", simulationText(screen, 100, 18))
	}
}

func TestPageCanonicalHeaderUpdatesFromRealtimePlanSavedEvent(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:       client.SessionSummary{ID: "s", Title: "Canonical title"},
		HasActivePlan: true,
		ActivePlan:    activePlanFixture("running", "in_progress", "cp-1", "First checkpoint"),
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 18)
	page.Draw(screen)
	screen.Show()
	if header := simulationRow(screen, 100, 0); !strings.Contains(header, "In Progress") || !strings.Contains(header, "cp-1 First checkpoint") {
		t.Fatalf("initial plan header = %q", header)
	}

	updated := activePlanFixture("waiting_review", "needs_review", "cp-1", "First checkpoint")
	payload, _ := json.Marshal(map[string]any{"has_active_plan": true, "active_plan": updated})
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "s", Seq: 1, EventType: "session.plan.saved", Payload: payload}}})
	page.Draw(screen)
	screen.Show()
	if header := simulationRow(screen, 100, 0); !strings.Contains(header, "Waiting review") || !strings.Contains(header, "cp-1 First checkpoint") || strings.Contains(header, "In Progress") {
		t.Fatalf("updated plan header = %q", header)
	}

	canonicalHeader := simulationRow(screen, 100, 0)
	page.HandleKey(tcell.NewEventKey(tcell.KeyF12, 0, tcell.ModNone))
	page.Draw(screen)
	screen.Show()
	if header := simulationRow(screen, 100, 0); header != canonicalHeader {
		t.Fatalf("F12 changed canonical header from %q to %q", canonicalHeader, header)
	}

	page.SetHeaderVisible(false)
	page.Draw(screen)
	screen.Show()
	if strings.Contains(simulationRow(screen, 100, 0), "Canonical title") {
		t.Fatal("persisted header visibility setting did not hide canonical header")
	}
}

func activePlanFixture(planStatus, checkpointStatus, checkpointID, checkpointTitle string) *client.SessionPlan {
	return &client.SessionPlan{
		ID: "plan", Status: planStatus, Active: true,
		Document: &client.SessionPlanDocument{
			Status:             planStatus,
			ActiveCheckpointID: checkpointID,
			ExecutionState:     &client.SessionPlanExecutionState{Status: planStatus},
			Checkpoints: []client.SessionPlanCheckpoint{{
				ID: checkpointID, Title: checkpointTitle, Status: checkpointStatus,
			}},
		},
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
	if !strings.Contains(drawn, "ctx 63%") {
		t.Fatalf("remaining context percentage missing:\n%s", drawn)
	}
	if got := conversationContextFacts(SelectUsage(store.Snapshot()), 0); len(got) != 1 || got[0] != "ctx 63%" {
		t.Fatalf("context facts = %#v", got)
	}
	topBorderRow, composerRow, bottomBorderRow, footerRow := simulationRow(screen, 80, 14), simulationRow(screen, 80, 15), simulationRow(screen, 80, 16), simulationRow(screen, 80, 17)
	if !strings.Contains(topBorderRow, "─") || !strings.Contains(composerRow, "> hi") || !strings.Contains(bottomBorderRow, "─") || !strings.Contains(footerRow, "Primary Desk") {
		t.Fatalf("composer/footer vertical layout mismatch: top=%q composer=%q bottom=%q footer=%q", topBorderRow, composerRow, bottomBorderRow, footerRow)
	}
	if strings.Contains(drawn, "F2 models") || strings.Contains(drawn, "thinking high") || strings.Contains(drawn, "Enter send") || strings.Contains(drawn, "PgUp/PgDn") || strings.Contains(drawn, "Esc home") || strings.Contains(drawn, "No messages yet") {
		t.Fatalf("invented bottom bar/help text remains:\n%s", drawn)
	}
}

func TestPageComposerDefaultsToOneRowAndExpandsForMultilineInput(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(40, 14)
	page.Draw(screen)
	screen.Show()
	if top, input, bottom := simulationRow(screen, 40, 10), simulationRow(screen, 40, 11), simulationRow(screen, 40, 12); !strings.Contains(top, "─") || !strings.HasPrefix(input, "> ") || !strings.Contains(bottom, "─") {
		t.Fatalf("empty composer geometry = top %q / input %q / bottom %q, want one editable row between borders", top, input, bottom)
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'n', tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone))
	page.Draw(screen)
	screen.Show()
	topBorder, compactRow, bottomBorder := simulationRow(screen, 40, 10), simulationRow(screen, 40, 11), simulationRow(screen, 40, 12)
	if !strings.Contains(topBorder, "─") || !strings.Contains(compactRow, "> one") || !strings.Contains(bottomBorder, "─") {
		t.Fatalf("compact composer geometry = top %q / input %q / bottom %q, want one editable row between borders", topBorder, compactRow, bottomBorder)
	}
	_, _, cursorStyle, _ := screen.GetContent(len([]rune("> one")), 11)
	_, _, cursorAttrs := cursorStyle.Decompose()
	if cursorAttrs&tcell.AttrReverse == 0 {
		t.Fatalf("cursor is not rendered on the sole composer content row")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyCtrlJ, 0, tcell.ModCtrl))
	for _, r := range "two" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.Draw(screen)
	screen.Show()
	if first, second, bottom := simulationRow(screen, 40, 10), simulationRow(screen, 40, 11), simulationRow(screen, 40, 12); !strings.Contains(first, "> one") || !strings.Contains(second, "  two") || !strings.Contains(bottom, "─") {
		t.Fatalf("expanded composer rows = %q / %q with bottom border %q", first, second, bottom)
	}
}

func TestPageComposerPastePreservesMultilineContentAndFollowsCursor(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	page.SetPasteActive(true)
	for _, r := range "first pasted line" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	for _, r := range "second pasted line with enough text to wrap" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.SetPasteActive(false)

	want := "first pasted line\nsecond pasted line with enough text to wrap"
	if got := page.InputValue(); got != want {
		t.Fatalf("pasted input = %q, want %q", got, want)
	}
	lines, cursorLine, cursorCol := composerLayout(page.InputValue(), len([]rune(page.InputValue())), 24)
	if len(lines) < 3 || cursorLine != len(lines)-1 || cursorCol <= 2 {
		t.Fatalf("paste layout = lines %#v, cursor %d:%d", lines, cursorLine, cursorCol)
	}
	if start := inputVisibleWindow(len(lines), 2, cursorLine); start+2 != len(lines) {
		t.Fatalf("visible composer starts at %d for %d lines; want tail containing cursor", start, len(lines))
	}

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(24, 14)
	page.Draw(screen)
	screen.Show()
	if drawn := simulationText(screen, 24, 14); !strings.Contains(drawn, "wrap") {
		t.Fatalf("composer does not show the pasted content near the cursor:\n%s", drawn)
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

func TestEscapeStopsActiveRunThroughCanonicalV3Path(t *testing.T) {
	transport := &fakeTransport{}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:         client.SessionSummary{ID: "session"},
		ActiveRunIntent: &client.SessionV3RunIntent{RunID: "run", Status: "running", StartedAt: 1},
	}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
	defer page.Close()
	if action := page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)); action != PageActionNone {
		t.Fatalf("Escape action = %v, want cancellation without navigation", action)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		transport.mu.Lock()
		request := transport.stopRequest
		transport.mu.Unlock()
		if request.runID != "" {
			if request.sessionID != "session" || request.runID != "run" || request.reason != "stopped from TUI" {
				t.Fatalf("stop request = %#v", request)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Escape did not invoke canonical StopSessionV3Run")
}

func TestEscapeReturnsHomeWithoutActiveRun(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	defer page.Close()
	if action := page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)); action != PageActionHome {
		t.Fatalf("Escape action = %v, want home", action)
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

func TestCanonicalFooterUsesFullContextUntilUsageArrives(t *testing.T) {
	got := conversationContextFacts(UsageState{}, 200000)
	if len(got) != 1 || got[0] != "ctx 100%" {
		t.Fatalf("context facts = %#v", got)
	}
}

func TestConversationContextPercentageBoundsRemainingTokens(t *testing.T) {
	for _, test := range []struct {
		name      string
		remaining int64
		want      string
	}{
		{name: "below zero", remaining: -1, want: "ctx 0%"},
		{name: "above window", remaining: 250000, want: "ctx 100%"},
	} {
		t.Run(test.name, func(t *testing.T) {
			usage := UsageState{Available: true, ContextWindow: 200000, RemainingTokens: test.remaining}
			got := conversationContextFacts(usage, 0)
			if len(got) != 1 || got[0] != test.want {
				t.Fatalf("context facts = %#v, want %q", got, test.want)
			}
		})
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

func TestPageFirstUserMessageHasBalancedSpacingBelowTitle(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:  client.SessionSummary{ID: "s", Title: "chat"},
		Messages: []client.SessionMessage{{ID: "user-1", Role: "user", Content: "first message"}},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(40, 14)
	page.Draw(screen)
	screen.Show()

	above, message, below := simulationRow(screen, 40, 1), simulationRow(screen, 40, 2), simulationRow(screen, 40, 3)
	if strings.TrimSpace(above) != "" || !strings.Contains(message, "> first message") || strings.TrimSpace(below) != "" {
		t.Fatalf("first user message spacing = above %q / message %q / below %q, want one blank row on each side", above, message, below)
	}
}

func TestPageUserMessagesUseThemedTextWithoutBackgroundBlock(t *testing.T) {
	styles := testPageStyles()
	styles.Text = tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorBlack)
	styles.Secondary = tcell.StyleDefault.Foreground(tcell.ColorBlue).Background(tcell.ColorRed)
	styles.Element = tcell.StyleDefault.Background(tcell.ColorGreen)
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), styles)

	rows := page.renderUserRows("message:user", "one two three four", 10, styles)
	if len(rows) != 4 {
		t.Fatalf("row count = %d, want 3 wrapped content rows plus spacing", len(rows))
	}
	if rows[0].text != "> one two" || rows[1].text != "  three" || rows[2].text != "  four" || rows[3].text != "" {
		t.Fatalf("user rows = %#v", rows)
	}
	for _, row := range rows {
		foreground, background, _ := row.style.Decompose()
		if foreground != tcell.ColorBlue || background != tcell.ColorBlack {
			t.Fatalf("user row colors = fg %v, bg %v; want themed text on normal background", foreground, background)
		}
		if strings.ContainsRune(row.text, '─') {
			t.Fatalf("user row still contains border chrome: %q", row.text)
		}
	}
	markerForeground, markerBackground, markerAttributes := rows[0].prefixStyle.Decompose()
	if rows[0].prefixWidth != 1 || markerForeground != tcell.ColorBlue || markerBackground != tcell.ColorBlack || markerAttributes&tcell.AttrBold == 0 {
		t.Fatalf("marker style = width %d, fg %v, bg %v, attrs %v", rows[0].prefixWidth, markerForeground, markerBackground, markerAttributes)
	}
	if rows[1].prefixWidth != 0 || rows[2].prefixWidth != 0 {
		t.Fatalf("continuation rows unexpectedly style a marker: %#v", rows)
	}
}

func TestPageToolNamesUseOneThemeColorSeparateFromHeaderAndResultText(t *testing.T) {
	styles := testPageStyles()
	styles.Text = tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorBlack)
	styles.Muted = tcell.StyleDefault.Foreground(tcell.ColorGray).Background(tcell.ColorBlack)
	styles.Primary = tcell.StyleDefault.Foreground(tcell.ColorPurple).Background(tcell.ColorRed)
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), styles)

	for _, tool := range []ToolTimelineItem{
		{ID: "read", Name: "read", Arguments: `{"path":"README.md"}`, Status: "completed"},
		{ID: "fallback", Name: "custom_tool", Output: "fallback result", Status: "completed"},
	} {
		rows := page.renderToolRows(tool, 80, styles)
		if len(rows) < 2 {
			t.Fatalf("%s rows = %#v", tool.Name, rows)
		}
		name := normalizeToolDisplayName(tool.Name)
		if rows[0].highlightWidth != len([]rune(name)) || runeSlice(rows[0].text, rows[0].highlightStart, rows[0].highlightStart+rows[0].highlightWidth) != name {
			t.Fatalf("%s title highlight = start %d, width %d in %q", tool.Name, rows[0].highlightStart, rows[0].highlightWidth, rows[0].text)
		}
		titleForeground, titleBackground, _ := rows[0].highlightStyle.Decompose()
		headerForeground, headerBackground, _ := rows[0].style.Decompose()
		if titleForeground != tcell.ColorPurple || titleBackground != tcell.ColorBlack {
			t.Fatalf("%s title colors = fg %v, bg %v; want primary foreground on text background", tool.Name, titleForeground, titleBackground)
		}
		if headerForeground != tcell.ColorWhite || headerBackground != tcell.ColorBlack || titleForeground == headerForeground {
			t.Fatalf("%s header colors = fg %v, bg %v; title fg %v", tool.Name, headerForeground, headerBackground, titleForeground)
		}
		if tool.Name == "custom_tool" {
			bodyForeground, bodyBackground, _ := rows[1].style.Decompose()
			if rows[1].text != "  fallback result" || bodyForeground != tcell.ColorGray || bodyBackground != tcell.ColorBlack {
				t.Fatalf("fallback result row = %#v, colors fg %v bg %v", rows[1], bodyForeground, bodyBackground)
			}
		}
	}
}

func TestPageRendersToolCallAndResultInCanonicalTimelineOrder(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), testPageStyles())
	state := State{
		Messages: []Message{
			{ID: "user", GlobalSeq: 1, Role: "user", Content: "start"},
			{ID: "assistant-before", GlobalSeq: 2, Role: "assistant", Content: "before tool"},
			{ID: "tool", GlobalSeq: 3, Role: "tool", Content: `{"path_id":"run.tool-history.v2","tool":"read","call_id":"call-read","arguments":"{\"path\":\"README.md\"}","completed_output":"README contents","duration_ms":25}`},
			{ID: "assistant-after", GlobalSeq: 4, Role: "assistant", Content: "after tool"},
		},
	}

	rows := page.renderRows(state, 80, testPageStyles())
	joined := make([]string, 0, len(rows))
	for _, row := range rows {
		joined = append(joined, row.text)
	}
	text := strings.Join(joined, "\n")
	for _, want := range []string{"✓ read README.md · 25ms"} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool timeline missing %q:\n%s", want, text)
		}
	}
	before := strings.Index(text, "before tool")
	tool := strings.Index(text, "✓ read README.md")
	after := strings.Index(text, "after tool")
	if before < 0 || tool < 0 || after < 0 || !(before < tool && tool < after) {
		t.Fatalf("canonical order mismatch: before=%d tool=%d after=%d\n%s", before, tool, after, text)
	}
}

func TestPageRendersLiveToolAtItsEventSequence(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), testPageStyles())
	state := State{
		Messages: []Message{
			{ID: "assistant-before", GlobalSeq: 2, Role: "assistant", Content: "before"},
			{ID: "assistant-after", GlobalSeq: 6, Role: "assistant", Content: "after"},
		},
		Tools: map[string]ToolTimelineItem{"call": {
			ID: "live-tool:call", CallID: "call", GlobalSeq: 5, Name: "bash", Arguments: `{"command":"pwd"}`, Status: "running",
		}},
	}
	rows := page.renderRows(state, 80, testPageStyles())
	var text strings.Builder
	for _, row := range rows {
		text.WriteString(row.text)
		text.WriteByte('\n')
	}
	rendered := text.String()
	before, tool, after := strings.Index(rendered, "before"), strings.Index(rendered, "• bash"), strings.Index(rendered, "after")
	if before < 0 || tool < 0 || after < 0 || !(before < tool && tool < after) {
		t.Fatalf("live tool order mismatch: before=%d tool=%d after=%d\n%s", before, tool, after, rendered)
	}
}

func TestPageAssistantRowsOmitRoleLabels(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), testPageStyles())
	state := State{
		Messages: []Message{{ID: "durable", Role: "assistant", Content: "durable response"}},
		Live:     map[string]LiveSegment{"run": {StreamID: "assistant:run", Text: "live response"}},
	}

	rows := page.renderRows(state, 40, testPageStyles())
	if len(rows) != 3 || rows[0].text != "durable response" || rows[1].text != "" || rows[2].text != "live response" {
		t.Fatalf("assistant rows = %#v", rows)
	}
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.text), "assistant") {
			t.Fatalf("assistant role label remains in row %q", row.text)
		}
	}
}

func TestPageAssistantMessagesUseInjectedCanonicalMarkdownRowsAndSpans(t *testing.T) {
	styles := testPageStyles()
	headingStyle := tcell.StyleDefault.Foreground(tcell.ColorPurple).Bold(true)
	strongStyle := tcell.StyleDefault.Foreground(tcell.ColorGreen).Bold(true)
	calls := 0
	styles.RenderMarkdown = func(body string, width int) []MarkdownLine {
		calls++
		if width != 40 || body != "# Heading with **strong** text" {
			t.Fatalf("markdown request = %q at width %d", body, width)
		}
		return []MarkdownLine{{
			Text:  "Heading with strong text",
			Style: headingStyle,
			Spans: []MarkdownSpan{{Text: "Heading with ", Style: headingStyle}, {Text: "strong", Style: strongStyle}, {Text: " text", Style: headingStyle}},
		}}
	}
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), styles)
	state := State{Messages: []Message{{ID: "assistant", Role: "assistant", Content: "# Heading with **strong** text"}}}

	rows := page.renderRows(state, 40, styles)
	if calls != 1 || len(rows) != 2 || rows[0].text != "Heading with strong text" || len(rows[0].spans) != 3 {
		t.Fatalf("canonical markdown rows = %#v, calls = %d", rows, calls)
	}
	if rows[0].spans[1].style != strongStyle || rows[0].style != headingStyle {
		t.Fatalf("canonical markdown styles were not preserved: %#v", rows[0])
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
