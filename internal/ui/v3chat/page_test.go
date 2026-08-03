package v3chat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui/footerbar"
)

func TestPageConsumeQuitScrollbackJumpRestoresLiveFollowWhilePausedOrBusy(t *testing.T) {
	for _, busy := range []bool{false, true} {
		page := NewPage(NewRuntime(nil, NewStore(), nil), PageStyles{})
		page.busy = busy
		page.HandleKey(tcell.NewEventKey(tcell.KeyPgUp, 0, tcell.ModNone))

		if !page.ConsumeQuitScrollbackJump() {
			t.Fatalf("busy=%v: scrolled page did not consume quit jump", busy)
		}
		if page.scroll != 0 || !page.follow {
			t.Fatalf("busy=%v: scroll=%d follow=%v, want live bottom", busy, page.scroll, page.follow)
		}
		if page.ConsumeQuitScrollbackJump() {
			t.Fatalf("busy=%v: page consumed quit jump again at live bottom", busy)
		}
	}
}

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

func TestPageReasoningFollowsSharedThinkingTagsSettingDuringStreaming(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s"}, SnapshotEndpointCursor: "cursor"}})
	payload, _ := json.Marshal(map[string]any{
		"run_id": "run", "step": 1, "step_id": "step-1", "reasoning_id": "reasoning-1", "reasoning_key": "analysis", "delta": "Inspecting current project state", "delta_mode": "replace", "recorded_at": int64(100),
	})
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{Seq: 1, EventType: "session.reasoning.delta", Payload: payload, TsUnixMS: 100}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	rows := page.renderRows(store.Snapshot(), 80, testPageStyles())
	visible := renderRowsText(rows)
	if !strings.Contains(visible, "Thinking") || !strings.Contains(visible, "Inspecting current project state") {
		t.Fatalf("enabled thinking tags did not render streaming reasoning: %q", visible)
	}
	if len(rows) == 0 || rows[len(rows)-1].text != "" {
		t.Fatalf("enabled thinking tags did not leave space below reasoning: %#v", rows)
	}
	page.SetThinkingTagsVisible(false)
	rows = page.renderRows(store.Snapshot(), 80, testPageStyles())
	hidden := renderRowsText(rows)
	if !strings.Contains(hidden, "Thinking") || strings.Contains(hidden, "Inspecting current project state") {
		t.Fatalf("disabled thinking tags did not preserve label and hide body: %q", hidden)
	}
	if len(rows) == 0 || rows[len(rows)-1].text != "" {
		t.Fatalf("disabled thinking tags did not leave space below reasoning: %#v", rows)
	}
}

func TestStructuredFinalHandoffRendersCompactCardAndLegacyMarkersAreSanitized(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "session-handoff", Title: "Handoff"},
		Messages: []client.SessionMessage{
			{ID: "legacy", SessionID: "session-handoff", GlobalSeq: 1, Role: "assistant", Content: "Before\n<swarm-handoff-summary>\nLegacy summary\n</swarm-handoff-summary>\nAfter"},
			{ID: "handoff", SessionID: "session-handoff", GlobalSeq: 2, Role: "system", Content: "compact", Metadata: testFinalHandoffMetadata()},
		},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 34)
	page.Draw(screen)
	screen.Show()
	drawn := simulationText(screen, 100, 34)
	for _, want := range []string{
		"FINAL HANDOFF  ·  ship",
		"Ready to review",
		"The focused change is complete.",
		"• Compact card",
		"RECOMMENDATION",
		"ship — review",
		"NEXT STEPS",
		"1. Review",
		"Details  ·  report  ·  result  ·  files 2  ·  validation 1",
		"Legacy summary",
	} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("final handoff card missing %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, "swarm-handoff-summary") {
		t.Fatalf("legacy marker leaked into transcript:\n%s", drawn)
	}
}

func TestFinalHandoffSectionsUseContentAwareSpacing(t *testing.T) {
	styles := testPageStyles()
	message := Message{
		ID: "spaced", Role: "system", Metadata: map[string]any{"source": finalHandoffSource, "outcome": "ship"},
		FinalHandoff: &client.PlanFinalHandoff{
			Title:         "Ready to review",
			Overview:      "The focused change is complete.",
			ImpactBullets: []string{"Compact card"},
			Recommendation: &client.SessionPlanCheckpointRecommendation{
				Decision: "ship",
				Action:   "review",
			},
			SuggestedPrompts: []client.PlanFinalHandoffSuggestedPrompt{{Label: "Review", Prompt: "Review it."}},
			Details:          client.PlanFinalHandoffDetails{Report: "Durable report"},
		},
	}
	rows := (&Page{}).renderFinalHandoffRows(message, 80, styles)
	text := make([]string, 0, len(rows))
	for _, row := range rows {
		text = append(text, strings.TrimSpace(strings.Trim(row.text, "│")))
	}
	for _, pair := range [][2]string{
		{"FINAL HANDOFF  ·  ship", "Ready to review"},
		{"• Compact card", "RECOMMENDATION"},
		{"ship — review", "NEXT STEPS"},
		{"1. Review", "Details  ·  report"},
		{"Details  ·  report", "Tab focus  ·  1–3 choose next step"},
	} {
		before, after := -1, -1
		for index, line := range text {
			if line == pair[0] {
				before = index
			}
			if line == pair[1] {
				after = index
			}
		}
		if before < 0 || after != before+2 || text[before+1] != "" {
			t.Fatalf("sections %q and %q were not separated by exactly one blank row: %#v", pair[0], pair[1], text)
		}
	}

	minimal := (&Page{}).renderFinalHandoffRows(Message{
		ID: "minimal", Role: "system", Metadata: map[string]any{"source": finalHandoffSource},
		FinalHandoff: &client.PlanFinalHandoff{},
	}, 40, styles)
	if len(minimal) != 4 {
		t.Fatalf("empty optional sections introduced spacing rows: %#v", minimal)
	}
}

func TestFinalHandoffCardWrapsTextWithChatPageCellAwareBehavior(t *testing.T) {
	const cardWidth = 20
	const contentWidth = cardWidth - 4
	title := "Unicode e\u0301 👩\u200d💻 content wraps across rows\r\nExplicit 界 line"
	message := Message{
		ID: "wrapped", Role: "system", Metadata: map[string]any{"source": finalHandoffSource, "outcome": "completed"},
		FinalHandoff: &client.PlanFinalHandoff{Title: title},
	}
	rows := (&Page{}).renderFinalHandoffRows(message, cardWidth, testPageStyles())
	for _, row := range rows {
		if width := displayCellWidth(row.text); width > cardWidth {
			t.Fatalf("card row exceeded requested width: width=%d row=%q", width, row.text)
		}
	}

	want := wrapText(title, contentWidth)
	if len(want) < 3 {
		t.Fatalf("test fixture did not require wrapping: %#v", want)
	}
	content := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.HasPrefix(row.text, "│ ") && strings.HasSuffix(row.text, " │") {
			content = append(content, strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(row.text, "│ "), " │"), " "))
		}
	}
	matched := false
	for start := 0; start+len(want) <= len(content); start++ {
		if reflect.DeepEqual(content[start:start+len(want)], want) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("final handoff did not use chat-page wrapping:\n got: %#v\nwant: %#v", content, want)
	}
	if strings.Contains(strings.Join(content, "\n"), "…") {
		t.Fatalf("wrapped final handoff was unexpectedly truncated: %#v", content)
	}
}

func TestFinalHandoffGraphemeWrappingPreservesCellWidthsAndClusters(t *testing.T) {
	text := "A界e\u0301👩\u200d💻B"
	lines := wrapDisplayText(text, 3)
	if got := strings.Join(lines, ""); got != text {
		t.Fatalf("grapheme wrapping changed content: got %q want %q", got, text)
	}
	for _, line := range lines {
		if width := displayCellWidth(line); width > 3 {
			t.Fatalf("wrapped line %q is %d cells wide", line, width)
		}
	}
	if strings.Contains(lines[0], "\u0301") && !strings.Contains(lines[0], "e\u0301") {
		t.Fatalf("combining grapheme was split: %#v", lines)
	}
	if !strings.Contains(strings.Join(lines, "|"), "👩\u200d💻") {
		t.Fatalf("emoji grapheme was split: %#v", lines)
	}

	card := (&Page{}).renderFinalHandoffRows(Message{
		ID: "wide", Role: "system", Metadata: map[string]any{"source": finalHandoffSource},
		FinalHandoff: &client.PlanFinalHandoff{SchemaVersion: 1, Title: "界界", Overview: "e\u0301 and 👩\u200d💻", Details: client.PlanFinalHandoffDetails{}},
	}, 12, testPageStyles())
	for _, row := range card {
		if width := displayCellWidth(row.text); width > 12 {
			t.Fatalf("card row exceeded requested width: width=%d row=%q", width, row.text)
		}
	}
}

func TestFinalHandoffKeyboardSuggestionUsesOrdinaryMessagePath(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:  client.SessionSummary{ID: "session-handoff"},
		Messages: []client.SessionMessage{{ID: "handoff", SessionID: "session-handoff", Role: "system", Metadata: testFinalHandoffMetadata()}},
	}})
	transport := &fakeTransport{}
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 30)
	page.Draw(screen)
	page.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	if !page.handoffFocus || page.handoffControl != 0 {
		t.Fatalf("Tab did not focus the first handoff control: focus=%t control=%d", page.handoffFocus, page.handoffControl)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	if page.handoffControl != 1 {
		t.Fatalf("Tab did not move handoff control: %d", page.handoffControl)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if page.handoffControl != 2 {
		t.Fatalf("arrow did not move to details control: %d", page.handoffControl)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !page.handoffDetailsModal {
		t.Fatal("Enter did not activate the focused details control")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if page.handoffDetailsModal || !page.handoffFocus {
		t.Fatal("Esc did not close details and return to the handoff controls")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if page.handoffFocus {
		t.Fatal("Esc did not return focus to the composer")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, '1', tcell.ModNone))
	deadline := time.Now().Add(time.Second)
	for {
		transport.mu.Lock()
		request := transport.messageRequest
		transport.mu.Unlock()
		if request.Content != "Review the final handoff." {
			if time.Now().After(deadline) {
				t.Fatalf("suggestion was not sent through V3 message path: %#v", request)
			}
			time.Sleep(time.Millisecond)
			continue
		}
		if request.Role != "user" || strings.TrimSpace(request.RunID) == "" || request.Metadata["operation_id"] == nil || len(request.Metadata) != 1 {
			t.Fatalf("suggestion bypassed ordinary user-message semantics: %#v", request)
		}
		break
	}
}

func TestFinalHandoffMouseDetailsModalScrollsAtNarrowWidthAndReturnsToTranscript(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:  client.SessionSummary{ID: "session-handoff"},
		Messages: []client.SessionMessage{{ID: "handoff", SessionID: "session-handoff", Role: "system", Metadata: testFinalHandoffMetadata()}},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(48, 24)
	page.Draw(screen)
	screen.Show()
	var details footerbar.Rect
	page.mu.Lock()
	for action, target := range page.handoffTargets {
		if strings.Contains(action, ":details:") {
			details = target
			break
		}
	}
	page.mu.Unlock()
	if details.W == 0 {
		t.Fatalf("details hit target was not rendered: %#v", page.handoffTargets)
	}
	page.HandleMouse(tcell.NewEventMouse(details.X, details.Y, tcell.Button1, tcell.ModNone))
	if !page.handoffDetailsModal {
		t.Fatal("details mouse target did not open modal")
	}
	screen.SetSize(30, 12)
	page.Draw(screen)
	screen.Show()
	modal := simulationText(screen, 30, 12)
	if !strings.Contains(modal, "FINAL HANDOFF DETAILS") || !strings.Contains(modal, "REPORT") || !strings.Contains(modal, "Full durable report") {
		t.Fatalf("narrow details modal missing durable evidence:\n%s", modal)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone))
	if page.handoffDetailsScroll == 0 {
		t.Fatal("details modal did not scroll")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if page.handoffDetailsModal || page.handoffDetails != nil {
		t.Fatal("details modal did not return cleanly to transcript")
	}
}

func testFinalHandoffMetadata() map[string]any {
	return map[string]any{
		"source":  "plan_execution_final_handoff",
		"kind":    "plan_final_checkpoint_handoff",
		"outcome": "ship",
		"final_handoff": map[string]any{
			"schema_version": 1,
			"title":          "Ready to review",
			"overview":       "The focused change is complete.",
			"impact_bullets": []any{"Compact card", "Ordinary chat continuation"},
			"recommendation": map[string]any{"decision": "ship", "action": "review", "reason": "The requested behavior is complete.", "action_state": "ready"},
			"suggested_prompts": []any{
				map[string]any{"label": "Review", "prompt": "Review the final handoff."},
				map[string]any{"label": "Continue", "prompt": "Continue with the next task."},
			},
			"details": map[string]any{
				"report":        "Full durable report\nwith additional evidence lines.",
				"result":        "done",
				"changed_files": []any{"internal/ui/v3chat/page.go", "internal/ui/v3chat/state.go"},
				"validation":    []any{"Focused regression passed"},
			},
		},
	}
}

func TestBashPermissionFromHydrationRendersInlineThemedCardAndActions(t *testing.T) {
	permission := client.PermissionRecord{
		ID:            "permission-bash",
		SessionID:     "session-bash",
		RunID:         "run-bash",
		CallID:        "call-bash",
		ToolName:      "functions.bash",
		Requirement:   "tool",
		Mode:          "auto",
		Status:        "pending",
		ToolArguments: `{"command":"python3 listener.py","explanation":["Start a listener on TCP port 8080.","Expose it on public network interfaces."],"category":"write","critical":true}`,
	}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:            client.SessionSummary{ID: "session-bash", Title: "Bash card"},
		PendingPermissions: []client.PermissionRecord{permission},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 28)

	page.Draw(screen)
	screen.Show()
	drawn := simulationText(screen, 100, 28)
	for _, want := range []string{
		"Bash permission",
		"WRITE",
		"PAY ATTENTION BEFORE APPROVING",
		"Start a listener on TCP port 8080.",
		"python3 listener.py",
		"available after approval",
		"Ctrl+D Always Deny",
		"Ctrl+A Always Allow",
		"Esc Deny",
		"Enter Approve",
	} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("rendered Bash permission card missing %q:\n%s", want, drawn)
		}
	}
	if !strings.Contains(drawn, "> ") {
		t.Fatalf("inline Bash permission card replaced the ordinary composer:\n%s", drawn)
	}
	card := strings.Index(drawn, "Bash permission")
	composer := strings.LastIndex(drawn, "> ")
	if card < 0 || composer < 0 || card >= composer {
		t.Fatalf("permission card is not inline above the composer: card=%d composer=%d\n%s", card, composer, drawn)
	}
}

func TestManageSessionsPendingDeployRendersCanonicalStructuredCardWithoutManifestJSON(t *testing.T) {
	arguments := `{"manifest_version":1,"action":"deploy","parent_session_id":"parent","account_scope_id":"account","user_id":"user","proposals":[{"id":"proposal-1","title":"Managed session test","prompt":"Run a minimal managed-session smoke test.","mode":"auto","agent_name":"swarm","agent_mode":"primary","workspace_path":"/workspace","workspace_name":"swarm-go","managed_worktree":true,"worktree_branch":"agent/managed-session-test","selected":true}],"allowed_workspaces":[{"id":"workspace","generation":2,"path":"/workspace","name":"swarm-go"}],"manifest_digest":"secret-digest","approved_arguments":{"action":"deploy","manifest_version":1,"manifest_digest":"secret-digest","selected_proposal_ids":["proposal-1"],"proposals":[]}}`
	permission := client.PermissionRecord{
		ID: "permission-sessions", SessionID: "session-parent", CallID: "call-sessions", ToolName: "functions.manage-sessions", Requirement: "session_deploy", Mode: "auto", Status: "pending", ToolArguments: arguments,
	}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-parent"}, PendingPermissions: []client.PermissionRecord{permission}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	rows := page.renderRows(store.Snapshot(), 96, testPageStyles())
	drawn := renderRowsText(rows)
	for _, want := range []string{"Manage sessions", "DEPLOY", "PROPOSALS", "Managed session test", "Run a minimal managed-session smoke test.", "swarm · auto", "swarm-go · managed worktree", "Enter Approve", "Esc Deny"} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("manage-sessions pending card missing %q:\n%s", want, drawn)
		}
	}
	for _, raw := range []string{`{"`, "manifest_version", "manifest_digest", "approved_arguments", "allowed_workspaces", "secret-digest", "Ctrl+A Always Allow", "Ctrl+D Always Deny"} {
		if strings.Contains(drawn, raw) {
			t.Fatalf("manage-sessions pending card leaked raw marker %q:\n%s", raw, drawn)
		}
	}
}

func TestManageSessionsTerminalPayloadsRenderStructuredCardsWithoutRawEnvelopes(t *testing.T) {
	durableDenied := Message{ID: "denied-message", Role: "tool", Content: `{"path_id":"run.v3.provider-tool-result.v1","type":"v3_provider_tool_result","run_id":"run","step":1,"step_id":"step-1","tool_name":"manage-sessions","call_id":"call-denied","tool_instance_id":"step-1:call-denied","arguments":"{\"action\":\"deploy\",\"proposals\":[{\"title\":\"Managed session test\",\"prompt\":\"Run smoke test\",\"mode\":\"auto\",\"worktree\":true}]}","output":"{\"permission\":{\"approved\":false,\"reason\":\"denied by user\",\"status\":\"denied\"},\"tool\":{\"arguments\":\"{\\\"action\\\":\\\"deploy\\\",\\\"proposals\\\":[{\\\"title\\\":\\\"Managed session test\\\",\\\"prompt\\\":\\\"Run smoke test\\\",\\\"mode\\\":\\\"auto\\\",\\\"worktree\\\":true}]}\",\"name\":\"manage-sessions\"}}","completed_output":"{\"permission\":{\"approved\":false,\"reason\":\"denied by user\",\"status\":\"denied\"},\"tool\":{\"arguments\":\"{\\\"action\\\":\\\"deploy\\\",\\\"proposals\\\":[{\\\"title\\\":\\\"Managed session test\\\",\\\"prompt\\\":\\\"Run smoke test\\\",\\\"mode\\\":\\\"auto\\\",\\\"worktree\\\":true}]}\",\"name\":\"manage-sessions\"}}","error":"permission denied","duration_ms":5093}`}
	deniedTool, ok := parseToolMessage(durableDenied)
	if !ok {
		t.Fatal("canonical durable denied provider-tool result did not parse")
	}
	success := `{"tool":"manage_sessions","action":"deploy","manifest_digest":"digest","selected_count":1,"results":[{"proposal_id":"proposal-1","session_id":"child-session","title":"Managed session test","mode":"auto","agent":"swarm","workspace_path":"/workspace","worktree":true,"status":"started","navigation":{"href":"/swarm-go/child-session"}}]}`
	failed := `{"tool":"manage_sessions","action":"deploy","selected_count":1,"results":[{"proposal_id":"proposal-1","title":"Broken session","mode":"auto","agent":"swarm","status":"error","error":"executor rejected the run"}]}`
	cases := []struct {
		name   string
		tool   ToolTimelineItem
		want   []string
		absent []string
	}{
		{name: "denied", tool: deniedTool, want: []string{"SESSIONS", "Managed session test", "Run smoke test", "Permission denied", "denied by user"}, absent: []string{"permission\":", "tool\":", `{"`}},
		{name: "success", tool: ToolTimelineItem{ID: "success", Name: "manage-sessions", Status: "completed", Output: success}, want: []string{"SESSIONS", "deploy", "Managed session test · started", "swarm · auto"}, absent: []string{"manifest_digest", "proposal_id", "session_id", "navigation", `{"`}},
		{name: "failed", tool: ToolTimelineItem{ID: "failed", Name: "manage-sessions", Status: "failed", Output: failed}, want: []string{"Broken session · error", "executor rejected the run"}, absent: []string{"selected_count", "proposal_id", `{"`}},
	}
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drawn := renderRowsText(page.renderToolRows(tc.tool, 72, testPageStyles()))
			for _, want := range tc.want {
				if !strings.Contains(drawn, want) {
					t.Fatalf("terminal card missing %q:\n%s", want, drawn)
				}
			}
			for _, raw := range tc.absent {
				if strings.Contains(drawn, raw) {
					t.Fatalf("terminal card leaked raw marker %q:\n%s", raw, drawn)
				}
			}
		})
	}
}

func TestManageSessionsDurableLogRendersTechnicalMetadata(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	tool := ToolTimelineItem{ID: "durable-log", Name: "manage-sessions", Status: "completed", Output: `{"action":"search","search_mode":"durable_log","source":"durable_v3_session_events","events":[{"id":"event-9","session_id":"session-1","seq":9,"event_type":"session.diagnostic.recorded","payload":{"message":"API probe"}}],"scan_truncated":true}`}
	drawn := renderRowsText(page.renderToolRows(tool, 80, testPageStyles()))
	for _, want := range []string{"Durable V3 event log", "1 match", "session.diagnostic.recorded", "seq 9", "API probe", "scan truncated"} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("durable-log card missing %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, `{"action"`) {
		t.Fatalf("durable-log card leaked result envelope:\n%s", drawn)
	}
}

func TestManageSessionsApprovalForwardsCanonicalApprovedArguments(t *testing.T) {
	approved := `{"action":"deploy","manifest_version":1,"manifest_digest":"digest","selected_proposal_ids":["proposal-1"],"proposals":[{"id":"proposal-1","prompt":"Do work","mode":"auto"}]}`
	permission := client.PermissionRecord{ID: "permission", SessionID: "parent", ToolName: "manage-sessions", Requirement: "session_deploy", Status: "pending", ToolArguments: `{"action":"deploy","proposals":[{"id":"proposal-1","title":"One","prompt":"Do work","mode":"auto"}],"approved_arguments":` + approved + `}`}
	resolved := permission
	resolved.Status = "approved"
	resolved.Decision = "allow_once"
	transport := &fakeTransport{resolvedPermission: resolved}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "parent"}, PendingPermissions: []client.PermissionRecord{permission}}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	deadline := time.Now().Add(time.Second)
	for page.PendingPermissionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	transport.mu.Lock()
	request := transport.permissionRequest
	transport.mu.Unlock()
	var gotApproved, wantApproved map[string]any
	gotErr := json.Unmarshal([]byte(request.approvedArguments), &gotApproved)
	wantErr := json.Unmarshal([]byte(approved), &wantApproved)
	if request.sessionID != "parent" || request.permissionID != "permission" || request.action != "allow_once" || gotErr != nil || wantErr != nil || !reflect.DeepEqual(gotApproved, wantApproved) {
		t.Fatalf("manage-sessions permission request = %#v", request)
	}
}

func TestManageSessionsPermissionCoalescesCorrelatedTerminalToolCard(t *testing.T) {
	arguments := `{"action":"deploy","proposals":[{"id":"proposal-1","title":"One session","prompt":"Do work","mode":"auto","managed_worktree":true}],"approved_arguments":{"action":"deploy"}}`
	permission := client.PermissionRecord{ID: "permission", CallID: "call-1", ToolName: "manage-sessions", Requirement: "session_deploy", Status: "approved", Decision: "allow_once", ExecutionStatus: "completed", ToolArguments: arguments, Output: `{"tool":"manage_sessions","action":"deploy","results":[{"title":"One session","status":"started"}]}`}
	state := NewState()
	state.Permissions = PermissionState{Records: []PermissionTimelineItem{{Record: permission}}}
	state.Messages = []Message{{ID: "tool", Role: "tool", Content: `{"path_id":"run.v3.provider-tool-result.v1","tool_name":"manage-sessions","call_id":"call-1","arguments":"` + strings.ReplaceAll(arguments, `"`, `\"`) + `","output":"{\"action\":\"deploy\",\"results\":[{\"title\":\"One session\",\"status\":\"started\"}]}"}`}}
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	drawn := renderRowsText(page.renderRows(state, 80, testPageStyles()))
	if count := strings.Count(drawn, "┌"); count != 1 {
		t.Fatalf("correlated permission and terminal result rendered %d boxes, want one:\n%s", count, drawn)
	}
	for _, want := range []string{"Manage sessions", "One session · started", "RESOLVED", "Approved once"} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("resolved manage-sessions card missing %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, `{"`) || strings.Contains(drawn, "manifest_digest") {
		t.Fatalf("coalesced manage-sessions card leaked raw JSON:\n%s", drawn)
	}
}

func TestManageSessionsCardUsesCellWidthForWideGraphemes(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	tool := ToolTimelineItem{ID: "wide", Name: "manage-sessions", Status: "completed", Output: `{"action":"list","items":[{"id":"session-wide","title":"界界界界界界界界界界界界界界界界界界","state":"completed"}]}`}
	rows := page.renderToolRows(tool, 32, testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(36, len(rows)+2)
	for index, row := range rows {
		if len(row.spans) > 0 {
			drawSpans(screen, 2, index+1, 32, row.spans)
		} else {
			drawText(screen, 2, index+1, 32, row.style, row.text)
		}
	}
	screen.Show()
	for index, row := range rows[:len(rows)-1] {
		if strings.HasSuffix(row.text, "│") || strings.HasSuffix(row.text, "┐") || strings.HasSuffix(row.text, "┘") {
			if got, _, _ := screen.Get(33, index+1); got != "│" && got != "┐" && got != "┘" {
				t.Fatalf("wide session title overwrote right card border on row %d: got %q", index, got)
			}
		}
	}
}

func TestPlanPermissionFromHydrationUsesStructuredCardAndFullPlanModal(t *testing.T) {
	permission := client.PermissionRecord{
		ID: "permission-plan", SessionID: "session-plan", ToolName: "plan_manage", Requirement: "plan_new_request", Mode: "auto", Status: "pending",
		ToolArguments: `{"path_id":"tool.plan-new-request.v1","document_operation":"request_new_plan","title":"Two-step completion plan","document":{"id":"plan-proposal","title":"Two-step completion plan","info":{"goal":"Finish the target work end-to-end.","scope":"Implement the focused target."},"checkpoints":[{"id":"cp-1","title":"Verify the work","status":"pending","order":1,"tasks":["Inspect the target"],"acceptance_criteria":["Scope is explicit"]},{"id":"cp-2","title":"Finish the work","status":"pending","order":2,"tasks":["Implement the target"],"acceptance_criteria":["Work is complete"]}]}}`,
	}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-plan", Title: "Plan card"}, PendingPermissions: []client.PermissionRecord{permission}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 30)
	page.Draw(screen)
	screen.Show()
	drawn := simulationText(screen, 100, 30)
	for _, want := range []string{"Plan approval", "PLAN", "Two-step completion plan", "Finish the target work end-to-end.", "CHECKPOINTS", "2 checkpoints", "1. Verify the work  ·  Pending", "2. Finish the work  ·  Pending", "Ctrl+P or /plan  Open full plan", "Enter Approve"} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("structured plan card missing %q:\n%s", want, drawn)
		}
	}
	for _, raw := range []string{"path_id", "document_operation", "acceptance_criteria", `{"`, "note ›"} {
		if strings.Contains(drawn, raw) {
			t.Fatalf("structured plan card leaked obsolete or raw marker %q:\n%s", raw, drawn)
		}
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone))
	if page.planModal {
		t.Fatal("plain p opened the full-plan modal")
	}
	if got := string(page.input); got != "p" {
		t.Fatalf("plain p input = %q, want %q", got, "p")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone))
	for _, r := range "/plan" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if action := page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); action != PageActionOpenCurrentPlan {
		t.Fatalf("/plan action = %v, want fresh current-plan request", action)
	}
	if page.planModal {
		t.Fatal("/plan opened the proposal payload before the current-plan API result")
	}
	if action := page.HandleKey(tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModNone)); action != PageActionOpenCurrentPlan {
		t.Fatalf("Ctrl+P action = %v, want fresh current-plan request", action)
	}
}

func TestExitPlanPermissionFromHydrationUsesUnifiedStructuredCard(t *testing.T) {
	permission := exitPlanPermissionRecord("permission-exit-hydration", "call-exit-hydration")
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:            client.SessionSummary{ID: "session-exit", Title: "Exit plan", Mode: "plan"},
		PendingPermissions: []client.PermissionRecord{permission},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(110, 32)
	page.Draw(screen)
	screen.Show()
	drawn := simulationText(screen, 110, 32)
	for _, want := range []string{
		"Plan approval", "PLAN", "Exit with the structured plan", "Implement the unified approval surface",
		"Scope · V3 TUI permission presentation", "plan plan-exit", "checkpointed execution",
		"CONTINUATION  ·  Review each checkpoint", "1. Unify the card  ·  Pending", "Enter Approve", "Esc Deny",
	} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("exit-plan card missing %q:\n%s", want, drawn)
		}
	}
	if got := strings.Count(drawn, "Plan approval"); got != 1 {
		t.Fatalf("exit-plan approval surfaces = %d, want 1:\n%s", got, drawn)
	}
	for _, raw := range []string{"exit_plan_mode", "approved_arguments", "path_id", "acceptance_criteria", `{"`} {
		if strings.Contains(drawn, raw) {
			t.Fatalf("exit-plan card leaked raw marker %q:\n%s", raw, drawn)
		}
	}
}

func TestExitPlanPermissionRealtimeResolutionUpdatesAndDismissesUnifiedCard(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-exit", Mode: "plan"}}})
	permission := exitPlanPermissionRecord("permission-exit-realtime", "call-exit-realtime")
	payload, err := json.Marshal(map[string]any{"permission": permission})
	if err != nil {
		t.Fatal(err)
	}
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "session-exit", Seq: 1, EventType: "permission.requested", Payload: payload}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	if !page.PendingPermissionVisible() {
		t.Fatal("realtime exit-plan permission did not activate the unified card")
	}
	pendingRendered := renderRowsText(page.renderRows(store.Snapshot(), 100, testPageStyles()))
	if strings.Count(pendingRendered, "Plan approval") != 1 || !strings.Contains(pendingRendered, "Exit with the structured plan") || strings.Contains(pendingRendered, `{"`) {
		t.Fatalf("realtime exit-plan permission did not use one structured card:\n%s", pendingRendered)
	}

	resolved := permission
	resolved.Status = "denied"
	resolved.Decision = "deny_once"
	resolved.ResolvedAt = 20
	payload, err = json.Marshal(map[string]any{"permission": resolved})
	if err != nil {
		t.Fatal(err)
	}
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "session-exit", Seq: 2, EventType: "permission.updated", Payload: payload}}})
	if page.PendingPermissionVisible() {
		t.Fatal("resolved realtime exit-plan permission stayed actionable")
	}
	items := SelectPermissions(store.Snapshot())
	if len(items) != 1 || items[0].GlobalSeq != 1 || items[0].Record.Status != "denied" {
		t.Fatalf("resolved exit-plan permission did not update in place: %#v", items)
	}
	rendered := renderRowsText(page.renderRows(store.Snapshot(), 100, testPageStyles()))
	if !strings.Contains(rendered, "Plan approval") || !strings.Contains(rendered, "Resolved · Denied once") || strings.Contains(rendered, "Enter Approve") {
		t.Fatalf("resolved exit-plan card did not dismiss its approval controls:\n%s", rendered)
	}
}

func TestPlanPermissionIntentPrefersCanonicalApprovedDocumentBackfill(t *testing.T) {
	record := exitPlanPermissionRecord("permission-exit-backfill", "call-exit-backfill")
	record.ApprovedArguments = `{"keep":"backfilled","title":"Backfilled title","document":{"id":"plan-backfilled","title":"Backfilled title","info":{"goal":"Use the backend-approved document"},"checkpoints":[{"id":"cp-backfilled","title":"Backfilled checkpoint","status":"pending","order":1}]},"continuation_policy":"automatic","continue_automatically":true}`
	intent, ok := parsePlanPermissionIntent(record)
	if !ok {
		t.Fatal("exit-plan permission was not recognized after approved-argument backfill")
	}
	if intent.Title != "Backfilled title" || intent.Goal != "Use the backend-approved document" || intent.PlanID != "plan-backfilled" || len(intent.Checkpoints) != 1 || intent.Checkpoints[0]["id"] != "cp-backfilled" || !intent.ContinueAutomatically {
		t.Fatalf("intent did not prefer canonical approved arguments: %#v", intent)
	}
}

func TestExitPlanPermissionApprovalAndDenialUseCanonicalArguments(t *testing.T) {
	t.Run("approval preserves document and selected continuation", func(t *testing.T) {
		permission := exitPlanPermissionRecord("permission-exit-approve", "call-exit-approve")
		resolved := permission
		resolved.Status = "approved"
		resolved.Decision = "allow_once"
		transport := &fakeTransport{resolvedPermission: resolved}
		store := NewStore()
		store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-exit", Mode: "plan"}, PendingPermissions: []client.PermissionRecord{permission}}})
		page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone))
		page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
		waitForPermissionResolution(t, page)

		transport.mu.Lock()
		request := transport.permissionRequest
		transport.mu.Unlock()
		if request.action != "allow_once" || request.approvedArguments == "" {
			t.Fatalf("exit-plan approval request = %#v", request)
		}
		var approved map[string]any
		if err := json.Unmarshal([]byte(request.approvedArguments), &approved); err != nil {
			t.Fatalf("approved arguments are invalid: %v (%q)", err, request.approvedArguments)
		}
		if approved["keep"] != "canonical" || approved["continuation_policy"] != "automatic" || approved["continue_automatically"] != true || approved["execution_granularity"] != "checkpointed" {
			t.Fatalf("approved execution arguments = %#v", approved)
		}
		document, _ := approved["document"].(map[string]any)
		if document["title"] != "Exit with the structured plan" || len(toolObjectSlice(document, "checkpoints")) != 1 {
			t.Fatalf("approved structured document was not preserved: %#v", document)
		}
	})

	t.Run("denial sends no approved arguments", func(t *testing.T) {
		permission := exitPlanPermissionRecord("permission-exit-deny", "call-exit-deny")
		resolved := permission
		resolved.Status = "denied"
		resolved.Decision = "deny_once"
		transport := &fakeTransport{resolvedPermission: resolved}
		store := NewStore()
		store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-exit", Mode: "plan"}, PendingPermissions: []client.PermissionRecord{permission}}})
		page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
		page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
		waitForPermissionResolution(t, page)

		transport.mu.Lock()
		request := transport.permissionRequest
		transport.mu.Unlock()
		if request.action != "deny_once" || request.approvedArguments != "" {
			t.Fatalf("exit-plan denial request = %#v", request)
		}
	})
}

func TestPageCoalescesExitPlanPermissionAndCorrelatedToolResultIntoOneCard(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), testPageStyles())
	permission := exitPlanPermissionRecord("permission-exit-coalesced", "call-exit-coalesced")
	permission.Status = "approved"
	permission.Decision = "allow_once"
	permission.ExecutionStatus = "completed"
	toolPayload, err := json.Marshal(map[string]any{
		"path_id": "run.tool-history.v2", "tool": "exit_plan_mode", "call_id": permission.CallID,
		"arguments": permission.ToolArguments, "completed_output": `{"status":"approved","mode_changed":true}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := State{
		Messages:    []Message{{ID: "tool-exit", GlobalSeq: 4, Role: "tool", Content: string(toolPayload)}},
		Permissions: PermissionState{Records: []PermissionTimelineItem{{Record: permission, GlobalSeq: 2}}},
	}
	rendered := renderRowsText(page.renderRows(state, 100, testPageStyles()))
	if got := strings.Count(rendered, "Plan approval"); got != 1 {
		t.Fatalf("coalesced exit-plan card count = %d, want 1:\n%s", got, rendered)
	}
	if got := strings.Count(rendered, "┌"); got != 1 {
		t.Fatalf("coalesced exit-plan interaction rendered %d boxes, want 1:\n%s", got, rendered)
	}
	if !strings.Contains(rendered, "Approved · Completed") || !strings.Contains(rendered, "Exit with the structured plan") || strings.Contains(rendered, "mode_changed") || strings.Contains(rendered, `{"`) {
		t.Fatalf("coalesced exit-plan card is not structured:\n%s", rendered)
	}
}

func exitPlanPermissionRecord(id, callID string) client.PermissionRecord {
	arguments := `{"path_id":"permission.exit-plan-mode.v1","tool":"exit_plan_mode","plan_id":"plan-exit","title":"Exit with the structured plan","document":{"id":"plan-exit","title":"Exit with the structured plan","info":{"goal":"Implement the unified approval surface","scope":"V3 TUI permission presentation"},"checkpoints":[{"id":"cp-1","title":"Unify the card","status":"pending","order":1,"tasks":["Render structured plan content"],"acceptance_criteria":["No raw JSON is shown"]}]},"execution_granularity":"checkpointed","continuation_policy":"review_each_checkpoint","continue_automatically":false,"approved_arguments":{"keep":"canonical","plan_id":"plan-exit","title":"Exit with the structured plan","document":{"id":"plan-exit","title":"Exit with the structured plan","info":{"goal":"Implement the unified approval surface","scope":"V3 TUI permission presentation"},"checkpoints":[{"id":"cp-1","title":"Unify the card","status":"pending","order":1,"tasks":["Render structured plan content"],"acceptance_criteria":["No raw JSON is shown"]}]},"execution_granularity":"checkpointed","continuation_policy":"review_each_checkpoint","continue_automatically":false}}`
	return client.PermissionRecord{
		ID: id, SessionID: "session-exit", RunID: "run-exit", CallID: callID, ToolName: "exit_plan_mode",
		Requirement: "exit_plan_mode", Mode: "plan", Status: "pending", ToolArguments: arguments,
	}
}

func waitForPermissionResolution(t *testing.T, page *Page) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for page.PendingPermissionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if page.PendingPermissionVisible() {
		t.Fatal("permission resolution did not dismiss the pending card")
	}
}

func TestOpenCurrentPlanModalUsesFetchedPlan(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-plan"}, HasActivePlan: true, ActivePlan: &client.SessionPlan{ID: "stale", Document: &client.SessionPlanDocument{Title: "Stale plan"}}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	fetched := client.SessionPlan{ID: "fresh", Document: &client.SessionPlanDocument{Title: "Fresh plan"}}
	if !page.OpenCurrentPlanModal(fetched) || !page.PlanModalVisible() {
		t.Fatal("fetched current plan did not open")
	}
	if page.planModalPlan == nil || page.planModalPlan.ID != "fresh" {
		t.Fatalf("plan modal = %#v, want freshly fetched plan", page.planModalPlan)
	}
}

func TestCtrlPRequestsFreshCurrentPlan(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-plan"}, HasActivePlan: true, ActivePlan: &client.SessionPlan{ID: "stale", Document: &client.SessionPlanDocument{Title: "Stale plan"}}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	if action := page.HandleKey(tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModNone)); action != PageActionOpenCurrentPlan {
		t.Fatalf("Ctrl+P action = %v, want fresh current-plan request", action)
	}
	if page.PlanModalVisible() {
		t.Fatal("Ctrl+P opened cached plan before the API result")
	}
}

func TestPlanToolRowsRenderDedicatedCardAndWideTextKeepsBorder(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-plan"}, HasActivePlan: true, ActivePlan: &client.SessionPlan{ID: "plan", Document: &client.SessionPlanDocument{Title: "界 plan"}}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	tool := ToolTimelineItem{ID: "plan-tool", Name: "plan_manage", Status: "completed", Output: `{"action":"save","plan":{"title":"界 plan","document":{"title":"界 plan","info":{"goal":"Render safely"},"checkpoints":[{"id":"cp-1","title":"One","status":"completed"}]}}}`}
	rows := page.renderToolRows(tool, 40, testPageStyles())
	if len(rows) < 5 || !strings.HasPrefix(rows[0].text, "┌") || !strings.Contains(rows[1].text, "PLAN") || !strings.Contains(renderRowsText(rows), "1. One  ·  Completed") || !strings.Contains(renderRowsText(rows), "Ctrl+P or /plan  Open full plan") || strings.Contains(renderRowsText(rows), `{"`) {
		t.Fatalf("plan tool rows are not a dedicated structured card: %#v", rows)
	}
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(44, len(rows)+2)
	for index, row := range rows {
		if len(row.spans) > 0 {
			drawSpans(screen, 2, index+1, 40, row.spans)
		} else {
			drawText(screen, 2, index+1, 40, row.style, row.text)
		}
	}
	screen.Show()
	for index, row := range rows[:len(rows)-1] {
		if strings.HasSuffix(row.text, "┐") || strings.HasSuffix(row.text, "│") || strings.HasSuffix(row.text, "┘") {
			if got := simulationRow(screen, 44, index+1); !strings.Contains(got, string([]rune(row.text)[len([]rune(row.text))-1])) {
				t.Fatalf("wide plan text overwrote card border on row %d: %q", index, got)
			}
		}
	}
}

func renderRowsText(rows []renderRow) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.text)
	}
	return strings.Join(parts, "\n")
}

func TestPermissionCardRowsMatchOldChatHierarchyAndFilledActions(t *testing.T) {
	styles := PageStyles{
		Panel:        tcell.StyleDefault.Background(tcell.ColorBlack),
		Border:       tcell.StyleDefault.Foreground(tcell.ColorGray),
		BorderActive: tcell.StyleDefault.Foreground(tcell.ColorPurple),
		Text:         tcell.StyleDefault.Foreground(tcell.ColorWhite),
		Muted:        tcell.StyleDefault.Foreground(tcell.ColorGray),
		Secondary:    tcell.StyleDefault.Foreground(tcell.ColorBlue),
		Success:      tcell.StyleDefault.Foreground(tcell.ColorGreen),
		Error:        tcell.StyleDefault.Foreground(tcell.ColorRed),
		Accent:       tcell.StyleDefault.Foreground(tcell.ColorPurple),
		Warning:      tcell.StyleDefault.Foreground(tcell.ColorYellow),
	}
	record := client.PermissionRecord{
		ID: "permission", ToolName: "bash", Status: "pending", Mode: "auto",
		ToolArguments: `{"command":"pwd","explanation":["Inspect the working directory."],"category":"read","critical":false}`,
	}
	rows := inlinePermissionCardRows(record, 1, 88, styles, "pwd", true, []rune("safe"), false, "")
	if len(rows) < 9 || !strings.HasPrefix(rows[0].text, "┌") || !strings.Contains(rows[1].text, "Bash permission") || !strings.Contains(rows[2].text, "Approval required") {
		t.Fatalf("permission card hierarchy does not match the old chat card: %#v", rows)
	}
	borderForeground, _, _ := rows[0].style.Decompose()
	if borderForeground != tcell.ColorPurple {
		t.Fatalf("selected card border = %v, want active border color", borderForeground)
	}
	var actionRow renderRow
	for _, row := range rows {
		if len(row.actions) > 0 {
			actionRow = row
			break
		}
	}
	if len(actionRow.actions) != 4 || len(actionRow.spans) < 2 {
		t.Fatalf("permission action row = %#v, want four filled old-chat actions", actionRow)
	}
	foreground, background, attributes := actionRow.spans[1].style.Decompose()
	if background != tcell.ColorGreen || foreground != tcell.ColorWhite || attributes&tcell.AttrBold == 0 {
		t.Fatalf("approve action style = fg %v bg %v attrs %v; want filled success button", foreground, background, attributes)
	}
}

func TestPermissionCardRendersOnlyOutlineWithoutPanelBackgroundBleed(t *testing.T) {
	background := tcell.ColorNavy
	panel := tcell.ColorMaroon
	styles := PageStyles{
		Background:   tcell.StyleDefault.Background(background),
		Panel:        tcell.StyleDefault.Background(panel),
		Border:       tcell.StyleDefault.Foreground(tcell.ColorGray),
		BorderActive: tcell.StyleDefault.Foreground(tcell.ColorPurple),
		Text:         tcell.StyleDefault.Foreground(tcell.ColorWhite),
		Muted:        tcell.StyleDefault.Foreground(tcell.ColorGray),
	}
	rows := permissionCardRows(permissionCardView{
		Model: permissionCardModel{
			Title:   "Bash permission",
			Meta:    "Approval required",
			Content: []permissionCardLine{{Text: "COMMAND", Style: styles.Muted}, {Text: "pwd", Style: styles.Text}},
		},
		Selected: true,
	}, 40, styles)

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(48, len(rows)+2)
	fill(screen, 0, 0, 48, len(rows)+2, styles.Background)
	cardX, cardY := 3, 1
	for index, row := range rows {
		if len(row.spans) > 0 {
			drawSpans(screen, cardX, cardY+index, 40, row.spans)
		} else {
			drawText(screen, cardX, cardY+index, 40, row.style, row.text)
		}
	}
	screen.Show()

	cardRows := len(rows) - 1 // The final empty row separates timeline items.
	for y := cardY; y < cardY+cardRows; y++ {
		for _, x := range []int{cardX - 1, cardX + 40} {
			_, _, style, _ := screen.GetContent(x, y)
			_, gotBackground, _ := style.Decompose()
			if gotBackground != background {
				t.Fatalf("cell immediately outside permission border at (%d,%d) has background %v, want timeline background %v", x, y, gotBackground, background)
			}
		}
		for _, x := range []int{cardX, cardX + 1, cardX + 39} {
			_, _, style, _ := screen.GetContent(x, y)
			_, gotBackground, _ := style.Decompose()
			if gotBackground != background {
				t.Fatalf("permission outline cell at (%d,%d) has background %v, want unfilled timeline background %v", x, y, gotBackground, background)
			}
		}
	}
}

func TestAskUserPermissionRendersInteractiveChoicesAndSubmitsCanonicalAnswer(t *testing.T) {
	permission := client.PermissionRecord{
		ID: "permission-ask", SessionID: "session-ask", ToolName: "ask-user", Requirement: "user_input", Status: "pending",
		ToolArguments: `{"title":"Choose deployment","context":"Pick the safe target.","question":"Where should this run?","options":[{"label":"Staging","value":"staging","description":"Use test infrastructure."},{"label":"Production","value":"production"}]}`,
	}
	resolved := permission
	resolved.Status = "approved"
	resolved.Decision = "allow_once"
	transport := &fakeTransport{resolvedPermission: resolved}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-ask"}, PendingPermissions: []client.PermissionRecord{permission}}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())

	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	rows := page.renderRows(store.Snapshot(), 88, testPageStyles())
	drawn := renderRowsText(rows)
	for _, want := range []string{"Choose deployment", "Pick the safe target.", "Where should this run?", "1 Staging", "2 Production", "3 Custom response", "Enter Select", "S Submit"} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("ask-user card missing %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, `{"title"`) {
		t.Fatalf("ask-user card leaked raw JSON:\n%s", drawn)
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	deadline := time.Now().Add(time.Second)
	for page.PendingPermissionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	transport.mu.Lock()
	request := transport.permissionRequest
	transport.mu.Unlock()
	if request.permissionID != permission.ID || request.action != "allow_once" || request.reason != "production" {
		t.Fatalf("ask-user resolution request = %#v", request)
	}
}

func TestAskUserPermissionCustomResponseSubmitsTypedAnswer(t *testing.T) {
	permission := client.PermissionRecord{
		ID: "permission-ask-custom", SessionID: "session-ask-custom", ToolName: "ask-user", Requirement: "user_input", Status: "pending",
		ToolArguments: `{"question":"Where should this run?","options":["Staging","Production"]}`,
	}
	resolved := permission
	resolved.Status = "approved"
	resolved.Decision = "allow_once"
	transport := &fakeTransport{resolvedPermission: resolved}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: permission.SessionID}, PendingPermissions: []client.PermissionRecord{permission}}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())

	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	for _, r := range "my private target" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))

	deadline := time.Now().Add(time.Second)
	for page.PendingPermissionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	transport.mu.Lock()
	request := transport.permissionRequest
	transport.mu.Unlock()
	if request.permissionID != permission.ID || request.action != "allow_once" || request.reason != "my private target" {
		t.Fatalf("ask-user custom resolution request = %#v", request)
	}
}

func TestWorkspaceScopePermissionRendersChoicesAndForwardsAddDirDecision(t *testing.T) {
	permission := client.PermissionRecord{
		ID: "permission-scope", SessionID: "session-scope", ToolName: "read", Requirement: "workspace_scope", Status: "pending",
		ToolArguments: `{"title":"Allow read access outside the current workspace?","summary":"Choose temporary or saved access.","tool":{"name":"read"},"request":{"requested_path":"/external/project/file.go","resolved_target_path":"/external/project/file.go","directory_path":"/external/project","access_label":"read access"},"workspace":{"exists":true,"path":"/workspace","name":"Saved workspace"},"actions":{"session_allow":{"decision":"session_allow","label":"Allow This Session","available":true},"workspace_add_dir":{"decision":"workspace_add_dir","label":"Add To Workspace","available":true}}}`,
	}
	resolved := permission
	resolved.Status = "approved"
	resolved.Decision = "allow_once"
	transport := &fakeTransport{resolvedPermission: resolved}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-scope"}, PendingPermissions: []client.PermissionRecord{permission}}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())

	page.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	rows := page.renderRows(store.Snapshot(), 100, testPageStyles())
	drawn := renderRowsText(rows)
	for _, want := range []string{"Allow read access outside the current workspace?", "REQUESTED PATH", "/external/project/file.go", "SESSION SCOPE ROOT", "Allow This Session", "Add To Workspace", "Deny"} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("workspace-scope card missing %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, `{"title"`) {
		t.Fatalf("workspace-scope card leaked raw JSON:\n%s", drawn)
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	deadline := time.Now().Add(time.Second)
	for page.PendingPermissionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	transport.mu.Lock()
	request := transport.permissionRequest
	transport.mu.Unlock()
	var reason map[string]string
	if err := json.Unmarshal([]byte(request.reason), &reason); err != nil {
		t.Fatalf("decode workspace decision: %v (%q)", err, request.reason)
	}
	if request.permissionID != permission.ID || request.action != "allow_once" || reason["path_id"] != workspaceScopeDecisionPathID || reason["decision"] != "workspace_add_dir" {
		t.Fatalf("workspace-scope resolution request = %#v reason=%#v", request, reason)
	}
}

func TestBashPermissionCardUsesBackendRulePreviewLikeDesktop(t *testing.T) {
	permission := client.PermissionRecord{
		ID: "permission-bash", SessionID: "session-bash", ToolName: "bash", Mode: "auto", Status: "pending",
		ToolArguments: `{"command":"npm run build","explanation":["Build the workspace."],"category":"write","critical":false}`,
	}
	transport := &fakeTransport{permissionExplain: client.PermissionExplain{RulePreview: "allow bash prefix: npm"}}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "session-bash"}, PendingPermissions: []client.PermissionRecord{permission},
	}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 28)

	deadline := time.Now().Add(time.Second)
	for {
		page.Draw(screen)
		screen.Show()
		if strings.Contains(simulationText(screen, 100, 28), "npm") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend Bash prefix preview did not render:\n%s", simulationText(screen, 100, 28))
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBashPermissionCardFromToolMetadataUsesBoundedScrollableContent(t *testing.T) {
	explanations := make([]string, 0, 18)
	for index := 0; index < 18; index++ {
		explanations = append(explanations, fmt.Sprintf("Permission detail %02d with enough text to remain visible after wrapping.", index+1))
	}
	arguments, err := json.Marshal(map[string]any{
		"command":     "printf 'bounded bash permission card'",
		"explanation": explanations,
		"category":    "read",
		"critical":    false,
	})
	if err != nil {
		t.Fatal(err)
	}
	permission := client.PermissionRecord{
		ID: "permission-bash-bounded", SessionID: "session-bash", ToolName: "functions.bash", Mode: "auto", Status: "pending",
		ToolArguments: string(arguments),
	}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "session-bash"}, PendingPermissions: []client.PermissionRecord{permission},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())

	const availableHeight = 18
	rows := page.renderRowsForHeight(store.Snapshot(), 88, availableHeight, testPageStyles())
	if len(rows) != availableHeight {
		t.Fatalf("bounded Bash permission card rows = %d, want %d", len(rows), availableHeight)
	}
	drawn := renderRowsText(rows)
	for _, want := range []string{"Bash permission", "DETAILS 1-", "Enter Approve", "Esc Deny"} {
		if !strings.Contains(drawn, want) {
			t.Fatalf("bounded Bash permission card missing %q:\n%s", want, drawn)
		}
	}
	page.mu.Lock()
	maxScroll := page.permissionContentMaxScroll
	page.mu.Unlock()
	if maxScroll <= 0 {
		t.Fatal("overflowing Bash permission details did not enable internal scrolling")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone))
	page.mu.Lock()
	scroll := page.permissionContentScroll
	page.mu.Unlock()
	if scroll <= 0 {
		t.Fatal("PageDown did not scroll inside the Bash permission card")
	}
	rows = page.renderRowsForHeight(store.Snapshot(), 88, availableHeight, testPageStyles())
	if len(rows) != availableHeight || !strings.Contains(renderRowsText(rows), fmt.Sprintf("DETAILS %d-", scroll+1)) {
		t.Fatalf("scrolled Bash permission card changed height or omitted scroll position:\n%s", renderRowsText(rows))
	}

	page.mu.Lock()
	page.permissionContentScroll = page.permissionContentMaxScroll
	page.mu.Unlock()
	rows = page.renderRowsForHeight(store.Snapshot(), 88, availableHeight, testPageStyles())
	drawn = renderRowsText(rows)
	if len(rows) != availableHeight || !strings.Contains(drawn, "COMMAND") || !strings.Contains(drawn, "printf 'bounded bash permission card'") {
		t.Fatalf("Bash command was not reachable inside the fixed-height card:\n%s", drawn)
	}
}

func TestBashPermissionCardApproveUsesCanonicalV3PermissionAPI(t *testing.T) {
	permission := client.PermissionRecord{
		ID: "permission-bash", SessionID: "session-bash", ToolName: "bash", Status: "pending",
		ToolArguments: `{"command":"npm run build","explanation":["Build the workspace."],"category":"write","critical":false}`,
	}
	resolved := permission
	resolved.Status = "approved"
	resolved.Decision = "allow_once"
	transport := &fakeTransport{resolvedPermission: resolved}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "session-bash"}, PendingPermissions: []client.PermissionRecord{permission},
	}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
	for _, r := range "looks good" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	deadline := time.Now().Add(time.Second)
	for page.PendingPermissionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if page.PendingPermissionVisible() {
		t.Fatal("approved Bash permission stayed pending")
	}
	resolvedItems := SelectPermissions(store.Snapshot())
	if len(resolvedItems) != 1 || resolvedItems[0].Record.Status != "approved" || resolvedItems[0].Record.Decision != "allow_once" {
		t.Fatalf("resolved permission timeline state = %#v", resolvedItems)
	}
	transport.mu.Lock()
	request := transport.permissionRequest
	transport.mu.Unlock()
	if request.sessionID != "session-bash" || request.permissionID != "permission-bash" || request.action != "allow_once" || request.reason != "looks good" {
		t.Fatalf("permission resolution request = %#v", request)
	}
}

func TestAgentModelFooterMouseRequestsCanonicalAgentsModal(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-agents"}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 28)
	page.Draw(screen)

	page.mu.Lock()
	target := page.agentModelTarget
	page.mu.Unlock()
	if target.W == 0 || target.H == 0 {
		t.Fatal("agent/model footer did not expose a mouse target")
	}
	page.HandleMouse(tcell.NewEventMouse(target.X, target.Y, tcell.Button1, tcell.ModNone))
	if !page.ConsumeOpenAgentsRequest() {
		t.Fatal("agent/model footer did not request /agents")
	}
	if page.modelPicker {
		t.Fatal("agent/model footer opened the legacy model picker")
	}
}

func TestBashPermissionCardMouseApproveUsesCanonicalV3PermissionAPI(t *testing.T) {
	permission := client.PermissionRecord{
		ID: "permission-mouse", SessionID: "session-bash", ToolName: "bash", Status: "pending",
		ToolArguments: `{"command":"pwd","explanation":["Inspect the working directory."],"category":"read","critical":false}`,
	}
	resolved := permission
	resolved.Status = "approved"
	resolved.Decision = "allow_once"
	transport := &fakeTransport{resolvedPermission: resolved}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "session-bash"}, PendingPermissions: []client.PermissionRecord{permission},
	}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 28)
	page.Draw(screen)

	page.mu.Lock()
	target := page.permissionApproveTarget
	page.mu.Unlock()
	if target.W == 0 || target.H == 0 {
		t.Fatal("inline approve action did not expose a mouse target")
	}
	page.HandleMouse(tcell.NewEventMouse(target.X, target.Y, tcell.Button1, tcell.ModNone))
	deadline := time.Now().Add(time.Second)
	for page.PendingPermissionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	transport.mu.Lock()
	request := transport.permissionRequest
	transport.mu.Unlock()
	if request.sessionID != "session-bash" || request.permissionID != "permission-mouse" || request.action != "allow_once" {
		t.Fatalf("mouse permission resolution request = %#v", request)
	}
}

func TestBashPermissionRealtimeEventSelectsPermissionCard(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-bash", Title: "Bash card"}}})
	permission := client.PermissionRecord{
		ID: "permission-bash", SessionID: "session-bash", RunID: "run-bash", CallID: "call-bash",
		ToolName: "bash", Requirement: "tool", Mode: "auto", Status: "pending",
		ToolArguments: `{"command":"npm run build","explanation":["Build the workspace."],"category":"write","critical":false}`,
	}
	payload, err := json.Marshal(map[string]any{"permission": permission})
	if err != nil {
		t.Fatal(err)
	}
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "session-bash", Seq: 1, EventType: "permission.requested", Payload: payload}}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	if !page.PendingPermissionVisible() {
		t.Fatal("realtime Bash permission did not activate permission card")
	}

	resolved := permission
	resolved.Status = "approved"
	resolved.Decision = "allow_once"
	payload, err = json.Marshal(map[string]any{"permission": resolved})
	if err != nil {
		t.Fatal(err)
	}
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{SessionID: "session-bash", Seq: 2, EventType: "permission.updated", Payload: payload}}})
	if page.PendingPermissionVisible() {
		t.Fatal("resolved realtime Bash permission stayed pending")
	}
	items := SelectPermissions(store.Snapshot())
	if len(items) != 1 || items[0].GlobalSeq != 1 || items[0].Record.Status != "approved" {
		t.Fatalf("resolved permission did not retain its timeline position: %#v", items)
	}

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 28)
	page.Draw(screen)
	screen.Show()
	drawn := simulationText(screen, 100, 28)
	if !strings.Contains(drawn, "Resolved · Approved once") || !strings.Contains(drawn, "RESOLVED") {
		t.Fatalf("resolved Bash permission card was removed instead of updated:\n%s", drawn)
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

func TestPageCanonicalHeaderKeepsPlanStateAndShowsRunStateAboveComposer(t *testing.T) {
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
		t.Fatalf("run indicator leaked into canonical header: %q", header)
	}
	conversationState := simulationRow(screen, 100, 13)
	if !strings.Contains(conversationState, "Running  0:05 (1:35)") {
		t.Fatalf("running state and timer missing above composer: %q", conversationState)
	}
	separator := simulationRow(screen, 100, 14)
	footerSeparator := simulationRow(screen, 100, 16)
	if strings.Contains(separator, "Running") || strings.Contains(separator, "0:05") || strings.Contains(footerSeparator, "Running") || strings.Contains(footerSeparator, "0:05") {
		t.Fatalf("run state remains on a composer/footer separator: composer=%q footer=%q", separator, footerSeparator)
	}
	if strings.Contains(conversationState, "Swarming") {
		t.Fatalf("legacy Swarming label remains in active run state: %q", conversationState)
	}
	if strings.Contains(simulationText(screen, 100, 18), "Connected") || strings.Contains(simulationText(screen, 100, 18), "connected") || strings.Contains(header, "Swarm") {
		t.Fatalf("redundant connection/header chrome remains:\n%s", simulationText(screen, 100, 18))
	}
}

func TestPageCanonicalHeaderWrapsToSecondLineAtNarrowWidth(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:       client.SessionSummary{ID: "s", Title: "A deliberately long canonical session title"},
		HasActivePlan: true,
		ActivePlan:    activePlanFixture("running", "in_progress", "cp-1", "A long checkpoint title that needs the second header row"),
		Messages:      []client.SessionMessage{{ID: "user-1", Role: "user", Content: "message below wrapped title"}},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	defer page.Close()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(42, 18)
	page.Draw(screen)
	screen.Show()

	first, second := simulationRow(screen, 42, 0), simulationRow(screen, 42, 1)
	if !strings.Contains(first, "A deliberately long canonical session") || strings.TrimSpace(second) == "" {
		t.Fatalf("narrow header did not use two rows: first=%q second=%q", first, second)
	}
	if transcript := simulationRow(screen, 42, 3); !strings.Contains(transcript, "> message below wrapped title") {
		t.Fatalf("wrapped header did not advance transcript offset: %q", transcript)
	}
}

func TestPageToolHeaderAndStyledAssistantContentWrapAtChatEdges(t *testing.T) {
	styles := testPageStyles()
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), styles)

	tool := ToolTimelineItem{
		ID:     "tool-wrap",
		Name:   "custom_tool",
		Output: `{"summary":"custom-tool a deliberately long tool summary that must wrap inside the transcript edge"}`,
		Status: "completed",
	}
	toolRows := page.renderToolRows(tool, 24, styles)
	if len(toolRows) < 4 || !strings.Contains(toolRows[0].text, "custom-tool") || !strings.Contains(toolRows[1].text, "deliberately") {
		t.Fatalf("tool header did not wrap across styled rows: %#v", toolRows)
	}
	for _, row := range toolRows {
		if width := displayCellWidth(row.text); width > 24 {
			t.Fatalf("tool row %q is %d cells wide, want at most 24", row.text, width)
		}
	}

	styles.RenderMarkdown = func(_ string, _ int) []MarkdownLine {
		return []MarkdownLine{{
			Style: styles.Text,
			Spans: []MarkdownSpan{
				{Text: "styled assistant content ", Style: styles.Text.Bold(true)},
				{Text: "that reaches the edge and wraps", Style: styles.Secondary},
			},
		}}
	}
	assistantRows := page.renderAssistantRows("ignored", 18, styles)
	if len(assistantRows) < 2 {
		t.Fatalf("styled assistant content did not wrap: %#v", assistantRows)
	}
	for _, row := range assistantRows {
		if width := displayCellWidth(row.text); width > 18 {
			t.Fatalf("assistant row %q is %d cells wide, want at most 18", row.text, width)
		}
	}
}

func TestPageRendersTerminalConversationStateAboveComposerWithoutFooterDisplacement(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "s", Title: "Canonical title"},
		ActiveRunIntent: &client.SessionV3RunIntent{
			RunID: "run", Status: "cancelled", DurationMS: 5_000,
		},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	page.SetRouteLabel("Primary Desk")
	defer page.Close()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 18)
	page.DrawAt(screen, time.UnixMilli(125_000))
	screen.Show()

	if stateRow := simulationRow(screen, 80, 13); !strings.Contains(stateRow, "Stopped  0:05") {
		t.Fatalf("terminal conversation state missing above composer: %q", stateRow)
	}
	if footer := simulationRow(screen, 80, 17); !strings.Contains(footer, "Primary Desk") || strings.Contains(footer, "Stopped") || strings.Contains(footer, "0:05") {
		t.Fatalf("terminal state displaced canonical footer metadata: %q", footer)
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
	if !strings.Contains(drawn, "Primary Desk") || strings.Contains(drawn, "Plan: off") || strings.Contains(drawn, " Plan ") || !strings.Contains(drawn, "[gpt-test · high · fast]") {
		t.Fatalf("canonical home footer tokens or hidden plan state are wrong:\n%s", drawn)
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

func TestPageComposerNoticePolicyShowsThinkingTagStatusWarningsAndStopsWithoutTruncatingFooter(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	page.SetRouteLabel("Primary Desk")
	page.SetProfileLabel("Focused work")

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 18)

	for _, hidden := range []string{"sent", "sending…", "reconnected", "model set • gpt-test"} {
		page.SetStatus(hidden)
		page.Draw(screen)
		screen.Show()
		if separator := simulationRow(screen, 80, 14); strings.Contains(separator, hidden) {
			t.Fatalf("routine status %q rendered on composer separator: %q", hidden, separator)
		}
	}

	for _, visible := range []string{"thinking tags off", "stop requested", "settings warning: profile unavailable"} {
		page.SetStatus(visible)
		page.Draw(screen)
		screen.Show()
		if separator := simulationRow(screen, 80, 14); !strings.Contains(separator, visible) {
			t.Fatalf("stop/warning status %q missing from composer separator: %q", visible, separator)
		}
	}

	footer := simulationRow(screen, 80, 17)
	if strings.Contains(footer, "warning") || strings.Contains(footer, "stop requested") || strings.Contains(footer, "sent") {
		t.Fatalf("composer notice remains in footer: %q", footer)
	}
	if strings.Contains(footer, "Plan: off") || strings.Contains(footer, " Plan ") {
		t.Fatalf("inactive plan indicator remains in footer: %q", footer)
	}
	for _, want := range []string{"Primary Desk", "[Focused work"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer token %q was displaced or truncated: %q", want, footer)
		}
	}
}

func TestPageLongGeneralErrorRendersRightAlignedOnComposerSeparator(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	page.SetRouteLabel("Primary Desk")
	page.SetCommandEmission("command output")
	page.finishAsync("", fmt.Errorf("provider rejected the request because the selected model is unavailable for this workspace and profile"))

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(60, 14)
	page.Draw(screen)
	screen.Show()

	separator := simulationRow(screen, 60, 10)
	footer := simulationRow(screen, 60, 13)
	if !strings.Contains(separator, "error • provider rejected") || !strings.Contains(separator, "…") {
		t.Fatalf("long error missing or not visibly truncated on composer separator: %q", separator)
	}
	if strings.HasSuffix(separator, "─") || !strings.HasSuffix(separator, " ") {
		t.Fatalf("composer error is not right-aligned against the separator edge: %q", separator)
	}
	if strings.Contains(footer, "error •") || strings.Contains(footer, "provider rejected") {
		t.Fatalf("general error remains in footer: %q", footer)
	}
	if !strings.Contains(footer, "Primary Desk") {
		t.Fatalf("footer metadata was displaced by general error: %q", footer)
	}
	if strings.Contains(separator, "command output") {
		t.Fatalf("command emission overlaps the higher-priority general error: %q", separator)
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

func TestPageComposerLargePasteFlushesInBatchesAtCursor(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, NewStore(), nil), testPageStyles())
	for _, r := range "after" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))

	paste := strings.Repeat("界", composerPasteFlushChunkRunes) + "\nsecond line"
	page.SetPasteActive(true)
	for i, r := range paste {
		flushed := page.HandlePasteKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		if i < composerPasteFlushChunkRunes-1 && flushed {
			t.Fatalf("paste flushed early after %d runes", i+1)
		}
		if i == composerPasteFlushChunkRunes-1 && !flushed {
			t.Fatalf("paste did not flush at %d-rune batch boundary", composerPasteFlushChunkRunes)
		}
	}
	page.SetPasteActive(false)

	want := paste + "after"
	if got := page.InputValue(); got != want {
		t.Fatalf("large pasted input = %q, want %q", got, want)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, '!', tcell.ModNone))
	if got := page.InputValue(); got != paste+"!after" {
		t.Fatalf("cursor after large paste = %q, want insertion before suffix", got)
	}
}

func TestComposerLayoutMovesWrappingWordToNextLine(t *testing.T) {
	text := "alpha beta"
	lines, cursorLine, cursorCol := composerLayout(text, len([]rune(text)), 9)

	want := []string{"> alpha", "  beta"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("composer lines = %#v, want %#v", lines, want)
	}
	if cursorLine != 1 || cursorCol != len([]rune(want[1])) {
		t.Fatalf("composer cursor = %d:%d, want 1:%d", cursorLine, cursorCol, len([]rune(want[1])))
	}
}

func TestComposerLayoutPreservesNewlinesAndSplitsOnlyOverlongWords(t *testing.T) {
	text := "123456789\nok"
	lines, cursorLine, cursorCol := composerLayout(text, len([]rune(text)), 8)

	want := []string{"> 123456", "  789", "  ok"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("composer lines = %#v, want %#v", lines, want)
	}
	if cursorLine != 2 || cursorCol != len([]rune(want[2])) {
		t.Fatalf("composer cursor = %d:%d, want 2:%d", cursorLine, cursorCol, len([]rune(want[2])))
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

func TestExitPlanModeCanonicalEventUpdatesRenderedFooterPolicy(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:         client.SessionSummary{ID: "s", Title: "chat", Mode: "plan", UpdatedAt: 100},
		Preference:      client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "low"},
		ContextWindow:   200000,
		MaxOutputTokens: 12000,
		AgentModelPolicy: client.SessionV3AgentModelPolicy{
			Locked: true, ProfileName: "Planning", ProfileSource: "saved",
			Preference: client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "low"}, ContextWindow: 200000, MaxOutputTokens: 12000,
		},
	}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	page.SetRouteLabel("Primary Desk")
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(90, 18)
	page.Draw(screen)
	screen.Show()
	before := simulationRow(screen, 90, 17)
	if !strings.Contains(before, " Plan ") || !strings.Contains(before, "[Planning · plan-model · low]") {
		t.Fatalf("plan footer = %q", before)
	}

	preference := client.ModelPreference{Provider: "codex", Model: "auto-model", Thinking: "high", ServiceTier: "fast"}
	policy := client.SessionV3AgentModelPolicy{
		Source: "agent_auto_preset", Locked: true, ProfileName: "Automatic", ProfileSource: "saved",
		Preference: preference, ContextWindow: 180000, MaxOutputTokens: 16000,
	}
	payload, _ := json.Marshal(map[string]any{
		"mode": "auto", "updated_at": int64(200), "preference": preference,
		"context_window": 180000, "max_output_tokens": 16000, "agent_model_policy": policy,
	})
	store.Dispatch(RealtimeFrameAction{Frame: client.V3RealtimeFrame{Kind: "event", Event: &client.SessionV3Event{
		SessionID: "s", Seq: 2, EventType: "session.mode.updated", Payload: payload,
	}}})
	page.Draw(screen)
	screen.Show()
	after := simulationRow(screen, 90, 17)
	if strings.Contains(after, " Plan ") || !strings.Contains(after, "[Automatic · auto-model · high · fast]") || strings.Contains(after, "plan-model") {
		t.Fatalf("auto footer after canonical exit_plan_mode event = %q", after)
	}
	state := store.Snapshot()
	if state.Session.Mode != "auto" || state.Session.UpdatedAt != 200 || state.Model.ContextWindow != 180000 || state.Model.MaxOutputTokens != 16000 {
		t.Fatalf("canonical footer state = %#v", state)
	}
}

func TestShiftTabCyclesRoutedPrimerPlanFlagLocally(t *testing.T) {
	transport := &fakeTransport{}
	runtime := NewRuntime(transport, nil, nil)
	if err := runtime.PrimeRoutedDraft(RoutedDraft{ManagedWorktreeRequested: true}); err != nil {
		t.Fatal(err)
	}
	page := NewPage(runtime, testPageStyles())
	page.HandleKey(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModShift))
	state := runtime.Store().Snapshot()
	draft, ok := SelectRoutedDraft(state)
	if !ok || state.Session.ID != "" || !draft.PlanModeRequested || !draft.ManagedWorktreeRequested {
		t.Fatalf("Shift+Tab routed draft state = %#v", state)
	}
	if got := page.Status(); got != "Plan: on" {
		t.Fatalf("plan draft status = %q", got)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModShift))
	draft, _ = SelectRoutedDraft(runtime.Store().Snapshot())
	if draft.PlanModeRequested || !draft.ManagedWorktreeRequested {
		t.Fatalf("Shift+Tab routed draft round trip = %#v", draft)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.calls) != 0 || len(transport.createRequests) != 0 || transport.modeRequest != "" {
		t.Fatalf("Shift+Tab persisted draft: calls=%#v creates=%#v mode=%q", transport.calls, transport.createRequests, transport.modeRequest)
	}
}

func TestShiftTabDoesNotMutateDurableSessionMode(t *testing.T) {
	transport := &fakeTransport{mode: client.SessionV3ModeResult{Mode: "plan"}}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "s", Mode: "auto"}, Preference: client.ModelPreference{Provider: "codex", Model: "auto-model"}}})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
	page.HandleKey(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModShift))
	state := store.Snapshot()
	if state.Session.Mode != "auto" {
		t.Fatalf("Shift+Tab mutated durable session mode: %#v", state)
	}
	transport.mu.Lock()
	modeRequest := transport.modeRequest
	transport.mu.Unlock()
	if modeRequest != "" {
		t.Fatalf("durable Shift+Tab mode request = %q", modeRequest)
	}
	if got := page.Status(); !strings.Contains(got, "new session draft") {
		t.Fatalf("durable Shift+Tab status = %q", got)
	}
}

func TestWorktreeCommandUpdatesOnlyReadyRoutedPrimer(t *testing.T) {
	runtime := NewRuntime(&fakeTransport{}, nil, nil)
	if err := runtime.PrimeRoutedDraft(RoutedDraft{}); err != nil {
		t.Fatal(err)
	}
	page := NewPage(runtime, testPageStyles())
	matched, err := page.ApplyWorktreeCommand("/wt on")
	if err != nil || !matched {
		t.Fatalf("apply /wt on = matched=%v err=%v", matched, err)
	}
	draft, _ := SelectRoutedDraft(runtime.Store().Snapshot())
	if !draft.ManagedWorktreeRequested || draft.PlanModeRequested || draft.ClientRequestID != "" {
		t.Fatalf("worktree primer = %#v", draft)
	}
	matched, err = page.ApplyWorktreeCommand("/worktrees")
	if err != nil || matched {
		t.Fatalf("/worktrees captured = matched=%v err=%v", matched, err)
	}
}

func TestFailedRoutedDraftRetriesOnEmptySubmit(t *testing.T) {
	transport := &fakeTransport{routedResponses: []client.RoutedSessionV3StartResponse{routedRuntimeResponse("session-retried")}}
	store := NewStore()
	store.Dispatch(PrimeRoutedDraftAction{Draft: RoutedDraft{Prompt: "retry this", ClientRequestID: "retry-id"}})
	store.Dispatch(RoutedDraftFailedAction{Error: "router unavailable"})
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())

	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	deadline := time.Now().Add(time.Second)
	for store.Snapshot().Session.ID == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := store.Snapshot().Session.ID; got != "session-retried" {
		t.Fatalf("empty-submit retry session = %q", got)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.routedRequests) != 1 || transport.routedRequests[0].ClientRequestID != "retry-id" {
		t.Fatalf("empty-submit retry requests = %#v", transport.routedRequests)
	}
}

func TestRoutedDraftRowsKeepPromptStatusFlagsAndRetryGuidanceLocal(t *testing.T) {
	store := NewStore()
	store.Dispatch(PrimeRoutedDraftAction{Draft: RoutedDraft{Prompt: "route this", PlanModeRequested: true, ManagedWorktreeRequested: true}})
	page := NewPage(NewRuntime(&fakeTransport{}, store, nil), testPageStyles())
	rows := page.renderRows(store.Snapshot(), 80, testPageStyles())
	joined := ""
	for _, row := range rows {
		joined += row.text + "\n"
	}
	for _, want := range []string{"route this", "Waiting...", "Plan: on", "Worktree: on"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ready routed rows missing %q:\n%s", want, joined)
		}
	}
	store.Dispatch(RoutedDraftRoutingAction{})
	rows = page.renderRows(store.Snapshot(), 80, testPageStyles())
	joined = ""
	for _, row := range rows {
		joined += row.text + "\n"
	}
	if !strings.Contains(joined, "Routing...") || !strings.Contains(joined, "route this") {
		t.Fatalf("routing rows lost local prompt/status:\n%s", joined)
	}
	store.Dispatch(RoutedDraftFailedAction{Error: "router unavailable"})
	rows = page.renderRows(store.Snapshot(), 80, testPageStyles())
	joined = ""
	for _, row := range rows {
		joined += row.text + "\n"
	}
	for _, want := range []string{"route this", "router unavailable", "retry the same request"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("failed routed rows missing %q:\n%s", want, joined)
		}
	}
	if state := store.Snapshot(); state.Session.ID != "" || state.Session.Title != "" || state.Session.WorkspaceName != "" {
		t.Fatalf("local draft invented canonical authority: %#v", state.Session)
	}
}

func TestEscapeStopsActiveRunThroughCanonicalV3Path(t *testing.T) {
	transport := &fakeTransport{}
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session:         client.SessionSummary{ID: "session", Metadata: map[string]any{"swarm_v3_runtime_swarm_id": "session-swarm"}},
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
			if request.sessionID != "session" || request.runID != "run" || request.targetSwarmID != "session-swarm" || request.reason != "stopped from TUI" {
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

func TestPageCoalescesPlanPermissionAndCorrelatedToolResultIntoOneCard(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), testPageStyles())
	arguments := `{"title":"One plan interaction","document":{"title":"One plan interaction","info":{"goal":"Avoid duplicate boxes."},"checkpoints":[{"id":"cp-1","title":"Do the work","status":"pending","order":1}]}}`
	permission := client.PermissionRecord{
		ID: "permission-plan", CallID: "call-plan", ToolName: "plan_manage", Requirement: "plan_new_request", Mode: "auto",
		Status: "approved", Decision: "allow_once", ExecutionStatus: "completed", ToolArguments: arguments,
	}
	toolPayload, err := json.Marshal(map[string]any{
		"path_id": "run.tool-history.v2", "tool": "plan_manage", "call_id": "call-plan",
		"arguments": arguments, "completed_output": `{"action":"request_new_plan","title":"One plan interaction","document":{"title":"One plan interaction","info":{"goal":"Avoid duplicate boxes."},"checkpoints":[{"id":"cp-1","title":"Do the work","status":"pending","order":1}]}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := State{
		Messages:    []Message{{ID: "tool-plan", GlobalSeq: 4, Role: "tool", Content: string(toolPayload)}},
		Permissions: PermissionState{Records: []PermissionTimelineItem{{Record: permission, GlobalSeq: 2}}},
	}

	rendered := renderRowsText(page.renderRows(state, 100, testPageStyles()))
	if got := strings.Count(rendered, "Plan approval"); got != 1 {
		t.Fatalf("plan approval card count = %d, want 1:\n%s", got, rendered)
	}
	if got := strings.Count(rendered, "┌"); got != 1 {
		t.Fatalf("plan interaction rendered %d card boxes, want 1:\n%s", got, rendered)
	}
	for _, want := range []string{"Approved · Completed", "One plan interaction", "1. Do the work  ·  Pending", "Ctrl+P or /plan  Open full plan"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("coalesced plan card missing %q:\n%s", want, rendered)
		}
	}
	for _, raw := range []string{"completed_output", "acceptance_criteria", `{"`} {
		if strings.Contains(rendered, raw) {
			t.Fatalf("coalesced plan card leaked raw JSON marker %q:\n%s", raw, rendered)
		}
	}
}

func TestPageDoesNotCoalesceUncorrelatedPlanToolResult(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), testPageStyles())
	state := State{
		Messages: []Message{{
			ID: "tool-plan", GlobalSeq: 4, Role: "tool",
			Content: `{"path_id":"run.tool-history.v2","tool":"plan_manage","call_id":"call-other","completed_output":"{\"action\":\"save\",\"document\":{\"title\":\"Independent update\",\"checkpoints\":[{\"id\":\"cp-1\",\"title\":\"One\"}]}}"}`,
		}},
		Permissions: PermissionState{Records: []PermissionTimelineItem{{Record: client.PermissionRecord{
			ID: "permission-plan", CallID: "call-plan", ToolName: "plan_manage", Requirement: "plan_new_request", Status: "approved",
			ToolArguments: `{"document":{"title":"Proposal","checkpoints":[{"id":"cp-1","title":"Propose"}]}}`,
		}, GlobalSeq: 2}}},
	}

	rendered := renderRowsText(page.renderRows(state, 100, testPageStyles()))
	if got := strings.Count(rendered, "┌"); got != 2 {
		t.Fatalf("uncorrelated plan interactions rendered %d card boxes, want 2:\n%s", got, rendered)
	}
	if !strings.Contains(rendered, "Independent update") {
		t.Fatalf("uncorrelated plan tool result was hidden:\n%s", rendered)
	}
}

func TestPageRendersPermissionAtDurableTimelinePositionAndAtBottomWithoutSequence(t *testing.T) {
	page := NewPage(NewRuntime(&fakeTransport{}, nil, nil), testPageStyles())
	permission := client.PermissionRecord{
		ID: "permission", ToolName: "bash", Status: "pending", CreatedAt: 300,
		ToolArguments: `{"command":"pwd","explanation":["Inspect the working directory."],"category":"read","critical":false}`,
	}
	state := State{
		Messages: []Message{
			{ID: "before", GlobalSeq: 2, CreatedAt: 200, Role: "assistant", Content: "before permission"},
			{ID: "after", GlobalSeq: 4, CreatedAt: 400, Role: "assistant", Content: "after permission"},
		},
		Permissions: PermissionState{Records: []PermissionTimelineItem{{Record: permission, GlobalSeq: 3}}},
	}
	rows := page.renderRows(state, 80, testPageStyles())
	var joined strings.Builder
	for _, row := range rows {
		joined.WriteString(row.text)
		joined.WriteByte('\n')
	}
	text := joined.String()
	before, card, after := strings.Index(text, "before permission"), strings.Index(text, "Bash permission"), strings.Index(text, "after permission")
	if before < 0 || card < 0 || after < 0 || !(before < card && card < after) {
		t.Fatalf("permission timeline order mismatch: before=%d card=%d after=%d\n%s", before, card, after, text)
	}

	state.Permissions.Records[0].GlobalSeq = 0
	state.Permissions.Records[0].Record.CreatedAt = 0
	rows = page.renderRows(state, 80, testPageStyles())
	joined.Reset()
	for _, row := range rows {
		joined.WriteString(row.text)
		joined.WriteByte('\n')
	}
	text = joined.String()
	if card, after = strings.Index(text, "Bash permission"), strings.Index(text, "after permission"); card <= after {
		t.Fatalf("unsequenced permission was not placed in the next available bottom section:\n%s", text)
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
	return PageStyles{Background: tcell.StyleDefault, Panel: tcell.StyleDefault, Border: tcell.StyleDefault, BorderActive: tcell.StyleDefault, Text: tcell.StyleDefault, Muted: tcell.StyleDefault, Primary: tcell.StyleDefault, Accent: tcell.StyleDefault, Secondary: tcell.StyleDefault, Success: tcell.StyleDefault, Warning: tcell.StyleDefault, Error: tcell.StyleDefault, Prompt: tcell.StyleDefault, Cursor: tcell.StyleDefault.Reverse(true)}
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
