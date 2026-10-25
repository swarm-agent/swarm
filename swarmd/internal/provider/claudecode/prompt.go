package claudecode

import (
	"encoding/json"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

const emptyTranscriptPrompt = "Continue the task using the latest context."

// buildTranscriptPrompt serializes the complete, explicitly selected epoch input
// into one user turn. Claude Code holds no conversation state between Swarm
// requests (stateless_full_input); the durable V3 transcript stays the only
// authority. Serialization is deterministic and append-only so the stable
// prefix keeps hitting the provider prompt cache as the conversation grows.
func buildTranscriptPrompt(input []map[string]any, dynamicContext string) string {
	var builder strings.Builder
	for _, item := range input {
		writeTranscriptItem(&builder, item)
	}
	if dynamicContext = strings.TrimSpace(dynamicContext); dynamicContext != "" {
		builder.WriteString(dynamicContext)
		builder.WriteString("\n")
	}
	prompt := strings.TrimSpace(builder.String())
	if prompt == "" {
		return emptyTranscriptPrompt
	}
	return prompt
}

func writeTranscriptItem(builder *strings.Builder, item map[string]any) {
	if len(item) == 0 {
		return
	}
	switch strings.ToLower(strings.TrimSpace(stringField(item, "type"))) {
	case "function_call":
		builder.WriteString("[assistant.tool_call]")
		if name := strings.TrimSpace(stringField(item, "name")); name != "" {
			builder.WriteString(" name=")
			builder.WriteString(name)
		}
		if callID := strings.TrimSpace(stringField(item, "call_id")); callID != "" {
			builder.WriteString(" call_id=")
			builder.WriteString(callID)
		}
		builder.WriteString("\n")
		if arguments := strings.TrimSpace(stringField(item, "arguments")); arguments != "" {
			builder.WriteString(arguments)
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
		return
	case "function_call_output":
		builder.WriteString("[tool.result]")
		if callID := strings.TrimSpace(stringField(item, "call_id")); callID != "" {
			builder.WriteString(" call_id=")
			builder.WriteString(callID)
		}
		builder.WriteString("\n")
		if output := contentText(item["output"]); output != "" {
			builder.WriteString(output)
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
		return
	case "reasoning":
		// Reasoning is provider-private; it is not replayed into a new chain.
		return
	}
	role := strings.TrimSpace(stringField(item, "role"))
	if role == "" {
		role = "unknown"
	}
	content := contentText(item["content"])
	builder.WriteString("[")
	builder.WriteString(role)
	builder.WriteString("]\n")
	if content != "" {
		builder.WriteString(content)
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
}

func contentText(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	case []map[string]any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := contentText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := contentText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		switch strings.ToLower(strings.TrimSpace(stringField(value, "type"))) {
		case "input_text", "output_text", "text":
			return strings.TrimSpace(stringField(value, "text"))
		case "session_media":
			// The CLI transcript is text-only. Image perception is available
			// through MCP media_inspect results, not this attachment reference.
			payload, ok := value["media"].(provideriface.SessionMediaPayload)
			if !ok || strings.TrimSpace(payload.AssetID) == "" {
				return "[Attachment not delivered: you cannot see or hear this attachment directly; its asset_id is unavailable.]"
			}
			arguments, _ := json.Marshal(map[string]string{"asset_id": payload.AssetID})
			return "[Attachment not delivered natively: you cannot see or hear this attachment directly. To inspect it, call media_inspect with " + string(arguments) + ".]"
		case "input_image", "image", "input_file", "file":
			// Raw attachment parts are not delivered through the text transcript.
			return "[attachment omitted]"
		}
		if text := strings.TrimSpace(stringField(value, "text")); text != "" {
			return text
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(encoded))
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(encoded))
	}
}

func stringField(item map[string]any, key string) string {
	if item == nil {
		return ""
	}
	text, _ := item[key].(string)
	return text
}
