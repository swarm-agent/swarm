package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

const (
	chatWorkspaceScopeSelectSession = 0
	chatWorkspaceScopeSelectAddDir  = 1
	chatWorkspaceScopeSelectDeny    = 2

	chatWorkspaceScopeDecisionPathID      = "permission.workspace_scope.decision.v1"
	chatWorkspaceScopeDecisionSessionRead = "session_allow"
	chatWorkspaceScopeDecisionAddDir      = "workspace_add_dir"
)

func isWorkspaceScopePermission(record ChatPermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Requirement), "workspace_scope")
}

func (p *ChatPage) workspaceScopeModalActive() bool {
	return p != nil && p.workspaceScopeVisible
}

func (p *ChatPage) closeWorkspaceScopeModal() {
	if p == nil {
		return
	}
	p.workspaceScopeVisible = false
	p.workspaceScopePermission = ""
	p.workspaceScopeTitle = ""
	p.workspaceScopeSummary = ""
	p.workspaceScopeToolName = ""
	p.workspaceScopeAccessLabel = ""
	p.workspaceScopeRequestedPath = ""
	p.workspaceScopeResolvedPath = ""
	p.workspaceScopeDirectory = ""
	p.workspaceScopeWorkspacePath = ""
	p.workspaceScopeWorkspaceName = ""
	p.workspaceScopeWorkspaceSaved = false
	p.workspaceScopeSelection = chatWorkspaceScopeSelectSession
	p.workspaceScopeScroll = 0
	p.workspaceScopeAllowRect = Rect{}
	p.workspaceScopeAddDirRect = Rect{}
	p.workspaceScopeDenyRect = Rect{}
}

func (p *ChatPage) OpenWorkspaceScopePermissionModal(record ChatPermissionRecord) bool {
	if !isWorkspaceScopePermission(record) {
		return false
	}
	title, summary, toolName, accessLabel, requestedPath, resolvedPath, directory, workspacePath, workspaceName, workspaceSaved := workspaceScopePermissionPayload(record)
	if title == "" {
		title = "Allow read access?"
	}
	if accessLabel == "" {
		accessLabel = workspaceScopeAccessLabelForTool(toolName)
	}
	if directory == "" {
		directory = firstNonEmptyStringUI(resolvedPath, requestedPath)
	}
	if summary == "" {
		if workspaceSaved {
			summary = fmt.Sprintf("Allow %s for this chat session only, or add the directory to the saved workspace permanently.", accessLabel)
		} else {
			summary = fmt.Sprintf("Allow %s for this chat session only.", accessLabel)
		}
	}

	p.workspaceScopeVisible = true
	p.workspaceScopePermission = strings.TrimSpace(record.ID)
	p.workspaceScopeTitle = strings.TrimSpace(title)
	p.workspaceScopeSummary = strings.TrimSpace(summary)
	p.workspaceScopeToolName = strings.TrimSpace(toolName)
	p.workspaceScopeAccessLabel = strings.TrimSpace(accessLabel)
	p.workspaceScopeRequestedPath = strings.TrimSpace(requestedPath)
	p.workspaceScopeResolvedPath = strings.TrimSpace(resolvedPath)
	p.workspaceScopeDirectory = strings.TrimSpace(directory)
	p.workspaceScopeWorkspacePath = strings.TrimSpace(workspacePath)
	p.workspaceScopeWorkspaceName = strings.TrimSpace(workspaceName)
	p.workspaceScopeWorkspaceSaved = workspaceSaved
	p.workspaceScopeSelection = chatWorkspaceScopeSelectSession
	p.workspaceScopeScroll = 0
	p.workspaceScopeAllowRect = Rect{}
	p.workspaceScopeAddDirRect = Rect{}
	p.workspaceScopeDenyRect = Rect{}
	p.statusLine = "workspace access permission active"
	return true
}

func (p *ChatPage) handleWorkspaceScopeModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.workspaceScopeModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons == 0 {
		if selection, ok := p.hoveredWorkspaceScopeSelection(x, y); ok {
			p.workspaceScopeSelection = selection
		} else {
			p.normalizeWorkspaceScopeSelection()
		}
		return true
	}
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.workspaceScopeAllowRect.Contains(x, y):
			p.workspaceScopeSelection = chatWorkspaceScopeSelectSession
			p.confirmWorkspaceScopeSelection()
			return true
		case p.workspaceScopeAddDirRect.Contains(x, y):
			if p.workspaceScopeHasAddDirOption() {
				p.workspaceScopeSelection = chatWorkspaceScopeSelectAddDir
				p.confirmWorkspaceScopeSelection()
			}
			return true
		case p.workspaceScopeDenyRect.Contains(x, y):
			p.workspaceScopeSelection = chatWorkspaceScopeSelectDeny
			p.confirmWorkspaceScopeSelection()
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.shiftWorkspaceScopeScroll(-1)
		return true
	case buttons&tcell.WheelDown != 0:
		p.shiftWorkspaceScopeScroll(1)
		return true
	default:
		return true
	}
}

func (p *ChatPage) handleWorkspaceScopeModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.workspaceScopeModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}

	switch ev.Key() {
	case tcell.KeyEscape:
		p.resolveWorkspaceScopeModal(false)
		return true
	case tcell.KeyEnter:
		p.confirmWorkspaceScopeSelection()
		return true
	case tcell.KeyTab, tcell.KeyRight:
		p.shiftWorkspaceScopeSelection(1)
		return true
	case tcell.KeyBacktab, tcell.KeyLeft:
		p.shiftWorkspaceScopeSelection(-1)
		return true
	case tcell.KeyUp:
		p.shiftWorkspaceScopeScroll(-1)
		return true
	case tcell.KeyDown:
		p.shiftWorkspaceScopeScroll(1)
		return true
	case tcell.KeyPgUp:
		p.shiftWorkspaceScopeScroll(-6)
		return true
	case tcell.KeyPgDn:
		p.shiftWorkspaceScopeScroll(6)
		return true
	case tcell.KeyHome:
		p.workspaceScopeScroll = 0
		return true
	case tcell.KeyEnd:
		p.workspaceScopeScroll = 1 << 30
		return true
	}

	if ev.Key() == tcell.KeyRune {
		switch ev.Rune() {
		case '1':
			p.workspaceScopeSelection = chatWorkspaceScopeSelectSession
			p.confirmWorkspaceScopeSelection()
			return true
		case '2':
			if p.workspaceScopeHasAddDirOption() {
				p.workspaceScopeSelection = chatWorkspaceScopeSelectAddDir
				p.confirmWorkspaceScopeSelection()
			} else {
				p.workspaceScopeSelection = chatWorkspaceScopeSelectDeny
				p.confirmWorkspaceScopeSelection()
			}
			return true
		case '3':
			if p.workspaceScopeHasAddDirOption() {
				p.workspaceScopeSelection = chatWorkspaceScopeSelectDeny
				p.confirmWorkspaceScopeSelection()
				return true
			}
		case 'h', 'H':
			p.shiftWorkspaceScopeSelection(-1)
			return true
		case 'l', 'L':
			p.shiftWorkspaceScopeSelection(1)
			return true
		case 'j', 'J':
			p.shiftWorkspaceScopeScroll(1)
			return true
		case 'k', 'K':
			p.shiftWorkspaceScopeScroll(-1)
			return true
		case 'd', 'D', 'n', 'N':
			p.workspaceScopeSelection = chatWorkspaceScopeSelectDeny
			p.confirmWorkspaceScopeSelection()
			return true
		}
	}
	return true
}

func (p *ChatPage) drawWorkspaceScopeModal(s tcell.Screen, screen Rect) {
	if !p.workspaceScopeModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}

	modalW := screen.W - 8
	if modalW > 104 {
		modalW = 104
	}
	if modalW < 52 {
		modalW = screen.W - 2
	}
	if modalW < 38 {
		return
	}
	bodyLines := p.workspaceScopeModalLines(maxInt(16, modalW-6))
	bodyH := len(bodyLines)
	if bodyH < 4 {
		bodyH = 4
	}
	maxBodyH := screen.H - 10
	if maxBodyH < 4 {
		maxBodyH = 4
	}
	if bodyH > maxBodyH {
		bodyH = maxBodyH
	}
	modalH := bodyH + 8
	if modalH > screen.H-2 {
		modalH = screen.H - 2
	}
	if modalH < 12 {
		modalH = 12
	}
	modal := Rect{
		X: screen.X + (screen.W-modalW)/2,
		Y: screen.Y + (screen.H-modalH)/2,
		W: modalW,
		H: modalH,
	}

	p.workspaceScopeAllowRect = Rect{}
	p.workspaceScopeAddDirRect = Rect{}
	p.workspaceScopeDenyRect = Rect{}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style {
		return styleWithBackgroundFrom(style, p.theme.Panel)
	}
	DrawBox(s, modal, onPanel(p.theme.BorderActive))

	header := strings.TrimSpace(p.workspaceScopeTitle)
	if header == "" {
		header = "Allow read access?"
	}
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), header)

	subtitle := fmt.Sprintf("Tool: %s  ·  scope request", emptyValue(strings.TrimSpace(p.workspaceScopeToolName), "tool"))
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), WrapLine(subtitle, modal.W-4))

	contentRect := Rect{
		X: modal.X + 2,
		Y: modal.Y + 3,
		W: modal.W - 4,
		H: modal.H - 6,
	}
	actionY := modal.Y + modal.H - 2
	contentRect.H = actionY - contentRect.Y - 1
	if contentRect.H < 1 {
		contentRect.H = 1
	}

	maxScroll := maxInt(0, len(bodyLines)-contentRect.H)
	if p.workspaceScopeScroll < 0 {
		p.workspaceScopeScroll = 0
	}
	if p.workspaceScopeScroll > maxScroll {
		p.workspaceScopeScroll = maxScroll
	}
	start := p.workspaceScopeScroll
	end := minInt(len(bodyLines), start+contentRect.H)
	y := contentRect.Y
	for i := start; i < end && y < contentRect.Y+contentRect.H; i++ {
		style := onPanel(p.theme.Text)
		if strings.TrimSpace(bodyLines[i]) == "" {
			style = onPanel(p.theme.TextMuted)
		}
		DrawText(s, contentRect.X, y, contentRect.W, style, bodyLines[i])
		y++
	}

	if maxScroll > 0 {
		scrollText := fmt.Sprintf("line %d/%d", p.workspaceScopeScroll+1, maxScroll+1)
		DrawTextRight(s, modal.X+modal.W-2, modal.Y+1, 18, onPanel(p.theme.TextMuted), scrollText)
	}

	actionX := modal.X + 2
	p.normalizeWorkspaceScopeSelection()
	allowLabel, addDirLabel, denyLabel := "1 Allow This Session", "2 Add To Workspace", "3 Deny"
	if !p.workspaceScopeHasAddDirOption() {
		allowLabel, addDirLabel, denyLabel = "1 Allow This Session", "", "2 Deny"
	}
	if modal.W < 62 {
		allowLabel, addDirLabel, denyLabel = "1 Session", "2 Add", "3 Deny"
		if !p.workspaceScopeHasAddDirOption() {
			allowLabel, addDirLabel, denyLabel = "1 Allow", "", "2 Deny"
		}
	}
	p.workspaceScopeAllowRect, actionX = drawWorkspaceScopeActionButton(s, actionX, actionY, modal.X+modal.W-2, allowLabel, p.workspaceScopeActionButtonStyle(chatWorkspaceScopeSelectSession, p.theme.Success))
	if p.workspaceScopeHasAddDirOption() {
		p.workspaceScopeAddDirRect, actionX = drawWorkspaceScopeActionButton(s, actionX, actionY, modal.X+modal.W-2, addDirLabel, p.workspaceScopeActionButtonStyle(chatWorkspaceScopeSelectAddDir, p.theme.Accent))
	}
	p.workspaceScopeDenyRect, _ = drawWorkspaceScopeActionButton(s, actionX, actionY, modal.X+modal.W-2, denyLabel, p.workspaceScopeActionButtonStyle(chatWorkspaceScopeSelectDeny, p.theme.Error))
	if p.workspaceScopeAllowRect.W == 0 && p.workspaceScopeAddDirRect.W == 0 && p.workspaceScopeDenyRect.W == 0 {
		compactHint := "Enter confirms • Tab switches • Esc denies"
		DrawText(s, modal.X+2, actionY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(compactHint, modal.W-4))
	}
}

func (p *ChatPage) workspaceScopeModalLines(width int) []string {
	if width <= 0 {
		width = 1
	}
	lines := make([]string, 0, 24)
	appendWorkspaceScopeWrapped := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			lines = append(lines, "")
			return
		}
		lines = append(lines, Wrap(text, width)...)
	}

	appendWorkspaceScopeWrapped(p.workspaceScopeSummary)
	lines = append(lines, "")

	appendWorkspaceScopeWrapped(fmt.Sprintf("Requested path: %s", emptyValue(p.workspaceScopeRequestedPath, "-")))
	if resolved := strings.TrimSpace(p.workspaceScopeResolvedPath); resolved != "" && resolved != strings.TrimSpace(p.workspaceScopeRequestedPath) {
		appendWorkspaceScopeWrapped(fmt.Sprintf("Resolved target: %s", resolved))
	}
	if directory := strings.TrimSpace(p.workspaceScopeDirectory); directory != "" {
		appendWorkspaceScopeWrapped(fmt.Sprintf("Session scope root: %s", directory))
	}
	lines = append(lines, "")

	accessLabel := emptyValue(strings.TrimSpace(p.workspaceScopeAccessLabel), "read access")
	appendWorkspaceScopeWrapped(fmt.Sprintf("Allow This Session: this grants %s to the directory above for this chat session only. It does not save or change the workspace. A different chat session will ask again.", accessLabel))
	lines = append(lines, "")

	if p.workspaceScopeHasAddDirOption() {
		workspaceLabel := emptyValue(strings.TrimSpace(p.workspaceScopeWorkspaceName), strings.TrimSpace(p.workspaceScopeWorkspacePath))
		appendWorkspaceScopeWrapped(fmt.Sprintf("Add To Workspace: this links %s into workspace %q. Future access from that workspace will stop asking for permission.", emptyValue(strings.TrimSpace(p.workspaceScopeDirectory), strings.TrimSpace(p.workspaceScopeResolvedPath)), workspaceLabel))
	} else {
		appendWorkspaceScopeWrapped("Permanent add-dir is not available here because this session is not currently using a saved workspace.")
		appendWorkspaceScopeWrapped("If you want permanent permissionless access later, save the workspace first with /workspace save, then use /add-dir.")
	}
	lines = append(lines, "")
	appendWorkspaceScopeWrapped("Enter confirms the selected action. Tab/Shift+Tab or Left/Right switches actions. Esc denies immediately.")
	return lines
}

func (p *ChatPage) workspaceScopeHasAddDirOption() bool {
	return p != nil && p.workspaceScopeWorkspaceSaved
}

func (p *ChatPage) workspaceScopeAvailableSelections() []int {
	if p.workspaceScopeHasAddDirOption() {
		return []int{chatWorkspaceScopeSelectSession, chatWorkspaceScopeSelectAddDir, chatWorkspaceScopeSelectDeny}
	}
	return []int{chatWorkspaceScopeSelectSession, chatWorkspaceScopeSelectDeny}
}

func (p *ChatPage) normalizeWorkspaceScopeSelection() {
	available := p.workspaceScopeAvailableSelections()
	for _, selection := range available {
		if p.workspaceScopeSelection == selection {
			return
		}
	}
	p.workspaceScopeSelection = available[0]
}

func (p *ChatPage) hoveredWorkspaceScopeSelection(x, y int) (int, bool) {
	switch {
	case p.workspaceScopeAllowRect.Contains(x, y):
		return chatWorkspaceScopeSelectSession, true
	case p.workspaceScopeAddDirRect.Contains(x, y) && p.workspaceScopeHasAddDirOption():
		return chatWorkspaceScopeSelectAddDir, true
	case p.workspaceScopeDenyRect.Contains(x, y):
		return chatWorkspaceScopeSelectDeny, true
	default:
		return 0, false
	}
}

func (p *ChatPage) confirmWorkspaceScopeSelection() {
	p.normalizeWorkspaceScopeSelection()
	if p.workspaceScopeSelection == chatWorkspaceScopeSelectDeny {
		p.resolveWorkspaceScopeModal(false)
		return
	}
	p.resolveWorkspaceScopeModal(true)
}

func (p *ChatPage) shiftWorkspaceScopeSelection(delta int) {
	if p == nil || delta == 0 {
		return
	}
	available := p.workspaceScopeAvailableSelections()
	if len(available) == 0 {
		return
	}
	p.normalizeWorkspaceScopeSelection()
	index := 0
	for i, selection := range available {
		if selection == p.workspaceScopeSelection {
			index = i
			break
		}
	}
	index = (index + delta) % len(available)
	if index < 0 {
		index += len(available)
	}
	p.workspaceScopeSelection = available[index]
}

func (p *ChatPage) shiftWorkspaceScopeScroll(delta int) {
	if p == nil || delta == 0 {
		return
	}
	p.workspaceScopeScroll += delta
	if p.workspaceScopeScroll < 0 {
		p.workspaceScopeScroll = 0
	}
}

func (p *ChatPage) resolveWorkspaceScopeModal(approve bool) {
	permissionID := strings.TrimSpace(p.workspaceScopePermission)
	selection := p.workspaceScopeSelection
	if selection == chatWorkspaceScopeSelectAddDir && !p.workspaceScopeHasAddDirOption() {
		selection = chatWorkspaceScopeSelectSession
	}
	p.closeWorkspaceScopeModal()
	if permissionID == "" {
		return
	}
	if !approve {
		p.queueResolvePermissionByID(permissionID, "deny", "")
		p.statusLine = "workspace access denied"
		return
	}
	reason := workspaceScopeDecisionReasonForSelection(selection)
	p.queueResolvePermissionByID(permissionID, "approve", reason)
	if selection == chatWorkspaceScopeSelectAddDir {
		p.statusLine = "workspace add-dir approved"
		return
	}
	p.statusLine = "temporary workspace access approved"
}

func workspaceScopePermissionPayload(record ChatPermissionRecord) (title, summary, toolName, accessLabel, requestedPath, resolvedPath, directory, workspacePath, workspaceName string, workspaceSaved bool) {
	payload := decodePermissionArguments(record.ToolArguments)
	tool := workspaceScopePayloadMap(payload, "tool")
	request := workspaceScopePayloadMap(payload, "request")
	workspace := workspaceScopePayloadMap(payload, "workspace")

	title = mapStringArg(payload, "title")
	summary = mapStringArg(payload, "summary")
	toolName = firstNonEmptyStringUI(mapStringArg(tool, "name"), record.ToolName)
	accessLabel = mapStringArg(request, "access_label")
	requestedPath = mapStringArg(request, "requested_path")
	resolvedPath = mapStringArg(request, "resolved_target_path")
	directory = firstNonEmptyStringUI(mapStringArg(request, "directory_path"), resolvedPath, requestedPath)
	workspacePath = mapStringArg(workspace, "path")
	workspaceName = mapStringArg(workspace, "name")
	workspaceSaved = workspaceScopePayloadBool(workspace["exists"])
	return
}

func workspaceScopePayloadMap(payload map[string]any, key string) map[string]any {
	if payload == nil {
		return nil
	}
	raw, _ := payload[key].(map[string]any)
	return raw
}

func workspaceScopePayloadBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func workspaceScopeAccessLabelForTool(toolName string) string {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read", "list", "grep", "agentic_search":
		return "read access"
	default:
		return "access"
	}
}

func workspaceScopeDecisionReasonForSelection(selection int) string {
	decision := chatWorkspaceScopeDecisionSessionRead
	if selection == chatWorkspaceScopeSelectAddDir {
		decision = chatWorkspaceScopeDecisionAddDir
	}
	payload := map[string]any{
		"path_id":  chatWorkspaceScopeDecisionPathID,
		"decision": decision,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return decision
	}
	return string(encoded)
}

func drawWorkspaceScopeActionButton(s tcell.Screen, x, y, maxX int, label string, style tcell.Style) (Rect, int) {
	text := " " + strings.TrimSpace(label) + " "
	w := utf8.RuneCountInString(text)
	if w <= 0 || x+w > maxX {
		return Rect{}, x
	}
	FillRect(s, Rect{X: x, Y: y, W: w, H: 1}, style)
	DrawText(s, x, y, w, style, text)
	next := x + w + 1
	return Rect{X: x, Y: y, W: w, H: 1}, next
}

func (p *ChatPage) workspaceScopeActionButtonStyle(selection int, tone tcell.Style) tcell.Style {
	if p != nil && p.workspaceScopeSelection == selection {
		return filledButtonStyle(tone)
	}
	fg, _, attrs := p.theme.Text.Decompose()
	_, bg, bgAttrs := p.theme.Element.Decompose()
	return tcell.StyleDefault.Foreground(fg).Background(bg).Attributes(attrs | bgAttrs | tcell.AttrBold)
}

func workspaceScopeApprovedStatus(reason string) string {
	switch workspaceScopeDecisionFromReasonUI(reason) {
	case chatWorkspaceScopeDecisionAddDir:
		return "workspace add-dir approved"
	default:
		return "temporary workspace access approved"
	}
}

func workspaceScopeDecisionFromReasonUI(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return chatWorkspaceScopeDecisionSessionRead
	}
	if strings.EqualFold(trimmed, chatWorkspaceScopeDecisionAddDir) {
		return chatWorkspaceScopeDecisionAddDir
	}
	if strings.EqualFold(trimmed, chatWorkspaceScopeDecisionSessionRead) {
		return chatWorkspaceScopeDecisionSessionRead
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return chatWorkspaceScopeDecisionSessionRead
	}
	decision := strings.TrimSpace(mapStringArg(payload, "decision"))
	if strings.EqualFold(decision, chatWorkspaceScopeDecisionAddDir) {
		return chatWorkspaceScopeDecisionAddDir
	}
	return chatWorkspaceScopeDecisionSessionRead
}

func firstNonEmptyStringUI(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func WrapLine(text string, width int) string {
	lines := Wrap(strings.TrimSpace(text), width)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}
