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
