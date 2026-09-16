package pebblestore

import (
	"path/filepath"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

func TestEnvironments_RestartRecovery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart_recovery.pebble")

	// Phase 1: Open store, save all entities, close store
	now := time.Now().UnixMilli()
	func() {
		store, err := Open(dbPath)
		if err != nil {
			t.Fatalf("phase 1 open store: %v", err)
		}
		defer func() {
			if err := store.Close(); err != nil {
				t.Fatalf("phase 1 close store: %v", err)
			}
		}()

		// 1. Connection
		cs := NewConnectionStore(store)
		conn := environments.Connection{
			ID:             "conn-restart-1",
			AccountScopeID: "acc-rec",
			WorkspaceID:    "ws-rec",
			Name:           "Local Docker",
			Kind:           environments.ConnectionKindLocalDocker,
			Capabilities: environments.ConnectionCapabilities{
				SupportsDocker:      true,
				SupportsDirectMount: true,
			},
			LocalDocker: &environments.LocalDockerConfig{
				SocketPath: "/var/run/docker.sock",
			},
		}
		if _, err := cs.Save(conn); err != nil {
			t.Fatalf("phase 1 save connection: %v", err)
		}

		// 2. Environment
		es := NewEnvironmentStore(store)
		env := environments.Environment{
			ID:                    "env-restart-1",
			AccountScopeID:        "acc-rec",
			WorkspaceID:           "ws-rec",
			Name:                  "Testbench Go",
			Mode:                  environments.EnvironmentModeDeployable,
			Role:                  environments.EnvironmentRoleTesting,
			PreferredConnectionID: "conn-restart-1",
			Container: environments.ContainerDefinition{
				Image: "golang:1.26-alpine",
			},
			Provisioning: environments.WorkspaceProvisioning{
				Strategy: environments.SourceStrategy{
					Kind: environments.SourceStrategyKindLocalMount,
					LocalMount: &environments.LocalMountConfig{
						HostPath:      "/home/dev/repo",
						ContainerPath: "/workspace",
					},
				},
			},
			DeploymentPolicy: environments.DeploymentPolicy{
				Reuse:           true,
				MaxInstances:    3,
				ReleaseBehavior: environments.ReleaseBehaviorRestart,
			},
		}
		if _, err := es.Save(env); err != nil {
			t.Fatalf("phase 1 save environment: %v", err)
		}

		// 3. Deployment with runtime metadata
		ds := NewDeploymentStore(store)
		dep := environments.Deployment{
			ID:             "dep-restart-1",
			AccountScopeID: "acc-rec",
			WorkspaceID:    "ws-rec",
			EnvironmentID:  "env-restart-1",
			ConnectionID:   "conn-restart-1",
			Name:           "Running Testbench 1",
			Status:         environments.DeploymentStatusRunning,
			Health:         environments.HealthStatusHealthy,
			Runtime: environments.RuntimeMetadata{
				ContainerID:        "c_recover_987",
				ProviderResourceID: "pres_987",
				Endpoint:           "http://127.0.0.1:18080",
				AssignedPorts: []environments.AssignedPort{
					{ContainerPort: 8080, HostPort: 18080, Protocol: "tcp", EndpointURL: "http://127.0.0.1:18080"},
				},
				RemoteWorkspacePath: "/workspace",
				RuntimeIP:           "172.17.0.10",
				EngineVersion:       "24.0.7",
			},
		}
		if _, err := ds.Save(dep); err != nil {
			t.Fatalf("phase 1 save deployment: %v", err)
		}

		// 4. Lease
		ls := NewLeaseStore(store)
		lease := environments.DeploymentLease{
			ID:             "lease-restart-1",
			AccountScopeID: "acc-rec",
			WorkspaceID:    "ws-rec",
			DeploymentID:   "dep-restart-1",
			EnvironmentID:  "env-restart-1",
			ConsumerType:   environments.ConsumerTypeSession,
			ConsumerID:     "session-xyz",
			AcquiredAt:     now,
			ExpiresAt:      now + 60000,
		}
		if _, err := ls.AcquireLease(lease); err != nil {
			t.Fatalf("phase 1 acquire lease: %v", err)
		}

		// 5. Workspace Entry & Settings
		ws := NewWorkspaceStore(store)
		wsEntry, err := ws.AddForAccount("acc-rec", "/home/dev/repo", "Repo")
		if err != nil {
			t.Fatalf("phase 1 add workspace: %v", err)
		}
		defaultEnv := "env-restart-1"
		defaultConn := "conn-restart-1"
		_, err = ws.UpdateWorkspaceSettings("acc-rec", wsEntry.WorkspaceID, &defaultEnv, &defaultConn)
		if err != nil {
			t.Fatalf("phase 1 update workspace settings: %v", err)
		}
	}()

	// Phase 2: Re-open store from the same path and verify everything is intact
	func() {
		store, err := Open(dbPath)
		if err != nil {
			t.Fatalf("phase 2 re-open store: %v", err)
		}
		defer func() {
			if err := store.Close(); err != nil {
				t.Fatalf("phase 2 close store: %v", err)
			}
		}()

		// 1. Verify Connection
		cs := NewConnectionStore(store)
		conn, found, err := cs.Get("acc-rec", "ws-rec", "conn-restart-1")
		if err != nil || !found {
			t.Fatalf("phase 2 get connection: found=%v err=%v", found, err)
		}
		if conn.Name != "Local Docker" || conn.Kind != environments.ConnectionKindLocalDocker {
			t.Fatalf("recovered connection mismatch: %+v", conn)
		}

		// 2. Verify Environment
		es := NewEnvironmentStore(store)
		env, found, err := es.Get("acc-rec", "ws-rec", "env-restart-1")
		if err != nil || !found {
			t.Fatalf("phase 2 get environment: found=%v err=%v", found, err)
		}
		if env.PreferredConnectionID != "conn-restart-1" || env.DeploymentPolicy.ReleaseBehavior != environments.ReleaseBehaviorRestart {
			t.Fatalf("recovered environment mismatch: %+v", env)
		}

		// 3. Verify Deployment and Runtime Metadata
		ds := NewDeploymentStore(store)
		dep, found, err := ds.Get("acc-rec", "ws-rec", "dep-restart-1")
		if err != nil || !found {
			t.Fatalf("phase 2 get deployment: found=%v err=%v", found, err)
		}
		if dep.Runtime.ContainerID != "c_recover_987" || dep.Runtime.Endpoint != "http://127.0.0.1:18080" {
			t.Fatalf("recovered deployment runtime mismatch: %+v", dep.Runtime)
		}
		if dep.Status != environments.DeploymentStatusRunning || dep.Health != environments.HealthStatusHealthy {
			t.Fatalf("recovered deployment status mismatch: status=%s health=%s", dep.Status, dep.Health)
		}

		// 4. Verify Active Lease
		ls := NewLeaseStore(store)
		activeLease, found, err := ls.GetActiveLease("acc-rec", "ws-rec", "dep-restart-1")
		if err != nil || !found {
			t.Fatalf("phase 2 get active lease: found=%v err=%v", found, err)
		}
		if activeLease.ID != "lease-restart-1" || !activeLease.Active {
			t.Fatalf("recovered active lease mismatch: %+v", activeLease)
		}

		// 5. Verify Workspace Settings
		ws := NewWorkspaceStore(store)
		wsEntry, found, err := ws.GetForAccount("acc-rec", "/home/dev/repo")
		if err != nil || !found {
			t.Fatalf("phase 2 get workspace entry: found=%v err=%v", found, err)
		}
		if wsEntry.DefaultTestEnvironmentID != "env-restart-1" {
			t.Fatalf("expected DefaultTestEnvironmentID 'env-restart-1', got %q", wsEntry.DefaultTestEnvironmentID)
		}
		if wsEntry.DefaultConnectionID != "conn-restart-1" {
			t.Fatalf("expected DefaultConnectionID 'conn-restart-1', got %q", wsEntry.DefaultConnectionID)
		}

		settings, found, err := ws.GetWorkspaceSettings("acc-rec", wsEntry.WorkspaceID)
		if err != nil || !found {
			t.Fatalf("phase 2 get workspace settings: found=%v err=%v", found, err)
		}
		if settings.DefaultTestEnvironmentID != "env-restart-1" || settings.DefaultConnectionID != "conn-restart-1" {
			t.Fatalf("recovered settings mismatch: %+v", settings)
		}
	}()
}

func TestEnvironments_WorkspaceSettingsUpdates(t *testing.T) {
	store := openEphemeralStore(t)
	ws := NewWorkspaceStore(store)

	entry, err := ws.AddForAccount("acc-s", "/home/user/project", "Project")
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	// Initially empty defaults
	settings, found, err := ws.GetWorkspaceSettings("acc-s", entry.WorkspaceID)
	if err != nil || !found {
		t.Fatalf("get settings: found=%v err=%v", found, err)
	}
	if settings.DefaultTestEnvironmentID != "" || settings.DefaultConnectionID != "" {
		t.Fatalf("expected empty defaults, got: %+v", settings)
	}

	// Update via UpdateWorkspaceSettings
	defaultEnv := "env-100"
	defaultConn := "conn-200"
	updatedEntry, err := ws.UpdateWorkspaceSettings("acc-s", entry.WorkspaceID, &defaultEnv, &defaultConn)
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if updatedEntry.DefaultTestEnvironmentID != "env-100" || updatedEntry.DefaultConnectionID != "conn-200" {
		t.Fatalf("updated entry defaults mismatch: %+v", updatedEntry)
	}

	// Update via UpdateForWorkspaceIDForAccountGuarded
	newEnv := "env-300"
	guardedEntry, err := ws.UpdateForWorkspaceIDForAccountGuarded("acc-s", "user-1", entry.WorkspaceID, WorkspaceCatalogUpdate{
		ExpectedGeneration:       updatedEntry.WorkspaceGeneration,
		DefaultTestEnvironmentID: &newEnv,
	})
	if err != nil {
		t.Fatalf("guarded update settings: %v", err)
	}
	if guardedEntry.DefaultTestEnvironmentID != "env-300" {
		t.Fatalf("guarded update default test env mismatch: %s", guardedEntry.DefaultTestEnvironmentID)
	}
	// DefaultConnectionID should be preserved when nil in update
	if guardedEntry.DefaultConnectionID != "conn-200" {
		t.Fatalf("guarded update default connection was wiped: %s", guardedEntry.DefaultConnectionID)
	}
}

func TestEnvironments_RejectsSecrets(t *testing.T) {
	store := openEphemeralStore(t)
	es := NewEnvironmentStore(store)

	// Environment with secret key in EnvVars
	envWithSecretVar := environments.Environment{
		ID:             "env-sec-var",
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-1",
		Name:           "Leaky Env Var",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "alpine",
			EnvVars: map[string]string{
				"API_KEY": "sk-12345",
			},
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
	}
	if _, err := es.Save(envWithSecretVar); err == nil {
		t.Fatal("expected EnvironmentStore.Save to reject API_KEY in EnvVars")
	}

	// Environment with password key in EnvVars
	envWithPasswordVar := environments.Environment{
		ID:             "env-sec-pass",
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-1",
		Name:           "Leaky Password Var",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "alpine",
			EnvVars: map[string]string{
				"DB_PASSWORD": "supersecretpassword",
			},
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
	}
	if _, err := es.Save(envWithPasswordVar); err == nil {
		t.Fatal("expected EnvironmentStore.Save to reject DB_PASSWORD in EnvVars")
	}

	// Environment with secret in Labels
	envWithSecretLabel := environments.Environment{
		ID:             "env-sec-lbl",
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-1",
		Name:           "Leaky Label",
		Mode:           environments.EnvironmentModeDeployable,
		Role:           environments.EnvironmentRoleTesting,
		Container: environments.ContainerDefinition{
			Image: "alpine",
		},
		Labels: map[string]string{
			"private_key_ref": "key-data",
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
	}
	if _, err := es.Save(envWithSecretLabel); err == nil {
		t.Fatal("expected EnvironmentStore.Save to reject private_key in Labels")
	}

	// Direct AssertNoSecretsRaw check for payloads
	badConnectionPayload := []byte(`{"id":"c1","account_scope_id":"a1","workspace_id":"w1","kind":"ssh","ssh":{"private_key":"PEM..."}}`)
	if err := environments.AssertNoSecretsRaw(badConnectionPayload); err == nil {
		t.Fatal("expected AssertNoSecretsRaw to reject SSH private_key in connection payload")
	}

	badDeploymentPayload := []byte(`{"id":"d1","account_scope_id":"a1","workspace_id":"w1","status":"running","runtime":{"access_token":"bearer-abc"}}`)
	if err := environments.AssertNoSecretsRaw(badDeploymentPayload); err == nil {
		t.Fatal("expected AssertNoSecretsRaw to reject access_token in runtime deployment payload")
	}
}

func TestEnvironments_IsolationMultiEntity(t *testing.T) {
	store := openEphemeralStore(t)
	cs := NewConnectionStore(store)
	es := NewEnvironmentStore(store)
	ds := NewDeploymentStore(store)
	ls := NewLeaseStore(store)

	accounts := []string{"acc-alpha", "acc-beta"}
	workspaces := []string{"ws-1", "ws-2"}

	for _, acc := range accounts {
		for _, ws := range workspaces {
			connID := "conn-" + acc + "-" + ws
			envID := "env-" + acc + "-" + ws
			depID := "dep-" + acc + "-" + ws
			leaseID := "lease-" + acc + "-" + ws

			if _, err := cs.Save(environments.Connection{
				ID:             connID,
				AccountScopeID: acc,
				WorkspaceID:    ws,
				Name:           "Conn " + acc + " " + ws,
				Kind:           environments.ConnectionKindLocalDocker,
			}); err != nil {
				t.Fatalf("save conn %s: %v", connID, err)
			}

			if _, err := es.Save(environments.Environment{
				ID:             envID,
				AccountScopeID: acc,
				WorkspaceID:    ws,
				Name:           "Env " + acc + " " + ws,
				Mode:           environments.EnvironmentModeDeployable,
				Role:           environments.EnvironmentRoleDevelopment,
				Container: environments.ContainerDefinition{
					Image: "golang:alpine",
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
			}); err != nil {
				t.Fatalf("save env %s: %v", envID, err)
			}

			if _, err := ds.Save(environments.Deployment{
				ID:             depID,
				AccountScopeID: acc,
				WorkspaceID:    ws,
				EnvironmentID:  envID,
				ConnectionID:   connID,
				Name:           "Dep " + acc + " " + ws,
				Status:         environments.DeploymentStatusRunning,
			}); err != nil {
				t.Fatalf("save dep %s: %v", depID, err)
			}

			if _, err := ls.AcquireLease(environments.DeploymentLease{
				ID:             leaseID,
				AccountScopeID: acc,
				WorkspaceID:    ws,
				DeploymentID:   depID,
				EnvironmentID:  envID,
				ConsumerType:   environments.ConsumerTypeSession,
				ConsumerID:     "sess-" + acc,
				AcquiredAt:     time.Now().UnixMilli(),
			}); err != nil {
				t.Fatalf("acquire lease %s: %v", leaseID, err)
			}
		}
	}

	// Verify each (acc, ws) scope sees strictly its own entities
	for _, acc := range accounts {
		for _, ws := range workspaces {
			conns, err := cs.List(acc, ws, 100)
			if err != nil || len(conns) != 1 {
				t.Fatalf("expected 1 conn for (%s, %s), got %d (err=%v)", acc, ws, len(conns), err)
			}
			if conns[0].ID != "conn-"+acc+"-"+ws {
				t.Fatalf("conn ID mismatch: %s", conns[0].ID)
			}

			envs, err := es.List(acc, ws, 100)
			if err != nil || len(envs) != 1 {
				t.Fatalf("expected 1 env for (%s, %s), got %d (err=%v)", acc, ws, len(envs), err)
			}
			if envs[0].ID != "env-"+acc+"-"+ws {
				t.Fatalf("env ID mismatch: %s", envs[0].ID)
			}

			deps, err := ds.List(acc, ws, 100)
			if err != nil || len(deps) != 1 {
				t.Fatalf("expected 1 dep for (%s, %s), got %d (err=%v)", acc, ws, len(deps), err)
			}
			if deps[0].ID != "dep-"+acc+"-"+ws {
				t.Fatalf("dep ID mismatch: %s", deps[0].ID)
			}

			leases, err := ls.ListActive(acc, ws, 100)
			if err != nil || len(leases) != 1 {
				t.Fatalf("expected 1 lease for (%s, %s), got %d (err=%v)", acc, ws, len(leases), err)
			}
			if leases[0].ID != "lease-"+acc+"-"+ws {
				t.Fatalf("lease ID mismatch: %s", leases[0].ID)
			}
		}
	}
}
