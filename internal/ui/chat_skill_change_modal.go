package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

type skillChangePreviewSection struct {
	Title       string
	BorderStyle tcell.Style
	TitleStyle  tcell.Style
	Lines       []chatRenderLine
}

func isSkillChangePermission(record ChatPermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Requirement), "skill_change")
}

func (p *ChatPage) skillChangeModalActive() bool {
	return strings.TrimSpace(p.skillChangePermission) != ""
}

func (p *ChatPage) OpenSkillChangePermissionModal(record ChatPermissionRecord) bool {
	if !isSkillChangePermission(record) {
		return false
	}
	p.skillChangePermission = strings.TrimSpace(record.ID)
	p.skillChangeScroll = 0
	p.skillChangeApproveRect = Rect{}
	p.skillChangeDenyRect = Rect{}
	p.statusLine = "skill change permission active"
	return true
}

func (p *ChatPage) closeSkillChangeModal() {
	p.skillChangePermission = ""
	p.skillChangeScroll = 0
	p.skillChangeApproveRect = Rect{}
	p.skillChangeDenyRect = Rect{}
}

func (p *ChatPage) handleSkillChangeModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.skillChangeModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.skillChangeApproveRect.Contains(x, y):
			p.resolveSkillChangeModal(true)
			return true
		case p.skillChangeDenyRect.Contains(x, y):
			p.resolveSkillChangeModal(false)
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.skillChangeScroll--
		if p.skillChangeScroll < 0 {
			p.skillChangeScroll = 0
		}
		return true
	case buttons&tcell.WheelDown != 0:
		p.skillChangeScroll++
		return true
	default:
		return true
	}
}

func (p *ChatPage) handleSkillChangeModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.skillChangeModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	switch {
	case p.keybinds.Match(ev, KeybindPermissionApprove):
		p.resolveSkillChangeModal(true)
		return true
	case p.keybinds.Match(ev, KeybindPermissionDeny):
		p.resolveSkillChangeModal(false)
		return true
	case p.keybinds.Match(ev, KeybindChatPageUp):
		p.skillChangeScroll -= 6
		if p.skillChangeScroll < 0 {
			p.skillChangeScroll = 0
		}
		return true
	case p.keybinds.Match(ev, KeybindChatPageDown):
		p.skillChangeScroll += 6
		return true
	case p.keybinds.Match(ev, KeybindChatJumpHome):
		p.skillChangeScroll = 0
		return true
	case p.keybinds.Match(ev, KeybindChatJumpEnd):
		p.skillChangeScroll = 1 << 30
		return true
	default:
		return true
	}
}

func (p *ChatPage) drawSkillChangeModal(s tcell.Screen, screen Rect) {
	if !p.skillChangeModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}
	record, ok := p.pendingPermissionByID(p.skillChangePermission)
	if !ok {
		p.closeSkillChangeModal()
		return
	}
	modalW := minInt(116, screen.W-8)
	if modalW < 52 {
		modalW = screen.W - 2
	}
	if modalW < 38 {
		return
	}
	lines := p.skillChangeModalLines(record, maxInt(16, modalW-4))
	bodyH := minInt(len(lines), screen.H-8)
	if bodyH < 4 {
		bodyH = 4
	}
	modalH := bodyH + 6
	if modalH > screen.H-2 {
		modalH = screen.H - 2
	}
	if modalH < 12 {
		modalH = 12
	}
	modal := Rect{X: maxInt(1, screen.X+(screen.W-modalW)/2), Y: maxInt(1, screen.Y+(screen.H-modalH)/2), W: modalW, H: modalH}

	p.skillChangeApproveRect = Rect{}
	p.skillChangeDenyRect = Rect{}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), "Review Skill Change")
	hint := "Enter approve · Esc deny"
	DrawTextRight(s, modal.X+modal.W-2, modal.Y+1, modal.W/2, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W/2))
	subtitle := "Review the before/after preview before allowing this skill change to be written to disk."
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(subtitle, modal.W-4))

	bodyRect := Rect{X: modal.X + 2, Y: modal.Y + 3, W: modal.W - 4, H: modal.H - 6}
	maxScroll := maxInt(0, len(lines)-bodyRect.H)
	if p.skillChangeScroll > maxScroll {
		p.skillChangeScroll = maxScroll
	}
	if p.skillChangeScroll < 0 {
		p.skillChangeScroll = 0
	}
	for row := 0; row < bodyRect.H; row++ {
		idx := p.skillChangeScroll + row
		if idx >= len(lines) {
			break
		}
		DrawTimelineLine(s, bodyRect.X, bodyRect.Y+row, bodyRect.W, lines[idx])
	}
	if maxScroll > 0 {
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.skillChangeScroll+1, maxScroll+1)
		DrawTextRight(s, modal.X+modal.W-2, modal.Y+2, 18, onPanel(p.theme.TextMuted), scrollLabel)
	}

	actionY := modal.Y + modal.H - 2
	actionX := modal.X + 2
	approveLabel, denyLabel := "Enter Approve", "Esc Deny"
	if modal.W < 52 {
		approveLabel, denyLabel = "Enter", "Esc"
	}
	p.skillChangeApproveRect, actionX = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, approveLabel, p.theme.Success)
	p.skillChangeDenyRect, _ = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, denyLabel, p.theme.Error)
	if p.skillChangeApproveRect.W == 0 && p.skillChangeDenyRect.W == 0 {
		DrawText(s, modal.X+2, actionY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W-4))
	}
}

func (p *ChatPage) skillChangeModalLines(record ChatPermissionRecord, width int) []chatRenderLine {
	width = maxInt(16, width)
	manifest := decodePermissionArguments(record.ToolArguments)
	if len(manifest) == 0 {
		return []chatRenderLine{{Text: "Unable to decode skill change preview.", Style: p.theme.Error}}
	}
	change := jsonObject(manifest, "change")
	skill := jsonObject(manifest, "skill")
	operation := strings.TrimSpace(firstNonEmptyToolValue(jsonString(change, "operation"), jsonString(manifest, "action")))
	canonical := strings.TrimSpace(firstNonEmptyToolValue(jsonString(skill, "canonical_name"), jsonString(skill, "name")))
	path := strings.TrimSpace(firstNonEmptyToolValue(jsonString(change, "path"), jsonString(skill, "path")))
	before := strings.TrimSpace(jsonString(change, "before"))
	after := strings.TrimSpace(jsonString(change, "after"))
	summary := strings.TrimSpace(jsonString(manifest, "summary"))

	overview := []chatRenderLine{p.taskLaunchTextLine("This request will modify a skill definition under .agents/skills.", p.theme.Text)}
	if summary != "" {
		overview = append(overview, p.taskLaunchTextLine(summary, p.theme.TextMuted))
	}
	overview = append(overview,
		p.taskLaunchKeyValueLine("operation", emptyValue(operation, "change"), p.theme.Text),
		p.taskLaunchKeyValueLine("skill", emptyValue(canonical, "skill"), p.theme.Text),
		p.taskLaunchKeyValueLine("path", emptyValue(path, "path unavailable"), p.theme.TextMuted),
	)
	controls := []chatRenderLine{
		p.taskLaunchTextLine("Enter writes the change to disk.", p.theme.Success),
		p.taskLaunchTextLine("Esc denies it and leaves files unchanged.", p.theme.Error),
		p.taskLaunchTextLine("PgUp/PgDn/Home/End or mouse wheel scroll the preview.", p.theme.TextMuted),
	}
	sections := []skillChangePreviewSection{
		{
			Title:       "Overview",
			BorderStyle: p.theme.BorderActive,
			TitleStyle:  p.theme.Secondary.Bold(true),
			Lines:       overview,
		},
		{
			Title:       "Before",
			BorderStyle: p.theme.Border,
			TitleStyle:  p.theme.Warning.Bold(true),
			Lines:       p.skillChangeContentLines(before, "File will be created."),
		},
		{
			Title:       "After",
			BorderStyle: p.theme.Border,
			TitleStyle:  p.theme.Success.Bold(true),
			Lines:       p.skillChangeContentLines(after, "File will be deleted."),
		},
		{
			Title:       "Controls",
			BorderStyle: p.theme.Border,
			TitleStyle:  p.theme.Accent.Bold(true),
			Lines:       controls,
		},
	}
	lines := make([]chatRenderLine, 0, len(sections)*8)
	for i, section := range sections {
		if i > 0 {
			lines = append(lines, chatRenderLine{Text: "", Style: p.theme.TextMuted})
		}
		lines = append(lines, p.skillChangeSectionBoxLines(section, width)...)
	}
	return lines
}

func (p *ChatPage) skillChangeContentLines(body, empty string) []chatRenderLine {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" {
		return []chatRenderLine{{Text: empty, Style: p.theme.TextMuted}}
	}
	rows := trimBlankRenderLines(p.renderMarkdownRows(body, p.theme.MarkdownText, p.theme.Text))
	if len(rows) == 0 {
		return []chatRenderLine{{Text: empty, Style: p.theme.TextMuted}}
	}
	return rows
}

func (p *ChatPage) skillChangeSectionBoxLines(section skillChangePreviewSection, width int) []chatRenderLine {
	width = maxInt(12, width)
	innerWidth := maxInt(1, width-4)
	lines := make([]chatRenderLine, 0, len(section.Lines)+2)
	lines = append(lines, p.taskLaunchSectionBorderLine('┌', '┐', section.Title, width, section.BorderStyle, section.TitleStyle))
	if len(section.Lines) == 0 {
		section.Lines = []chatRenderLine{{Text: "", Style: p.theme.Text}}
	}
	for _, line := range section.Lines {
		if chatRenderLineText(line) == "" {
			lines = append(lines, p.taskLaunchSectionContentLine(chatRenderLine{Text: "", Style: p.theme.Text}, innerWidth, section.BorderStyle))
			continue
		}
		for _, wrapped := range wrapRenderLineWithCustomPrefixes("", "", line, innerWidth) {
			lines = append(lines, p.taskLaunchSectionContentLine(wrapped, innerWidth, section.BorderStyle))
		}
	}
	lines = append(lines, p.taskLaunchSectionBorderLine('└', '┘', "", width, section.BorderStyle, section.TitleStyle))
	return lines
}

func (p *ChatPage) resolveSkillChangeModal(approve bool) {
	permissionID := strings.TrimSpace(p.skillChangePermission)
	record, _ := p.pendingPermissionByID(permissionID)
	p.closeSkillChangeModal()
	if permissionID == "" {
		return
	}
	if approve {
		reason := strings.TrimSpace(p.skillChangeApprovalReason(record))
		p.queueResolvePermissionByID(permissionID, "approve", reason)
		return
	}
	p.queueResolvePermissionByID(permissionID, "deny", "")
}

func (p *ChatPage) skillChangeApprovalReason(record ChatPermissionRecord) string {
	payload := decodePermissionArguments(record.ToolArguments)
	if len(payload) == 0 {
		return ""
	}
	change := jsonObject(payload, "change")
	skill := jsonObject(payload, "skill")
	action := strings.TrimSpace(firstNonEmptyToolValue(jsonString(payload, "action"), jsonString(change, "operation")))
	canonical := strings.TrimSpace(jsonString(skill, "canonical_name"))
	name := strings.TrimSpace(firstNonEmptyToolValue(jsonString(skill, "name"), canonical))
	content := jsonString(change, "after")
	if action == "" || (canonical == "" && name == "") {
		return ""
	}
	request := map[string]any{
		"action":  action,
		"confirm": true,
	}
	if canonical != "" {
		request["skill"] = canonical
	} else if name != "" {
		request["skill"] = name
	}
	if action == "create" || action == "update" {
		request["content"] = content
		if rawName := strings.TrimSpace(jsonString(skill, "name")); rawName != "" && rawName != canonical {
			request["name"] = rawName
		}
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return ""
	}
	return string(raw)
}
