package api

import (
	"encoding/json"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

// Extract only committed canonical projections; metadata never authorizes usage
// invalidations. Account/principal visibility is checked by the realtime caller.
func usageScopesFromRealtimeRecord(record sessionruntime.RealtimeOutboxRecord) []pebblestore.UsageScopeTotal {
	if record.Event.EventType == "usage.scope.updated" {
		var payload struct { ScopeTotals []pebblestore.UsageScopeTotal `json:"scope_totals"` }
		if json.Unmarshal(record.Event.Payload, &payload) != nil { return nil }
		return payload.ScopeTotals
	}
	if record.Event.EventType != "run.usage.updated" && record.Event.EventType != "session.media_usage.recorded" { return nil }
	var payload struct {
		TurnUsage *pebblestore.SessionTurnUsageSnapshot `json:"turn_usage"`
		MediaUsage *pebblestore.SessionMediaUsageRecord `json:"media_usage"`
	}
	if json.Unmarshal(record.Event.Payload, &payload) != nil { return nil }
	if payload.TurnUsage != nil { return payload.TurnUsage.ScopeTotals }
	if payload.MediaUsage != nil { return payload.MediaUsage.ScopeTotals }
	return nil
}
