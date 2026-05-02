package api

import (
	"fmt"
	"log"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func flowRouteDiagLog(stage string, fields ...any) {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "unknown"
	}
	parts := make([]string, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		key := strings.TrimSpace(fmt.Sprint(fields[i]))
		if key == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", key, strings.TrimSpace(fmt.Sprint(fields[i+1]))))
	}
	log.Printf("flow_route_diag stage=%q %s", stage, strings.Join(parts, " "))
}

func flowRouteDiagMetadataValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func flowRouteDiagMetadataMarksFlow(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	return strings.EqualFold(flowRouteDiagMetadataValue(metadata, "source"), "flow") ||
		strings.EqualFold(flowRouteDiagMetadataValue(metadata, "lineage_kind"), "flow") ||
		strings.EqualFold(flowRouteDiagMetadataValue(metadata, "owner_transport"), "flow_scheduler") ||
		flowRouteDiagMetadataValue(metadata, "flow_id") != ""
}

func flowRouteDiagSessionMetadataValue(session *pebblestore.SessionSnapshot, key string) string {
	if session == nil {
		return ""
	}
	return flowRouteDiagMetadataValue(session.Metadata, key)
}
