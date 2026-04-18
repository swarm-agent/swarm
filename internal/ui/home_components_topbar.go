package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func summaryBackgroundMetadata(summary model.SessionSummary) (bool, string, string) {
	lineage := SessionLineageFromSummary(summary)
	if strings.TrimSpace(lineage.ParentSessionID) != "" {
		return false, "", ""
	}
	return lineage.Background, strings.TrimSpace(lineage.TargetKind), strings.TrimSpace(lineage.TargetName)
}

func normalizeBackgroundStatus(status string, pendingPermissions int) string {
	if pendingPermissions > 0 {
		return "blocked"
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "ready":
		return "idle"
	default:
		return strings.TrimSpace(status)
	}
}

func backgroundHeaderBadge(records []model.BackgroundSessionSummary) string {
	if len(records) == 0 {
		return ""
	}
	blocked := 0
	running := 0
	for _, record := range records {
		switch normalizeBackgroundStatus(record.Status, record.PendingPermissions) {
		case "blocked":
			blocked++
		case "running":
			running++
		}
	}
	parts := []string{fmt.Sprintf("bg:%d", len(records))}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("run:%d", running))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("blocked:%d", blocked))
	}
	return strings.Join(parts, " ")
}

func (p *HomePage) drawSwarmTopBar(s tcell.Screen, rect Rect) {
	if rect.H < 4 || rect.W < 32 {
		return
	}
	p.topBarTargets = p.topBarTargets[:0]

	contentX := rect.X + 1
	contentW := rect.W - 2
	if contentW < 12 {
		contentW = rect.W - 2
	}

	workspaces := p.workspaceItems()
	y0 := rect.Y

	p.drawTopItemRow(s, y0, contentX, contentW, workspaces, true)

	infoRect := Rect{
		X: contentX,
		Y: y0 + 1,
		W: contentW,
		H: rect.H - 2,
	}
	p.drawWorkspaceInfoBox(s, infoRect)

	DrawHLine(s, rect.X, rect.Y+rect.H-1, rect.W, p.theme.Border)
}

func (p *HomePage) drawWorkspaceInfoBox(s tcell.Screen, rect Rect) {
	if rect.W < 24 || rect.H < 3 {
		return
	}

	d := p.primaryDirectory()
	innerW := rect.W - 4
	if innerW < 8 {
		innerW = rect.W - 2
	}
	kind := "workspace"
	if !d.IsWorkspace {
		kind = "directory"
	}
	nameW := rect.W - 4 - utf8.RuneCountInString(" workspace:  ")
	if nameW < 1 {
		nameW = 1
	}
	name := clampEllipsis(d.Name, nameW)
	if name == "" {
		name = clampEllipsis(d.Path, nameW)
	}
	workspaceLabel := fmt.Sprintf(" %s:%s ", kind, name)

	lineY := rect.Y + 1

	leftW := innerW / 2
	if leftW < 12 {
		leftW = 12
	}
	if leftW > innerW-12 {
		leftW = innerW - 12
	}
	if leftW < 12 {
		leftW = innerW
	}
	rightW := innerW - leftW - 1
	if rightW < 0 {
		rightW = 0
	}

	cwdLine := "cwd " + strings.TrimSpace(d.Path)
	if strings.TrimSpace(d.Path) == "" {
		cwdLine = "cwd ."
	}
	if linked := p.activeWorkspaceLinkedDirectories(); len(linked) > 0 {
		cwdLine += fmt.Sprintf("  ·  linked:%d", len(linked))
	}
	if badge := backgroundHeaderBadge(p.model.BackgroundSessions); badge != "" {
		cwdLine += "  ·  " + badge
	}
	if !d.IsWorkspace {
		cwdLine += "  /workspace save"
	}
	cwdLine = clampEllipsis(cwdLine, leftW)
	gitSummary := p.homeGitSummarySpans(d)

	DrawBox(s, rect, p.theme.BorderActive)
	DrawText(s, rect.X+2, rect.Y, rect.W-4, p.theme.TextMuted, workspaceLabel)
	DrawText(s, rect.X+2, lineY, leftW, p.theme.Text, cwdLine)
	if linked := p.activeWorkspaceLinkedDirectories(); len(linked) > 0 && rect.H > 3 {
		hint := fmt.Sprintf("Multi-root workspace • %d linked • /workspace to delink", len(linked))
		DrawText(s, rect.X+2, rect.Y+2, rect.W-4, p.theme.Secondary, clampEllipsis(hint, rect.W-4))
	}
	if rightW > 0 {
		gitX := rect.X + 2 + leftW + 1
		p.drawRightAlignedHomeSpans(s, gitX+rightW-1, lineY, rightW, gitSummary)
		p.registerTopTarget(Rect{X: gitX, Y: lineY, W: rightW, H: 1}, "open-git", 0)
	}
}

func (p *HomePage) drawTopItemRow(s tcell.Screen, y, x, maxW int, items []topItem, centered bool) int {
	if maxW <= 0 || len(items) == 0 {
		return x
	}
	gap := 2
	totalW := 0
	for i, item := range items {
		if i > 0 {
			totalW += gap
		}
		totalW += utf8.RuneCountInString(item.Label)
	}
	startX := x
	if centered && totalW < maxW {
		startX = x + (maxW-totalW)/2
	}

	limitX := x + maxW
	cx := startX
	for i, item := range items {
		labelW := utf8.RuneCountInString(item.Label)
		if labelW <= 0 || cx+labelW > limitX {
			break
		}
		DrawText(s, cx, y, limitX-cx, item.Style, item.Label)
		p.registerTopTarget(Rect{X: cx, Y: y, W: labelW, H: 1}, item.Action, item.Index)
		cx += labelW
		if i < len(items)-1 {
			if cx+gap > limitX {
				break
			}
			cx += gap
		}
	}
	return cx
}

func (p *HomePage) workspaceButtonState(action string) workspaceButtonState {
	if action == "" {
		return workspaceButtonIdle
	}
	if p.pressedTopFrames > 0 && p.pressedTopAction == action {
		return workspaceButtonPressed
	}
	if p.hoverTopAction == action {
		return workspaceButtonHover
	}
	if p.selectedTopAction == action {
		return workspaceButtonSelected
	}
	return workspaceButtonIdle
}

func homeGitSummaryLine(d model.DirectoryItem) string {
	return spansPlainText((&HomePage{}).homeGitSummarySpans(d))
}

func (p *HomePage) homeGitSummarySpans(d model.DirectoryItem) []chatRenderSpan {
	if !d.HasGit {
		return []chatRenderSpan{{Text: "git: no repo", Style: p.theme.TextMuted}}
	}

	spans := []chatRenderSpan{
		{Text: "git ", Style: p.theme.TextMuted},
		{Text: homeGitBranchLabel(d), Style: p.theme.Secondary},
	}
	if upstream := strings.TrimSpace(d.Upstream); upstream != "" {
		spans = append(spans,
			chatRenderSpan{Text: " @", Style: p.theme.TextMuted},
			chatRenderSpan{Text: upstream, Style: p.theme.TextMuted},
		)
	}
	if d.StagedCount > 0 {
		spans = append(spans,
			chatRenderSpan{Text: " +", Style: p.theme.TextMuted},
			chatRenderSpan{Text: fmt.Sprintf("%d", d.StagedCount), Style: p.theme.Success},
		)
	}
	if d.ModifiedCount > 0 {
		spans = append(spans,
			chatRenderSpan{Text: " ~", Style: p.theme.TextMuted},
			chatRenderSpan{Text: fmt.Sprintf("%d", d.ModifiedCount), Style: p.theme.Warning},
		)
	}
	if d.UntrackedCount > 0 {
		spans = append(spans,
			chatRenderSpan{Text: " ?", Style: p.theme.TextMuted},
			chatRenderSpan{Text: fmt.Sprintf("%d", d.UntrackedCount), Style: p.theme.Accent},
		)
	}
	if d.ConflictCount > 0 {
		spans = append(spans,
			chatRenderSpan{Text: " !", Style: p.theme.TextMuted},
			chatRenderSpan{Text: fmt.Sprintf("%d", d.ConflictCount), Style: p.theme.Error},
		)
	}
	if d.AheadCount > 0 {
		spans = append(spans,
			chatRenderSpan{Text: " ↑", Style: p.theme.TextMuted},
			chatRenderSpan{Text: fmt.Sprintf("%d", d.AheadCount), Style: p.theme.Secondary},
		)
	}
	if d.BehindCount > 0 {
		spans = append(spans,
			chatRenderSpan{Text: " ↓", Style: p.theme.TextMuted},
			chatRenderSpan{Text: fmt.Sprintf("%d", d.BehindCount), Style: p.theme.Secondary},
		)
	}
	if d.DirtyCount == 0 && d.AheadCount == 0 && d.BehindCount == 0 {
		spans = append(spans,
			chatRenderSpan{Text: " clean", Style: p.theme.TextMuted},
		)
	}
	return spans
}

func (p *HomePage) drawRightAlignedHomeSpans(s tcell.Screen, xRight, y, maxWidth int, spans []chatRenderSpan) {
	if maxWidth <= 0 || len(spans) == 0 {
		return
	}
	trimmed := trimLeftRenderSpansToWidth(spans, maxWidth)
	width := renderSpansRuneCount(trimmed)
	if width <= 0 {
		return
	}
	startX := xRight - width + 1
	DrawTimelineLine(s, startX, y, width, chatRenderLine{Spans: trimmed})
}

func trimLeftRenderSpansToWidth(spans []chatRenderSpan, width int) []chatRenderSpan {
	if width <= 0 || len(spans) == 0 {
		return nil
	}
	if renderSpansRuneCount(spans) <= width {
		out := make([]chatRenderSpan, len(spans))
		copy(out, spans)
		return out
	}

	out := make([]chatRenderSpan, 0, len(spans))
	remaining := width
	for i := len(spans) - 1; i >= 0 && remaining > 0; i-- {
		span := spans[i]
		runes := []rune(span.Text)
		if len(runes) == 0 {
			continue
		}
		if len(runes) > remaining {
			runes = runes[len(runes)-remaining:]
		}
		out = append(out, chatRenderSpan{Text: string(runes), Style: span.Style})
		remaining -= len(runes)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func spansPlainText(spans []chatRenderSpan) string {
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(span.Text)
	}
	return b.String()
}

func homeGitStatusText(d model.DirectoryItem) string {
	if !d.HasGit {
		return fmt.Sprintf("git: no repo in %s", d.Path)
	}
	parts := []string{fmt.Sprintf("git %s", homeGitBranchLabel(d))}
	if upstream := strings.TrimSpace(d.Upstream); upstream != "" {
		parts = append(parts, fmt.Sprintf("remote %s", upstream))
	} else {
		parts = append(parts, "remote none")
	}
	parts = append(parts,
		fmt.Sprintf("staged %d", d.StagedCount),
		fmt.Sprintf("modified %d", d.ModifiedCount),
		fmt.Sprintf("untracked %d", d.UntrackedCount),
		fmt.Sprintf("conflicts %d", d.ConflictCount),
	)
	if d.AheadCount > 0 || d.BehindCount > 0 {
		parts = append(parts, fmt.Sprintf("↑%d ↓%d", d.AheadCount, d.BehindCount))
	} else {
		parts = append(parts, "in sync")
	}
	return strings.Join(parts, " • ")
}

func homeGitBranchLabel(d model.DirectoryItem) string {
	branch := strings.TrimSpace(d.Branch)
	if branch == "" || branch == "-" {
		return "detached"
	}
	return branch
}

func (p *HomePage) workspaceButtonStyle(base tcell.Style, state workspaceButtonState) tcell.Style {
	switch state {
	case workspaceButtonPressed:
		return base.Reverse(true).Bold(true).Underline(true)
	case workspaceButtonHover:
		return base.Bold(true).Underline(true)
	case workspaceButtonSelected:
		return base.Bold(true)
	default:
		return base
	}
}

func (p *HomePage) workspaceItems() []topItem {
	if len(p.model.Workspaces) == 0 {
		return nil
	}

	items := make([]topItem, 0, len(p.model.Workspaces))
	for i, ws := range p.model.Workspaces {
		label := fmt.Sprintf("[%s %s]", ws.Icon, ws.Name)
		style := p.theme.TextMuted
		if ws.Active {
			style = p.theme.Primary.Bold(true)
		}
		items = append(items, topItem{
			Label:  label,
			Style:  style,
			Action: "workspace",
			Index:  i,
		})
	}
	return items
}
