package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

func (p *HomePage) SelectedTaskIndex() int {
	if p == nil || len(p.model.ProjectTasks) == 0 {
		return 0
	}
	if p.taskCursorIndex < 0 {
		return 0
	}
	if p.taskCursorIndex >= len(p.model.ProjectTasks) {
		return len(p.model.ProjectTasks) - 1
	}
	return p.taskCursorIndex
}

func (p *HomePage) SetSelectedTaskIndex(idx int) {
	if p == nil {
		return
	}
	p.taskCursorIndex = idx
}

func (p *HomePage) SelectedTask() (client.ProjectTaskRecord, bool) {
	if p == nil || len(p.model.ProjectTasks) == 0 {
		return client.ProjectTaskRecord{}, false
	}
	idx := p.SelectedTaskIndex()
	return p.model.ProjectTasks[idx], true
}

func (p *HomePage) MoveTaskSelection(delta int) {
	if p == nil || len(p.model.ProjectTasks) == 0 {
		return
	}
	count := len(p.model.ProjectTasks)
	next := (p.taskCursorIndex + delta) % count
	if next < 0 {
		next += count
	}
	p.taskCursorIndex = next
}

func (p *HomePage) drawProjectTasks(s tcell.Screen, rect Rect, centered bool) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	innerW := rect.W
	if innerW > 88 {
		innerW = 88
	}
	if innerW < 24 {
		innerW = rect.W
	}
	startX := rect.X
	if centered && innerW < rect.W {
		startX = rect.X + (rect.W-innerW)/2
	}

	boxRect := Rect{X: startX, Y: rect.Y, W: innerW, H: rect.H}
	boxBorder := p.theme.Border
	if p.taskBoxFocused {
		boxBorder = p.theme.BorderActive
	}
	DrawBox(s, boxRect, boxBorder)

	// Title on top border
	title := " Project Tasks "
	if name := strings.TrimSpace(p.model.ActiveProjectName); name != "" {
		title = fmt.Sprintf(" Tasks · %s ", name)
	}
	title = clampEllipsis(title, innerW-4)
	DrawText(s, boxRect.X+2, boxRect.Y, innerW-4, p.theme.Primary.Bold(true), title)

	tasks := p.model.ProjectTasks
	if len(tasks) == 0 {
		emptyMsg := "No tasks yet. Enter a goal below or press Enter to chat with Orchestrator."
		emptyMsg = clampEllipsis(emptyMsg, innerW-4)
		DrawText(s, boxRect.X+2, boxRect.Y+1, innerW-4, p.theme.TextMuted, emptyMsg)
		if boxRect.H >= 4 {
			hintMsg := "Ctrl+X: Orchestrator Chat  •  Alt+W: Switch Project"
			hintMsg = clampEllipsis(hintMsg, innerW-4)
			DrawText(s, boxRect.X+2, boxRect.Y+2, innerW-4, p.theme.Secondary, hintMsg)
		}
		return
	}

	maxVisible := boxRect.H - 2
	if maxVisible <= 0 {
		return
	}

	selected := p.SelectedTaskIndex()
	scrollOffset := 0
	if selected >= maxVisible {
		scrollOffset = selected - maxVisible + 1
	}

	for i := 0; i < maxVisible; i++ {
		taskIdx := scrollOffset + i
		if taskIdx >= len(tasks) {
			break
		}
		task := tasks[taskIdx]
		y := boxRect.Y + 1 + i

		isSelected := (taskIdx == selected)

		statusBadge := "[TODO]"
		badgeStyle := p.theme.TextMuted
		switch strings.ToLower(strings.TrimSpace(task.Status)) {
		case "completed", "done":
			statusBadge = "[DONE]"
			badgeStyle = p.theme.Success
		case "in_progress", "running":
			statusBadge = "[RUNNING]"
			badgeStyle = p.theme.Warning
		case "needs_review", "review":
			statusBadge = "[REVIEW]"
			badgeStyle = p.theme.Secondary
		case "failed", "error":
			statusBadge = "[FAILED]"
			badgeStyle = p.theme.Error
		case "pending_approval", "pending":
			statusBadge = "[PENDING]"
			badgeStyle = p.theme.Secondary
		default:
			statusBadge = "[QUEUED]"
			badgeStyle = p.theme.TextMuted
		}

		rowStyle := p.theme.Text
		prefix := "  "
		if isSelected {
			prefix = "› "
			if p.taskBoxFocused {
				rowStyle = p.theme.Primary.Bold(true)
				FillRect(s, Rect{X: boxRect.X + 1, Y: y, W: boxRect.W - 2, H: 1}, p.theme.Element)
			} else {
				rowStyle = p.theme.Secondary
			}
		}

		curX := boxRect.X + 2
		DrawText(s, curX, y, 2, rowStyle, prefix)
		curX += 2

		DrawText(s, curX, y, len(statusBadge), badgeStyle, statusBadge)
		curX += len(statusBadge) + 1

		availW := (boxRect.X + boxRect.W - 2) - curX
		agentTag := ""
		if strings.TrimSpace(task.Agent) != "" {
			agentTag = fmt.Sprintf(" (%s)", task.Agent)
		}
		agentW := len(agentTag)

		titleW := availW - agentW
		if titleW < 8 {
			titleW = availW
			agentTag = ""
		}

		taskTitle := clampEllipsis(task.Title, titleW)
		DrawText(s, curX, y, titleW, rowStyle, taskTitle)
		curX += len(taskTitle)

		if agentTag != "" && curX < boxRect.X+boxRect.W-2 {
			DrawText(s, curX, y, agentW, p.theme.TextMuted, agentTag)
		}
	}

	if boxRect.H >= 4 {
		if p.taskBoxFocused {
			bottomHint := " [Ctrl+Down / Esc: Back to Prompt • Enter: Open Task] "
			DrawText(s, boxRect.X+2, boxRect.Y+boxRect.H-1, innerW-4, p.theme.Secondary, clampEllipsis(bottomHint, innerW-4))
		} else {
			bottomHint := " [Ctrl+Up: Navigate Tasks] "
			DrawText(s, boxRect.X+2, boxRect.Y+boxRect.H-1, innerW-4, p.theme.TextMuted, clampEllipsis(bottomHint, innerW-4))
		}
	}
}
