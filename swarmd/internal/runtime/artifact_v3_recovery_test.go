package runtime

import (
	"encoding/json"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: exact retained candidate identity outranks recency. Threat: a
// newer successful sibling hides a failed child or is accidentally resumed.
// artifactV3ExactDraft is the narrowest selection boundary; compare the complete
// repository after both successful and rejected lookups to prove read-only use.
func TestArtifactV3RecoveryExactOlderCandidate(t *testing.T) {
	repository := pebblestore.ArtifactV3RepositoryProjection{ArtifactID: "artifact", OwnerSessionID: "parent", Drafts: map[string]pebblestore.ArtifactV3DraftProjection{}}
	for i, id := range []string{"failed", "ready"} {
		grant, _ := json.Marshal(tool.ArtifactV3AuthorGrant{ID: id, ArtifactID: "artifact", OwnerSessionID: "parent", TurnID: "turn", CandidateID: id})
		repository.Drafts[id] = pebblestore.ArtifactV3DraftProjection{GrantID: id, Grant: grant, EventSeq: uint64(i+1), Sequence: 1, Status: id}
	}
	before, _ := json.Marshal(repository)
	got, err := artifactV3ExactDraft(repository, "turn", "failed")
	if err != nil || got.GrantID != "failed" { t.Fatalf("selected=%+v error=%v", got, err) }
	for _, ids := range [][2]string{{"", ""}, {"turn", ""}, {"", "failed"}, {"turn", "missing"}} {
		if _, err := artifactV3ExactDraft(repository, ids[0], ids[1]); err == nil { t.Fatalf("accepted ambiguous/unknown identity: %v", ids) }
	}
	after, _ := json.Marshal(repository)
	if !reflect.DeepEqual(before, after) { t.Fatal("lookup changed siblings") }
}
