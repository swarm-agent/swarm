package pebblestore

import (
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
)

func TestEnvironmentStore_CRUD(t *testing.T) {
	store := openEphemeralStore(t)
	es := NewEnvironmentStore(store)

	envDef := environments.Environment{
		ID:                    "env-test-1",
		AccountScopeID:        "acc-test",
		WorkspaceID:           "ws-test",
		Name:                  "Go 1.26 Testbench",
		Description:           "Containerized Go test environment",
		Mode:                  environments.EnvironmentModeDeployable,
		Role:                  environments.EnvironmentRoleTesting,
		PreferredConnectionID: "conn-local-1",
		Container: environments.ContainerDefinition{
			Image:      "golang:1.26",
			WorkingDir: "/workspace",
			ExposedPorts: []environments.PortMapping{
				{ContainerPort: 8080, HostPort: 18080, Protocol: "tcp"},
			},
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{
					HostPath:      "/home/user/project",
					ContainerPath: "/workspace",
					ReadOnly:      false,
				},
			},
		},
		DeploymentPolicy: environments.DeploymentPolicy{
			Reuse:           true,
			MaxInstances:    2,
			ReleaseBehavior: environments.ReleaseBehaviorRestart,
		},
	}

	saved, err := es.Save(envDef)
	if err != nil {
		t.Fatalf("save environment: %v", err)
	}
	if saved.CreatedAt <= 0 || saved.UpdatedAt <= 0 {
		t.Fatalf("expected positive timestamps, got created=%d updated=%d", saved.CreatedAt, saved.UpdatedAt)
	}

	got, found, err := es.Get("acc-test", "ws-test", "env-test-1")
	if err != nil || !found {
		t.Fatalf("get environment: found=%v, err=%v", found, err)
	}
	if got.Name != "Go 1.26 Testbench" || got.PreferredConnectionID != "conn-local-1" {
		t.Fatalf("unexpected environment: %+v", got)
	}
	if got.DeploymentPolicy.ReleaseBehavior != environments.ReleaseBehaviorRestart {
		t.Fatalf("unexpected release behavior: %s", got.DeploymentPolicy.ReleaseBehavior)
	}

	// Update
	got.DeploymentPolicy.MaxInstances = 5
	updated, err := es.Save(got)
	if err != nil {
		t.Fatalf("update environment: %v", err)
	}
	if updated.CreatedAt != saved.CreatedAt {
		t.Fatalf("created_at altered on update: %d != %d", updated.CreatedAt, saved.CreatedAt)
	}
	if updated.DeploymentPolicy.MaxInstances != 5 {
		t.Fatalf("updated field mismatch: %d", updated.DeploymentPolicy.MaxInstances)
	}

	// List
	list, err := es.List("acc-test", "ws-test", 100)
	if err != nil {
		t.Fatalf("list environments: %v", err)
	}
	if len(list) != 1 || list[0].ID != "env-test-1" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Delete
	deleted, err := es.Delete("acc-test", "ws-test", "env-test-1")
	if err != nil || !deleted {
		t.Fatalf("delete environment: deleted=%v, err=%v", deleted, err)
	}

	_, found, err = es.Get("acc-test", "ws-test", "env-test-1")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if found {
		t.Fatal("expected environment to be deleted")
	}
}

func TestEnvironmentStore_Isolation(t *testing.T) {
	store := openEphemeralStore(t)
	es := NewEnvironmentStore(store)

	env := environments.Environment{
		ID:             "env-iso-1",
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-1",
		Name:           "Isolated Env",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleDevelopment,
		Container: environments.ContainerDefinition{
			Image: "alpine:latest",
		},
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{
					HostPath:      "/tmp/host",
					ContainerPath: "/workspace",
				},
			},
		},
		DeploymentPolicy: environments.DeploymentPolicy{
			ReleaseBehavior: environments.ReleaseBehaviorNone,
		},
	}

	if _, err := es.Save(env); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Cross-account lookup
	_, found, err := es.Get("acc-2", "ws-1", "env-iso-1")
	if err != nil {
		t.Fatalf("get cross-account: %v", err)
	}
	if found {
		t.Fatal("cross-account leakage detected")
	}

	// Cross-workspace lookup
	_, found, err = es.Get("acc-1", "ws-2", "env-iso-1")
	if err != nil {
		t.Fatalf("get cross-workspace: %v", err)
	}
	if found {
		t.Fatal("cross-workspace leakage detected")
	}

	// List cross-account
	list, err := es.List("acc-2", "ws-1", 10)
	if err != nil {
		t.Fatalf("list cross-account: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list cross-account, got %d", len(list))
	}
}

func TestEnvironmentStore_Validation(t *testing.T) {
	store := openEphemeralStore(t)
	es := NewEnvironmentStore(store)

	// Missing container image
	badEnv := environments.Environment{
		ID:             "env-bad",
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-1",
		Name:           "Bad Env",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Provisioning: environments.WorkspaceProvisioning{
			Strategy: environments.SourceStrategy{
				Kind: environments.SourceStrategyKindLocalMount,
				LocalMount: &environments.LocalMountConfig{
					HostPath:      "/tmp/host",
					ContainerPath: "/workspace",
				},
			},
		},
	}
	if _, err := es.Save(badEnv); err == nil {
		t.Fatal("expected validation error for missing container image")
	}
}
