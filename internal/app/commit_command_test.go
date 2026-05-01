package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestCommitLineageMetadataTargetsMemoryAgent(t *testing.T) {
	a := &App{}
	metadata := a.commitLineageMetadata("parent-1", model.SessionSummary{}, "", nil)

	if got, _ := metadata["lineage_label"].(string); got != "@memory" {
		t.Fatalf("lineage_label = %q, want @memory", got)
	}
	if got, _ := metadata["requested_background_agent"].(string); got != "memory" {
		t.Fatalf("requested_background_agent = %q, want memory", got)
	}
	if got, _ := metadata["background_agent"].(string); got != "memory" {
		t.Fatalf("background_agent = %q, want memory", got)
	}
}

func TestStartBackgroundCommitRunTargetsMemoryAgent(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/child-1/run/stream" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          true,
			"session_id":  "child-1",
			"run_id":      "run-1",
			"status":      "running",
			"background":  true,
			"target_kind": "background",
			"target_name": "memory",
		})
	}))
	defer server.Close()

	a := &App{api: client.New(server.URL)}
	if _, err := a.startBackgroundCommitRun(context.Background(), model.SessionSummary{ID: "child-1"}, ""); err != nil {
		t.Fatalf("startBackgroundCommitRun() error = %v", err)
	}
	if got, _ := body["target_kind"].(string); got != "background" {
		t.Fatalf("target_kind = %q, want background", got)
	}
	if got, _ := body["target_name"].(string); got != "memory" {
		t.Fatalf("target_name = %q, want memory", got)
	}
}
