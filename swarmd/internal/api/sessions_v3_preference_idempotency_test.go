package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3PreferenceRequiresCallerIdempotencyKey(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "preference-key-required.pebble"))
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "preference-required-create", "preference required", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})

	rec := postSessionsV3PreferenceTest(t, server, created.ID, `{"model":"next-model"}`, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "client_request_id is required") {
		t.Fatalf("missing key status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSessionsV3PreferenceIdempotencyReplayConflictAndDistinctKey(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "preference-idempotency.pebble"))
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "preference-idempotency-create", "preference idempotency", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})

	first := postSessionsV3PreferenceTest(t, server, created.ID, `{"idempotency_key":"preference-key","model":"first-model"}`, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	firstSeq, firstReplayed := decodeSessionsV3PreferenceMutation(t, first)
	if firstReplayed {
		t.Fatal("first preference mutation was marked replayed")
	}

	replay := postSessionsV3PreferenceTest(t, server, created.ID, `{"client_request_id":"preference-key","model":"first-model"}`, "")
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	replaySeq, replayed := decodeSessionsV3PreferenceMutation(t, replay)
	if !replayed || replaySeq != firstSeq {
		t.Fatalf("replay mutation seq=%d replayed=%t, want seq=%d replayed=true", replaySeq, replayed, firstSeq)
	}

	conflict := postSessionsV3PreferenceTest(t, server, created.ID, `{"model":"changed-model"}`, "preference-key")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "different payload hash") {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	distinct := postSessionsV3PreferenceTest(t, server, created.ID, `{"model":"changed-model"}`, "preference-key-2")
	if distinct.Code != http.StatusOK {
		t.Fatalf("distinct status=%d body=%s", distinct.Code, distinct.Body.String())
	}
	distinctSeq, distinctReplayed := decodeSessionsV3PreferenceMutation(t, distinct)
	if distinctReplayed || distinctSeq == firstSeq {
		t.Fatalf("distinct mutation seq=%d replayed=%t, first seq=%d", distinctSeq, distinctReplayed, firstSeq)
	}
	stored, found, err := sessionSvc.GetSession(created.ID)
	if err != nil || !found || stored.Preference.Model != "changed-model" {
		t.Fatalf("stored preference found=%t err=%v preference=%+v", found, err, stored.Preference)
	}
}

func postSessionsV3PreferenceTest(t *testing.T, server *Server, sessionID, body, headerKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/preference", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if headerKey != "" {
		req.Header.Set("Idempotency-Key", headerKey)
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec
}

func decodeSessionsV3PreferenceMutation(t *testing.T, rec *httptest.ResponseRecorder) (uint64, bool) {
	t.Helper()
	var response struct {
		Mutation struct {
			Event    pebblestore.V3SessionEvent `json:"event"`
			Replayed bool                       `json:"replayed"`
		} `json:"mutation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode preference response: %v", err)
	}
	return response.Mutation.Event.Seq, response.Mutation.Replayed
}
