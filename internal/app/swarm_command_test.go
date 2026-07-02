package app

import (
	"strings"
	"testing"
)

func TestSwarmStatusLinesPreserveIdentityCommandsWithoutDashboard(t *testing.T) {
	app := &App{
		config: defaultAppConfig(),
	}
	app.config.Swarm.Name = "Primary"
	app.config.Swarm.Role = bootstrapRoleMaster

	joined := strings.Join(app.swarmStatusLines(), "\n")
	for _, want := range []string{
		"swarm name: Primary",
		"swarm role: master",
		"usage: /swarm set <name>",
		"/swarm role <master|child>",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("swarm status lines missing %q in %q", want, joined)
		}
	}
	for _, removed := range []string{"/swarm status", "/swarm pending", "/swarm approve", "/swarm reject", "pending enrollments", "pairing state", "dashboard"} {
		if strings.Contains(joined, removed) {
			t.Fatalf("swarm status lines still contain removed dashboard flow %q in %q", removed, joined)
		}
	}
}
