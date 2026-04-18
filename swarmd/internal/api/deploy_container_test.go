package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/stream"
)

func TestDeployContainerAttachApproveAcceptsPeerAuthTokens(t *testing.T) {
	server := NewServer("test", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	fakeDeploy := &fakeReplicateDeployService{}
	server.SetDeployContainerService(fakeDeploy)

	payload, err := json.Marshal(map[string]any{
		"deployment_id":                 "deployment-1",
		"bootstrap_secret":              "bootstrap-secret",
		"host_swarm_id":                 "host-swarm",
		"host_display_name":             "Host",
		"host_public_key":               "host-public-key",
		"host_fingerprint":              "host-fingerprint",
		"host_backend_url":              "http://127.0.0.1:7781",
		"host_desktop_url":              "http://127.0.0.1:5555",
		"host_to_child_peer_auth_token": "host-to-child-token",
		"child_to_host_peer_auth_token": "child-to-host-token",
		"group_id":                      "group-1",
		"group_name":                    "Primary Group",
		"group_network_name":            "group-net",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/attach/approve", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if fakeDeploy.lastAttachApproveInput.HostToChildPeerAuthToken != "host-to-child-token" {
		t.Fatalf("host to child token = %q, want %q", fakeDeploy.lastAttachApproveInput.HostToChildPeerAuthToken, "host-to-child-token")
	}
	if fakeDeploy.lastAttachApproveInput.ChildToHostPeerAuthToken != "child-to-host-token" {
		t.Fatalf("child to host token = %q, want %q", fakeDeploy.lastAttachApproveInput.ChildToHostPeerAuthToken, "child-to-host-token")
	}
}
