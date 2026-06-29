package ui

import "strings"

func chatMessagesContainAssistantForRun(messages []ChatMessageRecord, runID string) bool {
	runID = strings.TrimSpace(runID)
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			continue
		}
		if runID == "" {
			return true
		}
		if strings.TrimSpace(metadataString(message.Metadata, "run_id")) == runID {
			return true
		}
	}
	return false
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 || key == "" {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func cloneChatToolStreamEntries(entries []chatToolStreamEntry) []chatToolStreamEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]chatToolStreamEntry, len(entries))
	copy(out, entries)
	return out
}

func mergeChatToolStreamEntries(current, restored []chatToolStreamEntry) []chatToolStreamEntry {
	if len(restored) == 0 {
		return current
	}
	out := cloneChatToolStreamEntries(current)
	seen := make(map[string]struct{}, len(out)+len(restored))
	for _, entry := range out {
		if key := toolStreamEntryKey(entry); key != "" {
			seen[key] = struct{}{}
		}
	}
	for _, entry := range restored {
		key := toolStreamEntryKey(entry)
		if key != "" {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, entry)
	}
	return out
}

func cloneStringSet(in map[string]struct{}) map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}

func mergeStringSets(current, restored map[string]struct{}) map[string]struct{} {
	if len(current) == 0 && len(restored) == 0 {
		return nil
	}
	out := cloneStringSet(current)
	if out == nil {
		out = make(map[string]struct{}, len(restored))
	}
	for key := range restored {
		out[key] = struct{}{}
	}
	return out
}
