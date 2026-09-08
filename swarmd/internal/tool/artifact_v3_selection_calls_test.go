package tool

import (
	"reflect"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: selection evidence uses turn event CAS, never schema version or
// repository projection, and excludes failed/stale candidates. This pure mapping
// is the narrow read-only handoff boundary; actual selection remains permissioned.
func TestArtifactV3SelectionCalls(t *testing.T) {
	s := pebblestore.ArtifactV3SelectedSource{ArtifactID: "art", CommitOID: "head", ProjectionSeq: 99, Turns: []pebblestore.ArtifactV3TurnProjection{{TurnID: "turn", BaseCommitOID: "head", EventSeq: 42}, {TurnID: "old", BaseCommitOID: "stale", EventSeq: 43}}, Candidates: []pebblestore.ArtifactV3CandidateProjection{{CandidateID: "ready", TurnID: "turn", Status: "ready"}, {CandidateID: "failed", TurnID: "turn", Status: "failed"}, {CandidateID: "stale", TurnID: "old", Status: "ready"}}}
	got := artifactV3SelectionCalls(s)
	want := []map[string]any{{"action": "select_v3", "artifact_id": "art", "turn_id": "turn", "candidate_id": "ready", "expected_head": "head", "expected_turn_revision": uint64(42)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}
