package v3chat

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui/footerbar"
)

const (
	finalHandoffSource   = "plan_execution_final_handoff"
	finalHandoffKind     = "plan_final_checkpoint_handoff"
	blockedHandoffSource = "plan_execution_blocked_handoff"
	blockedHandoffKind   = "plan_blocked_checkpoint_handoff"
)

func isStructuredFinalHandoffMessage(message Message) bool {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "system") || message.FinalHandoff == nil {
		return false
	}
	return strings.EqualFold(metadataString(message.Metadata, "source"), finalHandoffSource) ||
		strings.EqualFold(metadataString(message.Metadata, "kind"), finalHandoffKind) ||
		strings.EqualFold(metadataString(message.Metadata, "source"), blockedHandoffSource) ||
		strings.EqualFold(metadataString(message.Metadata, "kind"), blockedHandoffKind)
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

func finalHandoffExpandableSections(handoff *client.PlanFinalHandoff) []string {
	if handoff == nil {
		return nil
	}
	sections := make([]string, 0, 4)
	if strings.TrimSpace(handoff.Details.Report) != "" || strings.TrimSpace(handoff.Details.Result) != "" {
		sections = append(sections, "details")
	}
	if len(handoff.Details.ChangedFiles) > 0 {
		sections = append(sections, "files")
	}
	if len(handoff.Details.Validation) > 0 {
		sections = append(sections, "validation")
	}
	if len(handoff.Artifacts) > 0 {
		sections = append(sections, "artifacts")
	}
	return sections
}

func finalHandoffRecommendationPrompt(rec *client.SessionPlanCheckpointRecommendation) string {
	if rec == nil {
		return ""
	}
	if prompt := strings.TrimSpace(rec.Prompt); prompt != "" {
		return prompt
	}
	if action := strings.TrimSpace(rec.Action); action != "" {
		return action
	}
	return strings.TrimSpace(rec.Decision)
}

func hasActionableRecommendation(handoff *client.PlanFinalHandoff) bool {
	if handoff == nil || handoff.Recommendation == nil {
		return false
	}
	return finalHandoffRecommendationPrompt(handoff.Recommendation) != ""
}

func finalHandoffControlCount(handoff *client.PlanFinalHandoff) int {
	if handoff == nil {
		return 0
	}
	count := len(handoff.SuggestedPrompts) + len(finalHandoffExpandableSections(handoff))
	if hasActionableRecommendation(handoff) {
		count++
	}
	return count
}

func finalHandoffSelectedAction(messages []Message, messageID string, control int) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.ID != messageID || !isStructuredFinalHandoffMessage(message) || message.FinalHandoff == nil {
			continue
		}
		offset := 0
		if hasActionableRecommendation(message.FinalHandoff) {
			if control == 0 {
				return finalHandoffRecommendationAction(message.ID)
			}
			offset = 1
		}
		promptIndex := control - offset
		if promptIndex >= 0 && promptIndex < len(message.FinalHandoff.SuggestedPrompts) {
			return finalHandoffPromptAction(message.ID, promptIndex)
		}
		sections := finalHandoffExpandableSections(message.FinalHandoff)
		sectionIndex := promptIndex - len(message.FinalHandoff.SuggestedPrompts)
		if sectionIndex >= 0 && sectionIndex < len(sections) {
			return finalHandoffSectionAction(message.ID, sections[sectionIndex])
		}
		return ""
	}
	return ""
}

func scrollToRenderAction(rows []renderRow, action string, visibleHeight, currentScroll int) int {
	if action == "" || visibleHeight <= 0 {
		return currentScroll
	}
	for rowIndex, row := range rows {
		for _, target := range row.actions {
			if target.action != action {
				continue
			}
			bottomStart := maxInt(0, len(rows)-visibleHeight)
			visibleStart := bottomStart - currentScroll
			if rowIndex < visibleStart {
				return maxInt(0, bottomStart-rowIndex)
			}
			if rowIndex >= visibleStart+visibleHeight {
				return maxInt(0, bottomStart-rowIndex+visibleHeight-1)
			}
			return currentScroll
		}
	}
	return currentScroll
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
	case tcell.KeyLeft, tcell.KeyBacktab:
		move(-1)
		return true
	case tcell.KeyRight:
		move(1)
		return true
	case tcell.KeyTab:
		return true
	case tcell.KeyEnter:
		p.activateFinalHandoffControlLocked(message, p.handoffControl)
		return true
	}
	return false
}

func (p *Page) activateFinalHandoffControlLocked(message Message, index int) {
	handoff := message.FinalHandoff
	if handoff == nil || index < 0 {
		return
	}
	if hasActionableRecommendation(handoff) {
		if index == 0 {
			prompt := finalHandoffRecommendationPrompt(handoff.Recommendation)
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
		index--
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
	sections := finalHandoffExpandableSections(handoff)
	sectionIndex := index - len(handoff.SuggestedPrompts)
	if sectionIndex >= 0 && sectionIndex < len(sections) {
		copy := cloneFinalHandoff(handoff)
		p.handoffDetails = &copy
		p.handoffDetailsMessageID = message.ID
		p.handoffDetailsSection = sections[sectionIndex]
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
	out.CopyableCodeBlocks = append([]client.PlanFinalHandoffCopyableCodeBlock(nil), value.CopyableCodeBlocks...)
	out.SuggestedPrompts = append([]client.PlanFinalHandoffSuggestedPrompt(nil), value.SuggestedPrompts...)
	out.Artifacts = append([]client.PlanFinalHandoffArtifact(nil), value.Artifacts...)
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
	p.handoffDetailsSection = ""
}

func (p *Page) activateFinalHandoffTargetLocked(action string) bool {
	parts := strings.Split(action, ":")
	if (len(parts) != 4 && len(parts) != 3) || parts[0] != "handoff" {
		return false
	}
	message, ok := p.latestFinalHandoffLocked()
	if !ok || message.ID != parts[1] || message.FinalHandoff == nil {
		return false
	}
	p.handoffMessageID = message.ID
	p.handoffFocus = true
	if parts[2] == "recommendation" {
		if hasActionableRecommendation(message.FinalHandoff) {
			p.handoffControl = 0
			p.activateFinalHandoffControlLocked(message, 0)
			return true
		}
		return false
	}
	if len(parts) == 4 && parts[2] == "prompt" {
		var index int
		if _, err := fmt.Sscanf(parts[3], "%d", &index); err == nil && index >= 0 && index < len(message.FinalHandoff.SuggestedPrompts) {
			controlIndex := index
			if hasActionableRecommendation(message.FinalHandoff) {
				controlIndex++
			}
			p.handoffControl = controlIndex
			p.activateFinalHandoffControlLocked(message, controlIndex)
			return true
		}
	}
	if len(parts) == 4 && parts[2] == "section" {
		sections := finalHandoffExpandableSections(message.FinalHandoff)
		for sectionIndex, section := range sections {
			if section != parts[3] {
				continue
			}
			controlIndex := len(message.FinalHandoff.SuggestedPrompts) + sectionIndex
			if hasActionableRecommendation(message.FinalHandoff) {
				controlIndex++
			}
			p.handoffControl = controlIndex
			p.activateFinalHandoffControlLocked(message, p.handoffControl)
			return true
		}
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
	if len(handoff.CopyableCodeBlocks) > 0 {
		appendSectionGap()
		appendBody("COPYABLE CODE", styles.Secondary.Bold(true), "", false)
		for _, block := range handoff.CopyableCodeBlocks {
			label := strings.TrimSpace(block.Label)
			if label == "" {
				label = "Copy this"
			}
			appendBody(label, styles.Text.Bold(true), "", false)
			appendBody(block.Code, styles.Text, "", false)
		}
	}
	if recommendation := handoff.Recommendation; recommendation != nil {
		appendSectionGap()
		appendBody("RECOMMENDATION", styles.Secondary.Bold(true), "", false)
		label := strings.TrimSpace(strings.ReplaceAll(recommendation.Decision, "_", " "))
		if action := strings.TrimSpace(strings.ReplaceAll(recommendation.Action, "_", " ")); action != "" {
			label = strings.TrimSpace(label + " — " + action)
		}
		recAction := ""
		recActive := false
		if hasActionableRecommendation(handoff) {
			recAction = finalHandoffRecommendationAction(message.ID)
			recActive = focused && selected == 0
		}
		appendBody(label, styles.Text.Bold(true), recAction, recActive)
		if recommendation.Reason != "" {
			appendBody(recommendation.Reason, styles.Muted, "", false)
		}
	}
	if len(handoff.SuggestedPrompts) > 0 {
		appendSectionGap()
		appendBody("NEXT STEPS", styles.Secondary.Bold(true), "", false)
		promptOffset := 0
		if hasActionableRecommendation(handoff) {
			promptOffset = 1
		}
		for index, suggestion := range handoff.SuggestedPrompts {
			action := finalHandoffPromptAction(message.ID, index)
			controlIndex := promptOffset + index
			appendBody(fmt.Sprintf("%d. %s", index+1, suggestion.Label), styles.Text, action, focused && selected == controlIndex)
		}
	}
	sections := finalHandoffExpandableSections(handoff)
	if len(sections) > 0 {
		appendSectionGap()
		appendBody("EVIDENCE", styles.Secondary.Bold(true), "", false)
		sectionOffset := len(handoff.SuggestedPrompts)
		if hasActionableRecommendation(handoff) {
			sectionOffset++
		}
		for sectionIndex, section := range sections {
			label := ""
			switch section {
			case "artifacts":
				label = fmt.Sprintf("▸ Artifacts (%d)", len(handoff.Artifacts))
			case "details":
				facts := make([]string, 0, 2)
				if strings.TrimSpace(handoff.Details.Report) != "" {
					facts = append(facts, "report")
				}
				if strings.TrimSpace(handoff.Details.Result) != "" {
					facts = append(facts, "result")
				}
				label = "▸ Details  ·  " + strings.Join(facts, "  ·  ")
			case "files":
				label = fmt.Sprintf("▸ Files (%d)", len(handoff.Details.ChangedFiles))
			case "validation":
				label = fmt.Sprintf("▸ Validation (%d)", len(handoff.Details.Validation))
			}
			controlIndex := sectionOffset + sectionIndex
			appendBody(label, styles.Muted, finalHandoffSectionAction(message.ID, section), focused && selected == controlIndex)
		}
	}
	if focused {
		appendSectionGap()
		appendBody("←/→ choose  ·  Enter execute/open  ·  Esc exit", styles.Muted, "", false)
	} else if finalHandoffControlCount(handoff) > 0 {
		appendSectionGap()
		appendBody("Tab focus  ·  ←/→ choose  ·  Enter execute/open", styles.Muted, "", false)
	}
	rows = append(rows, renderRow{text: "└" + strings.Repeat("─", innerWidth) + "┘", style: borderStyle})
	return append(rows, renderRow{text: "", style: styles.Text})
}

func finalHandoffDetailsLines(handoff *client.PlanFinalHandoff, section, sessionID string, width int, styles PageStyles) []permissionCardLine {
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
	switch section {
	case "artifacts":
		appendArtifactList(&lines, handoff.Artifacts, sessionID, width, styles)
	case "details":
		appendSection("REPORT", handoff.Details.Report)
		appendSection("RESULT", handoff.Details.Result)
	case "files":
		appendList("CHANGED FILES", handoff.Details.ChangedFiles)
	case "validation":
		appendList("VALIDATION", handoff.Details.Validation)
	default:
		appendArtifactList(&lines, handoff.Artifacts, sessionID, width, styles)
		appendSection("REPORT", handoff.Details.Report)
		appendSection("RESULT", handoff.Details.Result)
		appendList("CHANGED FILES", handoff.Details.ChangedFiles)
		appendList("VALIDATION", handoff.Details.Validation)
	}
	if len(lines) == 0 {
		lines = append(lines, permissionCardLine{Text: "No durable evidence was provided.", Style: styles.Muted})
	}
	return lines
}

func appendArtifactList(lines *[]permissionCardLine, artifacts []client.PlanFinalHandoffArtifact, sessionID string, width int, styles PageStyles) {
	if len(artifacts) == 0 {
		return
	}
	*lines = append(*lines, permissionCardLine{Text: "DELIVERABLE ARTIFACTS", Style: styles.Secondary.Bold(true)})
	for index, artifact := range artifacts {
		label := firstNonEmpty(
			strings.TrimSpace(artifact.Label),
			strings.TrimSpace(artifact.Description),
			strings.TrimSpace(artifact.Filename),
			strings.TrimSpace(artifact.VariantID),
			strings.TrimSpace(artifact.WorkspaceRelativePath),
			strings.TrimSpace(artifact.RelativePath),
			strings.TrimSpace(artifact.Path),
			fmt.Sprintf("Artifact %d", index+1),
		)
		mediaType := firstNonEmpty(strings.TrimSpace(artifact.MediaType), "file")

		status := strings.TrimSpace(artifact.Status)
		availability := "download"
		if artifact.Previewable {
			availability = "preview available"
		}
		if status != "" {
			if strings.EqualFold(status, "ready") {
				if artifact.Previewable {
					availability = "ready · preview available"
				} else {
					availability = "ready"
				}
			} else {
				availability = status
			}
		}

		for lineIndex, line := range wrapDisplayText(fmt.Sprintf("%d. %s  ·  %s  ·  %s", index+1, label, mediaType, availability), maxInt(1, width-2)) {
			prefix := "  "
			if lineIndex == 0 {
				prefix = "• "
			}
			*lines = append(*lines, permissionCardLine{Text: prefix + line, Style: styles.Text})
		}

		identityParts := make([]string, 0, 4)
		if sID := strings.TrimSpace(artifact.SessionID); sID != "" {
			identityParts = append(identityParts, "session="+sID)
		}
		if cID := strings.TrimSpace(artifact.CollectionID); cID != "" {
			identityParts = append(identityParts, "collection="+cID)
		}
		if vID := strings.TrimSpace(artifact.VariantID); vID != "" {
			identityParts = append(identityParts, "variant="+vID)
		}
		if artifact.EventSeq > 0 {
			identityParts = append(identityParts, fmt.Sprintf("event_seq=%d", artifact.EventSeq))
		}
		if len(identityParts) > 0 {
			for _, line := range wrapDisplayText("Identity: "+strings.Join(identityParts, " · "), maxInt(1, width-2)) {
				*lines = append(*lines, permissionCardLine{Text: "  " + line, Style: styles.Muted})
			}
		}

		path := firstNonEmpty(strings.TrimSpace(artifact.WorkspaceRelativePath), strings.TrimSpace(artifact.RelativePath), strings.TrimSpace(artifact.Path))
		if path != "" {
			for _, line := range wrapDisplayText("Path: "+path, maxInt(1, width-2)) {
				*lines = append(*lines, permissionCardLine{Text: "  " + line, Style: styles.Muted})
			}
		}

		artifactID := firstNonEmpty(strings.TrimSpace(artifact.ArtifactID), strings.TrimSpace(artifact.ID), strings.TrimSpace(artifact.VariantID))
		if artifactID != "" {
			sessionSegment := firstNonEmpty(strings.TrimSpace(artifact.SessionID), strings.TrimSpace(sessionID), "{session_id}")
			route := strings.TrimSpace(artifact.PreviewURL)
			if route == "" {
				route = fmt.Sprintf("/v3/sessions/%s/artifacts/%s", url.PathEscape(sessionSegment), url.PathEscape(artifactID))
			}
			for _, line := range wrapDisplayText("Route: "+route, maxInt(1, width-2)) {
				*lines = append(*lines, permissionCardLine{Text: "  " + line, Style: styles.Muted})
			}
		}

		if index+1 < len(artifacts) {
			*lines = append(*lines, permissionCardLine{Text: "", Style: styles.Muted})
		}
	}
	*lines = append(*lines,
		permissionCardLine{Text: "", Style: styles.Muted},
		permissionCardLine{Text: "Use the authenticated route through the same local or remote Swarm connection. The path is only a workspace-relative fallback.", Style: styles.Muted},
	)
}

func finalHandoffMessageSessionID(messages []Message, messageID string) string {
	for _, message := range messages {
		if message.ID == messageID {
			return strings.TrimSpace(message.SessionID)
		}
	}
	return ""
}

func (p *Page) drawFinalHandoffDetailsModal(screen tcell.Screen, width, height int, styles PageStyles, handoff *client.PlanFinalHandoff, section, sessionID string, scroll int) {
	if handoff == nil || width < 8 || height < 6 {
		return
	}
	modalWidth := minInt(112, maxInt(8, width-2))
	modalHeight := minInt(maxInt(6, height-2), height)
	x, y := (width-modalWidth)/2, (height-modalHeight)/2
	fill(screen, x, y, modalWidth, modalHeight, styles.Panel)
	drawBox(screen, x, y, modalWidth, modalHeight, styles.BorderActive)
	contentWidth := maxInt(1, modalWidth-4)
	title := map[string]string{"artifacts": "FINAL HANDOFF ARTIFACTS", "details": "FINAL HANDOFF DETAILS", "files": "FINAL HANDOFF FILES", "validation": "FINAL HANDOFF VALIDATION"}[section]
	if title == "" {
		title = "FINAL HANDOFF EVIDENCE"
	}
	drawText(screen, x+2, y+1, contentWidth, styles.Primary.Bold(true), title)
	if modalHeight > 4 {
		drawText(screen, x+2, y+2, contentWidth, styles.Muted, "↑/↓ scroll  ·  d, q, or Esc close")
	}
	lines := finalHandoffDetailsLines(handoff, section, sessionID, contentWidth, styles)
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

func finalHandoffRecommendationAction(messageID string) string {
	return fmt.Sprintf("handoff:%s:recommendation", messageID)
}

func finalHandoffPromptAction(messageID string, index int) string {
	return fmt.Sprintf("handoff:%s:prompt:%d", messageID, index)
}

func finalHandoffSectionAction(messageID, section string) string {
	return fmt.Sprintf("handoff:%s:section:%s", messageID, section)
}

func finalHandoffTargetAt(targets map[string]footerbar.Rect, x, y int) string {
	for action, target := range targets {
		if containsFooterPoint(target, x, y) {
			return action
		}
	}
	return ""
}
