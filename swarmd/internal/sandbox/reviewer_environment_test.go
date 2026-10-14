package sandbox

import (
	"strings"
	"testing"
)

// Purpose: SetReviewEnvironment and scopeRunArgs must expose only captured
// reviewer env in a registered reviewer scope, never in coder containers.
// Mount argv is the narrowest hermetic proof of container environment policy.
func TestReviewerEnvironment(t *testing.T) {
	checkout := "/review-checkout"
	m := &Manager{cfg: Config{Image: DefaultImage}, reviews: map[string]string{checkout: "/source"}}
	if err := m.SetReviewEnvironment(checkout, map[string]string{"SWARM_REVIEW_HEAD": "head"}); err == nil {
		t.Fatal("partial context accepted")
	}
	env := map[string]string{"SWARM_REVIEW_HEAD": "head", "SWARM_REVIEW_BASE": "refs/swarm/publish/dev", "SWARM_REVIEW_JOB": "worker/w/r"}
	if err := m.SetReviewEnvironment(checkout, env); err != nil {
		t.Fatal(err)
	}
	defer m.ForgetReview(checkout)
	args := strings.Join(m.scopeRunArgs("review", "/source", "spec", nil, SecretSetup{}, EnvironmentImage{Image: DefaultImage}, checkout), "\n")
	for name, value := range env {
		if !strings.Contains(args, name+"="+value) {
			t.Fatal(args)
		}
	}
	coder := strings.Join(m.scopeRunArgs("coder", "/source", "spec", nil, SecretSetup{}, EnvironmentImage{Image: DefaultImage}, ""), "\n")
	if strings.Contains(coder, "SWARM_REVIEW_") {
		t.Fatal("review context leaked into coder")
	}
}
