package tool

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: manage_environments must persist the same typed build contract for
// top-level create/update and nested imports, and reject unknown source fields
// without changing the existing definition. Real tool/store dispatch is the
// narrowest layer proving strict schema wiring and saved postconditions.
func TestManagedBuildDefinitionRoundTrip(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	definition := map[string]any{"product": map[string]any{"workspace_id": "product", "workspace_generation": 1, "commit": strings.Repeat("a", 40)}, "recipe": map[string]any{"workspace_id": "recipe", "workspace_generation": 1, "commit": strings.Repeat("b", 40)}, "recipe_directory": "recipe", "recipe_file": "recipe/Containerfile"}
	args := map[string]any{"action": "create", "id": "managed-build-env", "name": "Managed image", "mode": "deployable", "role": "testing", "build": definition,
		"container":         map[string]any{"image": "managed-build", "command": []string{"/sbin/init"}, "rootless_systemd": map[string]any{"cgroup_namespace": "private", "network": "slirp4netns", "pids_limit": 1024}},
		"provisioning":      map[string]any{"strategy": map[string]any{"kind": "registry_image", "registry_image": map[string]any{"image": "managed-build", "pull_policy": "never"}}},
		"deployment_policy": map[string]any{"reuse": true, "max_instances": 1, "release_behavior": "none", "idle_timeout_seconds": 0}}
	created := strictEnvironmentCall(t, h, args)
	if created.Build == nil || created.Build.Product.Commit != strings.Repeat("a", 40) {
		t.Fatal("build discarded")
	}
	updated := strictEnvironmentCall(t, h, map[string]any{"action": "update", "id": created.ID, "build": definition})
	if !reflect.DeepEqual(created.Build, updated.Build) {
		t.Fatal("build update changed fields")
	}
	raw, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	imported := strictEnvironmentCall(t, h, map[string]any{"action": "import", "json": string(raw)})
	if !reflect.DeepEqual(created.Build, imported.Build) {
		t.Fatal("build import lost fields")
	}
	definition["host_path"] = "/untrusted"
	if _, err := execTool(t, h, "manage_environments", map[string]any{"action": "update", "id": created.ID, "build": definition}); err == nil {
		t.Fatal("raw path accepted")
	}
	stored, found, err := h.envStore.Get(created.AccountScopeID, created.WorkspaceID, created.ID)
	if err != nil || !found || !reflect.DeepEqual(stored.Build, created.Build) {
		t.Fatal("rejected update mutated build")
	}
	schema := manageEnvironmentsDefinition().Parameters["properties"].(map[string]any)
	want := environmentValueSchema(reflect.TypeOf(environments.ImageBuildDefinition{}))
	if !reflect.DeepEqual(schema["build"], want) {
		t.Fatal("build schema drift")
	}
}
