package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	"swarm/packages/swarmd/internal/stream"
)

func TestRemoteDeploySSHSessionCreateAndStartAreRetired(t *testing.T) {
	server := NewServer("test", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))

	cases := []struct {
		name   string
		path   string
		body   string
		pathID string
	}{
		{
			name:   "create",
			path:   "/v1/deploy/remote/session/create",
			body:   `{"name":"remote","ssh_session_target":"host"}`,
			pathID: remotedeploy.PathSessionCreate,
		},
		{
			name:   "start",
			path:   "/v1/deploy/remote/session/start",
			body:   `{"session_id":"remote-session"}`,
			pathID: remotedeploy.PathSessionStart,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusGone {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusGone, rec.Body.String())
			}
			var payload struct {
				OK     bool   `json:"ok"`
				PathID string `json:"path_id"`
				Error  string `json:"error"`
				Code   string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.OK {
				t.Fatal("ok = true, want false")
			}
			if payload.PathID != tc.pathID {
				t.Fatalf("path_id = %q, want %q", payload.PathID, tc.pathID)
			}
			if payload.Code != "410" {
				t.Fatalf("code = %q, want 410", payload.Code)
			}
			if !strings.Contains(payload.Error, "SSH remote deploy is retired") || !strings.Contains(payload.Error, "Add Remote Swarm pairing") {
				t.Fatalf("error = %q, want retired guidance", payload.Error)
			}
		})
	}
}
