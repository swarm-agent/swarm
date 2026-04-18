package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

type themeChangePreviewSection struct {
	Title       string
	BorderStyle tcell.Style
	TitleStyle  tcell.Style
	Lines       []chatRenderLine
}

func (p *ChatPage) themeChangeModalActive() bool {
	return strings.TrimSpace(p.themeChangePermission) != ""
}

func (p *ChatPage) OpenThemeChangePermissionModal(record ChatPermissionRecord) bool {
	if !isThemeChangePermission(record) {
		return false
	}
	p.themeChangePermission = strings.TrimSpace(record.ID)
	p.themeChangeScroll = 0
	p.themeChangeApproveRect = Rect{}
	p.themeChangeDenyRect = Rect{}
	p.statusLine = "theme change permission active"
	return true
}

func (p *ChatPage) closeThemeChangeModal() {
	p.themeChangePermission = ""
	p.themeChangeScroll = 0
	p.themeChangeApproveRect = Rect{}
	p.themeChangeDenyRect = Rect{}
}

func (p *ChatPage) handleThemeChangeModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.themeChangeModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.themeChangeApproveRect.Contains(x, y):
			p.resolveThemeChangeModal(true)
			return true
		case p.themeChangeDenyRect.Contains(x, y):
			p.resolveThemeChangeModal(false)
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.themeChangeScroll--
		if p.themeChangeScroll < 0 {
			p.themeChangeScroll = 0
		}
		return true
	case buttons&tcell.WheelDown != 0:
		p.themeChangeScroll++
		return true
	default:
		return true
	}
}

func (p *ChatPage) handleThemeChangeModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.themeChangeModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	switch {
	case p.keybinds.Match(ev, KeybindPermissionApprove):
		p.resolveThemeChangeModal(true)
		return true
	case p.keybinds.Match(ev, KeybindPermissionDeny):
		p.resolveThemeChangeModal(false)
		return true
	case p.keybinds.Match(ev, KeybindChatPageUp):
		p.themeChangeScroll -= 6
		if p.themeChangeScroll < 0 {
			p.themeChangeScroll = 0
		}
		return true
	case p.keybinds.Match(ev, KeybindChatPageDown):
		p.themeChangeScroll += 6
		return true
	case p.keybinds.Match(ev, KeybindChatJumpHome):
		p.themeChangeScroll = 0
		return true
	case p.keybinds.Match(ev, KeybindChatJumpEnd):
		p.themeChangeScroll = 1 << 30
		return true
	default:
		return true
	}
}

func (p *ChatPage) drawThemeChangeModal(s tcell.Screen, screen Rect) {
	if !p.themeChangeModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}
	record, ok := p.pendingPermissionByID(p.themeChangePermission)
	if !ok {
		p.closeThemeChangeModal()
		return
	}
	modalW := minInt(116, screen.W-8)
	if modalW < 52 {
		modalW = screen.W - 2
	}
	if modalW < 38 {
		return
	}
	lines := p.themeChangeModalLines(record, maxInt(16, modalW-4))
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

	p.themeChangeApproveRect = Rect{}
	p.themeChangeDenyRect = Rect{}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), "Review Theme Change")
	hint := "Enter approve · Esc deny"
	DrawTextRight(s, modal.X+modal.W-2, modal.Y+1, modal.W/2, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W/2))
	subtitle := "Review the theme mutation preview before allowing it to change saved UI settings or workspace theme state."
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(subtitle, modal.W-4))

	bodyRect := Rect{X: modal.X + 2, Y: modal.Y + 3, W: modal.W - 4, H: modal.H - 6}
	maxScroll := maxInt(0, len(lines)-bodyRect.H)
	if p.themeChangeScroll > maxScroll {
		p.themeChangeScroll = maxScroll
	}
	if p.themeChangeScroll < 0 {
		p.themeChangeScroll = 0
	}
	for row := 0; row < bodyRect.H; row++ {
		idx := p.themeChangeScroll + row
		if idx >= len(lines) {
			break
		}
		DrawTimelineLine(s, bodyRect.X, bodyRect.Y+row, bodyRect.W, lines[idx])
	}
	if maxScroll > 0 {
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.themeChangeScroll+1, maxScroll+1)
		DrawTextRight(s, modal.X+modal.W-2, modal.Y+2, 18, onPanel(p.theme.TextMuted), scrollLabel)
	}

	actionY := modal.Y + modal.H - 2
	actionX := modal.X + 2
	approveLabel, denyLabel := "Enter Approve", "Esc Deny"
	if modal.W < 52 {
		approveLabel, denyLabel = "Enter", "Esc"
	}
	p.themeChangeApproveRect, actionX = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, approveLabel, p.theme.Success)
	p.themeChangeDenyRect, _ = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, denyLabel, p.theme.Error)
	if p.themeChangeApproveRect.W == 0 && p.themeChangeDenyRect.W == 0 {
		DrawText(s, modal.X+2, actionY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W-4))
	}
}

func (p *ChatPage) themeChangeModalLines(record ChatPermissionRecord, width int) []chatRenderLine {
	width = maxInt(16, width)
	manifest := decodePermissionArguments(record.ToolArguments)
	if len(manifest) == 0 {
		return []chatRenderLine{{Text: "Unable to decode theme change preview.", Style: p.theme.Error}}
	}
	change := jsonObject(manifest, "change")
	action := strings.TrimSpace(firstNonEmptyToolValue(jsonString(manifest, "action"), jsonString(change, "operation")))
	summary := strings.TrimSpace(jsonString(manifest, "summary"))
	themeID := strings.TrimSpace(firstNonEmptyToolValue(jsonString(manifest, "theme_id"), jsonString(manifest, "theme"), jsonString(change, "theme_id"), jsonString(change, "theme")))
	workspacePath := strings.TrimSpace(firstNonEmptyToolValue(jsonString(manifest, "workspace_path"), jsonString(change, "workspace_path")))
	overview := []chatRenderLine{p.taskLaunchTextLine("This request will change persisted theme configuration using the existing UI settings or workspace theme path.", p.theme.Text)}
	if summary != "" {
		overview = append(overview, p.taskLaunchTextLine(summary, p.theme.TextMuted))
	}
	overview = append(overview,
		p.taskLaunchKeyValueLine("action", emptyValue(action, "change"), p.theme.Text),
		p.taskLaunchKeyValueLine("theme", emptyValue(themeID, "theme"), p.theme.Text),
	)
	if workspacePath != "" {
		overview = append(overview, p.taskLaunchKeyValueLine("workspace", workspacePath, p.theme.TextMuted))
	} else {
		overview = append(overview, p.taskLaunchKeyValueLine("scope", "global", p.theme.TextMuted))
	}
	controls := []chatRenderLine{
		p.taskLaunchTextLine("Enter applies the theme mutation.", p.theme.Success),
		p.taskLaunchTextLine("Esc denies it and leaves current settings unchanged.", p.theme.Error),
		p.taskLaunchTextLine("PgUp/PgDn/Home/End or mouse wheel scroll the preview.", p.theme.TextMuted),
	}
	sections := []themeChangePreviewSection{
		{Title: "Overview", BorderStyle: p.theme.BorderActive, TitleStyle: p.theme.Secondary.Bold(true), Lines: overview},
		{Title: "Before", BorderStyle: p.theme.Border, TitleStyle: p.theme.Warning.Bold(true), Lines: p.themeChangeContentLines(change["before"], "No prior value.")},
		{Title: "After", BorderStyle: p.theme.Border, TitleStyle: p.theme.Success.Bold(true), Lines: p.themeChangeContentLines(change["after"], "Value will be cleared.")},
		{Title: "Controls", BorderStyle: p.theme.Border, TitleStyle: p.theme.Accent.Bold(true), Lines: controls},
	}
	lines := make([]chatRenderLine, 0, len(sections)*8)
	for i, section := range sections {
		if i > 0 {
			lines = append(lines, chatRenderLine{Text: "", Style: p.theme.TextMuted})
		}
		lines = append(lines, p.themeChangeSectionBoxLines(section, width)...)
	}
	return lines
}

func (p *ChatPage) themeChangeContentLines(value any, empty string) []chatRenderLine {
	text := p.themeChangeValueText(value)
	if strings.TrimSpace(text) == "" {
		return []chatRenderLine{{Text: empty, Style: p.theme.TextMuted}}
	}
	rows := trimBlankRenderLines(p.renderMarkdownRows(text, p.theme.MarkdownText, p.theme.Text))
	if len(rows) == 0 {
		return []chatRenderLine{{Text: empty, Style: p.theme.TextMuted}}
	}
	return rows
}

func (p *ChatPage) themeChangeValueText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(strings.ReplaceAll(typed, "\r\n", "\n"))
	default:
		raw, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return strings.TrimSpace(fmt.Sprintf("%v", typed))
		}
		return strings.TrimSpace(string(raw))
	}
}

func (p *ChatPage) themeChangeSectionBoxLines(section themeChangePreviewSection, width int) []chatRenderLine {
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

func (p *ChatPage) resolveThemeChangeModal(approve bool) {
	permissionID := strings.TrimSpace(p.themeChangePermission)
	record, _ := p.pendingPermissionByID(permissionID)
	p.closeThemeChangeModal()
	if permissionID == "" {
		return
	}
	if approve {
		reason := strings.TrimSpace(p.themeChangeApprovalReason(record))
		p.queueResolvePermissionByID(permissionID, "approve", reason)
		return
	}
	p.queueResolvePermissionByID(permissionID, "deny", "")
}

func (p *ChatPage) themeChangeApprovalReason(record ChatPermissionRecord) string {
	payload := decodePermissionArguments(record.ToolArguments)
	if len(payload) == 0 {
		return ""
	}
	action := strings.TrimSpace(jsonString(payload, "action"))
	if action == "" {
		action = strings.TrimSpace(jsonString(jsonObject(payload, "change"), "operation"))
	}
	if action == "" {
		return ""
	}
	request := map[string]any{"action": action, "confirm": true}
	if approved, ok := payload["approved_arguments"].(map[string]any); ok && len(approved) > 0 {
		for key, value := range approved {
			request[key] = value
		}
		request["action"] = action
		request["confirm"] = true
	} else {
		if themeID := strings.TrimSpace(firstNonEmptyToolValue(jsonString(payload, "theme_id"), jsonString(payload, "theme"))); themeID != "" {
			request["theme_id"] = themeID
		}
		if workspacePath := strings.TrimSpace(jsonString(payload, "workspace_path")); workspacePath != "" {
			request["workspace_path"] = workspacePath
		}
		if content, ok := payload["content"]; ok {
			request["content"] = content
		}
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return ""
	}
	return string(raw)
}
