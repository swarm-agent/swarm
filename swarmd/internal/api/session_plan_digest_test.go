package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: plan read/save responses must expose the same typed serialization
// digest used by automation pins. Client object ordering must not become another
// authority. This helper-level test isolates serialization; route authorization
// and acceptance lifecycle still require integration tests.
func TestSessionPlanDocumentDigestCanonical(t *testing.T) {
	var document store.SessionPlanDocument
	if err := json.Unmarshal([]byte(`{"title":"<review>&","id":"instructions","info":{"goal":"execute"}}`), &document); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(&document)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := sessionPlanDocumentDigest(&document); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest differs from canonical pin serialization: %s", got)
	}
	if got := sessionPlanDocumentDigest(nil); got != "" {
		t.Fatalf("absent document produced usable digest: %s", got)
	}
}
