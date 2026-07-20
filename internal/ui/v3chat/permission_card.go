package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

type bashPermissionIntent struct {
	Command     string
	Explanation []string
	Category    string
	Critical    bool
}

// permissionCardModel is tool-agnostic. Tool presenters supply content while
// the shared component owns the themed card surface and layout.
type permissionCardModel struct {
	Title      string
	Badge      string
	Meta       string
	Content    []permissionCardLine
	FooterRows int
}

type permissionCardLine struct {
	Text  string
	Style tcell.Style
}

type permissionCardLayout struct {
	ContentY int
	ContentH int
	FooterY  int
}

func normalizePermissionToolName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "functions.")
	return strings.ReplaceAll(value, "-", "_")
}

func parseBashPermissionIntent(record client.PermissionRecord) (bashPermissionIntent, bool) {
	if normalizePermissionToolName(record.ToolName) != "bash" {
		return bashPermissionIntent{}, false
	}
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(record.ToolArguments)), &payload) != nil || payload == nil {
		return bashPermissionIntent{}, false
	}
	command, _ := payload["command"].(string)
	category, _ := payload["category"].(string)
	critical, hasCritical := payload["critical"].(bool)
	command = strings.TrimSpace(command)
	category = strings.ToLower(strings.TrimSpace(category))
	var explanation []string
	if raw, ok := payload["explanation"].([]any); ok {
		for _, item := range raw {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				explanation = append(explanation, strings.TrimSpace(text))
			}
		}
	}
	if command == "" || len(explanation) == 0 || !hasCritical {
		return bashPermissionIntent{}, false
	}
	switch category {
	case "read", "write", "update":
	default:
		return bashPermissionIntent{}, false
	}
	return bashPermissionIntent{Command: command, Explanation: explanation, Category: category, Critical: critical}, true
}

func permissionCardModelForRecord(record client.PermissionRecord, pendingCount, width int, styles PageStyles, prefixPreview string) permissionCardModel {
	if normalizePermissionToolName(record.ToolName) == "bash" {
		return bashPermissionCardModel(record, pendingCount, width, styles, prefixPreview)
	}
	mode := strings.TrimSpace(record.Mode)
	if mode == "" {
		mode = "auto"
	}
	toolName := strings.TrimSpace(record.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	meta := fmt.Sprintf("Approval required  ·  %s  ·  mode %s", permissionRequirementLabel(record.Requirement), mode)
	if pendingCount > 1 {
		meta += fmt.Sprintf("  ·  %d pending", pendingCount)
	}
	model := permissionCardModel{Title: toolName + " permission", Meta: meta, FooterRows: 4}
	model.Content = append(model.Content, permissionCardLine{Text: "REQUEST", Style: styles.Muted.Bold(true)})
	arguments := strings.TrimSpace(record.ToolArguments)
	if arguments == "" {
		arguments = "{}"
	}
	for _, line := range wrapText(arguments, maxInt(1, width)) {
		model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Text})
	}
	return model
}

func bashPermissionCardModel(record client.PermissionRecord, pendingCount, width int, styles PageStyles, prefixPreview string) permissionCardModel {
	mode := strings.TrimSpace(record.Mode)
	if mode == "" {
		mode = "auto"
	}
	meta := fmt.Sprintf("Approval required  ·  %s  ·  mode %s", permissionRequirementLabel(record.Requirement), mode)
	if pendingCount > 1 {
		meta += fmt.Sprintf("  ·  %d pending", pendingCount)
	}
	model := permissionCardModel{Title: "Bash permission", Meta: meta, FooterRows: 4}
	intent, ok := parseBashPermissionIntent(record)
	if !ok {
		model.Content = []permissionCardLine{{
			Text:  "Invalid Bash request: explanation, read/write/update category, and critical flag are required.",
			Style: styles.Error,
		}}
		return model
	}
	model.Badge = intent.Category
	if intent.Critical {
		model.Content = append(model.Content,
			permissionCardLine{Text: "! PAY ATTENTION BEFORE APPROVING", Style: styles.Warning.Bold(true)},
			permissionCardLine{Text: "The AI marked this command as critical.", Style: styles.Warning},
			permissionCardLine{Text: "", Style: styles.Muted},
		)
	}
	model.Content = append(model.Content, permissionCardLine{Text: "WHAT THIS COMMAND WILL DO", Style: styles.Muted.Bold(true)})
	for _, item := range intent.Explanation {
		for index, line := range wrapText(item, maxInt(1, width-2)) {
			prefix := "  "
			if index == 0 {
				prefix = "• "
			}
			model.Content = append(model.Content, permissionCardLine{Text: prefix + line, Style: styles.Text})
		}
	}
	model.Content = append(model.Content,
		permissionCardLine{Text: "", Style: styles.Muted},
		permissionCardLine{Text: "COMMAND", Style: styles.Muted.Bold(true)},
	)
	for _, line := range wrapText(intent.Command, maxInt(1, width)) {
		model.Content = append(model.Content, permissionCardLine{Text: line, Style: styles.Text})
	}
	if strings.TrimSpace(prefixPreview) == "" {
		prefixPreview = record.SavedRulePreview
	}
	model.Content = append(model.Content,
		permissionCardLine{Text: "", Style: styles.Muted},
		permissionCardLine{Text: "ALWAYS ALLOW PREFIX", Style: styles.Muted.Bold(true)},
		permissionCardLine{Text: bashPermissionPreviewPrefix(prefixPreview), Style: styles.Accent},
		permissionCardLine{Text: "Future Bash commands starting with this prefix will be approved automatically.", Style: styles.Muted},
	)
	return model
}

func drawPermissionCard(screen tcell.Screen, x, y, width, height int, styles PageStyles, model permissionCardModel, scroll int) permissionCardLayout {
	footerRows := maxInt(1, model.FooterRows)
	layout := permissionCardLayout{ContentY: y + 4, ContentH: maxInt(0, height-footerRows-5), FooterY: y + height - footerRows}
	if width < 8 || height < footerRows+5 {
		return layout
	}
	panel := styles.Panel
	fill(screen, x, y, width, height, panel)
	drawBox(screen, x, y, width, height, styleOnPermissionCard(styles.Border, panel))
	innerX, innerWidth := x+2, width-4
	titleStyle := styleOnPermissionCard(styles.Text.Bold(true), panel)
	mutedStyle := styleOnPermissionCard(styles.Muted, panel)
	badgeStyle := styleOnPermissionCard(styles.Secondary.Bold(true), panel)
	dividerStyle := styleOnPermissionCard(styles.Border, panel)

	badge := strings.ToUpper(strings.TrimSpace(model.Badge))
	badgeWidth := 0
	if badge != "" {
		badge = " " + badge + " "
		badgeWidth = utf8.RuneCountInString(badge)
		if badgeWidth+1 < innerWidth {
			badgeX := innerX + innerWidth - badgeWidth
			fill(screen, badgeX, y+1, badgeWidth, 1, badgeStyle)
			drawText(screen, badgeX, y+1, badgeWidth, badgeStyle, badge)
		}
	}
	titleWidth := innerWidth
	if badgeWidth > 0 && badgeWidth+1 < innerWidth {
		titleWidth -= badgeWidth + 1
	}
	drawText(screen, innerX, y+1, titleWidth, titleStyle, truncateRunes(strings.TrimSpace(model.Title), titleWidth))
	drawText(screen, innerX, y+2, innerWidth, mutedStyle, truncateRunes(strings.TrimSpace(model.Meta), innerWidth))
	drawHLine(screen, x+1, y+3, width-2, dividerStyle)
	drawHLine(screen, x+1, layout.FooterY-1, width-2, dividerStyle)

	maxScroll := maxInt(0, len(model.Content)-layout.ContentH)
	scroll = maxInt(0, minInt(scroll, maxScroll))
	end := minInt(len(model.Content), scroll+layout.ContentH)
	for index := scroll; index < end; index++ {
		line := model.Content[index]
		drawText(screen, innerX, layout.ContentY+index-scroll, innerWidth, styleOnPermissionCard(line.Style, panel), line.Text)
	}
	return layout
}

func styleOnPermissionCard(style, surface tcell.Style) tcell.Style {
	foreground, _, attributes := style.Decompose()
	_, background, _ := surface.Decompose()
	return tcell.StyleDefault.Foreground(foreground).Background(background).Attributes(attributes)
}

func permissionRequirementLabel(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "_", " "), "-", " "))
	if value == "" || strings.EqualFold(value, "tool") || strings.EqualFold(value, "permission") {
		return "tool permission"
	}
	return value
}

func bashPermissionPreviewPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "available after approval"
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"allow bash command prefix:", "deny bash command prefix:", "allow bash prefix:", "deny bash prefix:"} {
		if index := strings.Index(lower, marker); index >= 0 {
			return strings.TrimSpace(value[index+len(marker):])
		}
	}
	return value
}
