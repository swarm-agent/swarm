package lifecycle

import (
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: EnsureDeployment reuse and deployment receipts must identify the
// actual local source mount, not falsely attest a caller's requested checkout.
// deploymentWorkspacePath is the shared provisioning identity boundary; testing
// strategy/override cases here is the narrowest deterministic regression layer.
func TestDeploymentWorkspacePath(t *testing.T) {
	root, fixed := t.TempDir(), t.TempDir()
	env := environments.Environment{}
	if got := deploymentWorkspacePath(env, root, "local_docker"); got != "" {
		t.Fatalf("non-local strategy attested a local tree: %s", got)
	}
	env.Provisioning.Strategy.Kind = environments.SourceStrategyKindLocalMount
	env.Provisioning.Strategy.LocalMount = &environments.LocalMountConfig{}
	if got := deploymentWorkspacePath(env, root, "local_docker"); got != root {
		t.Fatalf("dynamic mount lost exact tree: %s", got)
	}
	env.Provisioning.Strategy.LocalMount.HostPath = fixed
	if got := deploymentWorkspacePath(env, root, "local_docker"); got != fixed {
		t.Fatalf("fixed mount falsely attested requested tree: %s", got)
	}
}
