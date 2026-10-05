package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Requirement: CreateProject must always include client_request_id in POST /v3/projects
// payload to prevent daemon 400 errors ("client_request_id is required").
func TestCreateProjectIncludesClientRequestID(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/projects" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		reqID, _ := receivedBody["client_request_id"].(string)
		if strings.TrimSpace(reqID) == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "client_request_id is required (maximum 200 bytes)"})
			return
		}
		name, _ := receivedBody["name"].(string)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"project": ProjectRecord{
				ID:   "proj-generated-123",
				Name: name,
			},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")

	// Case 1: Caller does not provide ClientRequestID; API must generate it.
	proj1, err := api.CreateProject(context.Background(), CreateProjectInput{
		Name: "test-auto-reqid",
	})
	if err != nil {
		t.Fatalf("expected project creation to succeed, got %v", err)
	}
	if proj1.ID != "proj-generated-123" {
		t.Fatalf("expected project ID 'proj-generated-123', got %q", proj1.ID)
	}
	reqID1, ok := receivedBody["client_request_id"].(string)
	if !ok || !strings.HasPrefix(reqID1, "project-create-") {
		t.Fatalf("expected client_request_id with prefix 'project-create-', got %v", receivedBody["client_request_id"])
	}

	// Case 2: Caller provides explicit ClientRequestID; API must preserve it.
	proj2, err := api.CreateProject(context.Background(), CreateProjectInput{
		ClientRequestID: "my-custom-request-id-456",
		Name:            "test-explicit-reqid",
	})
	if err != nil {
		t.Fatalf("expected project creation to succeed, got %v", err)
	}
	if proj2.Name != "test-explicit-reqid" {
		t.Fatalf("expected project name 'test-explicit-reqid', got %q", proj2.Name)
	}
	reqID2, ok := receivedBody["client_request_id"].(string)
	if !ok || reqID2 != "my-custom-request-id-456" {
		t.Fatalf("expected client_request_id 'my-custom-request-id-456', got %v", receivedBody["client_request_id"])
	}
}
