package ui

import (
	"strings"

	"swarm-refactor/swarmtui/internal/model"
)

type SessionLineage struct {
	Background      bool
	ParentSessionID string
	LineageKind     string
	LineageLabel    string
	TargetKind      string
	TargetName      string
}

func SessionLineageFromSummary(summary model.SessionSummary) SessionLineage {
	metadata := summary.Metadata
	if len(metadata) == 0 {
		return SessionLineage{}
	}
	lineage := SessionLineage{
		Background:      sessionLineageMetadataBool(metadata, "background") || strings.EqualFold(sessionLineageMetadataString(metadata, "launch_mode"), "background"),
		ParentSessionID: sessionLineageMetadataString(metadata, "parent_session_id"),
		LineageKind:     sessionLineageMetadataString(metadata, "lineage_kind"),
		LineageLabel:    normalizeSessionLineageLabel(sessionLineageMetadataString(metadata, "lineage_label")),
		TargetKind:      sessionLineageMetadataString(metadata, "target_kind"),
		TargetName:      sessionLineageMetadataString(metadata, "target_name"),
	}
	if lineage.LineageLabel == "" {
		lineage.LineageLabel = normalizeSessionLineageLabel(sessionLineageFirstNonEmpty(
			sessionLineageMetadataString(metadata, "subagent"),
			sessionLineageMetadataString(metadata, "requested_subagent"),
			sessionLineageMetadataString(metadata, "background_agent"),
			sessionLineageMetadataString(metadata, "requested_background_agent"),
		))
	}
	if lineage.LineageLabel == "" && strings.TrimSpace(lineage.ParentSessionID) != "" {
		lineage.LineageLabel = normalizeSessionLineageLabel(lineage.TargetName)
	}
	return lineage
}

func SessionLineageFromTab(tab ChatSessionTab) SessionLineage {
	return SessionLineage{
		Background:      tab.Background,
		ParentSessionID: strings.TrimSpace(tab.ParentSessionID),
		LineageKind:     strings.TrimSpace(tab.LineageKind),
		LineageLabel:    normalizeSessionLineageLabel(tab.LineageLabel),
		TargetKind:      strings.TrimSpace(tab.TargetKind),
		TargetName:      strings.TrimSpace(tab.TargetName),
	}
}

func SessionLineageFromPaletteItem(item ChatSessionPaletteItem) SessionLineage {
	return SessionLineage{
		Background:      item.Background,
		ParentSessionID: strings.TrimSpace(item.ParentSessionID),
		LineageKind:     strings.TrimSpace(item.LineageKind),
		LineageLabel:    normalizeSessionLineageLabel(item.LineageLabel),
		TargetKind:      strings.TrimSpace(item.TargetKind),
		TargetName:      strings.TrimSpace(item.TargetName),
	}
}

func SessionLineageDisplay(lineage SessionLineage) string {
	if strings.TrimSpace(lineage.ParentSessionID) != "" {
		if label := normalizeSessionLineageLabel(lineage.LineageLabel); label != "" {
			return label
		}
		return "child"
	}
	if lineage.Background {
		if targetKind := strings.ToLower(strings.TrimSpace(lineage.TargetKind)); targetKind != "" && targetKind != "background" {
			return ""
		}
		target := strings.TrimSpace(lineage.TargetName)
		if target == "" {
			target = strings.TrimPrefix(normalizeSessionLineageLabel(lineage.LineageLabel), "@")
		}
		if target != "" {
			return "bg:" + target
		}
		return "background"
	}
	return ""
}

func SessionDepth(summary model.SessionSummary) int {
	return maxSessionDepth(sessionLineageMetadataInt(summary.Metadata, "ui_depth"), summary.Depth)
}

func SessionIndentedPrefix(depth int) string {
	depth = maxSessionDepth(0, depth)
	if depth <= 0 {
		return ""
	}
	if depth > 5 {
		depth = 5
	}
	return strings.Repeat("  ", depth) + "↳ "
}

func sessionDisplayTitle(title, id string) string {
	title = strings.TrimSpace(title)
	if title != "" {
		return title
	}
	return strings.TrimSpace(id)
}

func sessionListPrimaryLine(prefix, title, lineageLabel, workspace, modelLabel string, compact bool) string {
	parts := make([]string, 0, 4)
	if lineageLabel = strings.TrimSpace(lineageLabel); lineageLabel != "" {
		parts = append(parts, lineageLabel)
	}
	if title = strings.TrimSpace(title); title != "" {
		parts = append(parts, title)
	}
	if !compact {
		if workspace = strings.TrimSpace(workspace); workspace != "" {
			parts = append(parts, workspace)
		}
		if modelLabel = strings.TrimSpace(modelLabel); modelLabel != "" && modelLabel != "unset" {
			parts = append(parts, modelLabel)
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(prefix)
	}
	return prefix + strings.Join(parts, " · ")
}

func normalizeSessionLineageLabel(label string) string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return ""
	}
	if trimmed == "child" || strings.EqualFold(trimmed, "background") || strings.HasPrefix(strings.ToLower(trimmed), "bg:") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "@") {
		return trimmed
	}
	if strings.Contains(trimmed, " ") {
		return ""
	}
	return "@" + trimmed
}

func sessionLineageMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func sessionLineageMetadataBool(metadata map[string]any, key string) bool {
	value, _ := metadata[key].(bool)
	return value
}

func sessionLineageMetadataInt(metadata map[string]any, key string) int {
	if len(metadata) == 0 {
		return 0
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func sessionLineageFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func maxSessionDepth(values ...int) int {
	best := 0
	for _, value := range values {
		if value > best {
			best = value
		}
	}
	return best
}
