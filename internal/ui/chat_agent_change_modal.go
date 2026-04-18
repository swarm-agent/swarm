package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

type agentChangePreviewSection struct {
	Title       string
	BorderStyle tcell.Style
	TitleStyle  tcell.Style
	Lines       []chatRenderLine
}

type agentChangeSummary struct {
	Action             string
	Target             string
	AgentName          string
	Purpose            string
	Mode               string
	Execution          string
	Tools              string
	Status             string
	Model              string
	Thinking           string
	DescriptionPreview string
	PromptPreview      string
	Summary            string
	ChangeRows         []agentChangeField
}

type agentChangeField struct {
	Label string
	Value string
}

type agentChangeModelOption struct {
	Provider  string
	Model     string
	Label     string
	Reasoning bool
}

func isAgentChangePermission(record ChatPermissionRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Requirement), "agent_change")
}

func (p *ChatPage) agentChangeModalActive() bool {
	return strings.TrimSpace(p.agentChangePermission) != ""
}

func (p *ChatPage) OpenAgentChangePermissionModal(record ChatPermissionRecord) bool {
	if !isAgentChangePermission(record) {
		return false
	}
	p.agentChangePermission = strings.TrimSpace(record.ID)
	p.agentChangeScroll = 0
	p.agentChangeApproveRect = Rect{}
	p.agentChangeDenyRect = Rect{}
	p.agentChangeModelPickerVisible = false
	p.agentChangeModelPickerSelected = 0
	p.agentChangeModelPickerProvider = ""
	p.agentChangeOverrideProvider = ""
	p.agentChangeOverrideModel = ""
	p.agentChangeOverrideThinking = ""
	p.statusLine = "agent change permission active"
	return true
}

func (p *ChatPage) closeAgentChangeModal() {
	p.agentChangePermission = ""
	p.agentChangeScroll = 0
	p.agentChangeApproveRect = Rect{}
	p.agentChangeDenyRect = Rect{}
	p.agentChangeModelPickerVisible = false
	p.agentChangeModelPickerSelected = 0
	p.agentChangeModelPickerProvider = ""
	p.agentChangeOverrideProvider = ""
	p.agentChangeOverrideModel = ""
	p.agentChangeOverrideThinking = ""
}

func (p *ChatPage) handleAgentChangeModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.agentChangeModalActive() {
		return false
	}
	x, y := ev.Position()
	buttons := ev.Buttons()
	if buttons&tcell.Button1 != 0 {
		switch {
		case p.agentChangeApproveRect.Contains(x, y):
			p.resolveAgentChangeModal(true)
			return true
		case p.agentChangeDenyRect.Contains(x, y):
			p.resolveAgentChangeModal(false)
			return true
		}
	}
	switch {
	case buttons&tcell.WheelUp != 0:
		p.agentChangeScroll--
		if p.agentChangeScroll < 0 {
			p.agentChangeScroll = 0
		}
		return true
	case buttons&tcell.WheelDown != 0:
		p.agentChangeScroll++
		return true
	default:
		return true
	}
}

func (p *ChatPage) handleAgentChangeModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.agentChangeModalActive() {
		return false
	}
	if p.keybinds == nil {
		p.keybinds = NewDefaultKeyBindings()
	}
	if p.agentChangeModelPickerVisible {
		switch {
		case p.keybinds.Match(ev, KeybindModalClose):
			p.agentChangeModelPickerVisible = false
			p.statusLine = "agent model picker closed"
			return true
		case p.keybinds.Match(ev, KeybindModalFocusLeft):
			p.moveAgentChangeModelPickerProvider(-1)
			return true
		case p.keybinds.Match(ev, KeybindModalFocusRight):
			p.moveAgentChangeModelPickerProvider(1)
			return true
		case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
			p.moveAgentChangeModelPicker(-1)
			return true
		case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
			p.moveAgentChangeModelPicker(1)
			return true
		case p.keybinds.Match(ev, KeybindModalPageUp):
			p.moveAgentChangeModelPicker(-8)
			return true
		case p.keybinds.Match(ev, KeybindModalPageDown):
			p.moveAgentChangeModelPicker(8)
			return true
		case p.keybinds.Match(ev, KeybindModalEnter):
			p.selectAgentChangeModelPickerOption()
			return true
		default:
			return true
		}
	}
	switch {
	case p.keybinds.Match(ev, KeybindPermissionApprove):
		p.resolveAgentChangeModal(true)
		return true
	case p.keybinds.Match(ev, KeybindPermissionDeny):
		p.resolveAgentChangeModal(false)
		return true
	case p.keybinds.Match(ev, KeybindChatPageUp):
		p.agentChangeScroll -= 6
		if p.agentChangeScroll < 0 {
			p.agentChangeScroll = 0
		}
		return true
	case p.keybinds.Match(ev, KeybindChatPageDown):
		p.agentChangeScroll += 6
		return true
	case p.keybinds.Match(ev, KeybindChatJumpHome):
		p.agentChangeScroll = 0
		return true
	case p.keybinds.Match(ev, KeybindChatJumpEnd):
		p.agentChangeScroll = 1 << 30
		return true
	default:
		if ev.Key() == tcell.KeyRune {
			switch ev.Rune() {
			case 'm', 'M':
				p.toggleAgentChangeModelPicker()
				return true
			case 't', 'T':
				p.cycleAgentChangeThinking()
				return true
			}
		}
		return true
	}
}

func (p *ChatPage) drawAgentChangeModal(s tcell.Screen, screen Rect) {
	if !p.agentChangeModalActive() || screen.W < 38 || screen.H < 12 {
		return
	}
	record, ok := p.pendingPermissionByID(p.agentChangePermission)
	if !ok {
		p.closeAgentChangeModal()
		return
	}
	modalW := minInt(116, screen.W-8)
	if modalW < 52 {
		modalW = screen.W - 2
	}
	if modalW < 38 {
		return
	}
	lines := p.agentChangeModalLines(record, maxInt(16, modalW-4))
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

	p.agentChangeApproveRect = Rect{}
	p.agentChangeDenyRect = Rect{}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	DrawBox(s, modal, onPanel(p.theme.BorderActive))
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), "Review Agent Change")
	hint := "Enter approve · Esc deny · m model · t thinking"
	if p.agentChangeModelPickerVisible {
		hint = "Enter select · Esc close · ←/→ provider · ↑/↓ model"
	}
	DrawTextRight(s, modal.X+modal.W-2, modal.Y+1, modal.W/2, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W/2))
	subtitle := "Quick review of the saved agent change before it is applied."
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(subtitle, modal.W-4))

	bodyRect := Rect{X: modal.X + 2, Y: modal.Y + 3, W: modal.W - 4, H: modal.H - 6}
	maxScroll := maxInt(0, len(lines)-bodyRect.H)
	if p.agentChangeScroll > maxScroll {
		p.agentChangeScroll = maxScroll
	}
	if p.agentChangeScroll < 0 {
		p.agentChangeScroll = 0
	}
	for row := 0; row < bodyRect.H; row++ {
		idx := p.agentChangeScroll + row
		if idx >= len(lines) {
			break
		}
		DrawTimelineLine(s, bodyRect.X, bodyRect.Y+row, bodyRect.W, lines[idx])
	}
	if maxScroll > 0 {
		scrollLabel := fmt.Sprintf("scroll %d/%d", p.agentChangeScroll+1, maxScroll+1)
		DrawTextRight(s, modal.X+modal.W-2, modal.Y+2, 18, onPanel(p.theme.TextMuted), scrollLabel)
	}
	if p.agentChangeModelPickerVisible {
		pickerRect := p.agentChangeModelPickerRect(modal)
		p.drawAgentChangeModelPicker(s, pickerRect)
	}

	actionY := modal.Y + modal.H - 2
	actionX := modal.X + 2
	approveLabel, denyLabel := "Enter Approve", "Esc Deny"
	if modal.W < 52 {
		approveLabel, denyLabel = "Enter", "Esc"
	}
	p.agentChangeApproveRect, actionX = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, approveLabel, p.theme.Success)
	p.agentChangeDenyRect, _ = drawPermissionActionButton(s, actionX, actionY, modal.X+modal.W-2, denyLabel, p.theme.Error)
	if p.agentChangeApproveRect.W == 0 && p.agentChangeDenyRect.W == 0 {
		DrawText(s, modal.X+2, actionY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(hint, modal.W-4))
	}
}

func (p *ChatPage) agentChangeModalLines(record ChatPermissionRecord, width int) []chatRenderLine {
	width = maxInt(16, width)
	manifest := decodePermissionArguments(record.ToolArguments)
	if len(manifest) == 0 {
		return []chatRenderLine{{Text: "Unable to decode agent change preview.", Style: p.theme.Error}}
	}
	summary := buildAgentChangeSummary(manifest)
	summary.Model = p.agentChangeDisplayModel(summary.Model)
	summary.Thinking = p.agentChangeDisplayThinking(summary.Thinking)
	overview := []chatRenderLine{p.taskLaunchTextLine(summary.Summary, p.theme.Text)}
	if summary.AgentName != "" {
		overview = append(overview, p.taskLaunchKeyValueLine("agent", "@"+summary.AgentName, p.theme.Text))
	}
	if summary.Target != "" {
		overview = append(overview, p.taskLaunchKeyValueLine("target", summary.Target, p.theme.Text))
	}
	if summary.Purpose != "" {
		overview = append(overview, p.taskLaunchKeyValueLine("purpose", summary.Purpose, p.theme.TextMuted))
	}

	highlights := make([]chatRenderLine, 0, 8)
	appendIf := func(label, value string, style tcell.Style) {
		value = strings.TrimSpace(value)
		if value != "" {
			highlights = append(highlights, p.taskLaunchKeyValueLine(label, value, style))
		}
	}
	appendIf("mode", summary.Mode, p.theme.Text)
	appendIf("execution", summary.Execution, p.theme.Text)
	appendIf("tools", summary.Tools, p.theme.Text)
	appendIf("status", summary.Status, p.theme.Text)
	appendIf("model", summary.Model, p.theme.TextMuted)
	appendIf("thinking", summary.Thinking, p.theme.TextMuted)
	if len(highlights) == 0 {
		highlights = append(highlights, p.taskLaunchTextLine("No headline settings to review.", p.theme.TextMuted))
	}

	changes := make([]chatRenderLine, 0, len(summary.ChangeRows))
	for _, row := range summary.ChangeRows {
		changes = append(changes, p.taskLaunchKeyValueLine(row.Label, row.Value, p.theme.Text))
	}
	if len(changes) == 0 {
		changes = append(changes, p.taskLaunchTextLine("No visible settings change.", p.theme.TextMuted))
	}

	details := make([]chatRenderLine, 0, 4)
	if summary.DescriptionPreview != "" {
		details = append(details, p.taskLaunchKeyValueLine("description", clampEllipsis(summary.DescriptionPreview, maxInt(12, width-20)), p.theme.TextMuted))
	}
	if summary.PromptPreview != "" {
		details = append(details, p.taskLaunchKeyValueLine("prompt", "updated", p.theme.TextMuted))
	}
	if len(details) == 0 {
		details = append(details, p.taskLaunchTextLine("Prompt and description are unchanged or not shown here.", p.theme.TextMuted))
	}

	controls := []chatRenderLine{
		p.taskLaunchTextLine("Enter applies the change.", p.theme.Success),
		p.taskLaunchTextLine("Esc denies it and leaves agent state unchanged.", p.theme.Error),
		p.taskLaunchTextLine("m opens model selection in this modal.", p.theme.Accent),
		p.taskLaunchTextLine("t cycles agent thinking for the selected model.", p.theme.Accent),
		p.taskLaunchTextLine("PgUp/PgDn/Home/End or mouse wheel scroll the preview.", p.theme.TextMuted),
	}
	sections := []agentChangePreviewSection{
		{Title: "Overview", BorderStyle: p.theme.BorderActive, TitleStyle: p.theme.Secondary.Bold(true), Lines: overview},
		{Title: "Highlights", BorderStyle: p.theme.Border, TitleStyle: p.theme.Primary.Bold(true), Lines: highlights},
		{Title: "What changes", BorderStyle: p.theme.Border, TitleStyle: p.theme.Success.Bold(true), Lines: changes},
		{Title: "Details", BorderStyle: p.theme.Border, TitleStyle: p.theme.Accent.Bold(true), Lines: details},
		{Title: "Controls", BorderStyle: p.theme.Border, TitleStyle: p.theme.Accent.Bold(true), Lines: controls},
	}
	lines := make([]chatRenderLine, 0, len(sections)*8)
	for i, section := range sections {
		if i > 0 {
			lines = append(lines, chatRenderLine{Text: "", Style: p.theme.TextMuted})
		}
		lines = append(lines, p.agentChangeSectionBoxLines(section, width)...)
	}
	return lines
}

func buildAgentChangeSummary(payload map[string]any) agentChangeSummary {
	change := jsonObject(payload, "change")
	before := normalizeAgentChangeValue(change["before"])
	after := normalizeAgentChangeValue(change["after"])
	beforeProfile := agentChangeRecord(before)
	afterProfile := agentChangeRecord(after)
	action := strings.TrimSpace(firstNonEmptyToolValue(jsonString(payload, "action"), jsonString(change, "operation")))
	target := strings.TrimSpace(firstNonEmptyToolValue(jsonString(change, "target"), jsonString(payload, "target")))
	purpose := strings.TrimSpace(firstNonEmptyToolValue(jsonString(payload, "purpose"), jsonString(change, "purpose")))
	agent := agentChangePayloadAgent(payload)
	snapshot := afterProfile
	if strings.EqualFold(action, "delete") || snapshot == nil {
		snapshot = beforeProfile
	}
	if snapshot == nil {
		snapshot = agentChangeRecord(agent)
	}
	agentName := strings.TrimSpace(firstNonEmptyToolValue(
		jsonString(agent, "name"),
		jsonString(snapshot, "name"),
		mapStringArg(payload, "agent"),
		mapStringArg(payload, "name"),
	))
	mode := ""
	execution := ""
	tools := ""
	status := ""
	model := ""
	description := ""
	prompt := ""
	if strings.EqualFold(target, "agent_profile") {
		mode = agentChangeMode(snapshot)
		execution = agentChangeExecution(snapshot)
		tools = agentChangeTools(snapshot)
		status = agentChangeStatus(snapshot)
		model = agentChangeModel(snapshot)
		description = strings.TrimSpace(jsonString(snapshot, "description"))
		if afterProfile != nil {
			prompt = strings.TrimSpace(jsonString(afterProfile, "prompt"))
		}
	}
	summaryText := strings.TrimSpace(jsonString(payload, "summary"))
	if summaryText == "" {
		summaryText = agentChangeSummaryText(action, target, agentName, purpose, mode, execution, tools)
	}
	return agentChangeSummary{
		Action:             action,
		Target:             emptyValue(target, "agent state"),
		AgentName:          agentName,
		Purpose:            purpose,
		Mode:               mode,
		Execution:          execution,
		Tools:              tools,
		Status:             status,
		Model:              model,
		Thinking:           normalizeAgentChangeThinkingValue(strings.TrimSpace(jsonString(snapshot, "thinking"))),
		DescriptionPreview: description,
		PromptPreview:      prompt,
		Summary:            summaryText,
		ChangeRows:         agentChangeFields(action, target, before, after, purpose),
	}
}

func agentChangePayloadAgent(payload map[string]any) map[string]any {
	if agent := jsonObject(payload, "agent"); len(agent) > 0 {
		return agent
	}
	if content := jsonObject(payload, "content"); len(content) > 0 {
		return content
	}
	change := jsonObject(payload, "change")
	target := strings.TrimSpace(firstNonEmptyToolValue(jsonString(change, "target"), jsonString(payload, "target")))
	if strings.EqualFold(target, "agent_profile") {
		if after := jsonObject(change, "after"); len(after) > 0 {
			return after
		}
		if before := jsonObject(change, "before"); len(before) > 0 {
			return before
		}
	}
	return nil
}

func agentChangeSummaryText(action, target, agentName, purpose, mode, execution, tools string) string {
	actionLabel := strings.TrimSpace(action)
	if actionLabel == "" {
		actionLabel = "change"
	}
	actionLabel = strings.ToUpper(actionLabel[:1]) + actionLabel[1:]
	if strings.EqualFold(action, "create") {
		actionLabel = "Create"
	} else if strings.EqualFold(action, "update") {
		actionLabel = "Update"
	} else if strings.EqualFold(action, "delete") {
		actionLabel = "Delete"
	}
	if strings.EqualFold(target, "agent_profile") {
		parts := make([]string, 0, 4)
		if agentName != "" {
			parts = append(parts, "@"+agentName)
		}
		if mode != "" {
			parts = append(parts, mode)
		}
		if execution != "" {
			parts = append(parts, execution)
		}
		if tools != "" {
			parts = append(parts, tools)
		}
		if len(parts) == 0 {
			return actionLabel + " agent"
		}
		return actionLabel + " " + strings.Join(parts, " · ")
	}
	if strings.EqualFold(target, "active_primary") {
		if agentName != "" {
			return actionLabel + " active primary → @" + agentName
		}
		return actionLabel + " active primary"
	}
	if strings.EqualFold(target, "active_subagent") {
		label := "subagent router"
		if purpose != "" {
			label = purpose + " router"
		}
		if agentName != "" {
			return actionLabel + " " + label + " → @" + agentName
		}
		return actionLabel + " " + label
	}
	if agentName != "" {
		return actionLabel + " @" + agentName
	}
	return actionLabel + " agent state"
}

func agentChangeFields(action, target string, before, after any, purpose string) []agentChangeField {
	if !strings.EqualFold(target, "agent_profile") {
		return agentChangeNonProfileFields(action, target, before, after, purpose)
	}
	beforeProfile := agentChangeRecord(before)
	afterProfile := agentChangeRecord(after)
	if strings.EqualFold(action, "create") {
		rows := []agentChangeField{{Label: "result", Value: "A new saved agent profile will be created."}}
		if afterProfile != nil && strings.TrimSpace(jsonString(afterProfile, "description")) != "" {
			rows = append(rows, agentChangeField{Label: "description", Value: "set"})
		}
		if afterProfile != nil && strings.TrimSpace(jsonString(afterProfile, "prompt")) != "" {
			rows = append(rows, agentChangeField{Label: "prompt", Value: "set"})
		}
		return rows
	}
	if strings.EqualFold(action, "delete") {
		return []agentChangeField{{Label: "result", Value: "This saved agent profile will be deleted."}}
	}
	rows := make([]agentChangeField, 0, 8)
	rows = appendAgentChangeField(rows, "mode", agentChangeMode(beforeProfile), agentChangeMode(afterProfile))
	rows = appendAgentChangeField(rows, "execution", agentChangeExecution(beforeProfile), agentChangeExecution(afterProfile))
	rows = appendAgentChangeField(rows, "tools", agentChangeTools(beforeProfile), agentChangeTools(afterProfile))
	rows = appendAgentChangeField(rows, "status", agentChangeStatus(beforeProfile), agentChangeStatus(afterProfile))
	rows = appendAgentChangeField(rows, "model", agentChangeModel(beforeProfile), agentChangeModel(afterProfile))
	rows = appendAgentChangeField(rows, "thinking", strings.TrimSpace(jsonString(beforeProfile, "thinking")), strings.TrimSpace(jsonString(afterProfile, "thinking")))
	rows = appendAgentTextField(rows, "description", strings.TrimSpace(jsonString(beforeProfile, "description")), strings.TrimSpace(jsonString(afterProfile, "description")))
	rows = appendAgentTextField(rows, "prompt", strings.TrimSpace(jsonString(beforeProfile, "prompt")), strings.TrimSpace(jsonString(afterProfile, "prompt")))
	if len(rows) == 0 {
		rows = append(rows, agentChangeField{Label: "result", Value: "Saved agent settings stay effectively the same."})
	}
	return rows
}

func agentChangeNonProfileFields(action, target string, before, after any, purpose string) []agentChangeField {
	beforeText := strings.TrimSpace(agentChangeValueText(before))
	afterText := strings.TrimSpace(agentChangeValueText(after))
	rows := make([]agentChangeField, 0, 3)
	if strings.EqualFold(target, "active_primary") {
		rows = append(rows, agentChangeField{Label: "primary", Value: agentChangeJoinChange(beforeText, afterText)})
		return rows
	}
	if strings.EqualFold(target, "active_subagent") {
		rows = append(rows, agentChangeField{Label: "purpose", Value: emptyValue(purpose, "subagent router")})
		if strings.EqualFold(action, "remove_active_subagent") {
			value := "assignment will be cleared"
			if beforeText != "" {
				value = beforeText + " will be cleared"
			}
			rows = append(rows, agentChangeField{Label: "result", Value: value})
			return rows
		}
		rows = append(rows, agentChangeField{Label: "assignment", Value: agentChangeJoinChange(beforeText, afterText)})
		return rows
	}
	rows = append(rows,
		agentChangeField{Label: "before", Value: emptyValue(beforeText, "No prior state")},
		agentChangeField{Label: "after", Value: emptyValue(afterText, "No resulting state")},
	)
	return rows
}

func appendAgentChangeField(rows []agentChangeField, label, before, after string) []agentChangeField {
	if strings.TrimSpace(before) == strings.TrimSpace(after) {
		return rows
	}
	return append(rows, agentChangeField{Label: label, Value: agentChangeJoinChange(before, after)})
}

func appendAgentTextField(rows []agentChangeField, label, before, after string) []agentChangeField {
	before = strings.TrimSpace(before)
	after = strings.TrimSpace(after)
	if before == after {
		return rows
	}
	value := "updated"
	if before == "" && after != "" {
		value = "set"
	} else if before != "" && after == "" {
		value = "cleared"
	}
	return append(rows, agentChangeField{Label: label, Value: value})
}

func agentChangeJoinChange(before, after string) string {
	before = strings.TrimSpace(before)
	after = strings.TrimSpace(after)
	if before == "" {
		before = "unset"
	}
	if after == "" {
		after = "unset"
	}
	if before == after {
		return after
	}
	return before + " → " + after
}

func agentChangeMode(profile map[string]any) string {
	if len(profile) == 0 {
		return ""
	}
	return emptyValue(strings.TrimSpace(jsonString(profile, "mode")), "unset")
}

func agentChangeExecution(profile map[string]any) string {
	if len(profile) == 0 {
		return ""
	}
	if jsonBool(profile, "exit_plan_mode_enabled") {
		return "plan → auto"
	}
	return emptyValue(strings.TrimSpace(jsonString(profile, "execution_setting")), "unset")
}

func agentChangeTools(profile map[string]any) string {
	if len(profile) == 0 {
		return ""
	}
	scope := jsonObject(profile, "tool_scope")
	allow := jsonStringSlice(scope, "allow_tools")
	deny := jsonStringSlice(scope, "deny_tools")
	bashPrefixes := jsonStringSlice(scope, "bash_prefixes")
	preset := strings.TrimSpace(jsonString(scope, "preset"))
	switch {
	case len(allow) > 0:
		return "limited to " + strings.Join(allow, ", ")
	case len(deny) > 0:
		return "removed: " + strings.Join(deny, ", ")
	case len(bashPrefixes) > 0:
		return "bash restricted"
	case preset != "":
		return "preset " + preset
	default:
		return "all enabled"
	}
}

func agentChangeStatus(profile map[string]any) string {
	if len(profile) == 0 {
		return ""
	}
	if jsonBool(profile, "enabled") {
		return "enabled"
	}
	return "disabled"
}

func agentChangeModel(profile map[string]any) string {
	if len(profile) == 0 {
		return ""
	}
	provider := strings.TrimSpace(jsonString(profile, "provider"))
	modelName := strings.TrimSpace(jsonString(profile, "model"))
	if provider == "" && modelName == "" {
		return ""
	}
	parts := make([]string, 0, 2)
	if provider != "" {
		parts = append(parts, provider)
	}
	if modelName != "" {
		parts = append(parts, model.DisplayModelName(provider, modelName))
	}
	return strings.Join(parts, " / ")
}

func normalizeAgentChangeValue(value any) any {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return ""
		}
		if (strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}")) || (strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]")) {
			var decoded any
			if err := json.Unmarshal([]byte(text), &decoded); err == nil {
				return normalizeAgentChangeValue(decoded)
			}
		}
		return text
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeAgentChangeValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, normalizeAgentChangeValue(item))
		}
		return out
	default:
		return value
	}
}

func agentChangeRecord(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return nil
}

func agentChangeValueText(value any) string {
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

func (p *ChatPage) agentChangeSectionBoxLines(section agentChangePreviewSection, width int) []chatRenderLine {
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

func (p *ChatPage) resolveAgentChangeModal(approve bool) {
	permissionID := strings.TrimSpace(p.agentChangePermission)
	record, _ := p.pendingPermissionByID(permissionID)
	approvedArguments := ""
	if approve {
		approvedArguments = strings.TrimSpace(p.agentChangeApprovedArguments(record))
		permissionParserDebugf("agent change approve permission=%s approved_args_chars=%d preview=%q", permissionID, len(approvedArguments), permissionDebugPreview(approvedArguments, 180))
	}
	p.closeAgentChangeModal()
	if permissionID == "" {
		return
	}
	if approve {
		p.queueResolvePermissionByID(permissionID, "approve", "", approvedArguments)
		return
	}
	p.queueResolvePermissionByID(permissionID, "deny", "", "")
}

func (p *ChatPage) agentChangeApprovedArguments(record ChatPermissionRecord) string {
	request := buildAgentChangeApprovalRequest(record)
	if len(request) == 0 {
		return ""
	}
	p.applyAgentChangeSelectedModel(request)
	raw, err := json.Marshal(request)
	if err != nil {
		return ""
	}
	provider, modelName := agentChangeRequestModelIdentity(request)
	permissionParserDebugf("agent change payload built permission=%s provider=%s model=%s thinking=%s chars=%d", strings.TrimSpace(record.ID), provider, modelName, strings.TrimSpace(jsonString(request, "thinking")), len(raw))
	return string(raw)
}

func buildAgentChangeApprovalRequest(record ChatPermissionRecord) map[string]any {
	payload := decodePermissionArguments(record.ToolArguments)
	if len(payload) == 0 {
		return nil
	}
	change := jsonObject(payload, "change")
	action := strings.TrimSpace(firstNonEmptyToolValue(jsonString(payload, "action"), jsonString(change, "operation")))
	if action == "" {
		return nil
	}
	request := map[string]any{
		"action":  action,
		"confirm": true,
	}
	if approved := jsonObject(payload, "approved_arguments"); len(approved) > 0 {
		for key, value := range approved {
			request[key] = cloneAgentChangeValue(value)
		}
		request["action"] = action
		request["confirm"] = true
	} else {
		agent := agentChangePayloadAgent(payload)
		if name := strings.TrimSpace(firstNonEmptyToolValue(jsonString(agent, "name"), mapStringArg(payload, "agent"), mapStringArg(payload, "name"))); name != "" {
			request["agent"] = name
			request["name"] = name
		}
		if purpose := strings.TrimSpace(firstNonEmptyToolValue(jsonString(payload, "purpose"), jsonString(change, "purpose"))); purpose != "" {
			request["purpose"] = purpose
		}
		if action == "create" || action == "update" {
			if after := jsonObject(change, "after"); len(after) > 0 {
				request["content"] = cloneAgentChangeMap(after)
			}
		}
	}
	return request
}

func normalizeAgentChangeModelOptions(models []ModelsModalEntry) []agentChangeModelOption {
	if len(models) == 0 {
		return nil
	}
	options := make([]agentChangeModelOption, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, entry := range models {
		provider := strings.ToLower(strings.TrimSpace(entry.Provider))
		modelName := strings.TrimSpace(entry.Model)
		if provider == "" || modelName == "" {
			continue
		}
		key := provider + "\x00" + modelName
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		options = append(options, agentChangeModelOption{
			Provider:  provider,
			Model:     modelName,
			Label:     provider + " / " + model.DisplayModelName(provider, modelName),
			Reasoning: entry.Reasoning,
		})
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].Provider != options[j].Provider {
			return options[i].Provider < options[j].Provider
		}
		return strings.ToLower(options[i].Model) < strings.ToLower(options[j].Model)
	})
	return options
}

func (p *ChatPage) agentChangeDisplayModel(current string) string {
	if provider := strings.TrimSpace(p.agentChangeOverrideProvider); provider != "" {
		if modelName := strings.TrimSpace(p.agentChangeOverrideModel); modelName != "" {
			return provider + " / " + model.DisplayModelName(provider, modelName) + " (selected)"
		}
	}
	if current = strings.TrimSpace(current); current != "" {
		return current
	}
	provider := strings.TrimSpace(p.modelProvider)
	modelName := strings.TrimSpace(p.modelName)
	if provider != "" && modelName != "" {
		return provider + " / " + model.DisplayModelName(provider, modelName) + " (inherited)"
	}
	return "unset"
}

func (p *ChatPage) toggleAgentChangeModelPicker() {
	if len(p.agentChangeModelOptions) == 0 {
		p.statusLine = "no model options available"
		return
	}
	p.agentChangeModelPickerVisible = !p.agentChangeModelPickerVisible
	if p.agentChangeModelPickerVisible {
		provider, _ := p.agentChangeSelectedModelIdentity()
		p.setAgentChangeModelPickerProvider(provider)
		if strings.TrimSpace(p.agentChangeModelPickerProvider) == "" {
			p.statusLine = "agent model picker active"
		} else {
			p.statusLine = "agent model picker active · provider " + p.agentChangeModelPickerProvider
		}
		return
	}
	p.statusLine = "agent model picker closed"
}

func (p *ChatPage) agentChangeSelectedModelIdentity() (string, string) {
	provider := strings.TrimSpace(p.agentChangeOverrideProvider)
	modelName := strings.TrimSpace(p.agentChangeOverrideModel)
	if provider != "" && modelName != "" {
		return provider, modelName
	}
	if requestedProvider, requestedModel := p.agentChangeRequestedModelIdentity(); requestedProvider != "" && requestedModel != "" {
		return requestedProvider, requestedModel
	}
	return strings.TrimSpace(p.modelProvider), strings.TrimSpace(p.modelName)
}

func (p *ChatPage) agentChangeRequestedModelIdentity() (string, string) {
	record, ok := p.pendingPermissionByID(strings.TrimSpace(p.agentChangePermission))
	if !ok {
		return "", ""
	}
	return agentChangeRequestModelIdentity(buildAgentChangeApprovalRequest(record))
}

func (p *ChatPage) agentChangeSelectedThinking() string {
	if thinking := normalizeAgentChangeThinkingValue(p.agentChangeOverrideThinking); thinking != "" {
		return thinking
	}
	if thinking := p.agentChangePayloadThinking(); thinking != "" {
		return thinking
	}
	return p.agentChangeDefaultThinking()
}

func (p *ChatPage) agentChangePayloadThinking() string {
	record, ok := p.pendingPermissionByID(strings.TrimSpace(p.agentChangePermission))
	if !ok {
		return ""
	}
	manifest := decodePermissionArguments(record.ToolArguments)
	if len(manifest) == 0 {
		return ""
	}
	return normalizeAgentChangeThinkingValue(buildAgentChangeSummary(manifest).Thinking)
}

func (p *ChatPage) agentChangeDefaultThinking() string {
	if thinking := normalizeAgentChangeThinkingValue(p.thinkingLevel); thinking != "" {
		return thinking
	}
	return "medium"
}

func normalizeAgentChangeThinkingValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "off", "low", "medium", "high", "xhigh":
		return value
	case "x-high":
		return "xhigh"
	default:
		return ""
	}
}

func reasoningLevelsForAgentChange(reasoning bool) []string {
	if !reasoning {
		return nil
	}
	return []string{"low", "medium", "high", "xhigh"}
}

func containsAgentChangeThinkingOption(options []string, target string) bool {
	target = normalizeAgentChangeThinkingValue(target)
	if target == "" {
		return false
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option), target) {
			return true
		}
	}
	return false
}

func agentChangeThinkingOptionIndex(options []string, target string) (int, bool) {
	target = normalizeAgentChangeThinkingValue(target)
	for i, option := range options {
		if strings.EqualFold(strings.TrimSpace(option), target) {
			return i, true
		}
	}
	return 0, false
}

func (p *ChatPage) agentChangeThinkingOptionsForModel(provider, modelName string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelName = strings.TrimSpace(modelName)
	options := []string{"off"}
	for _, option := range p.agentChangeModelOptions {
		if !strings.EqualFold(strings.TrimSpace(option.Provider), provider) || !strings.EqualFold(strings.TrimSpace(option.Model), modelName) {
			continue
		}
		for _, level := range reasoningLevelsForAgentChange(option.Reasoning) {
			if !containsAgentChangeThinkingOption(options, level) {
				options = append(options, level)
			}
		}
		break
	}
	return options
}

func (p *ChatPage) cycleAgentChangeThinking() {
	provider, modelName := p.agentChangeSelectedModelIdentity()
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(modelName) == "" {
		p.statusLine = "select a model before changing thinking"
		return
	}
	options := p.agentChangeThinkingOptionsForModel(provider, modelName)
	if len(options) == 0 {
		p.statusLine = "no thinking options available"
		return
	}
	if normalizeAgentChangeThinkingValue(p.agentChangeOverrideThinking) == "" {
		if current := p.agentChangePayloadThinking(); current == "" {
			thinking := p.agentChangeDefaultThinking()
			if !containsAgentChangeThinkingOption(options, thinking) {
				thinking = options[0]
			}
			p.agentChangeOverrideThinking = thinking
			p.statusLine = "agent thinking: " + thinking
			return
		}
	}
	current := p.agentChangeSelectedThinking()
	idx := 0
	if matched, ok := agentChangeThinkingOptionIndex(options, current); ok {
		idx = (matched + 1) % len(options)
	} else if matchedMedium, ok := agentChangeThinkingOptionIndex(options, "medium"); ok {
		idx = matchedMedium
	}
	thinking := options[idx]
	p.agentChangeOverrideThinking = thinking
	p.statusLine = "agent thinking: " + thinking
}

func (p *ChatPage) agentChangeDisplayThinking(current string) string {
	selected := p.agentChangeSelectedThinking()
	if selected != "" {
		if normalizeAgentChangeThinkingValue(p.agentChangeOverrideThinking) != "" {
			return selected + " (selected)"
		}
		return selected + " (default)"
	}
	if current = normalizeAgentChangeThinkingValue(current); current != "" {
		return current
	}
	return "unset"
}

func (p *ChatPage) agentChangeModelPickerProviders() []string {
	if len(p.agentChangeModelOptions) == 0 {
		return nil
	}
	providers := make([]string, 0, len(p.agentChangeModelOptions))
	seen := make(map[string]struct{}, len(p.agentChangeModelOptions))
	for _, option := range p.agentChangeModelOptions {
		provider := strings.ToLower(strings.TrimSpace(option.Provider))
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	return providers
}

func (p *ChatPage) setAgentChangeModelPickerProvider(provider string) {
	providers := p.agentChangeModelPickerProviders()
	if len(providers) == 0 {
		p.agentChangeModelPickerProvider = ""
		p.agentChangeModelPickerSelected = 0
		return
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider, _ = p.agentChangeSelectedModelIdentity()
		provider = strings.ToLower(strings.TrimSpace(provider))
	}
	matched := false
	for _, candidate := range providers {
		if strings.EqualFold(candidate, provider) {
			provider = candidate
			matched = true
			break
		}
	}
	if !matched {
		provider = providers[0]
	}
	p.agentChangeModelPickerProvider = provider
	p.agentChangeModelPickerSelected = p.agentChangeSelectedModelIndex()
	options := p.agentChangeModelPickerCurrentOptions()
	if len(options) == 0 {
		p.agentChangeModelPickerSelected = 0
		return
	}
	if p.agentChangeModelPickerSelected < 0 {
		p.agentChangeModelPickerSelected = 0
	}
	if p.agentChangeModelPickerSelected >= len(options) {
		p.agentChangeModelPickerSelected = len(options) - 1
	}
}

func (p *ChatPage) agentChangeModelPickerOptionsForProvider(provider string) []agentChangeModelOption {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	options := make([]agentChangeModelOption, 0, len(p.agentChangeModelOptions))
	for _, option := range p.agentChangeModelOptions {
		if strings.EqualFold(strings.TrimSpace(option.Provider), provider) {
			options = append(options, option)
		}
	}
	return options
}

func (p *ChatPage) agentChangeModelPickerCurrentOptions() []agentChangeModelOption {
	provider := strings.TrimSpace(p.agentChangeModelPickerProvider)
	if provider == "" {
		providers := p.agentChangeModelPickerProviders()
		if len(providers) == 0 {
			return nil
		}
		provider = providers[0]
	}
	return p.agentChangeModelPickerOptionsForProvider(provider)
}

func (p *ChatPage) agentChangeSelectedModelIndex() int {
	currentProvider := strings.TrimSpace(p.agentChangeModelPickerProvider)
	if currentProvider == "" {
		currentProvider, _ = p.agentChangeSelectedModelIdentity()
	}
	currentProvider = strings.ToLower(strings.TrimSpace(currentProvider))
	if currentProvider == "" {
		return -1
	}
	selectedProvider, modelName := p.agentChangeSelectedModelIdentity()
	if !strings.EqualFold(strings.TrimSpace(selectedProvider), currentProvider) || strings.TrimSpace(modelName) == "" {
		return -1
	}
	options := p.agentChangeModelPickerOptionsForProvider(currentProvider)
	for i, option := range options {
		if strings.EqualFold(option.Model, modelName) {
			return i
		}
	}
	return -1
}

func (p *ChatPage) moveAgentChangeModelPickerProvider(delta int) {
	providers := p.agentChangeModelPickerProviders()
	if len(providers) == 0 || delta == 0 {
		return
	}
	currentProvider := strings.TrimSpace(p.agentChangeModelPickerProvider)
	idx := 0
	for i, provider := range providers {
		if strings.EqualFold(provider, currentProvider) {
			idx = i
			break
		}
	}
	idx = (idx + delta) % len(providers)
	if idx < 0 {
		idx += len(providers)
	}
	p.setAgentChangeModelPickerProvider(providers[idx])
	options := p.agentChangeModelPickerCurrentOptions()
	suffix := "models"
	if len(options) == 1 {
		suffix = "model"
	}
	p.statusLine = fmt.Sprintf("agent provider %d/%d: %s (%d %s)", idx+1, len(providers), providers[idx], len(options), suffix)
}

func (p *ChatPage) moveAgentChangeModelPicker(delta int) {
	options := p.agentChangeModelPickerCurrentOptions()
	if len(options) == 0 {
		return
	}
	idx := p.agentChangeModelPickerSelected + delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(options) {
		idx = len(options) - 1
	}
	p.agentChangeModelPickerSelected = idx
	option := options[idx]
	p.statusLine = "agent model: " + option.Provider + " / " + model.DisplayModelName(option.Provider, option.Model)
}

func (p *ChatPage) selectAgentChangeModelPickerOption() {
	options := p.agentChangeModelPickerCurrentOptions()
	if len(options) == 0 {
		return
	}
	if p.agentChangeModelPickerSelected < 0 || p.agentChangeModelPickerSelected >= len(options) {
		p.agentChangeModelPickerSelected = 0
	}
	option := options[p.agentChangeModelPickerSelected]
	p.agentChangeModelPickerProvider = option.Provider
	p.agentChangeOverrideProvider = option.Provider
	p.agentChangeOverrideModel = option.Model
	p.agentChangeModelPickerVisible = false
	p.statusLine = "agent model selected: " + option.Label
}

func (p *ChatPage) agentChangeModelPickerRect(modal Rect) Rect {
	width := minInt(72, modal.W-8)
	if width < 40 {
		width = modal.W - 4
	}
	if width > modal.W-4 {
		width = modal.W - 4
	}
	height := minInt(16, modal.H-8)
	if height < 8 {
		height = modal.H - 4
	}
	if height < 6 {
		height = 6
	}
	if height > modal.H-3 {
		height = modal.H - 3
	}
	return Rect{
		X: modal.X + modal.W - width - 2,
		Y: modal.Y + 3,
		W: width,
		H: height,
	}
}

func (p *ChatPage) drawAgentChangeModelPicker(s tcell.Screen, rect Rect) {
	if rect.W < 18 || rect.H < 6 {
		return
	}
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	FillRect(s, rect, p.theme.Panel)
	DrawBox(s, rect, onPanel(p.theme.BorderActive))

	providers := p.agentChangeModelPickerProviders()
	currentProvider := strings.TrimSpace(p.agentChangeModelPickerProvider)
	if currentProvider == "" && len(providers) > 0 {
		currentProvider = providers[0]
		p.agentChangeModelPickerProvider = currentProvider
	}
	options := p.agentChangeModelPickerCurrentOptions()
	if len(options) == 0 {
		p.agentChangeModelPickerSelected = 0
	} else {
		if p.agentChangeModelPickerSelected < 0 {
			p.agentChangeModelPickerSelected = 0
		}
		if p.agentChangeModelPickerSelected >= len(options) {
			p.agentChangeModelPickerSelected = len(options) - 1
		}
	}
	providerIndex := 0
	for i, provider := range providers {
		if strings.EqualFold(provider, currentProvider) {
			providerIndex = i
			break
		}
	}

	DrawText(s, rect.X+2, rect.Y, rect.W-4, onPanel(p.theme.Accent.Bold(true)), " Select model ")
	status := "←/→ switch provider · ↑/↓ pick model"
	if len(providers) > 0 {
		status = fmt.Sprintf("←/→ provider %d/%d · ↑/↓ %d model", providerIndex+1, len(providers), len(options))
		if len(options) != 1 {
			status += "s"
		}
	}
	DrawTextRight(s, rect.X+rect.W-2, rect.Y, rect.W/2, onPanel(p.theme.TextMuted), clampEllipsis(status, rect.W/2))

	content := Rect{X: rect.X + 1, Y: rect.Y + 1, W: rect.W - 2, H: rect.H - 2}
	if content.W < 10 || content.H < 2 {
		return
	}
	if content.W < 46 || content.H < 8 {
		providerLine := p.agentChangeModelPickerProvidersLine(providers, currentProvider)
		DrawText(s, content.X, content.Y, content.W, onPanel(p.theme.TextMuted), clampEllipsis(providerLine, content.W))
		modelHeader := "Models"
		if currentProvider != "" {
			modelHeader += " · " + currentProvider
		}
		if content.H > 1 {
			DrawText(s, content.X, content.Y+1, content.W, onPanel(p.theme.TextMuted), clampEllipsis(modelHeader, content.W))
		}
		listRect := Rect{X: content.X, Y: content.Y + 2, W: content.W, H: content.H - 2}
		p.drawAgentChangeModelPickerOptions(s, listRect, options, false)
		return
	}

	providerW := maxInt(16, content.W/3)
	if providerW > 24 {
		providerW = 24
	}
	if providerW > content.W/2 {
		providerW = content.W / 2
	}
	providerRect := Rect{X: content.X, Y: content.Y, W: providerW, H: content.H}
	modelRect := Rect{X: providerRect.X + providerRect.W + 1, Y: content.Y, W: content.W - providerRect.W - 1, H: content.H}
	if modelRect.W < 16 {
		providerLine := p.agentChangeModelPickerProvidersLine(providers, currentProvider)
		DrawText(s, content.X, content.Y, content.W, onPanel(p.theme.TextMuted), clampEllipsis(providerLine, content.W))
		listRect := Rect{X: content.X, Y: content.Y + 1, W: content.W, H: content.H - 1}
		p.drawAgentChangeModelPickerOptions(s, listRect, options, true)
		return
	}

	DrawBox(s, providerRect, onPanel(p.theme.Border))
	DrawText(s, providerRect.X+2, providerRect.Y, providerRect.W-4, onPanel(p.theme.TextMuted), "Providers")
	providerRows := providerRect.H - 2
	if len(providers) == 0 || providerRows <= 0 {
		DrawText(s, providerRect.X+1, providerRect.Y+1, providerRect.W-2, onPanel(p.theme.Warning), "no providers")
	} else {
		start := 0
		if providerIndex >= providerRows {
			start = providerIndex - providerRows + 1
		}
		for row := 0; row < providerRows; row++ {
			idx := start + row
			if idx >= len(providers) {
				break
			}
			provider := providers[idx]
			style := onPanel(p.theme.TextMuted)
			prefix := "  "
			if idx == providerIndex {
				style = onPanel(p.theme.Accent.Bold(true))
				prefix = "> "
			}
			DrawText(s, providerRect.X+1, providerRect.Y+1+row, providerRect.W-2, style, clampEllipsis(prefix+provider, providerRect.W-2))
		}
	}

	DrawBox(s, modelRect, onPanel(p.theme.BorderActive))
	modelHeader := "Models"
	if currentProvider != "" {
		modelHeader += " · " + currentProvider
	}
	DrawText(s, modelRect.X+2, modelRect.Y, modelRect.W-4, onPanel(p.theme.TextMuted), clampEllipsis(modelHeader, modelRect.W-4))
	listRect := Rect{X: modelRect.X + 1, Y: modelRect.Y + 1, W: modelRect.W - 2, H: modelRect.H - 2}
	p.drawAgentChangeModelPickerOptions(s, listRect, options, false)
}

func (p *ChatPage) agentChangeModelPickerProvidersLine(providers []string, currentProvider string) string {
	if len(providers) == 0 {
		return "providers: none"
	}
	parts := make([]string, 0, len(providers))
	for _, provider := range providers {
		label := provider
		if strings.EqualFold(provider, currentProvider) {
			label = "[" + provider + "]"
		}
		parts = append(parts, label)
	}
	return "providers: " + strings.Join(parts, " · ")
}

func (p *ChatPage) drawAgentChangeModelPickerOptions(s tcell.Screen, rect Rect, options []agentChangeModelOption, includeProvider bool) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	onPanel := func(style tcell.Style) tcell.Style { return styleWithBackgroundFrom(style, p.theme.Panel) }
	if len(options) == 0 {
		DrawText(s, rect.X, rect.Y, rect.W, onPanel(p.theme.Warning), "no models")
		return
	}
	selected := p.agentChangeModelPickerSelected
	if selected < 0 {
		selected = 0
	}
	if selected >= len(options) {
		selected = len(options) - 1
	}
	start := 0
	if selected >= rect.H {
		start = selected - rect.H + 1
	}
	for row := 0; row < rect.H; row++ {
		idx := start + row
		if idx >= len(options) {
			break
		}
		option := options[idx]
		style := onPanel(p.theme.Text)
		prefix := "  "
		if idx == selected {
			style = onPanel(p.theme.Accent.Bold(true))
			prefix = "▶ "
		}
		label := model.DisplayModelName(option.Provider, option.Model)
		if includeProvider {
			label = option.Provider + " / " + label
		}
		DrawText(s, rect.X, rect.Y+row, rect.W, style, clampEllipsis(prefix+label, rect.W))
	}
}

func cloneAgentChangeMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneAgentChangeValue(value)
	}
	return out
}

func cloneAgentChangeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAgentChangeMap(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneAgentChangeValue(item))
		}
		return out
	default:
		return value
	}
}

func agentChangeRequestModelIdentity(request map[string]any) (string, string) {
	if len(request) == 0 {
		return "", ""
	}
	content := agentChangeRequestContentObject(request)
	provider := strings.ToLower(strings.TrimSpace(firstNonEmptyToolValue(jsonString(request, "provider"), jsonString(content, "provider"))))
	modelName := strings.TrimSpace(firstNonEmptyToolValue(jsonString(request, "model"), jsonString(content, "model")))
	return provider, modelName
}

func agentChangeRequestContentObject(request map[string]any) map[string]any {
	if len(request) == 0 {
		return nil
	}
	if content, ok := request["content"].(map[string]any); ok && content != nil {
		return content
	}
	if raw, ok := request["content"].(string); ok {
		text := strings.TrimSpace(raw)
		if text == "" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(text), &payload); err == nil && len(payload) > 0 {
			content := cloneAgentChangeMap(payload)
			request["content"] = content
			return content
		}
	}
	if raw, ok := request["content"].([]byte); ok {
		text := strings.TrimSpace(string(raw))
		if text == "" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err == nil && len(payload) > 0 {
			content := cloneAgentChangeMap(payload)
			request["content"] = content
			return content
		}
	}
	return nil
}

func (p *ChatPage) applyAgentChangeSelectedModel(request map[string]any) {
	provider := strings.TrimSpace(p.agentChangeOverrideProvider)
	modelName := strings.TrimSpace(p.agentChangeOverrideModel)
	thinking := normalizeAgentChangeThinkingValue(p.agentChangeOverrideThinking)
	if provider == "" && modelName == "" && thinking == "" {
		return
	}
	content := agentChangeRequestContentObject(request)
	if content == nil && (thinking != "" || (provider != "" && modelName != "")) {
		content = map[string]any{}
		request["content"] = content
	}
	if provider != "" && modelName != "" {
		content["provider"] = provider
		content["model"] = modelName
		request["provider"] = provider
		request["model"] = modelName
	}
	if thinking != "" {
		content["thinking"] = thinking
		request["thinking"] = thinking
	}
}
