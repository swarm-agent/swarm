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
