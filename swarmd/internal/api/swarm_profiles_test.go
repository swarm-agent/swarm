package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSwarmProfilesAPIHardCutRejectsLegacySplitPayload(t *testing.T) {
	server := &Server{}
	body := `{"name":"Crew","members":[{"agent_id":"swarm","model_mode":"split","plan":{"provider":"codex","model":"plan"},"auto":{"provider":"codex","model":"action"}}]}`
	for _, path := range []string{swarmProfilesPath, swarmProfilesPath + "/legacy"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		res := httptest.NewRecorder()
		if path == swarmProfilesPath {
			server.handleSwarmProfiles(res, req)
		} else {
			server.handleSwarmProfileByID(res, req)
		}
		if res.Code != http.StatusGone {
			t.Fatalf("POST %s = %d, want %d: %s", path, res.Code, http.StatusGone, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "Action") || !strings.Contains(res.Body.String(), "Plan") {
			t.Fatalf("POST %s did not direct client to canonical mode settings: %s", path, res.Body.String())
		}
	}
}
