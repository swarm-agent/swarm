package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestAgentsAPIV2List(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agents-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	server := NewServer("test", nil, agentSvc, nil, nil, nil, nil, nil, nil, nil, nil, eventLog, hub)
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/v2/agents", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v2/agents status = %d, want 200", rec.Code)
	}

	var payload struct {
		State struct {
			ActivePrimary string `json:"active_primary"`
		} `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if payload.State.ActivePrimary != "swarm" {
		t.Fatalf("active primary = %q, want swarm", payload.State.ActivePrimary)
	}
}
