package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm-refactor/swarmtui/internal/model"
)

func TestCommitLineageMetadataIsV2Safe(t *testing.T) {
	a := &App{}
	metadata := a.commitLineageMetadata("parent-1", model.SessionSummary{}, "")

	if got, _ := metadata["lineage_label"].(string); got != "@memory" {
		t.Fatalf("lineage_label = %q, want @memory", got)
	}
	for _, key := range []string{"requested_background_agent", "background_agent", "execution_context"} {
		if _, ok := metadata[key]; ok {
			t.Fatalf("%s present in v2 create metadata: %#v", key, metadata)
		}
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

	a := &App{api: testAPIWithToken(server.URL)}
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
