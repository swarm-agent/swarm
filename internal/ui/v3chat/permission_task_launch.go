package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui/footerbar"
)

type taskLaunchPermissionIntent struct {
	Goal          string
	Description   string
	Prompt        string
	LaunchCount   int
	ResolvedAgent string
	Launches      []map[string]any
}

func isTaskLaunchPermission(record client.PermissionRecord) bool {
	return normalizePermissionToolName(record.ToolName) == "task" && strings.EqualFold(strings.TrimSpace(record.Requirement), "task_launch")
}

func parseTaskLaunchPermissionIntent(record client.PermissionRecord) (taskLaunchPermissionIntent, bool) {
	if !isTaskLaunchPermission(record) {
		return taskLaunchPermissionIntent{}, false
	}
	payload := parseToolObject(record.ToolArguments)
	if payload == nil {
		return taskLaunchPermissionIntent{}, false
	}
	launches := toolObjectSlice(payload, "launches")
	launchCount := toolInt(payload, "launch_count")
	if launchCount < len(launches) {
		launchCount = len(launches)
	}
	if launchCount < 1 {
		launchCount = 1
	}
	return taskLaunchPermissionIntent{
		Goal:          toolString(payload, "goal"),
		Description:   toolString(payload, "description"),
		Prompt:        toolString(payload, "prompt"),
		LaunchCount:   launchCount,
		ResolvedAgent: toolString(payload, "resolved_agent_name"),
		Launches:      launches,
	}, true
}

func taskLaunchPermissionCardModel(record client.PermissionRecord, intent taskLaunchPermissionIntent, pendingCount, width int, styles PageStyles) permissionCardModel {
	mode := firstNonEmpty(strings.TrimSpace(record.Mode), "auto")
	model := permissionCardModel{
		Title:      fmt.Sprintf("Launch %d subagent%s", intent.LaunchCount, pluralSuffix(intent.LaunchCount)),
		Badge:      "SUBAGENTS",
		Meta:       permissionCardMeta(record, pendingCount, mode),
		FooterRows: permissionCardFooterRows(record),
	}
	summary := firstNonEmptyToolRaw(intent.Description, intent.Goal, intent.Prompt, "Review the exact subagent wave before launch.")
	for _, line := range wrapText(summary, maxInt(1, width)) {
		model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Text})
	}
	model.Content = append(model.Content, permissionCardLine{Text: "Open review modal  ·  Enter approve  ·  Esc deny", Style: styles.Muted})
	if !permissionPending(record) {
		model.Content = append(model.Content,
			permissionCardLine{Text: "RESOLVED", Style: styles.Muted.Bold(true)},
			permissionCardLine{Text: permissionResolvedLabel(record), Style: permissionResolvedStyle(record, styles).Bold(true)},
		)
	}
	return model
}

func taskLaunchPermissionModalLines(record client.PermissionRecord, width int, styles PageStyles) []permissionCardLine {
	intent, ok := parseTaskLaunchPermissionIntent(record)
	if !ok {
		return []permissionCardLine{{Text: "The task launch manifest could not be decoded.", Style: styles.Error}}
	}
	width = maxInt(1, width)
	lines := make([]permissionCardLine, 0, minInt(512, len(intent.Launches)*12+16))
	appendWrapped := func(label, value string, style tcell.Style) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, line := range wrapText(label+value, width) {
			lines = append(lines, permissionCardLine{Text: line, Style: style})
		}
	}
	appendWrapped("", "Review the exact wave before any child session or worktree is created.", styles.Warning)
	appendWrapped("Launches: ", fmt.Sprintf("%d", intent.LaunchCount), styles.Text.Bold(true))
	appendWrapped("Task: ", firstNonEmptyToolRaw(intent.Description, intent.Goal), styles.Text)
	appendWrapped("Router: ", intent.ResolvedAgent, styles.Secondary)
	for index, launch := range intent.Launches {
		lines = append(lines, permissionCardLine{Text: "", Style: styles.Muted})
		title := firstNonEmptyToolRaw(toolString(launch, "child_title_preview"), toolString(launch, "assignment_label"), toolString(launch, "description"), fmt.Sprintf("Subagent %d", index+1))
		agent := firstNonEmptyToolRaw(toolString(launch, "resolved_agent_name"), toolString(launch, "requested_subagent_type"), "subagent")
		lines = append(lines, permissionCardLine{Text: fmt.Sprintf("%d. %s  ·  %s", index+1, title, agent), Style: styles.Primary.Bold(true)})
		facts := []struct {
			label string
			value string
		}{
			{"Model", strings.Trim(strings.Join([]string{toolString(launch, "subagent_provider"), toolString(launch, "subagent_model")}, "/"), "/")},
			{"Mode", firstNonEmptyToolRaw(toolString(launch, "effective_child_mode"), toolString(launch, "child_mode"))},
			{"Deliverable", toolString(launch, "deliverable")},
			{"Owned scope", strings.Join(toolStringSlice(launch, "owned_scope"), ", ")},
			{"Concurrency", toolString(launch, "concurrency_reason")},
			{"Dependency evidence", toolString(launch, "dependency_evidence")},
		}
		for _, fact := range facts {
			appendWrapped("  "+fact.label+": ", fact.value, styles.Text)
		}
		appendWrapped("  Assignment: ", firstNonEmptyToolRaw(toolString(launch, "meta_prompt"), toolString(launch, "role")), styles.Muted)
	}
	if len(intent.Launches) == 0 {
		lines = append(lines, permissionCardLine{Text: "No per-subagent rows were included in the manifest.", Style: styles.Warning})
	}
	if strings.TrimSpace(intent.Prompt) != "" {
		lines = append(lines, permissionCardLine{Text: "", Style: styles.Muted}, permissionCardLine{Text: "PARENT PROMPT", Style: styles.Muted.Bold(true)})
		appendWrapped("", intent.Prompt, styles.Text)
	}
	return lines
}

func taskLaunchApprovedArguments(record client.PermissionRecord) string {
	approved := strings.TrimSpace(record.ApprovedArguments)
	if parseToolObject(approved) != nil {
		return approved
	}
	payload := parseToolObject(record.ToolArguments)
	if payload == nil {
		return ""
	}
	approvedObject := toolObject(payload, "approved_arguments")
	if approvedObject == nil {
		return ""
	}
	encoded, err := json.Marshal(approvedObject)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (p *Page) taskLaunchPermissionIndexLocked(permissions []client.PermissionRecord) int {
	for index, record := range permissions {
		if isTaskLaunchPermission(record) {
			return index
		}
	}
	return -1
}

func (p *Page) handleTaskLaunchPermissionKeyLocked(record client.PermissionRecord, ev *tcell.EventKey) PageAction {
	if p.permissionBusy {
		return PageActionNone
	}
	switch ev.Key() {
	case tcell.KeyEscape:
		p.resolvePermissionLocked(record, "deny_once")
	case tcell.KeyEnter:
		p.resolvePermissionLocked(record, "allow_once")
	case tcell.KeyUp:
		p.permissionContentScroll = maxInt(0, p.permissionContentScroll-1)
	case tcell.KeyDown:
		p.permissionContentScroll = minInt(p.permissionContentMaxScroll, p.permissionContentScroll+1)
	case tcell.KeyPgUp:
		p.permissionContentScroll = maxInt(0, p.permissionContentScroll-6)
	case tcell.KeyPgDn:
		p.permissionContentScroll = minInt(p.permissionContentMaxScroll, p.permissionContentScroll+6)
	case tcell.KeyHome:
		p.permissionContentScroll = 0
	case tcell.KeyEnd:
		p.permissionContentScroll = p.permissionContentMaxScroll
	}
	return PageActionNone
}

func (p *Page) drawTaskLaunchPermissionModal(screen tcell.Screen, width, height int, styles PageStyles, record client.PermissionRecord, scroll int) {
	if width < 38 || height < 12 {
		return
	}
	intent, _ := parseTaskLaunchPermissionIntent(record)
	modalWidth := minInt(112, width-4)
	modalHeight := minInt(height-4, maxInt(12, height-6))
	x, y := (width-modalWidth)/2, (height-modalHeight)/2
	fill(screen, x, y, modalWidth, modalHeight, styles.Panel)
	drawBox(screen, x, y, modalWidth, modalHeight, styles.BorderActive)
	contentWidth := maxInt(1, modalWidth-4)
	title := fmt.Sprintf("LAUNCH %d SUBAGENT%s?", intent.LaunchCount, strings.ToUpper(pluralSuffix(intent.LaunchCount)))
	drawText(screen, x+2, y+1, contentWidth, styles.Warning.Bold(true), truncateCells(title, contentWidth))
	drawText(screen, x+2, y+2, contentWidth, styles.Muted, "Bounded exact-wave review  ·  ↑/↓ or PgUp/PgDn scroll")
	lines := taskLaunchPermissionModalLines(record, contentWidth, styles)
	visibleRows := maxInt(1, modalHeight-7)
	maxScroll := maxInt(0, len(lines)-visibleRows)
	scroll = minInt(maxInt(0, scroll), maxScroll)
	for row := 0; row < visibleRows && scroll+row < len(lines); row++ {
		line := lines[scroll+row]
		drawText(screen, x+2, y+3+row, contentWidth, line.Style, line.Text)
	}
	actionY := y + modalHeight - 2
	denyLabel, approveLabel := " Esc Deny ", " Enter Launch "
	denyWidth, approveWidth := utf8.RuneCountInString(denyLabel), utf8.RuneCountInString(approveLabel)
	drawText(screen, x+2, actionY, denyWidth, permissionButtonStyle(styles.Error), denyLabel)
	drawText(screen, x+3+denyWidth, actionY, approveWidth, permissionButtonStyle(styles.Success), approveLabel)
	if maxScroll > 0 {
		indicator := fmt.Sprintf("%d/%d", scroll+1, maxScroll+1)
		drawText(screen, x+modalWidth-2-utf8.RuneCountInString(indicator), actionY, utf8.RuneCountInString(indicator), styles.Muted, indicator)
	}
	p.mu.Lock()
	p.permissionContentID = record.ID
	p.permissionContentScroll = scroll
	p.permissionContentMaxScroll = maxScroll
	p.permissionDenyTarget = footerbar.Rect{X: x + 2, Y: actionY, W: denyWidth, H: 1}
	p.permissionApproveTarget = footerbar.Rect{X: x + 3 + denyWidth, Y: actionY, W: approveWidth, H: 1}
	p.permissionAlwaysTarget = footerbar.Rect{}
	p.permissionAlwaysDenyTarget = footerbar.Rect{}
	p.mu.Unlock()
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
