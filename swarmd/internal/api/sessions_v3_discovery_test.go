package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3DiscoveryEndpointIsMetadataOnly(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "discover-a", "Discover A")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "hello")
	body := `{"global":true,"recent":{"limit":10}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:discover", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("discovery status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK                     bool                                        `json:"ok"`
		Rev                    uint64                                      `json:"rev"`
		SnapshotEndpointCursor string                                      `json:"snapshot_endpoint_cursor"`
		SessionsByID           map[string]pebblestore.SessionSnapshot      `json:"sessions_by_id"`
		ProjectionsBySession   map[string]pebblestore.V3SessionProjection  `json:"projections_by_session"`
		MessagesBySession      map[string][]pebblestore.MessageSnapshot    `json:"messages_by_session"`
		EventsBySession        map[string][]pebblestore.V3SessionEvent     `json:"events_by_session"`
		RunIntentsBySession    map[string][]pebblestore.V3SessionRunIntent `json:"run_intents_by_session"`
		PreferencesBySession   map[string]any                              `json:"preferences_by_session"`
		UsageBySession         map[string]any                              `json:"usage_by_session"`
		PermissionsBySession   map[string]any                              `json:"permissions_by_session"`
		PlansBySession         map[string]any                              `json:"plans_by_session"`
		SessionOrder           []string                                    `json:"session_order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode discovery response: %v", err)
	}
	if !payload.OK || payload.Rev == 0 || payload.SnapshotEndpointCursor == "" {
		t.Fatalf("discovery envelope missing metadata: ok=%t rev=%d cursor=%q", payload.OK, payload.Rev, payload.SnapshotEndpointCursor)
	}
	if payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("sessions_by_id = %+v", payload.SessionsByID)
	}
	if payload.ProjectionsBySession[created.ID].SessionID != created.ID {
		t.Fatalf("projections_by_session = %+v", payload.ProjectionsBySession)
	}
	if len(payload.SessionOrder) == 0 || payload.SessionOrder[0] != created.ID {
		t.Fatalf("session_order = %+v", payload.SessionOrder)
	}
	if payload.MessagesBySession != nil || payload.EventsBySession != nil || payload.RunIntentsBySession != nil {
		t.Fatalf("discovery must omit messages/events/run intents: messages=%+v events=%+v run_intents=%+v", payload.MessagesBySession, payload.EventsBySession, payload.RunIntentsBySession)
	}
	if payload.PreferencesBySession != nil || payload.UsageBySession != nil || payload.PermissionsBySession != nil || payload.PlansBySession != nil {
		t.Fatalf("discovery must omit selected-session resources: preferences=%+v usage=%+v permissions=%+v plans=%+v", payload.PreferencesBySession, payload.UsageBySession, payload.PermissionsBySession, payload.PlansBySession)
	}
}
