package v3chat

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui/footerbar"
)

const (
	finalHandoffSource = "plan_execution_final_handoff"
	finalHandoffKind   = "plan_final_checkpoint_handoff"
)

func isStructuredFinalHandoffMessage(message Message) bool {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "system") || message.FinalHandoff == nil {
		return false
	}
	return strings.EqualFold(metadataString(message.Metadata, "source"), finalHandoffSource) ||
		strings.EqualFold(metadataString(message.Metadata, "kind"), finalHandoffKind)
}

func sanitizeLegacyHandoffMarkers(content string) string {
	const openTag = "<swarm-handoff-summary>"
	const closeTag = "</swarm-handoff-summary>"
	if !strings.Contains(content, openTag) && !strings.Contains(content, closeTag) {
		return content
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == openTag || trimmed == closeTag {
			continue
		}
		// Malformed legacy payloads still must not leak marker text into the
		// transcript. Keep all surrounding durable prose intact.
		line = strings.ReplaceAll(strings.ReplaceAll(line, openTag, ""), closeTag, "")
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func finalHandoffHasDetails(handoff *client.PlanFinalHandoff) bool {
	if handoff == nil {
		return false
	}
	return strings.TrimSpace(handoff.Details.Report) != "" ||
		strings.TrimSpace(handoff.Details.Result) != "" ||
		len(handoff.Details.ChangedFiles) > 0 || len(handoff.Details.Validation) > 0
}

func finalHandoffControlCount(handoff *client.PlanFinalHandoff) int {
	if handoff == nil {
		return 0
	}
	count := len(handoff.SuggestedPrompts)
	if finalHandoffHasDetails(handoff) {
		count++
	}
	return count
}

func (p *Page) latestFinalHandoffLocked() (Message, bool) {
	if p.runtime == nil || p.runtime.Store() == nil {
		return Message{}, false
	}
	messages := p.runtime.Store().Snapshot().Messages
	for index := len(messages) - 1; index >= 0; index-- {
		if isStructuredFinalHandoffMessage(messages[index]) {
			return messages[index], true
		}
	}
	return Message{}, false
}

func (p *Page) focusLatestFinalHandoffLocked() bool {
	message, ok := p.latestFinalHandoffLocked()
	if !ok || finalHandoffControlCount(message.FinalHandoff) == 0 {
		return false
	}
	if p.handoffMessageID != message.ID {
		p.handoffControl = 0
	}
	p.handoffMessageID = message.ID
	p.handoffFocus = true
	p.follow = true
	p.scroll = 0
	return true
}

func (p *Page) handleFinalHandoffKeyLocked(ev *tcell.EventKey) bool {
	if ev == nil || !p.handoffFocus {
		return false
	}
	message, ok := p.latestFinalHandoffLocked()
	if !ok || message.ID != p.handoffMessageID {
		p.handoffFocus = false
		p.handoffMessageID = ""
		return false
	}
	controlCount := finalHandoffControlCount(message.FinalHandoff)
	if controlCount == 0 {
		p.handoffFocus = false
		return false
	}
	move := func(delta int) {
		p.handoffControl = (p.handoffControl + delta + controlCount) % controlCount
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.handoffFocus = false
		return true
	case tcell.KeyUp, tcell.KeyLeft, tcell.KeyBacktab:
		move(-1)
		return true
	case tcell.KeyDown, tcell.KeyRight, tcell.KeyTab:
		move(1)
		return true
	case tcell.KeyEnter:
		p.activateFinalHandoffControlLocked(message, p.handoffControl)
		return true
	case tcell.KeyRune:
		if ev.Rune() >= '1' && ev.Rune() <= '3' {
			index := int(ev.Rune() - '1')
			action := finalHandoffPromptAction(message.ID, index)
			if index < len(message.FinalHandoff.SuggestedPrompts) && p.handoffTargets[action].W > 0 {
				p.activateFinalHandoffControlLocked(message, index)
			}
			return true
		}
	}
	return false
}

func (p *Page) activateFinalHandoffControlLocked(message Message, index int) {
	handoff := message.FinalHandoff
	if handoff == nil || index < 0 {
		return
	}
	if index < len(handoff.SuggestedPrompts) {
		prompt := strings.TrimSpace(handoff.SuggestedPrompts[index].Prompt)
		if prompt == "" || p.busy {
			return
		}
		p.handoffFocus = false
		p.input = nil
		p.cursor = 0
		p.follow = true
		p.scroll = 0
		go p.Send(prompt)
		return
	}
	if index == len(handoff.SuggestedPrompts) && finalHandoffHasDetails(handoff) {
		copy := cloneFinalHandoff(handoff)
		p.handoffDetails = &copy
		p.handoffDetailsMessageID = message.ID
		p.handoffDetailsModal = true
		p.handoffDetailsScroll = 0
	}
}

func cloneFinalHandoff(value *client.PlanFinalHandoff) client.PlanFinalHandoff {
	if value == nil {
		return client.PlanFinalHandoff{}
	}
	out := *value
	out.ImpactBullets = append([]string(nil), value.ImpactBullets...)
	out.SuggestedPrompts = append([]client.PlanFinalHandoffSuggestedPrompt(nil), value.SuggestedPrompts...)
	out.Details.ChangedFiles = append([]string(nil), value.Details.ChangedFiles...)
	out.Details.Validation = append([]string(nil), value.Details.Validation...)
	if value.Recommendation != nil {
		recommendation := *value.Recommendation
		out.Recommendation = &recommendation
	}
	return out
}

func (p *Page) handleFinalHandoffDetailsKeyLocked(ev *tcell.EventKey) PageAction {
	if ev == nil {
		return PageActionNone
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.closeFinalHandoffDetailsLocked()
	case tcell.KeyUp:
		p.handoffDetailsScroll = maxInt(0, p.handoffDetailsScroll-1)
	case tcell.KeyDown:
		p.handoffDetailsScroll++
	case tcell.KeyPgUp:
		p.handoffDetailsScroll = maxInt(0, p.handoffDetailsScroll-8)
	case tcell.KeyPgDn:
		p.handoffDetailsScroll += 8
	case tcell.KeyHome:
		p.handoffDetailsScroll = 0
	case tcell.KeyRune:
		if ev.Rune() == 'q' || ev.Rune() == 'd' {
			p.closeFinalHandoffDetailsLocked()
		}
	}
	return PageActionNone
}

func (p *Page) closeFinalHandoffDetailsLocked() {
	p.handoffDetailsModal = false
	p.handoffDetailsScroll = 0
	p.handoffDetails = nil
	p.handoffDetailsMessageID = ""
}

func (p *Page) activateFinalHandoffTargetLocked(action string) bool {
	parts := strings.Split(action, ":")
	if len(parts) != 4 || parts[0] != "handoff" {
		return false
	}
	message, ok := p.latestFinalHandoffLocked()
	if !ok || message.ID != parts[1] || message.FinalHandoff == nil {
		return false
	}
	p.handoffMessageID = message.ID
	p.handoffFocus = true
	if parts[2] == "prompt" {
		var index int
		if _, err := fmt.Sscanf(parts[3], "%d", &index); err == nil && index >= 0 && index < len(message.FinalHandoff.SuggestedPrompts) {
			p.handoffControl = index
			p.activateFinalHandoffControlLocked(message, index)
			return true
		}
	}
	if parts[2] == "details" {
		p.handoffControl = len(message.FinalHandoff.SuggestedPrompts)
		p.activateFinalHandoffControlLocked(message, p.handoffControl)
		return true
	}
	return false
}

func (p *Page) renderFinalHandoffRows(message Message, width int, styles PageStyles) []renderRow {
	handoff := message.FinalHandoff
	if handoff == nil || width < 8 {
		return nil
	}
	p.mu.Lock()
	focused := p.handoffFocus && p.handoffMessageID == message.ID
	selected := p.handoffControl
	p.mu.Unlock()
	borderStyle := styles.BorderActive
	if focused {
		borderStyle = styles.Primary.Bold(true)
	}
	innerWidth := maxInt(1, width-2)
	contentWidth := maxInt(1, innerWidth-2)
	rows := []renderRow{{text: "┌" + strings.Repeat("─", innerWidth) + "┐", style: borderStyle}}
	bodyRows := 0
	appendBody := func(text string, style tcell.Style, action string, active bool) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, line := range wrapText(text, contentWidth) {
			lineWidth := displayCellWidth(line)
			padding := strings.Repeat(" ", maxInt(0, contentWidth-lineWidth))
			bodyStyle := style
			if active {
				bodyStyle = styleWithForeground(styles.Element, styles.Primary).Bold(true)
			}
			row := renderRow{
				text:  "│ " + line + padding + " │",
				style: bodyStyle,
				spans: []renderSpan{
					{text: "│ ", style: borderStyle},
					{text: line + padding, style: bodyStyle},
					{text: " │", style: borderStyle},
				},
			}
			if action != "" {
				row.actions = []renderActionTarget{{x: 2, width: contentWidth, action: action}}
			}
			rows = append(rows, row)
			bodyRows++
		}
	}
	appendSectionGap := func() {
		if bodyRows == 0 {
			return
		}
		rows = append(rows, renderRow{
			text: "│ " + strings.Repeat(" ", contentWidth) + " │",
			spans: []renderSpan{
				{text: "│ ", style: borderStyle},
				{text: strings.Repeat(" ", contentWidth), style: styles.Text},
				{text: " │", style: borderStyle},
			},
		})
		bodyRows++
	}
	outcome := firstNonEmpty(metadataString(message.Metadata, "outcome"), metadataString(message.Metadata, "execution_status"))
	if outcome == "" && handoff.Recommendation != nil {
		outcome = handoff.Recommendation.Decision
	}
	outcome = strings.ReplaceAll(strings.ReplaceAll(firstNonEmpty(outcome, "completed"), "_", " "), "-", " ")
	appendBody("FINAL HANDOFF  ·  "+outcome, styles.Primary.Bold(true), "", false)
	if strings.TrimSpace(handoff.Title) != "" || strings.TrimSpace(handoff.Overview) != "" || len(handoff.ImpactBullets) > 0 {
		appendSectionGap()
		appendBody(handoff.Title, styles.Text.Bold(true), "", false)
		appendBody(handoff.Overview, styles.Muted, "", false)
		for _, impact := range handoff.ImpactBullets {
			appendBody("• "+impact, styles.Muted, "", false)
		}
	}
	if recommendation := handoff.Recommendation; recommendation != nil {
		appendSectionGap()
		appendBody("RECOMMENDATION", styles.Secondary.Bold(true), "", false)
		label := strings.TrimSpace(strings.ReplaceAll(recommendation.Decision, "_", " "))
		if action := strings.TrimSpace(strings.ReplaceAll(recommendation.Action, "_", " ")); action != "" {
			label = strings.TrimSpace(label + " — " + action)
		}
		appendBody(label, styles.Text.Bold(true), "", false)
		if recommendation.Reason != "" {
			appendBody(recommendation.Reason, styles.Muted, "", false)
		}
	}
	if len(handoff.SuggestedPrompts) > 0 {
		appendSectionGap()
		appendBody("NEXT STEPS", styles.Secondary.Bold(true), "", false)
		for index, suggestion := range handoff.SuggestedPrompts {
			action := finalHandoffPromptAction(message.ID, index)
			appendBody(fmt.Sprintf("%d. %s", index+1, suggestion.Label), styles.Text, action, focused && selected == index)
		}
	}
	if finalHandoffHasDetails(handoff) {
		appendSectionGap()
		facts := make([]string, 0, 4)
		if strings.TrimSpace(handoff.Details.Report) != "" {
			facts = append(facts, "report")
		}
		if strings.TrimSpace(handoff.Details.Result) != "" {
			facts = append(facts, "result")
		}
		if count := len(handoff.Details.ChangedFiles); count > 0 {
			facts = append(facts, fmt.Sprintf("files %d", count))
		}
		if count := len(handoff.Details.Validation); count > 0 {
			facts = append(facts, fmt.Sprintf("validation %d", count))
		}
		action := fmt.Sprintf("handoff:%s:details:0", message.ID)
		appendBody("Details  ·  "+strings.Join(facts, "  ·  "), styles.Muted, action, focused && selected == len(handoff.SuggestedPrompts))
	}
	if focused {
		appendSectionGap()
		appendBody("↑/↓ or Tab move  ·  Enter select  ·  Esc exit", styles.Muted, "", false)
	} else if finalHandoffControlCount(handoff) > 0 {
		appendSectionGap()
		appendBody("Tab focus  ·  1–3 choose next step", styles.Muted, "", false)
	}
	rows = append(rows, renderRow{text: "└" + strings.Repeat("─", innerWidth) + "┘", style: borderStyle})
	return append(rows, renderRow{text: "", style: styles.Text})
}

func finalHandoffDetailsLines(handoff *client.PlanFinalHandoff, width int, styles PageStyles) []permissionCardLine {
	if handoff == nil {
		return nil
	}
	lines := make([]permissionCardLine, 0, 32)
	appendSection := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if len(lines) > 0 {
			lines = append(lines, permissionCardLine{Text: "", Style: styles.Muted})
		}
		lines = append(lines, permissionCardLine{Text: label, Style: styles.Secondary.Bold(true)})
		for _, line := range wrapDisplayText(value, width) {
			lines = append(lines, permissionCardLine{Text: line, Style: styles.Text})
		}
	}
	appendList := func(label string, values []string) {
		if len(values) == 0 {
			return
		}
		if len(lines) > 0 {
			lines = append(lines, permissionCardLine{Text: "", Style: styles.Muted})
		}
		lines = append(lines, permissionCardLine{Text: label, Style: styles.Secondary.Bold(true)})
		for _, value := range values {
			for index, line := range wrapDisplayText(value, maxInt(1, width-2)) {
				prefix := "  "
				if index == 0 {
					prefix = "• "
				}
				lines = append(lines, permissionCardLine{Text: prefix + line, Style: styles.Text})
			}
		}
	}
	appendSection("REPORT", handoff.Details.Report)
	appendSection("RESULT", handoff.Details.Result)
	appendList("CHANGED FILES", handoff.Details.ChangedFiles)
	appendList("VALIDATION", handoff.Details.Validation)
	if len(lines) == 0 {
		lines = append(lines, permissionCardLine{Text: "No durable evidence was provided.", Style: styles.Muted})
	}
	return lines
}

func (p *Page) drawFinalHandoffDetailsModal(screen tcell.Screen, width, height int, styles PageStyles, handoff *client.PlanFinalHandoff, scroll int) {
	if handoff == nil || width < 8 || height < 6 {
		return
	}
	modalWidth := minInt(112, maxInt(8, width-2))
	modalHeight := minInt(maxInt(6, height-2), height)
	x, y := (width-modalWidth)/2, (height-modalHeight)/2
	fill(screen, x, y, modalWidth, modalHeight, styles.Panel)
	drawBox(screen, x, y, modalWidth, modalHeight, styles.BorderActive)
	contentWidth := maxInt(1, modalWidth-4)
	drawText(screen, x+2, y+1, contentWidth, styles.Primary.Bold(true), "FINAL HANDOFF DETAILS")
	if modalHeight > 4 {
		drawText(screen, x+2, y+2, contentWidth, styles.Muted, "↑/↓ scroll  ·  d, q, or Esc close")
	}
	lines := finalHandoffDetailsLines(handoff, contentWidth, styles)
	visibleRows := maxInt(1, modalHeight-5)
	maxScroll := maxInt(0, len(lines)-visibleRows)
	scroll = minInt(maxInt(0, scroll), maxScroll)
	p.mu.Lock()
	p.handoffDetailsScroll = scroll
	p.mu.Unlock()
	for row := 0; row < visibleRows && scroll+row < len(lines); row++ {
		line := lines[scroll+row]
		drawText(screen, x+2, y+3+row, contentWidth, line.Style, line.Text)
	}
	if maxScroll > 0 && modalWidth >= 10 {
		indicator := fmt.Sprintf("%d/%d", scroll+1, maxScroll+1)
		drawText(screen, x+modalWidth-2-utf8.RuneCountInString(indicator), y+modalHeight-2, utf8.RuneCountInString(indicator), styles.Muted, indicator)
	}
}

func wrapDisplayText(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		var line strings.Builder
		lineWidth := 0
		remaining := paragraph
		for remaining != "" {
			cluster, rest, clusterWidth, _ := uniseg.FirstGraphemeClusterInString(remaining, -1)
			if cluster == "" {
				break
			}
			if clusterWidth <= 0 {
				clusterWidth = 1
			}
			if lineWidth > 0 && lineWidth+clusterWidth > width {
				out = append(out, strings.TrimRight(line.String(), " \t"))
				line.Reset()
				lineWidth = 0
				if strings.TrimSpace(cluster) == "" {
					remaining = rest
					continue
				}
			}
			line.WriteString(cluster)
			lineWidth += clusterWidth
			remaining = rest
		}
		out = append(out, line.String())
	}
	return out
}

func displayCellWidth(text string) int {
	width := 0
	for text != "" {
		cluster, rest, clusterWidth, _ := uniseg.FirstGraphemeClusterInString(text, -1)
		if cluster == "" {
			break
		}
		if clusterWidth <= 0 {
			clusterWidth = 1
		}
		width += clusterWidth
		text = rest
	}
	return width
}

func finalHandoffPromptAction(messageID string, index int) string {
	return fmt.Sprintf("handoff:%s:prompt:%d", messageID, index)
}

func finalHandoffTargetAt(targets map[string]footerbar.Rect, x, y int) string {
	for action, target := range targets {
		if containsFooterPoint(target, x, y) {
			return action
		}
	}
	return ""
}
