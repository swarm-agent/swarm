package ui

import "strings"

const chatPendingAfterAuthoritativeSeqMetadataKey = "swarm_ui_pending_after_global_seq"

func (p *ChatPage) trackPendingLocalUserMessage(content string, createdAt int64) {
	content = strings.TrimSpace(content)
	if p == nil || content == "" {
		return
	}
	p.pendingLocalUserMessages = append(p.pendingLocalUserMessages, pendingLocalUserMessage{
		Content:                 content,
		CreatedAt:               createdAt,
		AfterAuthoritativeSeq:   p.authoritativeMessageSeq,
		AfterAuthoritativeCount: p.authoritativeMessageCount,
	})
}

// reconcilePendingLocalUserMessages keeps optimistic user turns visible until the
// V3 projection contains the corresponding durable record. The authoritative
// sequence boundary survives tail-window rotation; the count remains a fallback
// for records that do not carry a V3 global sequence.
func (p *ChatPage) reconcilePendingLocalUserMessages(messages []ChatMessageRecord) []ChatMessageRecord {
	if p == nil {
		return messages
	}
	p.authoritativeMessageCount = len(messages)
	if latestSeq := latestAuthoritativeMessageSeq(messages); latestSeq > p.authoritativeMessageSeq {
		p.authoritativeMessageSeq = latestSeq
	}
	if len(p.pendingLocalUserMessages) == 0 {
		return messages
	}

	matched := make([]bool, len(messages))
	remaining := make([]pendingLocalUserMessage, 0, len(p.pendingLocalUserMessages))
	for _, pending := range p.pendingLocalUserMessages {
		found := -1
		start := pending.AfterAuthoritativeCount
		if start < 0 {
			start = 0
		}
		if start > len(messages) {
			start = len(messages)
		}
		for i := range messages {
			if matched[i] || !strings.EqualFold(strings.TrimSpace(messages[i].Role), "user") {
				continue
			}
			if pending.AfterAuthoritativeSeq > 0 && messages[i].GlobalSeq > 0 {
				if messages[i].GlobalSeq <= pending.AfterAuthoritativeSeq {
					continue
				}
			} else if i < start {
				continue
			}
			if strings.TrimSpace(messages[i].Content) == pending.Content {
				found = i
				break
			}
		}
		if found >= 0 {
			matched[found] = true
			continue
		}
		remaining = append(remaining, pending)
	}
	p.pendingLocalUserMessages = remaining
	if len(remaining) == 0 {
		return messages
	}

	out := make([]ChatMessageRecord, 0, len(messages)+len(remaining))
	pendingIndex := 0
	for i := 0; i <= len(messages); i++ {
		for pendingIndex < len(remaining) && pendingLocalUserMessagePosition(remaining[pendingIndex], messages) <= i {
			pending := remaining[pendingIndex]
			metadata := map[string]any(nil)
			if pending.AfterAuthoritativeSeq > 0 {
				metadata = map[string]any{chatPendingAfterAuthoritativeSeqMetadataKey: pending.AfterAuthoritativeSeq}
			}
			out = append(out, ChatMessageRecord{Role: "user", Content: pending.Content, Metadata: metadata, CreatedAt: pending.CreatedAt})
			pendingIndex++
		}
		if i < len(messages) {
			out = append(out, messages[i])
		}
	}
	return out
}

func pendingLocalUserMessagePosition(pending pendingLocalUserMessage, messages []ChatMessageRecord) int {
	if pending.AfterAuthoritativeSeq > 0 {
		for i, message := range messages {
			if message.GlobalSeq <= pending.AfterAuthoritativeSeq {
				continue
			}
			return i
		}
		return len(messages)
	}
	position := pending.AfterAuthoritativeCount
	if position < 0 {
		return 0
	}
	if position > len(messages) {
		return len(messages)
	}
	return position
}

func latestAuthoritativeMessageSeq(messages []ChatMessageRecord) uint64 {
	var latest uint64
	for _, message := range messages {
		if message.GlobalSeq > latest {
			latest = message.GlobalSeq
		}
	}
	return latest
}

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
