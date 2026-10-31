package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Session reads (manage_projects read_task_session and get_task_session,
// manage-sessions read_messages) spend a text budget on message content. A
// stored message can carry provider-internal fields a reader has no use for:
// a Gemini thought signature, encrypted reasoning, a raw provider payload.
// They are opaque and often far longer than the readable text, so a read
// leaves them out and says so in one line; the readable fields come first.
//
// The transform is deterministic: the same stored content always reads the
// same, so a chunk's content_offset continues the same text.

// ProviderInternalMarkerKey names the one-line marker a read adds in place of
// the provider-internal fields it left out.
const ProviderInternalMarkerKey = "provider_internal"

// providerInternalKeys are the fields that are provider-internal wherever
// they appear in a message's JSON content.
var providerInternalKeys = map[string]bool{
	"thought_signature":           true,
	"thoughtSignature":            true,
	"encrypted_content":           true,
	"encryptedContent":            true,
	"encrypted_reasoning":         true,
	"reasoning_encrypted_content": true,
	"provider_payload":            true,
	"raw_provider_payload":        true,
	"provider_raw":                true,
	"raw_response":                true,
}

// readableFirstKeys are read first, in this order; other keys follow sorted.
var readableFirstKeys = []string{"tool_name", "name", "status", "error", "output", "completed_output", "text", "content", "summary", "arguments"}

// ReadableMessageContent is a message's content as a session read shows it:
// JSON object content without provider-internal fields (each left out field
// counted in a one-line provider_internal marker at the end), readable fields
// first. Content that is not a JSON object, or holds no provider-internal
// field, is returned unchanged.
func ReadableMessageContent(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "{") || !strings.Contains(trimmed, "\"") {
		return content
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var object map[string]any
	if decoder.Decode(&object) != nil || decoder.More() {
		return content
	}
	removed := map[string]int{}
	cleaned, _ := stripProviderInternal(object, removed)
	if len(removed) == 0 {
		return content
	}
	out, ok := cleaned.(map[string]any)
	if !ok {
		out = map[string]any{}
	}
	names := make([]string, 0, len(removed))
	total := 0
	for name, count := range removed {
		names = append(names, name)
		total += count
	}
	sort.Strings(names)
	out[ProviderInternalMarkerKey] = fmt.Sprintf("%d provider-internal field(s) left out (%s)", total, strings.Join(names, ", "))
	encoded, err := encodeReadableObject(out)
	if err != nil {
		return content
	}
	return encoded
}

// ChunkReadableMessageContent is ChunkMessageContent over the message's
// readable content (ReadableMessageContent): what every session read pages.
func ChunkReadableMessageContent(content string, offset, budget int) (MessageContentChunk, error) {
	return ChunkMessageContent(ReadableMessageContent(content), offset, budget)
}

// stripProviderInternal removes provider-internal keys at any depth and
// counts them by name. A map or list left empty by the removal is dropped
// (keep=false), so a metadata wrapper that only held a signature goes too.
func stripProviderInternal(value any, removed map[string]int) (any, bool) {
	switch v := value.(type) {
	case map[string]any:
		if len(v) == 0 {
			return v, true
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			if providerInternalKeys[key] {
				removed[key]++
				continue
			}
			if cleaned, keep := stripProviderInternal(item, removed); keep {
				out[key] = cleaned
			}
		}
		return out, len(out) > 0
	case []any:
		if len(v) == 0 {
			return v, true
		}
		out := make([]any, 0, len(v))
		for _, item := range v {
			if cleaned, keep := stripProviderInternal(item, removed); keep {
				out = append(out, cleaned)
			}
		}
		return out, len(out) > 0
	}
	return value, true
}

// encodeReadableObject encodes object with readableFirstKeys first, then the
// other keys sorted, then the provider_internal marker; text is not
// HTML-escaped so it reads as written.
func encodeReadableObject(object map[string]any) (string, error) {
	keys := make([]string, 0, len(object))
	seen := map[string]bool{ProviderInternalMarkerKey: true}
	for _, key := range readableFirstKeys {
		if _, ok := object[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	var rest []string
	for key := range object {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	if _, ok := object[ProviderInternalMarkerKey]; ok {
		keys = append(keys, ProviderInternalMarkerKey)
	}
	encode := func(value any) ([]byte, error) {
		var part bytes.Buffer
		encoder := json.NewEncoder(&part)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			return nil, err
		}
		return bytes.TrimRight(part.Bytes(), "\n"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := encode(key)
		if err != nil {
			return "", err
		}
		value, err := encode(object[key])
		if err != nil {
			return "", err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.String(), nil
}
