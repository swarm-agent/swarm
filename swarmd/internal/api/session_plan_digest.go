package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// The pin hashes the typed Go document, not a client's reserialization of it.
// An unavailable document never produces a usable reference.
func sessionPlanDocumentDigest(document *store.SessionPlanDocument) string {
	if document == nil {
		return ""
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
