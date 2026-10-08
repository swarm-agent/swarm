package environments

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ConnectionTransportDigest fences edits to SSH routing/auth file references.
// It stores a digest, not file contents, credentials or resolved SSH config.
// External SSH config/agent rotation remains operator-owned; host key checking
// is mandatory and Swarm cannot attest externally mutable system configuration.
func ConnectionTransportDigest(c *Connection) string {
	if c == nil || c.Kind != ConnectionKindSSH {
		return ""
	}
	raw, _ := json.Marshal(struct {
		Account string
		ID      string
		Kind    ConnectionKind
		SSH     any
	}{c.AccountScopeID, c.ID, c.Kind, c.SSH})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
