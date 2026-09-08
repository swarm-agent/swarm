package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: the authenticated native artifact detail route preserves exact
// group slots and never invokes selection. The handler fixture is the narrowest
// test for JSON fidelity and foreign-session rejection before service access;
// runtime/store tests separately prove membership authority and restart.
func TestArtifactV3HTTPGenerationContext(t *testing.T) {
	server, service := newArtifactV3APITestServer(t)
	service.artifact = artifactV3APITestArtifact()
	member := pebblestore.ArtifactV3GenerationMember{WaveID: "wave", Index: 1, Count: 2, ArtifactID: "artifact-1", TurnID: "turn", CandidateID: "candidate"}
	failed := member
	failed.Index = 2
	failed.ArtifactID = "artifact-2"
	failed.CandidateID = "failed"
	service.artifact.Generations = []pebblestore.ArtifactV3GenerationMember{member}
	service.artifact.GenerationGroups = []ArtifactV3GenerationGroup{{WaveID: "wave", Count: 2, Members: []ArtifactV3GenerationSibling{{ArtifactV3GenerationMember: member, Status: "selected", CommitOID: strings.Repeat("a", 40)}, {ArtifactV3GenerationMember: failed, Status: "error"}}}}
	before := service.artifact
	route := "/v3/sessions/artifact-v3-api/artifacts-v3/artifact-1"
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, withTestPrincipal(httptest.NewRequest(http.MethodGet, route, nil)))
		var body struct {
			Artifact ArtifactV3Artifact `json:"artifact"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || !reflect.DeepEqual(body.Artifact.GenerationGroups, before.GenerationGroups) {
			t.Fatalf("context lost %s", response.Body.String())
		}
	}
	calls := service.calls
	foreign := httptest.NewRecorder()
	server.Handler().ServeHTTP(foreign, withAccountPrincipal(httptest.NewRequest(http.MethodGet, route, nil), "foreign", "user-1"))
	if foreign.Code != http.StatusNotFound || service.calls != calls || strings.Contains(foreign.Body.String(), "wave") {
		t.Fatal("foreign membership disclosed")
	}
	if service.selectionCount != 0 || !reflect.DeepEqual(service.artifact, before) {
		t.Fatal("navigation selected state")
	}
}
