package ui

import "strings"

const chatModeTransitionMetadataSource = "session_mode_transition"

func shouldSuppressModeTransitionSystemRecord(message ChatMessageRecord) bool {
	return shouldSuppressModeTransitionSystemMessage(message.Role, message.Content, message.Metadata)
}

func shouldSuppressModeTransitionSystemItem(message chatMessageItem) bool {
	return shouldSuppressModeTransitionSystemMessage(message.Role, message.Text, message.Metadata)
}

func shouldSuppressModeTransitionSystemMessage(role, content string, metadata map[string]any) bool {
	if !strings.EqualFold(strings.TrimSpace(role), "system") {
		return false
	}
	if metadataSourceMatches(metadata, chatModeTransitionMetadataSource) {
		return true
	}
	return isModeTransitionSystemContent(content)
}

func metadataSourceMatches(metadata map[string]any, want string) bool {
	if len(metadata) == 0 {
		return false
	}
	value, _ := metadata["source"].(string)
	return strings.EqualFold(strings.TrimSpace(value), want)
}

func isModeTransitionSystemContent(content string) bool {
	content = strings.TrimSpace(content)
	switch {
	case strings.HasPrefix(content, "Session mode changed to auto.") && strings.Contains(content, "explicitly exited plan mode"):
		return true
	case strings.HasPrefix(content, "Session mode changed to plan.") && strings.Contains(content, "explicitly re-entered plan mode"):
		return true
	default:
		return false
	}
}
