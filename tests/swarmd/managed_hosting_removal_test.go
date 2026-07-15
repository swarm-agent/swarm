package swarmd_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRetiredSessionHostingResidueRemoved(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))

	for _, path := range []string{
		"docs/checkpoints/sessions-api-v2-primary-local-containers.md",
		"scripts/diagnose-remote-deploy-live-ui.mjs",
		"scripts/diagnose-remote-deploy-live.sh",
		"scripts/rebuild-container-remote.sh",
		"swarmd/internal/deploy",
		"swarmd/internal/api/sessions_v2_primary.go",
		"swarmd/internal/api/sessions_v2_lifecycle.go",
		"swarmd/internal/api/sessions_v2_list.go",
		"swarmd/internal/api/topology_session_routes.go",
		"swarmd/internal/session/session_execution_v2.go",
		"swarmd/internal/store/pebble/session_route_store.go",
		"swarmd/internal/store/pebble/deploy_container_store.go",
		"swarmd/internal/store/pebble/deploy_container_store_test.go",
		"swarmd/internal/store/pebble/workspace_replication_store.go",
		"swarmd/internal/store/pebble/workspace_replication_store_test.go",
		"swarmd/internal/workspace/replication.go",
		"swarmd/internal/workspace/replication_test.go",
		"tests/swarmd/live_prod_update_e2e.sh",
		"tests/swarmd/remote_deploy_e2e.sh",
		"tests/swarmd/remote_deploy_recovery_e2e.sh",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			t.Errorf("retired session-hosting artifact remains: %s", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}

	checks := map[string][]string{
		"docs/swarm-harness-vm.md": {
			"remote_deploy_e2e.sh",
		},
		"scripts/check-daemon-storage-paths.sh": {
			"swarmd/internal/remotedeploy",
		},
		"scripts/lib-lane.sh": {
			"managed_host_sync_",
			"remote_deploy_enabled",
			"remote_deploy_host_api_base_url",
			"remote_deploy_session_id",
			"remote_deploy_sync_enabled",
		},
		"internal/app/app.go": {
			"SessionExecutionV2",
			"swarm_v2_",
			"swarm_routed_",
		},
		"internal/client/api.go": {
			"/v2/sessions",
			"SessionExecutionV2",
		},
		"swarmd/internal/api/server_routes.go": {
			"/v1/deploy/container",
			"/v2/sessions",
		},
		"swarmd/internal/api/sessions_v3_primary.go": {
			"swarm_v2_",
			"swarm_routed_",
		},
		"swarmd/internal/api/sessions_v3_primary_test.go": {
			"SessionsV2",
		},
		"swarmd/internal/store/pebble/keys.go": {
			"deploy/container/",
			"deploy/container_by_account/",
			"session_execution_v2/",
			"session_execution_v2_by_account/",
			"session_route/",
			"topology/session_route/",
			"topology/session_route_by_account/",
			"workspace/replication/",
		},
		"swarmd/internal/store/pebble/topology_container_sync.go": {
			"WorkspaceReplicationLink",
			"TopologyHostContainerSourceDeployContainer",
			"TopologyRuntimeSourceDeployContainer",
		},
		"swarmd/internal/store/pebble/topology_store.go": {
			"deployment_id",
		},
		"web/src/features/desktop/chat/services/chat-routing.ts": {
			"swarm_v2_",
			"swarm_routed_",
		},
		"web/src/features/desktop/services/session-workspace.ts": {
			"swarm_v2_",
			"swarm_routed_",
		},
		"web/src/features/workspaces/launcher/types/workspace-overview.ts": {
			"session_execution",
		},
	}
	for path, forbiddenMarkers := range checks {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, forbidden := range forbiddenMarkers {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s still contains retired marker %q", path, forbidden)
			}
		}
	}
}
