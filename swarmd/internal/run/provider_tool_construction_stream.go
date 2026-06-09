package run

import (
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func providerToolConstructionStreamEvent(step int, event provideriface.StreamEvent) StreamEvent {
	callID := strings.TrimSpace(event.ToolCallID)
	out := StreamEvent{
		Type:              runProviderToolConstructionEventType(event.Type),
		Step:              step,
		ToolName:          strings.TrimSpace(event.ToolName),
		CallID:            callID,
		ToolCallID:        callID,
		ToolCallIndex:     cloneIntPtr(event.ToolCallIndex),
		Arguments:         strings.TrimSpace(event.Arguments),
		ArgumentsDelta:    event.ArgumentsDelta,
		ArgumentsSnapshot: strings.TrimSpace(event.ArgumentsSnapshot),
		Metadata:          cloneGenericMap(event.Metadata),
	}
	if out.Type == StreamEventProviderToolCallArgumentsDelta && out.ArgumentsDelta == "" {
		out.ArgumentsDelta = event.Delta
	}
	if out.Type == StreamEventProviderToolCallArgumentsSnapshot && out.ArgumentsSnapshot == "" {
		out.ArgumentsSnapshot = strings.TrimSpace(event.Arguments)
	}
	if out.Type == StreamEventProviderToolCallCompleted && out.Arguments == "" {
		out.Arguments = strings.TrimSpace(firstNonEmptyString(event.Arguments, event.ArgumentsSnapshot))
	}
	return out
}

func runProviderToolConstructionEventType(eventType provideriface.StreamEventType) string {
	switch eventType {
	case provideriface.StreamEventToolCallStarted:
		return StreamEventProviderToolCallStarted
	case provideriface.StreamEventToolCallArgumentsDelta:
		return StreamEventProviderToolCallArgumentsDelta
	case provideriface.StreamEventToolCallArgumentsSnapshot:
		return StreamEventProviderToolCallArgumentsSnapshot
	case provideriface.StreamEventToolCallCompleted:
		return StreamEventProviderToolCallCompleted
	default:
		return strings.TrimSpace(string(eventType))
	}
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
