package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	integrationruntime "swarm/packages/swarmd/internal/integration"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestIntegrationsAPIDraftCRUDRedactsCredentialRefs(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "integrations-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	server := NewServer("test", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, stream.NewHub(events))
	server.SetIntegrationService(integrationruntime.NewService(pebblestore.NewIntegrationStore(store)))

	postIntegration(t, server, map[string]any{"action": "create", "resource": "pack", "content": map[string]any{"pack_id": "Demo", "display_name": "Demo"}})
	postIntegration(t, server, map[string]any{"action": "create", "resource": "version", "pack_id": "Demo", "id": "Draft", "content": map[string]any{"status": "draft"}})
	postIntegration(t, server, map[string]any{"action": "create", "resource": "adapter", "pack_id": "Demo", "version_id": "Draft", "id": "Local", "content": map[string]any{"type": "host_http_bridge", "credential_refs": map[string]any{"token": "vault://demo/token"}}})

	req := httptest.NewRequest(http.MethodGet, "/v1/integrations?action=list&resource=adapter&pack_id=Demo&version_id=Draft", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "vault://demo/token") || strings.Contains(body, "credential_refs") {
		t.Fatalf("credential refs leaked: %s", body)
	}
	if !strings.Contains(body, "credential_ref_keys") {
		t.Fatalf("missing credential ref key metadata: %s", body)
	}
}

func postIntegration(t *testing.T, server *Server, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/integrations", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
