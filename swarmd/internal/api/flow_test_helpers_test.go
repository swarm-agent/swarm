package api

import (
	"encoding/json"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func requireSessionCreatedPayload(t *testing.T, server *Server, sessionID string) pebblestore.SessionSnapshot {
	t.Helper()
	if server == nil || server.events == nil {
		t.Fatal("event log not configured")
	}
	events, err := server.events.ReadFrom(1, 100)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, event := range events {
		if event.EventType != "session.created" || event.EntityID != sessionID {
			continue
		}
		var payload pebblestore.SessionSnapshot
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode session.created payload: %v", err)
		}
		return payload
	}
	t.Fatalf("session.created for %q not found in events %+v", sessionID, events)
	return pebblestore.SessionSnapshot{}
}
