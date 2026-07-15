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
		"swarmd/internal/api/server_routes.go": {
			"/v1/deploy/container",
			"/v2/sessions",
		},
		"swarmd/internal/store/pebble/keys.go": {
			"deploy/container/",
			"deploy/container_by_host/",
			"session_route/",
			"workspace/replication/",
		},
		"swarmd/internal/store/pebble/topology_container_sync.go": {
			"WorkspaceReplicationLink",
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
