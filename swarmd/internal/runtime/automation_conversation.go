package runtime

import (
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/session"
)

// composeConversationAutomation retains the session service's snapshot-resolving
// active-plan lookup; the raw store returns only an active-plan locator.
func composeConversationAutomation(repo automation.Repository, sessions *session.Service, access automation.Access, now func() time.Time) (*automation.Service, error) {
	return automation.New(repo, sessions, access, now)
}
