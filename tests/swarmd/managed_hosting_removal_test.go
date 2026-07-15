package swarmd_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagedHostingResidueRemoved(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))

	for _, path := range []string{
		"scripts/diagnose-remote-deploy-live-ui.mjs",
		"scripts/diagnose-remote-deploy-live.sh",
		"tests/swarmd/remote_deploy_e2e.sh",
		"tests/swarmd/remote_deploy_recovery_e2e.sh",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			t.Errorf("retired managed-hosting artifact remains: %s", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}

	checks := map[string][]string{
		"scripts/lib-lane.sh": {
			"remote_deploy_enabled",
			"remote_deploy_session_id",
			"remote_deploy_host_api_base_url",
			"remote_deploy_sync_enabled",
			"managed_host_sync_",
		},
		"scripts/check-daemon-storage-paths.sh": {
			"swarmd/internal/remotedeploy",
		},
		"docs/swarm-harness-vm.md": {
			"remote_deploy_e2e.sh",
		},
		"tests/swarmd/live_prod_update_e2e.sh": {
			"remote_deploy_enabled",
			"remote_deploy_session_id",
			"remote_deploy_host_api_base_url",
			"remote_deploy_sync_enabled",
		},
		"tests/swarmd/local_replicate_e2e.sh": {
			"remote_deploy_enabled",
			"remote_deploy_session_id",
			"remote_deploy_host_api_base_url",
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
