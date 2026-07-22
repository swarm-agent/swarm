package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func planExecutionTestPage(status string) *ChatPage {
	p := &ChatPage{input: ""}
	p.SetPlanExecutionState(ChatSessionPlan{ID: "plan-1", Title: "Launch plan", Status: "running", Document: map[string]any{
		"active_checkpoint_id": "cp-1", "continuation_policy": "automatic",
		"checkpoints": []any{
			map[string]any{"id": "cp-1", "title": "Build sidebar", "status": status, "order": float64(1), "tasks": []any{"Render progress"}, "subtasks": []any{map[string]any{"id": "task-1", "title": "Layout", "status": "in_progress"}}},
			map[string]any{"id": "cp-2", "title": "Verify", "status": "pending", "order": float64(2)},
		},
	}}, nil, "run-1", "running")
	return p
}

func TestPlanExecutionSidebarProjectionAndResponsiveLayout(t *testing.T) {
	p := planExecutionTestPage("in_progress")
	v, ok := p.planExecutionView()
	if !ok || v.total != 2 || v.completed != 0 || mapStringArg(v.active, "id") != "cp-1" || mapStringArg(v.next, "id") != "cp-2" {
		t.Fatalf("unexpected view: %#v", v)
	}
	if p.planExecutionPanelWidth(95) != 0 {
		t.Fatal("narrow terminals must relocate/collapse sidebar")
	}
	if p.planExecutionPanelWidth(120) < 30 {
		t.Fatal("wide terminal should show sidebar")
	}
}

func TestPlanExecutionPlanViewerUsesCtrlPAndKeepsPlainPForComposer(t *testing.T) {
	p := planExecutionTestPage("in_progress")
	if p.handlePlanExecutionKey(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone)) {
		t.Fatal("plain p should remain available to the composer")
	}
	if p.planEditorVisible {
		t.Fatal("plain p opened the plan viewer")
	}
	if !p.handlePlanExecutionKey(tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModNone)) || !p.planEditorVisible {
		t.Fatal("Ctrl+P did not open the plan viewer")
	}
}

func TestPlanExecutionPlanViewerOpensFromPlanCommand(t *testing.T) {
	p := planExecutionTestPage("in_progress")
	p.input = "/plan"
	p.inputCursor = len([]rune(p.input))
	if !p.handlePlanExecutionKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)) || !p.planEditorVisible {
		t.Fatal("/plan did not open the plan viewer")
	}
	if p.input != "" || p.inputCursor != 0 {
		t.Fatalf("composer was not cleared: input=%q cursor=%d", p.input, p.inputCursor)
	}
}

func TestPlanExecutionActionsRespectCheckpointState(t *testing.T) {
	p := planExecutionTestPage("blocked")
	if !p.handlePlanExecutionKey(tcell.NewEventKey(tcell.KeyRune, 'n', tcell.ModNone)) {
		t.Fatal("blocked resolve-next should be handled")
	}
	action, ok := p.PopChatAction()
	if !ok || action.Kind != ChatActionPlanExecution || action.PlanExecution.Operation != "resolve" || !action.PlanExecution.StartNext || action.PlanExecution.CheckpointID != "cp-1" {
		t.Fatalf("unexpected action: %#v", action)
	}
	if p.handlePlanExecutionKey(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone)) {
		t.Fatal("blocked checkpoint must not allow stop")
	}
}

func TestPlanExecutionFailedCheckpointQueuesCanonicalRecoveryActions(t *testing.T) {
	p := planExecutionTestPage("failed")
	for _, tt := range []struct {
		key  rune
		want string
	}{{'t', "restart"}, {'w', "rewind"}} {
		if !p.handlePlanExecutionKey(tcell.NewEventKey(tcell.KeyRune, tt.key, tcell.ModNone)) {
			t.Fatalf("%s key should be handled", tt.want)
		}
		action, ok := p.PopChatAction()
		if !ok || action.Kind != ChatActionPlanExecution || action.PlanExecution.Operation != tt.want || action.PlanExecution.CheckpointID != "cp-1" {
			t.Fatalf("%s action = %#v", tt.want, action)
		}
	}
}

func TestPlanExecutionSidebarAdvertisesPlanCommand(t *testing.T) {
	p := planExecutionTestPage("in_progress")
	if hint := p.planExecutionControlHint(planExecutionView{status: "in_progress"}); !strings.Contains(hint, "Ctrl+P or /plan") {
		t.Fatalf("control hint = %q, want Ctrl+P or /plan guidance", hint)
	}
}

func TestPlanExecutionSidebarRendersCriticalState(t *testing.T) {
	p := planExecutionTestPage("needs_review")
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(40, 15)
	p.drawPlanExecutionSidebar(s, Rect{X: 0, Y: 0, W: 40, H: 15})
	s.Show()
	cells, w, _ := s.GetContents()
	var b strings.Builder
	for i, cell := range cells {
		if i > 0 && i%w == 0 {
			b.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			b.WriteRune(cell.Runes[0])
		} else {
			b.WriteByte(' ')
		}
	}
	text := b.String()
	for _, want := range []string{"Launch plan", "needs review", "Build sidebar", "accept"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}
