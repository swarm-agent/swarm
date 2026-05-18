package run

import (
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func buildRunTurnMessageMetadata(agentName, providerID string, preference pebblestore.ModelPreference, runID, targetKind, targetName string) map[string]any {
	metadata := make(map[string]any, 8)
	setRunTurnMetadataString(metadata, "source", messageMetadataSourceRunTurn)
	setRunTurnMetadataString(metadata, "agent_name", agentName)
	setRunTurnMetadataString(metadata, "provider", providerID)
	setRunTurnMetadataString(metadata, "model", preference.Model)
	setRunTurnMetadataString(metadata, "thinking", preference.Thinking)
	setRunTurnMetadataString(metadata, "service_tier", preference.ServiceTier)
	setRunTurnMetadataString(metadata, "context_mode", preference.ContextMode)
	setRunTurnMetadataString(metadata, "run_id", runID)
	setRunTurnMetadataString(metadata, "target_kind", targetKind)
	setRunTurnMetadataString(metadata, "target_name", targetName)
	return metadata
}

func runMessageMetadataWith(base map[string]any, extra map[string]any) map[string]any {
	metadata := cloneGenericMap(base)
	if metadata == nil {
		metadata = make(map[string]any, len(extra))
	}
	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		metadata[key] = cloneGenericValue(value)
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func setRunTurnMetadataString(metadata map[string]any, key, value string) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	metadata[key] = value
}
