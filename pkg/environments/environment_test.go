package environments

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEnvironment_Valid(t *testing.T) {
	env := &Environment{
		ID:                    "env-dev-1",
		AccountScopeID:        "acct-1",
		WorkspaceID:           "ws-1",
		Name:                  "Go Development Container",
		Description:           "Standard Go 1.26 container with build tools",
		Mode:                  EnvironmentModeDeployable,
		Role:                  EnvironmentRoleDevelopment,
		PreferredConnectionID: "conn-local-1",
		Container: ContainerDefinition{
			Image:      "golang:1.26-bookworm",
			Command:    []string{"/bin/sh", "-c"},
			Args:       []string{"tail -f /dev/null"},
			EnvVars:    map[string]string{"GOPROXY": "direct", "CGO_ENABLED": "1"},
			Privileged: false,
			User:       "1000:1000",
			WorkingDir: "/workspace",
			ExposedPorts: []PortMapping{
				{ContainerPort: 8080, HostPort: 18080, Protocol: "tcp"},
			},
		},
		Provisioning: WorkspaceProvisioning{
			Strategy: SourceStrategy{
				Kind: SourceStrategyKindLocalMount,
				LocalMount: &LocalMountConfig{
					ContainerPath: "/workspace",
				},
			},
			ContainerWorkingDir: "/workspace",
		},
		DeploymentPolicy: DeploymentPolicy{
			Reuse:              true,
			MaxInstances:       2,
			ReleaseBehavior:    ReleaseBehaviorRestart,
			IdleTimeoutSeconds: 3600,
		},
		HealthCheck: &HealthCheck{
			Test:            []string{"CMD", "go", "version"},
			IntervalSeconds: 15,
			TimeoutSeconds:  5,
			Retries:         3,
		},
		Resources: &ResourceRequirements{
			CPULimit:    "4.0",
			MemoryLimit: "8Gi",
			GPURequired: false,
		},
		Labels: map[string]string{
			"team": "core",
			"tier": "dev",
		},
		CreatedAt: 1700000000000,
		UpdatedAt: 1700000000000,
	}

	if err := env.Validate(); err != nil {
		t.Fatalf("expected valid environment, got: %v", err)
	}

	// JSON Roundtrip
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var roundtrip Environment
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if roundtrip.ID != env.ID || roundtrip.Container.Image != env.Container.Image {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", roundtrip, env)
	}
	if roundtrip.DeploymentPolicy.ReleaseBehavior != ReleaseBehaviorRestart {
		t.Fatalf("release behavior mismatch: got %s", roundtrip.DeploymentPolicy.ReleaseBehavior)
	}
}

// Invariant Test: Verify that Environment strictly contains NO ephemeral runtime state.
func TestEnvironment_NoEphemeralRuntimeFields(t *testing.T) {
	forbiddenRuntimeKeywords := []string{
		"container_id",
		"containerid",
		"provider_resource",
		"runtime_ip",
		"runtimeip",
		"assigned_port",
		"assignedport",
		"endpoint",
		"live_status",
		"status",
		"health_status",
		"pid",
		"process",
	}

	envType := reflect.TypeOf(Environment{})
	checkNoForbiddenFields(t, envType, forbiddenRuntimeKeywords, "Environment")
}

// Invariant Test: Verify that domain structs contain NO secret or credential fields.
func TestDomainTypes_NoSecretFields(t *testing.T) {
	forbiddenSecretKeywords := []string{
		"password",
		"passwd",
		"privatekey",
		"private_key",
		"secret",
		"token",
		"apikey",
		"api_key",
		"credential",
	}

	typesToCheck := []any{
		Connection{},
		ConnectionCapabilities{},
		LocalDockerConfig{},
		SSHConfig{},
		Environment{},
		ContainerDefinition{},
		DeploymentPolicy{},
		WorkspaceProvisioning{},
		SourceStrategy{},
		Deployment{},
		RuntimeMetadata{},
		DeploymentLease{},
	}

	for _, obj := range typesToCheck {
		typ := reflect.TypeOf(obj)
		checkNoForbiddenFields(t, typ, forbiddenSecretKeywords, typ.Name())
	}
}

func checkNoForbiddenFields(t *testing.T, typ reflect.Type, forbiddenWords []string, path string) {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fieldNameLower := strings.ToLower(field.Name)
		jsonTagLower := strings.ToLower(field.Tag.Get("json"))

		// Allowed exceptions:
		// Deployment and DeploymentLease legitimately have Status and Health,
		// but Environment must NEVER have them.
		isEphemeralCheck := false
		for _, w := range forbiddenWords {
			if w == "container_id" || w == "endpoint" || w == "status" {
				isEphemeralCheck = true
				break
			}
		}

		for _, forbidden := range forbiddenWords {
			// If checking ephemeral fields on Environment, flag if found
			if isEphemeralCheck {
				if strings.Contains(fieldNameLower, forbidden) || strings.Contains(jsonTagLower, forbidden) {
					t.Errorf("ephemeral runtime field detected in %s.%s (json: %q, forbidden keyword: %q)",
						path, field.Name, jsonTagLower, forbidden)
				}
			} else {
				// Secret keywords check
				if strings.Contains(fieldNameLower, forbidden) || strings.Contains(jsonTagLower, forbidden) {
					t.Errorf("secret or credential field detected in %s.%s (json: %q, forbidden keyword: %q)",
						path, field.Name, jsonTagLower, forbidden)
				}
			}
		}

		// Recursively check nested structs
		fieldType := field.Type
		if fieldType.Kind() == reflect.Pointer || fieldType.Kind() == reflect.Slice {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() == reflect.Struct && fieldType.PkgPath() == typ.PkgPath() {
			checkNoForbiddenFields(t, fieldType, forbiddenWords, path+"."+field.Name)
		}
	}
}

func TestEnvironment_Validation_Errors(t *testing.T) {
	validEnv := func() *Environment {
		return &Environment{
			ID:             "env-1",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "Dev Env",
			Mode:           EnvironmentModeDeployable,
			Role:           EnvironmentRoleDevelopment,
			Container: ContainerDefinition{
				Image: "ubuntu:24.04",
			},
			Provisioning: WorkspaceProvisioning{
				Strategy: SourceStrategy{
					Kind: SourceStrategyKindLocalMount,
					LocalMount: &LocalMountConfig{
						ContainerPath: "/workspace",
					},
				},
			},
			DeploymentPolicy: DeploymentPolicy{
				Reuse:           true,
				MaxInstances:    1,
				ReleaseBehavior: ReleaseBehaviorRestart,
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(e *Environment)
		wantErr string
	}{
		{
			name: "missing id",
			mutate: func(e *Environment) {
				e.ID = ""
			},
			wantErr: "environment id cannot be empty",
		},
		{
			name: "missing account scope id",
			mutate: func(e *Environment) {
				e.AccountScopeID = ""
			},
			wantErr: "account_scope_id cannot be empty",
		},
		{
			name: "missing workspace id",
			mutate: func(e *Environment) {
				e.WorkspaceID = ""
			},
			wantErr: "workspace_id cannot be empty",
		},
		{
			name: "missing name",
			mutate: func(e *Environment) {
				e.Name = ""
			},
			wantErr: "environment name cannot be empty",
		},
		{
			name: "unsupported mode",
			mutate: func(e *Environment) {
				e.Mode = "invalid_mode"
			},
			wantErr: "unsupported environment mode",
		},
		{
			name: "unsupported role",
			mutate: func(e *Environment) {
				e.Role = "invalid_role"
			},
			wantErr: "unsupported environment role",
		},
		{
			name: "missing container image",
			mutate: func(e *Environment) {
				e.Container.Image = ""
			},
			wantErr: "container image cannot be empty",
		},
		{
			name: "invalid exposed port container_port",
			mutate: func(e *Environment) {
				e.Container.ExposedPorts = []PortMapping{{ContainerPort: 0}}
			},
			wantErr: "container_port must be between 1 and 65535",
		},
		{
			name: "invalid exposed port host_port",
			mutate: func(e *Environment) {
				e.Container.ExposedPorts = []PortMapping{{ContainerPort: 80, HostPort: 99999}}
			},
			wantErr: "host_port must be between 0 and 65535",
		},
		{
			name: "invalid exposed port protocol",
			mutate: func(e *Environment) {
				e.Container.ExposedPorts = []PortMapping{{ContainerPort: 80, Protocol: "sctp"}}
			},
			wantErr: "protocol must be 'tcp' or 'udp'",
		},
		{
			name: "unsupported release behavior",
			mutate: func(e *Environment) {
				e.DeploymentPolicy.ReleaseBehavior = "kill_everything"
			},
			wantErr: "unsupported release_behavior",
		},
		{
			name: "invalid health check port",
			mutate: func(e *Environment) {
				e.HealthCheck = &HealthCheck{HTTPPort: 99999}
			},
			wantErr: "health_check http_port must be between 0 and 65535",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			tc.mutate(env)
			err := env.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestEnvironment_ReleaseBehaviors(t *testing.T) {
	behaviors := []ReleaseBehavior{
		ReleaseBehaviorNone,
		ReleaseBehaviorRestart,
		ReleaseBehaviorRecreate,
	}

	for _, b := range behaviors {
		env := &Environment{
			ID:             "env-1",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			Name:           "Env",
			Mode:           EnvironmentModeDeployable,
			Role:           EnvironmentRoleTesting,
			Container:      ContainerDefinition{Image: "ubuntu:latest"},
			Provisioning: WorkspaceProvisioning{
				Strategy: SourceStrategy{
					Kind:               SourceStrategyKindLocalMount,
					LocalMount:         &LocalMountConfig{ContainerPath: "/workspace"},
				},
			},
			DeploymentPolicy: DeploymentPolicy{
				ReleaseBehavior: b,
			},
		}
		if err := env.Validate(); err != nil {
			t.Fatalf("expected release behavior %q to be valid, got: %v", b, err)
		}
		if env.DeploymentPolicy.ReleaseBehavior != b {
			t.Fatalf("expected release behavior %q, got %q", b, env.DeploymentPolicy.ReleaseBehavior)
		}
	}
}

func TestEnvironment_CloneImmutability(t *testing.T) {
	orig := &Environment{
		ID:             "env-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Orig",
		Container: ContainerDefinition{
			Image:   "image:1",
			Command: []string{"cmd1"},
			EnvVars: map[string]string{"K": "V"},
		},
		Labels: map[string]string{"env": "test"},
	}

	cloned := orig.Clone()
	cloned.Name = "Mutated"
	cloned.Container.Command[0] = "mutated_cmd"
	cloned.Container.EnvVars["K"] = "mutated_val"
	cloned.Labels["env"] = "mutated_env"

	if orig.Name != "Orig" {
		t.Fatalf("original Name mutated: %s", orig.Name)
	}
	if orig.Container.Command[0] != "cmd1" {
		t.Fatalf("original Command mutated: %s", orig.Container.Command[0])
	}
	if orig.Container.EnvVars["K"] != "V" {
		t.Fatalf("original EnvVars mutated: %s", orig.Container.EnvVars["K"])
	}
	if orig.Labels["env"] != "test" {
		t.Fatalf("original Labels mutated: %s", orig.Labels["env"])
	}
}
