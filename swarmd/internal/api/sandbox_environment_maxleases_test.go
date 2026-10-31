package api

import (
	"fmt"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: update_environment that changes the definition and max_leases with
// wait_seconds keeps max_leases once the build (which here finishes at once)
// is ready, every time, locally and over the link. Threat: the intermittent
// loss of max_leases (read back 0 with status ready) when the build's status
// write stored a record read before max_leases was applied. Boundary:
// Server.ManageFolderEnvironment -> UpdateSandboxEnvironment ->
// DefineSandboxEnvironment -> SandboxEnvironmentStore.DefineWithMaxLeases and
// the build goroutine's Finish. Repetition exercises the window the flake hit;
// the deterministic ordering is proven in the store's race test.
func TestUpdateEnvironmentKeepsMaxLeasesWhenBuildFinishesAtOnce(t *testing.T) {
	f := newLinkedEnvironmentFixture(t)
	for i := 0; i < 20; i++ {
		machine := ""
		if i%2 == 1 {
			machine = "box"
		}
		max := i%5 + 1
		out, err := f.call(t, machine, tool.FolderEnvironmentUpdate, map[string]any{
			"workspace_path": f.folder, "base_image": "debian:12",
			"setup":      []any{"apt-get install -y git", fmt.Sprintf("echo run-%d", i)},
			"env":        map[string]any{"APP_MODE": "dev"},
			"max_leases": float64(max), "wait_seconds": float64(10),
		})
		if err != nil {
			t.Fatalf("iteration %d (machine %q): %v", i, machine, err)
		}
		view := out["environment"].(map[string]any)
		if view["status"] != pebblestore.SandboxEnvironmentReady || out["build_started"] != true || view["max_leases"] != float64(max) {
			t.Fatalf("iteration %d (machine %q) view: %v", i, machine, out)
		}
		stored := f.stored(t)
		if stored.MaxLeases != max || stored.Status != pebblestore.SandboxEnvironmentReady || stored.ReadyHash != stored.Hash ||
			stored.Definition.BaseImage != "debian:12" || len(stored.Definition.Setup) != 2 || stored.Definition.Env["APP_MODE"] != "dev" {
			t.Fatalf("iteration %d (machine %q) stored: %+v", i, machine, stored)
		}
	}
}
