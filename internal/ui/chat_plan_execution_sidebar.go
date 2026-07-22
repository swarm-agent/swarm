package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
)

const planExecutionSidebarMinWidth = 96

type planExecutionView struct {
	active, next     map[string]any
	status, policy   string
	completed, total int
	critical         bool
}

func (p *ChatPage) planExecutionView() (planExecutionView, bool) {
	if p == nil || strings.TrimSpace(p.planExecutionPlan.ID) == "" {
		return planExecutionView{}, false
	}
	doc, ok := p.planExecutionPlan.Document.(map[string]any)
	if !ok {
		return planExecutionView{}, false
	}
	checkpoints := mapAnyObjectSlice(doc, "checkpoints")
	sort.SliceStable(checkpoints, func(i, j int) bool { return mapAnyInt(checkpoints[i], "order") < mapAnyInt(checkpoints[j], "order") })
	v := planExecutionView{total: len(checkpoints), status: firstNonEmptyToolValue(mapStringArg(doc, "status"), p.planExecutionPlan.Status, p.planExecutionRunStatus)}
	activeID := firstNonEmptyToolValue(mapStringArg(doc, "active_checkpoint_id"), mapStringArg(doc, "activeCheckpointId"))
	for _, cp := range checkpoints {
		status := strings.ReplaceAll(strings.ToLower(quietPlanStatus(mapStringArg(cp, "status"))), " ", "_")
		if status == "completed" {
			v.completed++
		}
		if mapStringArg(cp, "id") == activeID {
			v.active = cp
		}
		if v.next == nil && (status == "pending" || status == "queued") {
			v.next = cp
		}
	}
	if v.active == nil && len(checkpoints) > 0 {
		v.active = checkpoints[0]
	}
	v.status = strings.ReplaceAll(strings.ToLower(firstNonEmptyToolValue(quietPlanStatus(mapStringArg(v.active, "status")), quietPlanStatus(v.status), "active")), " ", "_")
	v.critical = v.status == "needs_review" || v.status == "blocked" || v.status == "failed" || v.status == "final_review"
	policy := firstNonEmptyToolValue(mapStringArg(doc, "continuation_policy"), mapStringArg(doc, "execution_policy"))
	if policy == "" {
		policy = "checkpointed"
	}
	v.policy = strings.ReplaceAll(policy, "_", " ")
	return v, true
}

func (p *ChatPage) planExecutionPanelWidth(screenW int) int {
	if _, ok := p.planExecutionView(); !ok || screenW < planExecutionSidebarMinWidth {
		return 0
	}
	w := screenW / 3
	if w < 30 {
		w = 30
	}
	if w > 42 {
		w = 42
	}
	return w
}

func (p *ChatPage) drawPlanExecutionSidebar(s tcell.Screen, rect Rect) {
	v, ok := p.planExecutionView()
	if !ok || rect.W < 20 || rect.H < 4 {
		return
	}
	DrawOpenBox(s, rect, p.theme.BorderActive)
	x, y, width := rect.X+1, rect.Y+1, rect.W-2
	lines := []string{
		clampEllipsis(firstNonEmptyToolValue(p.planExecutionPlan.Title, "Plan execution"), width),
		fmt.Sprintf("%s  ·  %d/%d complete", strings.ReplaceAll(v.status, "_", " "), v.completed, v.total),
		"Policy: " + v.policy,
	}
	activeTitle := firstNonEmptyToolValue(mapStringArg(v.active, "title"), mapStringArg(v.active, "id"), "none")
	lines = append(lines, "", "Active: "+activeTitle)
	for _, task := range mapAnyStringSlice(v.active, "tasks") {
		lines = append(lines, "  • "+task)
	}
	for _, sub := range mapAnyObjectSlice(v.active, "subtasks") {
		lines = append(lines, fmt.Sprintf("  [%s] %s", firstNonEmptyToolValue(quietPlanStatus(mapStringArg(sub, "status")), "pending"), firstNonEmptyToolValue(mapStringArg(sub, "title"), mapStringArg(sub, "id"))))
	}
	if v.next != nil {
		lines = append(lines, "", "Next: "+firstNonEmptyToolValue(mapStringArg(v.next, "title"), mapStringArg(v.next, "id")))
	}
	if recommendation := mapStringArg(v.active, "recommendation"); recommendation != "" {
		lines = append(lines, "", "Review: "+recommendation)
	}
	if notes := firstNonEmptyToolValue(mapStringArg(v.active, "blocker"), mapStringArg(v.active, "error")); notes != "" {
		lines = append(lines, "", strings.Title(v.status)+": "+notes)
	}
	lines = append(lines, "", p.planExecutionControlHint(v))
	for i, line := range lines {
		if y+i >= rect.Y+rect.H-1 {
			break
		}
		style := p.theme.Text
		if i == 0 {
			style = p.theme.Secondary.Bold(true)
		}
		if i == 1 && v.critical {
			style = p.theme.Warning
		}
		DrawText(s, x, y+i, width, style, clampEllipsis(line, width))
	}
}

func (p *ChatPage) planExecutionControlHint(v planExecutionView) string {
	switch v.status {
	case "needs_review", "final_review":
		return "[Ctrl+P or /plan] plan  [a] accept  [o] archive"
	case "blocked":
		return "[Ctrl+P or /plan] plan  [r] resolve  [n] resolve + next"
	case "failed":
		return "[Ctrl+P or /plan] plan  [t] restart  [w] rewind  [x] recovery"
	case "in_progress", "running":
		return "[Ctrl+P or /plan] plan  [s] stop  [m] policy  [x] recovery"
	default:
		return "[Ctrl+P or /plan] full plan  [m] policy  [x] recovery"
	}
}

func (p *ChatPage) queuePlanExecutionOperation(operation string, startNext bool) {
	v, ok := p.planExecutionView()
	if !ok {
		return
	}
	checkpointID := mapStringArg(v.active, "id")
	p.pendingChatAction = &ChatAction{Kind: ChatActionPlanExecution, Plan: p.planExecutionPlan, PlanExecution: ChatPlanExecutionAction{Operation: operation, CheckpointID: checkpointID, StartNext: startNext, Automatic: strings.Contains(v.policy, "automatic")}}
	p.statusLine = "plan action: " + strings.ReplaceAll(operation, "_", " ")
}

func (p *ChatPage) handlePlanExecutionKey(ev *tcell.EventKey) bool {
	v, ok := p.planExecutionView()
	if !ok {
		return false
	}
	if ev.Key() == tcell.KeyCtrlP {
		if strings.TrimSpace(p.input) != "" {
			return false
		}
		p.openPlanEditorModalWithPlans(p.planExecutionPlan, p.planExecutionRevisions, p.planExecutionPlan.ID)
		return true
	}
	if ev.Key() == tcell.KeyEnter {
		command := strings.ToLower(strings.TrimSpace(p.input))
		if command == "/plan" || command == "/plan show" {
			p.input = ""
			p.inputCursor = 0
			p.openPlanEditorModalWithPlans(p.planExecutionPlan, p.planExecutionRevisions, p.planExecutionPlan.ID)
			return true
		}
	}
	if strings.TrimSpace(p.input) != "" {
		return false
	}
	if ev.Key() != tcell.KeyRune {
		return false
	}
	switch ev.Rune() {
	case 's':
		if v.status != "in_progress" && v.status != "running" {
			return false
		}
		p.queuePlanExecutionOperation("stop", false)
	case 'a':
		if v.status != "needs_review" && v.status != "final_review" {
			return false
		}
		p.queuePlanExecutionOperation("accept", true)
	case 'o':
		if v.status != "needs_review" && v.status != "final_review" {
			return false
		}
		p.queuePlanExecutionOperation("accept", false)
	case 'r':
		if v.status != "blocked" {
			return false
		}
		p.queuePlanExecutionOperation("resolve", false)
	case 'n':
		if v.status != "blocked" {
			return false
		}
		p.queuePlanExecutionOperation("resolve", true)
	case 't':
		if v.status != "failed" {
			return false
		}
		p.queuePlanExecutionOperation("restart", false)
	case 'w':
		if v.status != "failed" {
			return false
		}
		p.queuePlanExecutionOperation("rewind", false)
	case 'm':
		p.queuePlanExecutionOperation("toggle_policy", false)
	case 'x':
		p.openPlanEditorModalWithPlans(p.planExecutionPlan, p.planExecutionRevisions, p.planExecutionPlan.ID)
	default:
		return false
	}
	return true
}
