package ui

import (
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func (p *HomePage) drawRecentSessions(s tcell.Screen, rect Rect) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	p.sessionRows = p.sessionRows[:0]
	p.sessionIndex = p.sessionIndex[:0]

	if rect.W < 22 || rect.H < 4 {
		total := len(p.model.RecentSessions)
		if total == 0 {
			DrawText(s, rect.X, rect.Y, rect.W, p.theme.TextMuted, "sessions 0")
			return
		}
		if p.selectedIndex >= total {
			p.selectedIndex = total - 1
		}
		if p.selectedIndex < 0 {
			p.selectedIndex = 0
		}
		selected := p.model.RecentSessions[p.selectedIndex]
		lineageLabel := SessionLineageDisplay(SessionLineageFromSummary(selected))
		indentPrefix := SessionIndentedPrefix(SessionDepth(selected))
		label := fmt.Sprintf("sessions %d · %s%s", total, indentPrefix, sessionDisplayTitle(selected.Title, selected.ID))
		if lineageLabel != "" {
			label += " · " + lineageLabel
		}
		DrawText(s, rect.X, rect.Y, rect.W, p.theme.TextMuted, clampEllipsis(label, rect.W))
		return
	}

	DrawBox(s, rect, p.theme.Border)

	if len(p.commandOverlay) > 0 {
		DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.TextMuted, "command output")
		availableRows := rect.H - 3
		if availableRows < 1 {
			availableRows = 1
		}
		rowY := rect.Y + 2
		for i := 0; i < availableRows && i < len(p.commandOverlay); i++ {
			DrawText(s, rect.X+2, rowY, rect.W-4, p.theme.Text, p.commandOverlay[i])
			rowY++
		}
		return
	}

	total := len(p.model.RecentSessions)
	if total == 0 {
		p.sessionsFocused = false
		DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.TextMuted, "0 sessions")
		DrawText(s, rect.X+2, rect.Y+2, rect.W-4, p.theme.TextMuted, "No recent sessions yet")
		return
	}

	if p.selectedIndex >= total {
		p.selectedIndex = total - 1
	}
	if p.selectedIndex < 0 {
		p.selectedIndex = 0
	}

	visibleRows := rect.H - 3
	if visibleRows < 1 {
		visibleRows = 1
	}
	if visibleRows > recentVisibleRows {
		visibleRows = recentVisibleRows
	}

	p.recentPageSize = visibleRows
	p.syncPageFromSelection(p.recentPageSize)

	start := p.recentPage
	summary := fmt.Sprintf("sessions: %d", total)
	pageLabel := "↑/↓"
	DrawText(s, rect.X+2, rect.Y+1, rect.W-4, p.theme.TextMuted, summary)
	DrawTextRight(s, rect.X+rect.W-3, rect.Y+1, 22, p.theme.Accent, pageLabel)

	rowY := rect.Y + 2
	for row := 0; row < visibleRows && rowY < rect.Y+rect.H-1; row++ {
		i := start + row
		if i < total {
			sel := p.sessionsFocused && i == p.selectedIndex
			prefix := "  "
			style := p.theme.Text
			if p.model.RecentSessions[i].PendingPermissionCount > 0 {
				prefix = "! "
				style = p.theme.Warning
			} else if lifecycle := p.model.RecentSessions[i].Lifecycle; lifecycle != nil && lifecycle.Active {
				prefix = "* "
				style = p.theme.Accent
			}
			if sel {
				prefix = "› "
				style = p.theme.Primary
			}
			rowRect := Rect{X: rect.X + 1, Y: rowY, W: rect.W - 2, H: 1}
			p.sessionRows = append(p.sessionRows, rowRect)
			p.sessionIndex = append(p.sessionIndex, i)

			session := p.model.RecentSessions[i]
			modelLabel := model.DisplayModelLabel(
				session.Preference.Provider,
				session.Preference.Model,
				session.Preference.ServiceTier,
				session.Preference.ContextMode,
			)
			line := sessionListPrimaryLine(prefix+SessionIndentedPrefix(SessionDepth(session)), sessionDisplayTitle(session.Title, session.ID), SessionLineageDisplay(SessionLineageFromSummary(session)), "", modelLabel, true)
			DrawText(s, rect.X+2, rowY, rect.W-12, style, clampEllipsis(line, rect.W-12))
			meta := p.model.RecentSessions[i].UpdatedAgo
			if lifecycle := p.model.RecentSessions[i].Lifecycle; lifecycle != nil && lifecycle.Active && lifecycle.StartedAt > 0 {
				meta = formatDurationCompact(time.Since(time.UnixMilli(lifecycle.StartedAt)))
			}
			DrawTextRight(s, rect.X+rect.W-3, rowY, 8, p.theme.TextMuted, meta)
		}
		rowY++
	}
}
