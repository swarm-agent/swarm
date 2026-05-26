package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestCurrentPlanModalCopyShortcutUsesClipboard(t *testing.T) {
	copied := ""
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
		CopyText: func(text string) error {
			copied = text
			return nil
		},
	})

	if !page.OpenCurrentPlanModal(ChatSessionPlan{ID: "plan_1", Title: "Release", Plan: "# Plan\n\n- [ ] ship", Status: "draft"}) {
		t.Fatalf("OpenCurrentPlanModal() = false, want true")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone))

	if copied != "# Plan\n\n- [ ] ship" {
		t.Fatalf("copied text = %q", copied)
	}
	if got := page.Status(); got != "copied current plan to clipboard" {
		t.Fatalf("status = %q, want copied current plan to clipboard", got)
	}
}

func TestCurrentPlanModalCopyUsesClipboard(t *testing.T) {
	copied := ""
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
		CopyText: func(text string) error {
			copied = text
			return nil
		},
	})

	if !page.OpenCurrentPlanModal(ChatSessionPlan{ID: "plan_1", Title: "Release", Plan: "# Plan\n\n- [ ] ship", Status: "draft"}) {
		t.Fatalf("OpenCurrentPlanModal() = false, want true")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if copied != "# Plan\n\n- [ ] ship" {
		t.Fatalf("copied text = %q", copied)
	}
	if got := page.Status(); got != "copied current plan to clipboard" {
		t.Fatalf("status = %q, want copied current plan to clipboard", got)
	}
}

func TestCurrentPlanModalCopyButtonUsesThemeAccentFill(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	if !page.OpenCurrentPlanModal(ChatSessionPlan{ID: "plan_1", Title: "Release", Plan: "draft", Status: "draft"}) {
		t.Fatalf("OpenCurrentPlanModal() = false, want true")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(110, 30)
	page.Draw(screen)

	lines := strings.Split(strings.TrimSuffix(dumpScreenText(screen, 110, 30), "\n"), "\n")
	lineIndex := lineIndexContaining(lines, "C Copy")
	if lineIndex < 0 {
		t.Fatalf("copy button not found in render")
	}
	x := strings.Index(lines[lineIndex], "C Copy")
	if x < 0 {
		t.Fatalf("copy button x position not found")
	}

	got := pStyleAt(screen, x, lineIndex)
	want := filledButtonStyle(page.theme.Accent)
	if !stylesEqual(got, want) {
		t.Fatalf("copy button style = %v, want %v", got, want)
	}
}

func TestCurrentPlanModalSaveQueuesPlanAction(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	if !page.OpenCurrentPlanModal(ChatSessionPlan{ID: "plan_1", Title: "Release", Plan: "draft", Status: "draft"}) {
		t.Fatalf("OpenCurrentPlanModal() = false, want true")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone))
	if got := page.planEditorSelection; got != chatPlanEditorSelectSave {
		t.Fatalf("planEditorSelection after edit = %d, want %d", got, chatPlanEditorSelectSave)
	}
	for _, r := range " updated" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := page.PopChatAction()
	if !ok {
		t.Fatalf("expected save-plan action")
	}
	if action.Kind != ChatActionSavePlan {
		t.Fatalf("action kind = %q, want %q", action.Kind, ChatActionSavePlan)
	}
	if action.Plan.Plan != "draft updated" {
		t.Fatalf("saved plan text = %q, want %q", action.Plan.Plan, "draft updated")
	}
}

func TestCurrentPlanModalCtrlJAddsNewlineAndEnterSaves(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	if !page.OpenCurrentPlanModal(ChatSessionPlan{ID: "plan_1", Title: "Release", Plan: "draft", Status: "draft"}) {
		t.Fatalf("OpenCurrentPlanModal() = false, want true")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyCtrlJ, 0, tcell.ModCtrl))
	for _, r := range "next" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := page.PopChatAction()
	if !ok {
		t.Fatalf("expected save-plan action")
	}
	if action.Kind != ChatActionSavePlan {
		t.Fatalf("action kind = %q, want %q", action.Kind, ChatActionSavePlan)
	}
	if action.Plan.Plan != "draft\nnext" {
		t.Fatalf("saved plan text = %q, want %q", action.Plan.Plan, "draft\nnext")
	}
}

func TestCurrentPlanModalTypingKeepsSaveSelectedInEditMode(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	if !page.OpenCurrentPlanModal(ChatSessionPlan{ID: "plan_1", Title: "Release", Plan: "draft", Status: "draft"}) {
		t.Fatalf("OpenCurrentPlanModal() = false, want true")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone))
	if got := page.planEditorSelection; got != chatPlanEditorSelectSave {
		t.Fatalf("planEditorSelection after edit = %d, want %d", got, chatPlanEditorSelectSave)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	if got := page.planEditorSelection; got != chatPlanEditorSelectCancel {
		t.Fatalf("planEditorSelection after tab = %d, want %d", got, chatPlanEditorSelectCancel)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))

	if got := page.planEditorSelection; got != chatPlanEditorSelectSave {
		t.Fatalf("planEditorSelection after typing = %d, want %d", got, chatPlanEditorSelectSave)
	}
}

func TestOpenCurrentPlanModalRejectsWhenAnotherModalIsVisible(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", AuthConfigured: true, SessionMode: "plan"})
	if !page.OpenExitPlanModeModal("Exit Plan", "body") {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}
	if page.OpenCurrentPlanModal(ChatSessionPlan{Title: "Blocked"}) {
		t.Fatalf("OpenCurrentPlanModal() = true, want false while exit modal is visible")
	}
}

func TestCurrentPlanModalWideLayoutShowsPlansOnLeft(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", ShowHeader: true, AuthConfigured: true, SessionMode: "plan"})
	plans := []ChatSessionPlan{
		{ID: "plan_1", Title: "Active Plan", Plan: "active body", Status: "draft", Active: true},
		{ID: "plan_2", Title: "Next Plan", Plan: "next body", Status: "draft"},
	}
	if !page.OpenCurrentPlanModalWithPlans(plans[0], plans, "plan_1") {
		t.Fatalf("OpenCurrentPlanModalWithPlans() = false, want true")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 32)
	page.Draw(screen)
	text := dumpScreenText(screen, 120, 32)
	for _, want := range []string{"Plans", "Active Plan", "Next Plan", "active body"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render missing %q:\n%s", want, text)
		}
	}
}

func TestCurrentPlanModalDownSelectsPlanAndShowsActivationHint(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", ShowHeader: true, AuthConfigured: true, SessionMode: "plan"})
	plans := []ChatSessionPlan{
		{ID: "plan_1", Title: "Active Plan", Plan: "active body", Status: "draft", Active: true},
		{ID: "plan_2", Title: "Next Plan", Plan: "next body", Status: "draft"},
	}
	if !page.OpenCurrentPlanModalWithPlans(plans[0], plans, "plan_1") {
		t.Fatalf("OpenCurrentPlanModalWithPlans() = false, want true")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if got := page.planEditorPlanSelection; got != 1 {
		t.Fatalf("planEditorPlanSelection = %d, want 1", got)
	}
	if got := page.planEditorPlan.ID; got != "plan_2" {
		t.Fatalf("selected plan id = %q, want plan_2", got)
	}
	if got := page.Status(); got != "Press a to activate as current plan" {
		t.Fatalf("status = %q, want activation hint", got)
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 32)
	page.Draw(screen)
	text := dumpScreenText(screen, 120, 32)
	if !strings.Contains(text, "Press a to activate as current plan") {
		t.Fatalf("render missing activation hint:\n%s", text)
	}
	if !strings.Contains(text, "next body") {
		t.Fatalf("render missing selected plan body:\n%s", text)
	}
}

func TestCurrentPlanModalActivateShortcutQueuesAction(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1", ShowHeader: true, AuthConfigured: true, SessionMode: "plan"})
	plans := []ChatSessionPlan{
		{ID: "plan_1", Title: "Active Plan", Plan: "active body", Status: "draft", Active: true},
		{ID: "plan_2", Title: "Next Plan", Plan: "next body", Status: "draft"},
	}
	if !page.OpenCurrentPlanModalWithPlans(plans[0], plans, "plan_1") {
		t.Fatalf("OpenCurrentPlanModalWithPlans() = false, want true")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))
	action, ok := page.PopChatAction()
	if !ok {
		t.Fatalf("expected activate-plan action")
	}
	if action.Kind != ChatActionActivatePlan {
		t.Fatalf("action kind = %q, want %q", action.Kind, ChatActionActivatePlan)
	}
	if action.Plan.ID != "plan_2" {
		t.Fatalf("action plan id = %q, want plan_2", action.Plan.ID)
	}
}
