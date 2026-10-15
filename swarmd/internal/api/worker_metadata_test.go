package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Purpose: V3 create/update must reject worker authority injection before any
// session mutation. HTTP handlers prove rejection and stored postconditions;
// merge coverage also proves an update omitting keys cannot strip authority.
func TestSessionsV3WorkerMetadataProtected(t *testing.T) {
	keys := []string{"worker_id", "worker_execution_run_id", "worker_name", "worker_job_title", "worker_revision", "worker_execution_mode", "worker_role", "worker_review_target", "worker_workspace_id"}
	server, svc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "worker-metadata", "Interactive")
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			metadata := map[string]any{key: "forged"}
			for _, route := range []string{"/v3/sessions", "/v3/sessions/" + created.ID + "/metadata"} {
				body, _ := json.Marshal(map[string]any{"client_request_id": key, "workspace_path": "/workspace/v3", "agent_name": "swarm", "metadata": metadata})
				req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				server.Handler().ServeHTTP(rec, withTestPrincipal(req))
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("%s status=%d body=%s", route, rec.Code, rec.Body.String())
				}
			}
			snapshot, ok, err := svc.GetSession(created.ID)
			if err != nil || !ok {
				t.Fatalf("session: %v", err)
			}
			if _, exists := snapshot.Metadata[key]; exists {
				t.Fatalf("injected %s", key)
			}
			if got := mergeSessionsV3MetadataUpdate(map[string]any{key: "pinned"}, map[string]any{}); got[key] != "pinned" {
				t.Fatalf("stripped %s", key)
			}
		})
	}
}
