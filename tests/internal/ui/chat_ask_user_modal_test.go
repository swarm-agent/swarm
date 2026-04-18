package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestAskUserPermissionUsesDedicatedModal(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_ask_1",
		SessionID:     "session-1",
		ToolName:      "ask-user",
		ToolArguments: `{"question":"Pick one","options":["A","B"]}`,
		Status:        "pending",
	})

	if !page.AskUserModalVisible() {
		t.Fatalf("expected ask-user modal to be visible")
	}
	if page.PermissionModalVisible() {
		t.Fatalf("generic permission modal should stay hidden for ask-user permission")
	}
}

func TestAskUserModalSubmitsMultipleQuestionAnswers(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:        "perm_ask_1",
		SessionID: "session-1",
		ToolName:  "ask-user",
		ToolArguments: `{
			"title":"Need input",
			"questions":[
				{"id":"q_mode","question":"Which mode?","options":[{"label":"Auto","value":"auto"},{"label":"Yolo","value":"yolo"}]},
				{"id":"q_apply","question":"Apply now?","options":[{"label":"Yes","value":"yes"},{"label":"No","value":"no"}]}
			]
		}`,
		Status: "pending",
	})

	if !page.AskUserModalVisible() {
		t.Fatalf("expected ask-user modal to open")
	}

	// Question 1: choose Yolo.
	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	// Question 2: keep default Yes and submit.
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	deadline := time.Now().Add(400 * time.Millisecond)
	for len(backend.resolveCalls) == 0 && time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.resolveCalls) == 0 {
		t.Fatalf("expected ResolvePermission call")
	}
	call := backend.resolveCalls[0]
	if call.Action != "approve" {
		t.Fatalf("resolve action = %q, want approve", call.Action)
	}

	var payload struct {
		Answers map[string]string `json:"answers"`
	}
	if err := json.Unmarshal([]byte(call.Reason), &payload); err != nil {
		t.Fatalf("decode ask-user reason payload: %v", err)
	}
	if got := payload.Answers["q_mode"]; got != "yolo" {
		t.Fatalf("q_mode answer = %q, want yolo", got)
	}
	if got := payload.Answers["q_apply"]; got != "yes" {
		t.Fatalf("q_apply answer = %q, want yes", got)
	}
}

func TestAskUserModalEscapeDeniesPermission(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_ask_1",
		SessionID:     "session-1",
		ToolName:      "ask-user",
		ToolArguments: `{"question":"Pick one","options":["A","B"]}`,
		Status:        "pending",
	})
	if !page.AskUserModalVisible() {
		t.Fatalf("expected ask-user modal to be visible")
	}

	if consumed := page.HandleEscape(); !consumed {
		t.Fatalf("HandleEscape() consumed = false, want true")
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for len(backend.resolveCalls) == 0 && time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.resolveCalls) == 0 {
		t.Fatalf("expected ResolvePermission call")
	}
	if backend.resolveCalls[0].Action != "deny" {
		t.Fatalf("resolve action = %q, want deny", backend.resolveCalls[0].Action)
	}
}

func TestAskUserModalCustomInputOptionSubmitsTypedResponse(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:        "perm_ask_custom",
		SessionID: "session-1",
		ToolName:  "ask-user",
		ToolArguments: `{
			"question":"Any custom notes?",
			"options":[
				{"label":"Type your response","value":"__custom__","allowCustom":true}
			]
		}`,
		Status: "pending",
	})
	if !page.AskUserModalVisible() {
		t.Fatalf("expected ask-user modal to be visible")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	for _, r := range "Ship now" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	deadline := time.Now().Add(400 * time.Millisecond)
	for len(backend.resolveCalls) == 0 && time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.resolveCalls) == 0 {
		t.Fatalf("expected ResolvePermission call")
	}
	call := backend.resolveCalls[0]
	if call.Action != "approve" {
		t.Fatalf("resolve action = %q, want approve", call.Action)
	}
	if call.Reason != "Ship now" {
		t.Fatalf("resolve reason = %q, want %q", call.Reason, "Ship now")
	}
}

func TestAskUserPayloadDefaultsToTypedResponseOption(t *testing.T) {
	record := ChatPermissionRecord{
		ToolName:      "ask-user",
		ToolArguments: `{"question":"Need details"}`,
	}
	_, _, questions := askUserPayloadFromPermission(record)
	if len(questions) != 1 {
		t.Fatalf("len(questions) = %d, want 1", len(questions))
	}
	if len(questions[0].Options) != 1 {
		t.Fatalf("len(options) = %d, want 1", len(questions[0].Options))
	}
	option := questions[0].Options[0]
	if !option.AllowCustom {
		t.Fatalf("option.AllowCustom = false, want true")
	}
	if option.Value != "__custom__" {
		t.Fatalf("option.Value = %q, want __custom__", option.Value)
	}
}

func TestAskUserPromptRenderLines_StripsInlineMarkdownMarkers(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.askUserContext = "Use the **current** workspace."
	lines := page.askUserPromptRenderLines(chatAskUserQuestion{
		Question: "Choose **mode**",
		Required: true,
	}, 80)
	if len(lines) == 0 {
		t.Fatalf("askUserPromptRenderLines returned no lines")
	}
	text := make([]string, 0, len(lines))
	for _, line := range lines {
		text = append(text, chatRenderLineText(line))
	}
	joined := strings.Join(text, "\n")
	if strings.Contains(joined, "**mode**") || strings.Contains(joined, "**current**") {
		t.Fatalf("inline markdown markers leaked into prompt rendering: %q", joined)
	}
	if !strings.Contains(joined, "Choose mode (required)") {
		t.Fatalf("required prompt label missing: %q", joined)
	}
}

func TestAskUserModalDrawShowsSelectedOptionDescription(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	page.upsertPendingPermission(ChatPermissionRecord{
		ID:        "perm_ask_desc",
		SessionID: "session-1",
		ToolName:  "ask-user",
		ToolArguments: `{
			"question":"Which mode?",
			"options":[
				{"label":"Auto","value":"auto","description":"Runs with normal approvals."},
				{"label":"Yolo","value":"yolo","description":"Runs without asking."}
			]
		}`,
		Status: "pending",
	})
	if !page.AskUserModalVisible() {
		t.Fatalf("expected ask-user modal to be visible")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(110, 30)
	page.Draw(screen)

	text := dumpScreenText(screen, 110, 30)
	if !strings.Contains(text, "Option: Runs with normal approvals.") {
		t.Fatalf("expected selected option description in modal render, got:\n%s", text)
	}
}

func TestAskUserModalDrawUsesSinglePanelBackground(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	page.upsertPendingPermission(ChatPermissionRecord{
		ID:        "perm_ask_bg",
		SessionID: "session-1",
		ToolName:  "ask-user",
		ToolArguments: `{
			"question":"Which mode?",
			"options":[
				{"label":"Auto","value":"auto"},
				{"label":"Yolo","value":"yolo"}
			]
		}`,
		Status: "pending",
	})
	if !page.AskUserModalVisible() {
		t.Fatalf("expected ask-user modal to be visible")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(110, 30)
	page.Draw(screen)

	text := dumpScreenText(screen, 110, 30)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	headerY := lineIndexContaining(lines, "Ask User")
	if headerY < 0 {
		t.Fatalf("ask-user modal header not found in render:\n%s", text)
	}
	headerX := strings.Index(lines[headerY], "Ask User")
	if headerX < 0 {
		t.Fatalf("header x not found in line")
	}
	_, headerBG, _ := styleAt(screen, headerX, headerY).Decompose()
	if headerBG == tcell.ColorDefault {
		t.Fatalf("modal background should be explicit, got default")
	}

	for _, sample := range []string{"Ask User", "Permission", "answered", "Option:"} {
		y := lineIndexContaining(lines, sample)
		if y < 0 {
			continue
		}
		x := strings.Index(lines[y], sample)
		if x < 0 {
			continue
		}
		for i, r := range sample {
			if r == ' ' {
				continue
			}
			_, bg, _ := styleAt(screen, x+i, y).Decompose()
			if bg != headerBG {
				t.Fatalf("sample %q rune %q bg=%v want=%v", sample, r, bg, headerBG)
			}
		}
	}
}

func styleAt(screen tcell.Screen, x, y int) tcell.Style {
	_, _, style, _ := screen.GetContent(x, y)
	return style
}

func TestAskUserModalDrawsOnNarrowScreen(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_ask_narrow",
		SessionID:     "session-1",
		ToolName:      "ask-user",
		ToolArguments: `{"question":"Pick one","options":["A","B"]}`,
		Status:        "pending",
	})
	if !page.AskUserModalVisible() {
		t.Fatalf("expected ask-user modal to be visible")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	width, height := 44, 16
	screen.SetSize(width, height)
	page.Draw(screen)

	text := dumpScreenText(screen, width, height)
	if !strings.Contains(text, "Ask User") {
		t.Fatalf("expected ask-user modal header on narrow screen, got:\n%s", text)
	}
	if !strings.Contains(text, "Enter") && !strings.Contains(text, "Esc") {
		t.Fatalf("expected ask-user modal controls on narrow screen, got:\n%s", text)
	}
}
