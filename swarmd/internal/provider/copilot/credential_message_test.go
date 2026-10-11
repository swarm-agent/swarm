package copilot

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the Copilot status reason reaches every client (desktop, terminal
// UI, SDK apps, MCP), so it must not send people to one client's /auth slash
// command. Owner: configuredCredentialMessage, the only producer of this text.
func TestConfiguredCredentialMessageIsClientNeutral(t *testing.T) {
	for _, kind := range []string{pebblestore.AuthTypeOAuth, pebblestore.AuthTypeAPI} {
		message := configuredCredentialMessage(pebblestore.AuthCredentialRecord{ID: "work", Type: kind})
		if strings.Contains(message, "/auth") || strings.Contains(message, "auth manager") || !strings.Contains(message, "is set up") {
			t.Fatalf("%s message = %q, want a client-neutral description", kind, message)
		}
	}
}
