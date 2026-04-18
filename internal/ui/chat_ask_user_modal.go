package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func (p *ChatPage) askUserModalActive() bool {
	return p.askUserVisible
}

func (p *ChatPage) closeAskUserModal() {
	p.askUserVisible = false
	p.askUserPermission = ""
	p.askUserTitle = ""
	p.askUserContext = ""
	p.askUserQuestions = nil
	p.askUserCurrent = 0
	p.askUserAnswers = nil
	p.askUserSelections = nil
	p.askUserScroll = 0
	p.askUserInputMode = false
	p.askUserInput = ""
}

func (p *ChatPage) OpenAskUserPermissionModal(record ChatPermissionRecord) bool {
	if !isAskUserPermission(record) {
		return false
	}
	title, context, questions := askUserPayloadFromPermission(record)
	if len(questions) == 0 {
		return false
	}
	answers := make(map[string]string, len(questions))
	selections := make(map[string]int, len(questions))
	for i := range questions {
		id := strings.TrimSpace(questions[i].ID)
		if id == "" {
			id = fmt.Sprintf("q_%d", i+1)
			questions[i].ID = id
		}
		selections[id] = 0
	}

	p.askUserVisible = true
	p.askUserPermission = strings.TrimSpace(record.ID)
	p.askUserTitle = strings.TrimSpace(title)
	p.askUserContext = strings.TrimSpace(context)
	p.askUserQuestions = questions
	p.askUserCurrent = 0
	p.askUserAnswers = answers
	p.askUserSelections = selections
	p.askUserScroll = 0
	p.askUserInputMode = false
	p.askUserInput = ""
	p.statusLine = "ask-user prompt active"
	return true
}

func (p *ChatPage) handleAskUserModalMouse(ev *tcell.EventMouse) bool {
	if ev == nil || !p.askUserModalActive() {
		return false
	}
	buttons := ev.Buttons()
	switch {
	case buttons&tcell.WheelUp != 0:
		p.shiftAskUserSelection(-1)
	case buttons&tcell.WheelDown != 0:
		p.shiftAskUserSelection(1)
	}
	return true
}

func (p *ChatPage) handleAskUserModalKey(ev *tcell.EventKey) bool {
	if ev == nil || !p.askUserModalActive() {
		return false
	}
	if p.askUserInputMode {
		return p.handleAskUserInputKey(ev)
	}

	switch ev.Key() {
	case tcell.KeyEscape:
		p.resolveAskUserModal(false)
		return true
	case tcell.KeyEnter:
		if p.currentAskUserSelectionAllowsCustom() {
			p.startAskUserInputMode()
			return true
		}
		p.captureAskUserSelection()
		if p.askUserCurrent >= len(p.askUserQuestions)-1 {
			p.resolveAskUserModal(true)
			return true
		}
		p.askUserCurrent++
		p.clampAskUserCurrent()
		return true
	case tcell.KeyUp:
		p.shiftAskUserSelection(-1)
		return true
	case tcell.KeyDown:
		p.shiftAskUserSelection(1)
		return true
	case tcell.KeyLeft:
		p.askUserCurrent--
		p.clampAskUserCurrent()
		return true
	case tcell.KeyRight:
		p.askUserCurrent++
		p.clampAskUserCurrent()
		return true
	case tcell.KeyTab:
		p.askUserCurrent++
		p.clampAskUserCurrent()
		return true
	}

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		switch {
		case r >= '1' && r <= '9':
			index := int(r - '1')
			p.setAskUserSelection(index)
			if p.currentAskUserSelectionAllowsCustom() {
				p.startAskUserInputMode()
				return true
			}
			p.captureAskUserSelection()
			if p.askUserCurrent < len(p.askUserQuestions)-1 {
				p.askUserCurrent++
				p.clampAskUserCurrent()
			}
			return true
		case r == 's' || r == 'S' || r == 'y' || r == 'Y':
			p.resolveAskUserModal(true)
			return true
		case r == 'n' || r == 'N' || r == 'd' || r == 'D':
			p.resolveAskUserModal(false)
			return true
		case r == 'h' || r == 'H':
			p.askUserCurrent--
			p.clampAskUserCurrent()
			return true
		case r == 'l' || r == 'L':
			p.askUserCurrent++
			p.clampAskUserCurrent()
			return true
		case r == 'j' || r == 'J':
			p.shiftAskUserSelection(1)
			return true
		case r == 'k' || r == 'K':
			p.shiftAskUserSelection(-1)
			return true
		default:
			if unicode.IsPrint(r) {
				return true
			}
		}
	}

	return true
}

func (p *ChatPage) handleAskUserInputKey(ev *tcell.EventKey) bool {
	if ev == nil {
		return false
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.askUserInputMode = false
		p.askUserInput = ""
		p.statusLine = "custom response cancelled"
		return true
	case tcell.KeyEnter:
		p.confirmAskUserInput()
		return true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(p.askUserInput) > 0 {
			_, sz := utf8.DecodeLastRuneInString(p.askUserInput)
			if sz > 0 {
				p.askUserInput = p.askUserInput[:len(p.askUserInput)-sz]
			}
		}
		return true
	case tcell.KeyCtrlU:
		p.askUserInput = ""
		return true
	case tcell.KeyRune:
		r := ev.Rune()
		if unicode.IsPrint(r) && utf8.RuneCountInString(p.askUserInput) < chatMaxInputRunes {
			p.askUserInput += string(r)
		}
		return true
	default:
		return true
	}
}

func (p *ChatPage) drawAskUserModal(s tcell.Screen, screen Rect) {
	if !p.askUserModalActive() || len(p.askUserQuestions) == 0 || screen.W < 40 || screen.H < 14 {
		return
	}
	modal, ok := p.askUserModalRect(screen)
	if !ok {
		return
	}

	FillRect(s, modal, p.theme.Panel)
	onPanel := func(style tcell.Style) tcell.Style {
		return styleWithBackgroundFrom(style, p.theme.Panel)
	}
	DrawBox(s, modal, onPanel(p.theme.BorderActive))

	title := strings.TrimSpace(p.askUserTitle)
	if title == "" {
		title = "Ask User"
	}
	header := fmt.Sprintf("%s  (%d/%d)", title, p.askUserCurrent+1, len(p.askUserQuestions))
	DrawText(s, modal.X+2, modal.Y+1, modal.W-4, onPanel(p.theme.Warning.Bold(true)), clampEllipsis(header, modal.W-4))

	subtitle := "Select options, then press S to submit all answers"
	if p.askUserInputMode {
		subtitle = "Type response, then press Enter to accept"
	}
	if strings.TrimSpace(p.askUserPermission) != "" && !p.askUserInputMode {
		subtitle = fmt.Sprintf("Permission %s", clampEllipsis(p.askUserPermission, 24))
	}
	DrawText(s, modal.X+2, modal.Y+2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(subtitle, modal.W-4))

	rowY := modal.Y + 4
	bodyW := modal.W - 4
	current := p.askUserQuestions[p.askUserCurrent]
	promptBottom := modal.Y + modal.H - 8
	if promptBottom < rowY {
		promptBottom = rowY
	}
	for _, line := range p.askUserPromptRenderLines(current, bodyW) {
		if rowY >= promptBottom {
			break
		}
		DrawTimelineLine(s, modal.X+2, rowY, bodyW, line)
		rowY++
	}
	if rowY < promptBottom {
		rowY++
	}

	showCustomInputHint := p.askUserInputMode || p.currentAskUserSelectionAllowsCustom()
	selectedOption := p.currentAskUserOption()
	optionHint := strings.TrimSpace(selectedOption.Description)
	if optionHint == "" && selectedOption.AllowCustom && !p.askUserInputMode {
		optionHint = "Press Enter to type a custom response."
	}
	showOptionHint := optionHint != "" && !p.askUserInputMode
	optionsTop := rowY
	reservedBottomLines := 5
	if showCustomInputHint {
		reservedBottomLines++
	}
	if showOptionHint {
		reservedBottomLines++
	}
	optionsHeight := (modal.Y + modal.H - reservedBottomLines) - optionsTop
	if optionsHeight < 1 {
		optionsHeight = 1
	}
	options := current.Options
	if len(options) == 0 {
		DrawText(s, modal.X+2, optionsTop, bodyW, onPanel(p.theme.TextMuted), "No options provided")
	} else {
		selected := p.currentAskUserSelection()
		maxScroll := maxInt(0, len(options)-optionsHeight)
		if selected < p.askUserScroll {
			p.askUserScroll = selected
		}
		if selected >= p.askUserScroll+optionsHeight {
			p.askUserScroll = selected - optionsHeight + 1
		}
		if p.askUserScroll < 0 {
			p.askUserScroll = 0
		}
		if p.askUserScroll > maxScroll {
			p.askUserScroll = maxScroll
		}

		for row := 0; row < optionsHeight; row++ {
			idx := p.askUserScroll + row
			if idx < 0 || idx >= len(options) {
				break
			}
			option := options[idx]
			prefix := "  "
			style := p.theme.Text
			if idx == selected {
				prefix = "› "
				style = p.theme.Primary.Bold(true)
			}
			currentAnswer := p.currentAskUserAnswer()
			if currentAnswer == optionAnswerValue(option) && currentAnswer != "" {
				style = p.theme.Success
				if idx == selected {
					style = p.theme.Success.Bold(true)
				}
			}
			if option.AllowCustom && currentAnswer != "" && currentAnswer != optionAnswerValue(option) {
				style = p.theme.Success
				if idx == selected {
					style = p.theme.Success.Bold(true)
				}
			}
			label := option.Label
			if label == "" {
				label = option.Value
			}
			if option.AllowCustom && strings.TrimSpace(label) == "" {
				label = "Custom response"
			}
			if option.AllowCustom && idx == selected && !p.askUserInputMode {
				label = label + "  (Enter to type)"
			}
			DrawText(s, modal.X+2, optionsTop+row, bodyW, onPanel(style), clampEllipsis(prefix+label, bodyW))
		}
	}

	answered := p.askUserAnsweredCount()
	progress := fmt.Sprintf("answered %d/%d", answered, len(p.askUserQuestions))
	DrawText(s, modal.X+2, modal.Y+modal.H-3, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(progress, modal.W-4))

	if showOptionHint {
		hintY := modal.Y + modal.H - 4
		if showCustomInputHint {
			hintY--
		}
		DrawText(s, modal.X+2, hintY, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis("Option: "+optionHint, modal.W-4))
	}

	if showCustomInputHint {
		if p.askUserInputMode {
			inputVisible := clampTail(p.askUserInput, maxInt(1, bodyW-2))
			DrawText(s, modal.X+2, modal.Y+modal.H-4, modal.W-4, onPanel(p.theme.Text), clampEllipsis("> "+inputVisible, modal.W-4))
			if (p.frameTick/chatCursorBlinkOn)%2 == 0 {
				cursorX := modal.X + 4 + utf8.RuneCountInString(inputVisible)
				maxX := modal.X + modal.W - 3
				if cursorX > maxX {
					cursorX = maxX
				}
				s.SetContent(cursorX, modal.Y+modal.H-4, chatCursorRune, nil, onPanel(p.theme.Primary))
			}
		} else {
			inputLine := "Press Enter on a custom option to start typing."
			if preview := strings.TrimSpace(p.currentAskUserAnswer()); preview != "" {
				inputLine = "Saved response: " + preview
			}
			DrawText(s, modal.X+2, modal.Y+modal.H-4, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(inputLine, modal.W-4))
		}
	}

	help := "↑/↓ pick • Enter select • ←/→ question • S submit • Esc deny"
	if showCustomInputHint {
		help = "↑/↓ pick • Enter select/type • ←/→ question • S submit • Esc deny"
	}
	if p.askUserInputMode {
		help = "Type response • Enter save • Esc cancel typing"
	}
	DrawText(s, modal.X+2, modal.Y+modal.H-2, modal.W-4, onPanel(p.theme.TextMuted), clampEllipsis(help, modal.W-4))
}

func (p *ChatPage) askUserPromptRenderLines(question chatAskUserQuestion, width int) []chatRenderLine {
	if width <= 0 {
		return nil
	}
	appendInline := func(out []chatRenderLine, text string, style tcell.Style) []chatRenderLine {
		text = strings.TrimSpace(text)
		if text == "" {
			return out
		}
		spans, _ := p.assistantInlineMarkdownSpans(text, style)
		line := chatRenderLine{Text: text, Style: style}
		if len(spans) > 0 {
			line = markdownLineWithInlineSpans("", spans, style)
		}
		line = renderLineForCurrentCellBackground(line)
		wrapped := wrapRenderLineWithCustomPrefixes("", "", line, width)
		for i := range wrapped {
			wrapped[i] = renderLineForCurrentCellBackground(wrapped[i])
		}
		return append(out, wrapped...)
	}

	lines := make([]chatRenderLine, 0, 8)
	label := strings.TrimSpace(question.Question)
	if label == "" {
		label = "Select an option"
	}
	if question.Required {
		label += " (required)"
	} else {
		label += " (optional)"
	}
	lines = appendInline(lines, label, p.theme.Text.Bold(true))

	if header := strings.TrimSpace(question.Header); header != "" {
		lines = appendInline(lines, "Header: "+header, p.theme.TextMuted)
	}
	if context := strings.TrimSpace(p.askUserContext); context != "" {
		lines = append(lines, chatRenderLine{Text: "", Style: styleForCurrentCellBackground(p.theme.TextMuted)})
		lines = appendInline(lines, context, p.theme.TextMuted)
	}
	for i := range lines {
		lines[i] = renderLineForCurrentCellBackground(lines[i])
	}
	return lines
}

func (p *ChatPage) askUserModalRect(screen Rect) (Rect, bool) {
	modalW := minInt(114, screen.W-8)
	if modalW < 54 {
		modalW = screen.W - 2
	}
	if modalW < 40 {
		return Rect{}, false
	}

	modalH := minInt(30, screen.H-4)
	if modalH < 18 {
		modalH = screen.H - 2
	}
	if modalH < 14 {
		return Rect{}, false
	}
	return Rect{
		X: maxInt(1, (screen.W-modalW)/2),
		Y: maxInt(1, (screen.H-modalH)/2),
		W: modalW,
		H: modalH,
	}, true
}

func (p *ChatPage) resolveAskUserModal(approve bool) {
	permissionID := strings.TrimSpace(p.askUserPermission)
	reason := ""
	if approve {
		if p.askUserInputMode {
			text := strings.TrimSpace(p.askUserInput)
			if text == "" {
				p.statusLine = "type response, then press Enter to accept"
				return
			}
			p.setCurrentAskUserAnswer(text)
			p.askUserInputMode = false
			p.askUserInput = ""
		}
		p.captureAskUserSelection()
		var ok bool
		reason, ok = p.askUserResolutionReason()
		if !ok {
			p.statusLine = "answer required for each required question"
			return
		}
	}
	p.closeAskUserModal()
	if permissionID == "" {
		return
	}
	if approve {
		p.queueResolvePermissionByID(permissionID, "approve", reason)
		p.statusLine = "ask-user response submitted"
		return
	}
	p.queueResolvePermissionByID(permissionID, "deny", "")
	p.statusLine = "ask-user denied"
}

func (p *ChatPage) askUserAnsweredCount() int {
	if len(p.askUserQuestions) == 0 {
		return 0
	}
	count := 0
	for _, question := range p.askUserQuestions {
		if strings.TrimSpace(p.askUserAnswers[strings.TrimSpace(question.ID)]) != "" {
			count++
		}
	}
	return count
}

func (p *ChatPage) askUserResolutionReason() (string, bool) {
	if len(p.askUserQuestions) == 0 {
		return "", true
	}
	answers := make(map[string]string, len(p.askUserQuestions))
	ordered := make([]map[string]string, 0, len(p.askUserQuestions))
	for _, question := range p.askUserQuestions {
		id := strings.TrimSpace(question.ID)
		answer := strings.TrimSpace(p.askUserAnswers[id])
		if answer == "" && question.Required {
			return "", false
		}
		if id != "" {
			answers[id] = answer
		}
		ordered = append(ordered, map[string]string{
			"id":       id,
			"question": strings.TrimSpace(question.Question),
			"answer":   answer,
		})
	}

	if len(p.askUserQuestions) == 1 {
		oneID := strings.TrimSpace(p.askUserQuestions[0].ID)
		return strings.TrimSpace(answers[oneID]), true
	}

	payload := map[string]any{
		"path_id": "tool.ask-user.ui.v1",
		"answers": answers,
		"items":   ordered,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func (p *ChatPage) currentAskUserSelection() int {
	if len(p.askUserQuestions) == 0 {
		return 0
	}
	current := p.askUserQuestions[p.askUserCurrent]
	id := strings.TrimSpace(current.ID)
	selected := p.askUserSelections[id]
	if selected < 0 {
		selected = 0
	}
	if selected >= len(current.Options) {
		selected = maxInt(0, len(current.Options)-1)
	}
	return selected
}

func (p *ChatPage) currentAskUserAnswer() string {
	if len(p.askUserQuestions) == 0 {
		return ""
	}
	id := strings.TrimSpace(p.askUserQuestions[p.askUserCurrent].ID)
	return strings.TrimSpace(p.askUserAnswers[id])
}

func (p *ChatPage) setAskUserSelection(index int) {
	if len(p.askUserQuestions) == 0 {
		return
	}
	current := p.askUserQuestions[p.askUserCurrent]
	if len(current.Options) == 0 {
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= len(current.Options) {
		index = len(current.Options) - 1
	}
	id := strings.TrimSpace(current.ID)
	if p.askUserSelections == nil {
		p.askUserSelections = make(map[string]int, 1)
	}
	p.askUserSelections[id] = index
}

func (p *ChatPage) shiftAskUserSelection(delta int) {
	if len(p.askUserQuestions) == 0 || delta == 0 {
		return
	}
	current := p.askUserQuestions[p.askUserCurrent]
	if len(current.Options) == 0 {
		return
	}
	selected := p.currentAskUserSelection() + delta
	if selected < 0 {
		selected = 0
	}
	if selected >= len(current.Options) {
		selected = len(current.Options) - 1
	}
	p.setAskUserSelection(selected)
}

func (p *ChatPage) captureAskUserSelection() {
	if len(p.askUserQuestions) == 0 {
		return
	}
	current := p.askUserQuestions[p.askUserCurrent]
	if len(current.Options) == 0 {
		return
	}
	selected := p.currentAskUserSelection()
	option := current.Options[selected]
	if option.AllowCustom {
		return
	}
	value := optionAnswerValue(option)
	if value == "" {
		return
	}
	if p.askUserAnswers == nil {
		p.askUserAnswers = make(map[string]string, len(p.askUserQuestions))
	}
	p.askUserAnswers[strings.TrimSpace(current.ID)] = value
}

func (p *ChatPage) clampAskUserCurrent() {
	if len(p.askUserQuestions) == 0 {
		p.askUserCurrent = 0
		return
	}
	if p.askUserCurrent < 0 {
		p.askUserCurrent = 0
	}
	if p.askUserCurrent >= len(p.askUserQuestions) {
		p.askUserCurrent = len(p.askUserQuestions) - 1
	}
	p.askUserScroll = 0
	p.askUserInputMode = false
	p.askUserInput = ""
}

func optionAnswerValue(option chatAskUserOption) string {
	if value := strings.TrimSpace(option.Value); value != "" {
		return value
	}
	return strings.TrimSpace(option.Label)
}

func askUserPayloadFromPermission(record ChatPermissionRecord) (string, string, []chatAskUserQuestion) {
	args := decodePermissionArguments(record.ToolArguments)
	if args == nil {
		return "Ask User", "", []chatAskUserQuestion{
			{
				ID:       "q_1",
				Question: "User input requested",
				Options:  defaultAskUserOptions(),
				Required: true,
			},
		}
	}
	title := mapStringArg(args, "title")
	if title == "" {
		title = "Ask User"
	}
	context := mapStringArg(args, "context")
	questions := parseAskUserQuestions(args)
	if len(questions) == 0 {
		question := mapStringArg(args, "question")
		if question == "" {
			question = "User input requested"
		}
		questions = []chatAskUserQuestion{
			{
				ID:       "q_1",
				Question: question,
				Options:  parseAskUserOptions(args["options"]),
				Required: true,
			},
		}
	}
	for i := range questions {
		if strings.TrimSpace(questions[i].ID) == "" {
			questions[i].ID = fmt.Sprintf("q_%d", i+1)
		}
		if strings.TrimSpace(questions[i].Question) == "" {
			questions[i].Question = "User input requested"
		}
		if len(questions[i].Options) == 0 {
			questions[i].Options = defaultAskUserOptions()
		}
	}
	return title, context, questions
}

func parseAskUserQuestions(args map[string]any) []chatAskUserQuestion {
	raw, ok := args["questions"]
	if !ok {
		return nil
	}
	typed, ok := raw.([]any)
	if !ok {
		return nil
	}
	questions := make([]chatAskUserQuestion, 0, len(typed))
	for i := range typed {
		current, ok := typed[i].(map[string]any)
		if !ok {
			continue
		}
		id := mapStringArg(current, "id")
		header := mapStringArg(current, "header")
		question := mapStringArg(current, "question")
		if question == "" {
			question = mapStringArg(current, "prompt")
		}
		if question == "" {
			question = mapStringArg(current, "text")
		}
		if question == "" {
			question = mapStringArg(current, "title")
		}
		required := true
		if rawRequired, ok := current["required"]; ok {
			switch typedRequired := rawRequired.(type) {
			case bool:
				required = typedRequired
			case string:
				switch strings.ToLower(strings.TrimSpace(typedRequired)) {
				case "false", "0", "no":
					required = false
				}
			}
		}
		options := parseAskUserOptions(current["options"])
		questions = append(questions, chatAskUserQuestion{
			ID:       id,
			Header:   header,
			Question: question,
			Options:  options,
			Required: required,
		})
	}
	return questions
}

func parseAskUserOptions(raw any) []chatAskUserOption {
	typed, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]chatAskUserOption, 0, len(typed))
	for i := range typed {
		switch current := typed[i].(type) {
		case string:
			label := strings.TrimSpace(current)
			if label == "" {
				continue
			}
			out = append(out, chatAskUserOption{
				Value:       label,
				Label:       label,
				AllowCustom: strings.EqualFold(label, "__custom__"),
			})
		case map[string]any:
			label := mapStringArg(current, "label")
			value := mapStringArg(current, "value")
			allowCustom := mapBoolArg(current, "allow_custom") || mapBoolArg(current, "allowCustom")
			if strings.EqualFold(strings.TrimSpace(value), "__custom__") {
				allowCustom = true
			}
			if allowCustom && value == "" {
				value = "__custom__"
			}
			if allowCustom && label == "" {
				label = "Custom response"
			}
			if label == "" {
				label = value
			}
			if value == "" {
				value = label
			}
			if label == "" && value == "" {
				continue
			}
			out = append(out, chatAskUserOption{
				Value:       value,
				Label:       label,
				Description: mapStringArg(current, "description"),
				AllowCustom: allowCustom,
			})
		}
	}
	return out
}

func mapBoolArg(payload map[string]any, key string) bool {
	v, ok := payload[key]
	if !ok {
		return false
	}
	switch typed := v.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "y":
			return true
		}
	}
	return false
}

func defaultAskUserOptions() []chatAskUserOption {
	return []chatAskUserOption{
		{
			Value:       "__custom__",
			Label:       "Custom response",
			Description: "Press Enter to type, then Enter to accept.",
			AllowCustom: true,
		},
	}
}

func (p *ChatPage) currentAskUserSelectionAllowsCustom() bool {
	if len(p.askUserQuestions) == 0 {
		return false
	}
	current := p.askUserQuestions[p.askUserCurrent]
	if len(current.Options) == 0 {
		return false
	}
	selected := p.currentAskUserSelection()
	if selected < 0 || selected >= len(current.Options) {
		return false
	}
	return current.Options[selected].AllowCustom
}

func (p *ChatPage) startAskUserInputMode() {
	if !p.currentAskUserSelectionAllowsCustom() {
		return
	}
	p.askUserInputMode = true
	answer := strings.TrimSpace(p.currentAskUserAnswer())
	if answer == optionAnswerValue(p.currentAskUserOption()) {
		answer = ""
	}
	p.askUserInput = answer
	p.statusLine = "type response, then press Enter to accept"
}

func (p *ChatPage) confirmAskUserInput() {
	text := strings.TrimSpace(p.askUserInput)
	if text == "" {
		p.statusLine = "type response, then press Enter to accept"
		return
	}
	p.setCurrentAskUserAnswer(text)
	p.askUserInputMode = false
	p.askUserInput = ""
	if p.askUserCurrent >= len(p.askUserQuestions)-1 {
		p.resolveAskUserModal(true)
		return
	}
	p.askUserCurrent++
	p.clampAskUserCurrent()
}

func (p *ChatPage) setCurrentAskUserAnswer(answer string) {
	if len(p.askUserQuestions) == 0 {
		return
	}
	id := strings.TrimSpace(p.askUserQuestions[p.askUserCurrent].ID)
	if id == "" {
		return
	}
	if p.askUserAnswers == nil {
		p.askUserAnswers = make(map[string]string, len(p.askUserQuestions))
	}
	p.askUserAnswers[id] = strings.TrimSpace(answer)
}

func (p *ChatPage) currentAskUserOption() chatAskUserOption {
	if len(p.askUserQuestions) == 0 {
		return chatAskUserOption{}
	}
	current := p.askUserQuestions[p.askUserCurrent]
	if len(current.Options) == 0 {
		return chatAskUserOption{}
	}
	selected := p.currentAskUserSelection()
	if selected < 0 || selected >= len(current.Options) {
		return chatAskUserOption{}
	}
	return current.Options[selected]
}

func askUserQuestionFromPermission(record ChatPermissionRecord) string {
	_, _, questions := askUserPayloadFromPermission(record)
	if len(questions) == 0 {
		return "User input requested"
	}
	return strings.TrimSpace(questions[0].Question)
}

func askUserOptionsFromPermission(record ChatPermissionRecord) []string {
	_, _, questions := askUserPayloadFromPermission(record)
	if len(questions) == 0 {
		return nil
	}
	options := make([]string, 0, len(questions[0].Options))
	for _, option := range questions[0].Options {
		label := strings.TrimSpace(option.Label)
		if label == "" {
			label = strings.TrimSpace(option.Value)
		}
		if label == "" {
			continue
		}
		options = append(options, label)
	}
	return options
}
