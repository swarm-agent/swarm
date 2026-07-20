package v3chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToolTimelineItem mirrors the Desktop V3 live-tool projection: tool lifecycle
// events are keyed by call ID and placed on the same event/message sequence used
// by the rest of the transcript. A durable role=tool message replaces the live
// projection once it arrives.
type ToolTimelineItem struct {
	ID             string
	CallID         string
	ToolInstanceID string
	GlobalSeq      uint64
	Name           string
	Arguments      string
	Output         string
	Error          string
	Status         string
	DurationMS     int64
	CreatedAt      int64
}

type toolHistoryPayload struct {
	PathID          string `json:"path_id"`
	Tool            string `json:"tool"`
	ToolName        string `json:"tool_name"`
	CallID          string `json:"call_id"`
	ToolInstanceID  string `json:"tool_instance_id"`
	Arguments       string `json:"arguments"`
	Output          string `json:"output"`
	CompletedOutput string `json:"completed_output"`
	Error           string `json:"error"`
	DurationMS      int64  `json:"duration_ms"`
}

func parseToolMessage(message Message) (ToolTimelineItem, bool) {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
		return ToolTimelineItem{}, false
	}
	var payload toolHistoryPayload
	if json.Unmarshal([]byte(message.Content), &payload) != nil {
		return ToolTimelineItem{}, false
	}
	pathID := strings.TrimSpace(payload.PathID)
	if pathID != "run.tool-history.v2" && pathID != "run.v3.provider-tool-result.v1" {
		return ToolTimelineItem{}, false
	}
	name := firstNonEmpty(payload.Tool, payload.ToolName)
	if name == "" {
		return ToolTimelineItem{}, false
	}
	return ToolTimelineItem{
		ID:             message.ID,
		CallID:         strings.TrimSpace(payload.CallID),
		ToolInstanceID: strings.TrimSpace(payload.ToolInstanceID),
		GlobalSeq:      message.GlobalSeq,
		Name:           name,
		Arguments:      strings.TrimSpace(payload.Arguments),
		Output:         firstNonEmptyRaw(payload.Output, payload.CompletedOutput),
		Error:          strings.TrimSpace(payload.Error),
		Status:         toolTerminalStatus(payload.Error),
		DurationMS:     payload.DurationMS,
		CreatedAt:      message.CreatedAt,
	}, true
}

func applyToolEvent(state State, event clientSessionV3Event, payload map[string]json.RawMessage) State {
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	if !isToolTimelineEvent(eventType) {
		return state
	}
	callID := rawString(payload, "call_id", "tool_call_id")
	if callID == "" {
		return state
	}
	item := state.Tools[callID]
	if item.CallID == "" {
		item = ToolTimelineItem{ID: "live-tool:" + callID, CallID: callID, CreatedAt: event.Timestamp}
	}
	item.GlobalSeq = maxUint64(item.GlobalSeq, event.Seq)
	item.ToolInstanceID = firstNonEmpty(rawString(payload, "tool_instance_id"), item.ToolInstanceID)
	item.Name = firstNonEmpty(rawString(payload, "tool_name"), item.Name, "tool")
	item.CreatedAt = firstPositiveInt64(item.CreatedAt, rawInt64(payload, "recorded_at"), event.Timestamp)

	arguments := rawString(payload, "arguments", "arguments_snapshot")
	argumentsDelta := rawString(payload, "arguments_delta")
	if arguments != "" {
		item.Arguments = arguments
	} else if argumentsDelta != "" {
		item.Arguments += argumentsDelta
	}
	if eventType == "session.tool.delta" {
		// Tool progress payloads carry the next chunk in output. Preserve it
		// byte-for-byte and append it so Bash behaves like a live terminal.
		item.Output += firstNonEmptyRaw(
			rawText(payload, "output"),
			rawText(payload, "raw_output"),
			rawText(payload, "output_delta"),
			rawText(payload, "delta"),
		)
	} else {
		output := firstNonEmptyRaw(rawText(payload, "completed_output"), rawText(payload, "raw_output"), rawText(payload, "output"))
		outputDelta := firstNonEmptyRaw(rawText(payload, "output_delta"), rawText(payload, "delta"))
		if output != "" {
			item.Output = output
		} else if outputDelta != "" {
			item.Output += outputDelta
		}
	}
	item.Error = firstNonEmpty(rawString(payload, "error"), item.Error)
	if duration := rawInt64(payload, "duration_ms"); duration != 0 {
		item.DurationMS = duration
	}
	item.Status = toolEventStatus(eventType, rawString(payload, "status"), item.Error)
	if hasDurableToolMessage(state.Messages, item) {
		delete(state.Tools, callID)
		return state
	}
	state.Tools[callID] = item
	return boundLiveTools(state)
}

// Keep this small adapter local to v3chat so tool projection logic can be
// tested without importing backend store types.
type clientSessionV3Event struct {
	Seq       uint64
	EventType string
	Timestamp int64
}

func isToolTimelineEvent(eventType string) bool {
	switch eventType {
	case "session.tool.started", "session.tool.delta", "session.tool.completed", "session.tool.failed", "session.tool.cancelled", "session.tool.canceled",
		"session.provider_tool_call.started", "session.provider_tool_call.arguments.delta", "session.provider_tool_call.arguments.snapshot", "session.provider_tool_call.completed":
		return true
	default:
		return false
	}
}

func toolEventStatus(eventType, payloadStatus, errorText string) string {
	if payloadStatus = strings.ToLower(strings.TrimSpace(payloadStatus)); payloadStatus != "" {
		return payloadStatus
	}
	switch eventType {
	case "session.tool.completed", "session.provider_tool_call.completed":
		return "completed"
	case "session.tool.failed":
		return "failed"
	case "session.tool.cancelled", "session.tool.canceled":
		return "cancelled"
	default:
		if strings.TrimSpace(errorText) != "" {
			return "failed"
		}
		return "running"
	}
}

func hasDurableToolMessage(messages []Message, item ToolTimelineItem) bool {
	for _, message := range messages {
		durable, ok := parseToolMessage(message)
		if !ok {
			continue
		}
		if (item.CallID != "" && durable.CallID == item.CallID) || (item.ToolInstanceID != "" && durable.ToolInstanceID == item.ToolInstanceID) {
			return true
		}
	}
	return false
}

func toolTerminalStatus(errorText string) string {
	if strings.TrimSpace(errorText) != "" {
		return "failed"
	}
	return "completed"
}

func rawString(payload map[string]json.RawMessage, keys ...string) string {
	return strings.TrimSpace(rawText(payload, keys...))
}

func rawText(payload map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if raw := payload[key]; len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
			if strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return ""
}

func rawInt64(payload map[string]json.RawMessage, key string) int64 {
	var value int64
	_ = json.Unmarshal(payload[key], &value)
	return value
}

func firstNonEmptyRaw(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func boundLiveTools(state State) State {
	if len(state.Tools) <= maxResidentMessages {
		return state
	}
	var oldestKey string
	var oldestSeq uint64
	for key, item := range state.Tools {
		if oldestKey == "" || (item.GlobalSeq != 0 && (oldestSeq == 0 || item.GlobalSeq < oldestSeq)) {
			oldestKey, oldestSeq = key, item.GlobalSeq
		}
	}
	delete(state.Tools, oldestKey)
	return state
}

func toolDurationLabel(ms int64) string {
	if ms <= 0 {
		return ""
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
