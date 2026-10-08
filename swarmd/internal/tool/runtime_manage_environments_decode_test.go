package tool

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

func strictEnvironmentFixture(t *testing.T) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(`{
		"name":"Clean room", "mode":"deployable", "role":"testing",
		"container":{"image":"alpine:3.21","command":["sleep"],"args":["infinity"],"env_vars":{"MODE":"test"},"exposed_ports":[{"container_port":8080,"host_port":0,"protocol":"tcp"}],"privileged":false,"user":"1000","working_dir":"/workspace","setup_commands":["echo ready"]},
		"provisioning":{"strategy":{"kind":"local_mount","local_mount":{"container_path":"/workspace","read_only":true}},"container_working_dir":"/workspace"},
		"deployment_policy":{"reuse":false,"max_instances":2,"release_behavior":"recreate","idle_timeout_seconds":60},
		"health_check":{"test":["CMD","true"],"http_path":"/health","http_port":8080,"interval_seconds":10,"timeout_seconds":2,"retries":3,"start_period_seconds":1},
		"resources":{"cpu_limit":"2","memory_limit":"512m","gpu_required":false,"gpu_count":0},
		"labels":{"purpose":"test"}
	}`), &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func strictEnvironmentCall(t *testing.T, h *toolTestHarness, args map[string]any) environments.Environment {
	t.Helper()
	out, err := execTool(t, h, "manage_environments", args)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Environment environments.Environment `json:"environment"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	return response.Environment
}

// Purpose: canonical definition fields must survive create/update/export/import.
// executeManageEnvironments and EnvironmentStore own this contract; dispatch with
// a temporary Pebble store proves persisted values and scope rebinding without
// invoking a container provider, the narrowest layer for this regression.
func TestEnvironmentStrictRoundTrip(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	fields := strictEnvironmentFixture(t)
	fields["action"], fields["id"] = "create", "strict-env"
	created := strictEnvironmentCall(t, h, fields)
	for _, action := range []string{"update", "create-object", "import-object", "import-json"} {
		t.Run(action, func(t *testing.T) {
			args := strictEnvironmentFixture(t)
			if action == "update" {
				args["action"], args["id"] = "update", created.ID
			} else {
				out, err := execTool(t, h, "manage_environments", map[string]any{"action": "export", "id": created.ID})
				if err != nil {
					t.Fatal(err)
				}
				var exported map[string]any
				if err := json.Unmarshal([]byte(out), &exported); err != nil {
					t.Fatal(err)
				}
				args = map[string]any{"action": "import"}
				if action == "import-json" {
					args["json"] = exported["json"]
				} else {
					object := exported["environment"].(map[string]any)
					object["account_scope_id"], object["workspace_id"] = "foreign-account", "foreign-workspace"
					args["environment"] = object
					if action == "create-object" {
						args["action"] = "create"
					}
				}
			}
			got := strictEnvironmentCall(t, h, args)
			got.CreatedAt, got.UpdatedAt = created.CreatedAt, created.UpdatedAt
			if !reflect.DeepEqual(got, created) {
				t.Fatalf("definition lost or changed fields: got %#v, want %#v", got, created)
			}
			stored, found, err := h.envStore.Get(created.AccountScopeID, created.WorkspaceID, created.ID)
			if err != nil || !found {
				t.Fatalf("persisted definition: found=%v err=%v", found, err)
			}
			stored.CreatedAt, stored.UpdatedAt = created.CreatedAt, created.UpdatedAt
			if !reflect.DeepEqual(stored, created) {
				t.Fatal("persisted definition differs")
			}
		})
	}
}

// Purpose: malformed definition input must fail before Save or default-setting
// side effects. Dispatch plus the real store proves no partial persistence for
// every mutation path, rather than only checking a decoder error/status.
func TestEnvironmentStrictRejectionNoPersistence(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	seed := strictEnvironmentFixture(t)
	seed["action"], seed["id"] = "create", "strict-existing"
	before := strictEnvironmentCall(t, h, seed)
	lease, err := h.leaseStore.AcquireLease(environments.DeploymentLease{
		ID: "strict-lease", AccountScopeID: before.AccountScopeID, WorkspaceID: before.WorkspaceID,
		DeploymentID: "strict-deployment", EnvironmentID: before.ID,
		ConsumerType: environments.ConsumerTypeSession, ConsumerID: h.scope.SessionID,
		AcquiredAt: time.Now().UnixMilli(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		field string
		value any
		path  string
	}{
		{"container", map[string]any{"env": map[string]any{"MODE": "lost"}}, "container.env"},
		{"container", map[string]any{"env_vars": map[string]any{"MODE": 5}}, "container.env_vars.MODE"},
		{"container", map[string]any{"exposed_ports": []any{map[string]any{"container_port": "8080"}}}, "container.exposed_ports[0].container_port"},
		{"container", `{"image":"alpine"}`, "container"},
		{"container", nil, "container"},
		{"provisioning", map[string]any{}, "provisioning"},
		{"provisioning", map[string]any{"strategy": map[string]any{"kind": "unsupported"}}, "provisioning"},
		{"provisioning", map[string]any{"strategy": map[string]any{"kind": "local_mount", "local_mount": map[string]any{"container_path": "/workspace", "readonly": true}}}, "provisioning.strategy.local_mount.readonly"},
		{"deployment_policy", map[string]any{"reuse": "true"}, "deployment_policy.reuse"},
		{"deployment_policy", map[string]any{"typo": true}, "deployment_policy.typo"},
		{"health_check", map[string]any{"retries": "three"}, "health_check.retries"},
		{"health_check", map[string]any{"typo": true}, "health_check.typo"},
		{"resources", map[string]any{"cpu_limit": 2}, "resources.cpu_limit"},
		{"resources", map[string]any{"systemd": true}, "resources.systemd"},
		{"labels", map[string]any{"purpose": false}, "labels.purpose"},
	}
	for _, action := range []string{"create", "update", "create-object", "import-object", "import-json"} {
		for _, tc := range cases {
			t.Run(action+"/"+tc.path, func(t *testing.T) {
				fields := strictEnvironmentFixture(t)
				fields[tc.field] = tc.value
				fields["id"] = before.ID
				args := fields
				switch action {
				case "create", "update":
					args["action"] = action
					args["set_default_test"] = true
				case "create-object":
					args = map[string]any{"action": "create", "environment": fields}
				case "import-object":
					args = map[string]any{"action": "import", "environment": fields}
				case "import-json":
					raw, err := json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
					args = map[string]any{"action": "import", "json": string(raw)}
				}
				_, err := execTool(t, h, "manage_environments", args)
				if err == nil || !strings.Contains(err.Error(), tc.path) {
					t.Fatalf("want error containing %q, got %v", tc.path, err)
				}
				got, found, err := h.envStore.Get(before.AccountScopeID, before.WorkspaceID, before.ID)
				if err != nil || !found || !reflect.DeepEqual(got, before) {
					t.Fatalf("rejected mutation changed persisted state: found=%v err=%v", found, err)
				}
				settings, foundSettings, settingsErr := h.wsStore.GetWorkspaceSettings(before.AccountScopeID, before.WorkspaceID)
				if settingsErr != nil || (foundSettings && settings.DefaultTestEnvironmentID != "") {
					t.Fatalf("rejected mutation changed default: %v", settingsErr)
				}
				active, found, err := h.leaseStore.GetActiveLease(before.AccountScopeID, before.WorkspaceID, lease.DeploymentID)
				if err != nil || !found || !reflect.DeepEqual(active, lease) {
					t.Fatalf("rejected definition mutation changed working lease: found=%v err=%v", found, err)
				}
			})
		}
	}
	for _, value := range []any{"", "{", "{} {}", "null", "[]", 42, `{"container":{"image":"first","image":"second"}}`} {
		if _, err := execTool(t, h, "manage_environments", map[string]any{"action": "import", "json": value}); err == nil {
			t.Fatalf("accepted malformed import %v", value)
		}
	}
}

// Purpose: applyEnvironmentUpdates must not alias caller maps/slices on failure;
// parser-level assertions are the narrowest way to expose aliasing hidden by a
// store that already returns copies. Also retain omitted-provisioning defaults
// and ports alias behavior, while rejecting supplied invalid provisioning.
func TestEnvironmentStrictAtomicUpdatesAndDefaults(t *testing.T) {
	current, err := parseEnvironmentInput(strictEnvironmentFixture(t), "account", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	before := current.Clone()
	_, err = applyEnvironmentUpdates(current, map[string]any{
		"container": map[string]any{"env_vars": map[string]any{"MODE": "changed"}, "exposed_ports": []any{map[string]any{"container_port": 9000}}},
		"resources": map[string]any{"unknown": true},
	})
	if err == nil || !reflect.DeepEqual(current, *before) {
		t.Fatal("failed update mutated caller-owned state")
	}
	defaults, err := parseEnvironmentInput(map[string]any{"name": "default", "image": "alpine:3.21"}, "account", "workspace")
	if err != nil || defaults.Provisioning.Strategy.LocalMount == nil || defaults.Provisioning.Strategy.LocalMount.ContainerPath != "/app" || defaults.DeploymentPolicy.MaxInstances != 1 {
		t.Fatalf("omitted defaults changed: %#v %v", defaults, err)
	}
	updated, err := applyEnvironmentUpdates(current, map[string]any{"ports": []any{map[string]any{"container_port": 9090}}})
	if err != nil || len(updated.Container.ExposedPorts) != 1 || updated.Container.ExposedPorts[0].ContainerPort != 9090 {
		t.Fatalf("ports alias lost: %#v %v", updated, err)
	}
	for _, ports := range []any{"8080", []any{map[string]any{"port": 8080}}, []any{map[string]any{"container_port": "8080"}}} {
		if _, err := applyEnvironmentUpdates(current, map[string]any{"ports": ports}); err == nil || !strings.Contains(err.Error(), "ports") {
			t.Fatalf("invalid ports accepted: %v", err)
		}
	}
	if _, err := applyEnvironmentUpdates(current, map[string]any{"provisioning": map[string]any{}}); err == nil {
		t.Fatal("empty provisioning inherited old strategy")
	}
}

// Purpose: model-visible schema must expose canonical nested keys and reject
// typos, not advertise opaque JSON strings. The definition factory is the
// narrowest registration boundary for schema assertions.
func TestEnvironmentStrictSchema(t *testing.T) {
	props := manageEnvironmentsDefinition().Parameters["properties"].(map[string]any)
	for _, section := range []string{"container", "provisioning", "deployment_policy", "health_check", "resources", "environment"} {
		schema := props[section].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false || len(schema["properties"].(map[string]any)) == 0 {
			t.Fatalf("opaque or permissive schema for %s", section)
		}
	}
	container := props["container"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"env_vars", "exposed_ports", "working_dir", "setup_commands"} {
		if container[key] == nil {
			t.Fatalf("missing canonical container field %s", key)
		}
	}
}

// Purpose: the new typed runtime must survive real tool/store export-import and
// update paths, while bad/unknown isolation options fail before persistence.
// This extends the existing strict decoder regression at its production boundary.
func TestEnvironmentRootlessSystemdRoundTrip(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	fields := strictEnvironmentFixture(t)
	fields["action"], fields["id"] = "create", "rootless-env"
	fields["container"].(map[string]any)["rootless_systemd"] = map[string]any{"cgroup_namespace": "private", "network": "slirp4netns", "pids_limit": 1024}
	fields["provisioning"] = map[string]any{"strategy": map[string]any{"kind": "registry_image", "registry_image": map[string]any{"image": "alpine:3.21", "pull_policy": "never"}}}
	created := strictEnvironmentCall(t, h, fields)
	if created.Container.RootlessSystemd == nil || created.Container.RootlessSystemd.PidsLimit != 1024 {
		t.Fatal("runtime lost on create")
	}
	out, err := execTool(t, h, "manage_environments", map[string]any{"action": "export", "id": created.ID})
	if err != nil {
		t.Fatal(err)
	}
	var exported map[string]any
	if err = json.Unmarshal([]byte(out), &exported); err != nil {
		t.Fatal(err)
	}
	imported := strictEnvironmentCall(t, h, map[string]any{"action": "import", "json": exported["json"]})
	if !reflect.DeepEqual(imported.Container, created.Container) {
		t.Fatal("runtime lost on export/import")
	}
	for _, bad := range []map[string]any{
		{"cgroup_namespace": "host", "network": "slirp4netns", "pids_limit": 1024},
		{"cgroup_namespace": "private", "network": "host", "pids_limit": 1024},
		{"cgroup_namespace": "private", "network": "slirp4netns", "pids_limit": 0},
		{"cgroup_namespace": "private", "network": "slirp4netns", "pids_limit": 1.5},
		{"cgroup_namespace": "private", "network": "slirp4netns", "pids_limit": 1024, "flags": []any{"--privileged"}},
	} {
		_, err = execTool(t, h, "manage_environments", map[string]any{"action": "update", "id": created.ID, "container": map[string]any{"rootless_systemd": bad}})
		if err == nil {
			t.Fatalf("unsafe runtime accepted: %#v", bad)
		}
		stored, found, err := h.envStore.Get(created.AccountScopeID, created.WorkspaceID, created.ID)
		if err != nil || !found || !reflect.DeepEqual(stored.Container, created.Container) {
			t.Fatal("invalid update changed persisted definition")
		}
	}
}

// Purpose: local Podman connection creation must remain account/workspace-scoped
// and cannot accept capability claims, sockets, remote targets or arbitrary flags.
// Real tool dispatch/store reads prove rejection has no mutation side effect.
func TestLocalPodmanConnectionRoundTrip(t *testing.T) {
	h := setupEnvironmentsToolHarness(t)
	out, err := execTool(t, h, "manage_connections", map[string]any{"action": "create", "id": "podman-local", "name": "Rootless local", "kind": "local_podman"})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Connection environments.Connection `json:"connection"`
	}
	if err = json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	c := response.Connection
	_, err = execTool(t, h, "manage_connections", map[string]any{"action": "create", "id": c.ID, "name": "replacement", "kind": "local_docker"})
	if err == nil {
		t.Fatal("create overwrote Podman connection engine")
	}
	if c.Kind != environments.ConnectionKindLocalPodman || c.Capabilities.SupportsPodman || c.Capabilities.RootlessSystemd {
		t.Fatalf("unverified claims: %+v", c)
	}
	for _, bad := range []string{"docker_host", "host", "socket_path", "capabilities", "flags"} {
		_, err := execTool(t, h, "manage_connections", map[string]any{"action": "update", "id": c.ID, bad: "unsupported", "name": "changed"})
		if err == nil {
			t.Fatalf("accepted %s", bad)
		}
		out, err = execTool(t, h, "manage_connections", map[string]any{"action": "get", "id": c.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(out), &response); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(c, response.Connection) {
			t.Fatal("invalid update changed connection")
		}
	}
}

// Purpose: strict definition decoding must retain explicit frontend intent through
// nested create/import and top-level updates without silently dropping fields or
// mutating the original on rejection. The decoder is the narrow input boundary;
// no schema/prompt changes or lifecycle execution are needed to prove it.
func TestEnvironmentFrontendDecode(t *testing.T) {
	endpoint := map[string]any{"id": "web", "name": "Web", "container_port": 8080, "scheme": "http", "health_path": "/health"}
	fields := strictEnvironmentFixture(t)
	fields["frontend_endpoints"] = []any{endpoint}
	created, err := parseEnvironmentInput(map[string]any{"environment": fields}, "account", "workspace")
	if err != nil || len(created.FrontendEndpoints) != 1 {
		t.Fatalf("nested create lost frontend: %+v %v", created, err)
	}
	imported, err := decodeEnvironmentImport(map[string]any{"environment": fields})
	if err != nil || !reflect.DeepEqual(imported.FrontendEndpoints, created.FrontendEndpoints) {
		t.Fatalf("nested import lost frontend: %+v %v", imported, err)
	}
	args := map[string]any{"frontend_endpoints": []any{}}
	if err := validateEnvironmentDefinitionArgs("update", args); err != nil {
		t.Fatal(err)
	}
	updated, err := applyEnvironmentUpdates(created, args)
	if err != nil || len(updated.FrontendEndpoints) != 0 || len(created.FrontendEndpoints) != 1 {
		t.Fatal("explicit empty update did not replace independently")
	}
	if _, err := parseEnvironmentInput(map[string]any{"environment": fields, "frontend_endpoints": []any{}}, "account", "workspace"); err == nil {
		t.Fatal("mixed frontend authorities admitted")
	}
	for _, invalid := range []any{nil, "[]", []any{map[string]any{"unknown": true}}} {
		if _, err := applyEnvironmentUpdates(created, map[string]any{"frontend_endpoints": invalid}); err == nil || len(created.FrontendEndpoints) != 1 || created.FrontendEndpoints[0].ID != "web" {
			t.Fatal("malformed frontend accepted or mutated source")
		}
	}
}
