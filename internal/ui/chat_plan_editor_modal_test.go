package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestPlanEditorRevisionSelectionPreviewsBeforeActivate(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	current := ChatSessionPlan{ID: "plan-1", Title: "Plan", Plan: "# Plan\n\ncurrent", Active: true, Version: 3}
	revisions := []ChatSessionPlan{
		current,
		{ID: "plan-1", Title: "Plan", Plan: "# Plan\n\nolder", Version: 2, UpdateSummary: "older body"},
	}
	page.openPlanEditorModalWithPlans(current, revisions, "plan-1")

	page.handlePlanEditorModalKey(tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone))
	page.handlePlanEditorModalKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))

	if !page.planEditorVisible || !page.planEditorRevisionFocus {
		t.Fatalf("revision preview should keep modal/focus open")
	}
	if got := page.planEditorPlan.Plan; !strings.Contains(got, "older") {
		t.Fatalf("down should preview selected revision, got %q", got)
	}
	if action, ok := page.PopChatAction(); ok {
		t.Fatalf("preview should not queue activation action: %#v", action)
	}

	page.handlePlanEditorModalKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := page.PopChatAction()
	if !ok {
		t.Fatalf("enter should queue activation action")
	}
	if action.Kind != ChatActionActivatePlan || action.Plan.ID != "plan-1" || !action.Plan.RestoreRevision {
		t.Fatalf("activation action = %#v", action)
	}
	if !strings.Contains(action.Plan.Plan, "older") {
		t.Fatalf("activation should use previewed revision body, got %q", action.Plan.Plan)
	}
	if page.planEditorVisible {
		t.Fatalf("activation should close plan modal")
	}
}

func TestNormalizePlanEditorRevisionsOnlyMarksCurrentRevisionActive(t *testing.T) {
	current := ChatSessionPlan{ID: "plan-1", Title: "Plan", Plan: "current", Version: 3}
	revisions := []ChatSessionPlan{
		current,
		{ID: "plan-1", Title: "Plan", Plan: "older", Version: 2},
	}
	items := normalizePlanEditorRevisions(current, revisions, "plan-1")
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if !items[0].Active {
		t.Fatalf("current revision should be active")
	}
	if items[1].Active {
		t.Fatalf("older revision should not be active")
	}
}

func TestPlanEditorStructuredDocumentAlwaysStacksDetailsAboveCheckpoints(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.planEditorPlan = ChatSessionPlan{ID: "plan-1", Title: "Stacked Plan", Document: stackedPlanTestDocument()}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 28)

	page.drawPlanEditorDocument(screen, Rect{X: 0, Y: 0, W: 112, H: 24}, func(style tcell.Style) tcell.Style { return style })
	text := planEditorScreenText(screen, 112, 24)
	detailsIndex := strings.Index(text, "Plan Details")
	checkpointsIndex := strings.Index(text, "Checkpoints (2)")
	if detailsIndex < 0 || checkpointsIndex < 0 {
		t.Fatalf("render missing stacked sections:\n%s", text)
	}
	if detailsIndex > checkpointsIndex {
		t.Fatalf("plan details should render before checkpoints:\n%s", text)
	}
	if strings.Contains(text, "│") {
		t.Fatalf("structured plan should not render a two-column divider:\n%s", text)
	}
}

func TestPlanExitModalStructuredDocumentStacksDetailsAboveCheckpoints(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	page.planExitTitle = "Stacked Plan"
	page.planExitDocument = StructuredPlanDocumentTextFromValue(stackedPlanTestDocument())

	lines := page.planExitModalLines(96)
	var rendered []string
	for _, line := range lines {
		rendered = append(rendered, chatRenderLineText(line))
	}
	text := strings.Join(rendered, "\n")
	infoIndex := strings.Index(text, "Plan info:")
	checkpointsIndex := strings.Index(text, "Checkpoints (2):")
	if infoIndex < 0 || checkpointsIndex < 0 {
		t.Fatalf("exit plan render missing stacked document sections:\n%s", text)
	}
	if infoIndex > checkpointsIndex {
		t.Fatalf("exit plan details should render before checkpoints:\n%s", text)
	}
}

func stackedPlanTestDocument() map[string]any {
	return map[string]any{
		"id":     "plan-1",
		"title":  "Stacked Plan",
		"status": "approved",
		"info": map[string]any{
			"goal":                "Keep terminal plans readable",
			"scope":               "single column TUI rendering",
			"validation_strategy": "targeted UI tests",
		},
		"checkpoints": []any{
			map[string]any{"id": "cp-1", "title": "Details", "status": "done", "order": float64(1)},
			map[string]any{"id": "cp-2", "title": "Checkpoints", "status": "pending", "order": float64(2)},
		},
	}
}

func planEditorScreenText(screen tcell.SimulationScreen, width, height int) string {
	var builder strings.Builder
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			mainc, _, _, _ := screen.GetContent(x, y)
			if mainc == 0 {
				mainc = ' '
			}
			builder.WriteRune(mainc)
		}
		builder.WriteByte('\n')
	}
	return builder.String()
}
