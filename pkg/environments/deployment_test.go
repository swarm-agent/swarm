package environments

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeployment_Valid(t *testing.T) {
	dep := &Deployment{
		ID:             "dep-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		EnvironmentID:  "env-1",
		ConnectionID:   "conn-1",
		Name:           "Dev Instance 1",
		Status:         DeploymentStatusRunning,
		Health:         HealthStatusHealthy,
		Runtime: RuntimeMetadata{
			ContainerID:         "docker-container-abc12345",
			ProviderResourceID:  "swarm-res-789",
			Endpoint:            "http://127.0.0.1:18080",
			RemoteWorkspacePath: "/workspace",
			RuntimeIP:           "172.17.0.2",
			EngineVersion:       "24.0.7",
			AssignedPorts: []AssignedPort{
				{
					ContainerPort: 8080,
					HostPort:      18080,
					Protocol:      "tcp",
					EndpointURL:   "http://127.0.0.1:18080",
				},
			},
		},
		Lifecycle: DeploymentLifecycle{
			CreatedAt:    1700000000000,
			StartedAt:    1700000001000,
			ReadyAt:      1700000005000,
			LastActiveAt: 1700000010000,
		},
		CreatedAt: 1700000000000,
		UpdatedAt: 1700000005000,
	}

	if err := dep.Validate(); err != nil {
		t.Fatalf("expected valid deployment, got: %v", err)
	}

	// JSON roundtrip
	data, err := json.Marshal(dep)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var roundtrip Deployment
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if roundtrip.ID != dep.ID || roundtrip.Runtime.ContainerID != dep.Runtime.ContainerID {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", roundtrip, dep)
	}
	if len(roundtrip.Runtime.AssignedPorts) != 1 || roundtrip.Runtime.AssignedPorts[0].HostPort != 18080 {
		t.Fatalf("assigned ports mismatch: %+v", roundtrip.Runtime.AssignedPorts)
	}
}

func TestDeployment_Validation_Errors(t *testing.T) {
	validDep := func() *Deployment {
		return &Deployment{
			ID:             "dep-1",
			AccountScopeID: "acct-1",
			WorkspaceID:    "ws-1",
			EnvironmentID:  "env-1",
			ConnectionID:   "conn-1",
			Name:           "Dep Name",
			Status:         DeploymentStatusRunning,
			Health:         HealthStatusHealthy,
		}
	}

	tests := []struct {
		name    string
		mutate  func(d *Deployment)
		wantErr string
	}{
		{
			name: "missing id",
			mutate: func(d *Deployment) {
				d.ID = ""
			},
			wantErr: "deployment id cannot be empty",
		},
		{
			name: "missing account scope id",
			mutate: func(d *Deployment) {
				d.AccountScopeID = ""
			},
			wantErr: "account_scope_id cannot be empty",
		},
		{
			name: "missing workspace id",
			mutate: func(d *Deployment) {
				d.WorkspaceID = ""
			},
			wantErr: "workspace_id cannot be empty",
		},
		{
			name: "missing environment id",
			mutate: func(d *Deployment) {
				d.EnvironmentID = ""
			},
			wantErr: "environment_id cannot be empty",
		},
		{
			name: "missing connection id",
			mutate: func(d *Deployment) {
				d.ConnectionID = ""
			},
			wantErr: "connection_id cannot be empty",
		},
		{
			name: "missing name",
			mutate: func(d *Deployment) {
				d.Name = ""
			},
			wantErr: "deployment name cannot be empty",
		},
		{
			name: "unsupported status",
			mutate: func(d *Deployment) {
				d.Status = "sleeping"
			},
			wantErr: "unsupported deployment status",
		},
		{
			name: "unsupported health",
			mutate: func(d *Deployment) {
				d.Health = "sick"
			},
			wantErr: "unsupported health status",
		},
		{
			name: "invalid assigned port container_port",
			mutate: func(d *Deployment) {
				d.Runtime.AssignedPorts = []AssignedPort{
					{ContainerPort: 0, HostPort: 8080},
				}
			},
			wantErr: "container_port must be between 1 and 65535",
		},
		{
			name: "invalid assigned port host_port",
			mutate: func(d *Deployment) {
				d.Runtime.AssignedPorts = []AssignedPort{
					{ContainerPort: 80, HostPort: 70000},
				}
			},
			wantErr: "host_port must be between 1 and 65535",
		},
		{
			name: "invalid assigned port protocol",
			mutate: func(d *Deployment) {
				d.Runtime.AssignedPorts = []AssignedPort{
					{ContainerPort: 80, HostPort: 8080, Protocol: "invalid"},
				}
			},
			wantErr: "protocol must be 'tcp' or 'udp'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dep := validDep()
			tc.mutate(dep)
			err := dep.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestDeployment_Helpers(t *testing.T) {
	dep := &Deployment{
		Status: DeploymentStatusRunning,
		Health: HealthStatusHealthy,
		Runtime: RuntimeMetadata{
			AssignedPorts: []AssignedPort{
				{ContainerPort: 3000, HostPort: 13000, Protocol: "tcp"},
				{ContainerPort: 8080, HostPort: 18080, Protocol: "tcp"},
			},
		},
	}

	if !dep.IsActive() {
		t.Fatal("expected IsActive to be true for running status")
	}
	if !dep.IsRunning() {
		t.Fatal("expected IsRunning to be true for running status")
	}
	if !dep.IsUsable() {
		t.Fatal("expected IsUsable to be true for healthy running deployment")
	}
	if dep.IsTerminal() {
		t.Fatal("expected IsTerminal to be false for running deployment")
	}

	// Port lookup
	port, ok := dep.GetPort(8080)
	if !ok || port.HostPort != 18080 {
		t.Fatalf("expected port 8080 -> 18080, got %v (found: %v)", port, ok)
	}

	_, ok = dep.GetPort(9999)
	if ok {
		t.Fatal("expected port 9999 to not be found")
	}

	// Test Unhealthy status makes it not usable
	dep.Health = HealthStatusUnhealthy
	if dep.IsUsable() {
		t.Fatal("expected unhealthy deployment to not be usable")
	}

	// Test terminal status
	dep.Status = DeploymentStatusStopped
	if !dep.IsTerminal() {
		t.Fatal("expected stopped deployment to be terminal")
	}
	if dep.IsActive() {
		t.Fatal("expected stopped deployment to not be active")
	}
}

func TestDeployment_CloneImmutability(t *testing.T) {
	orig := &Deployment{
		ID:   "dep-1",
		Name: "Orig",
		Runtime: RuntimeMetadata{
			AssignedPorts: []AssignedPort{
				{ContainerPort: 80, HostPort: 8080},
			},
		},
	}

	cloned := orig.Clone()
	cloned.Name = "Mutated"
	cloned.Runtime.AssignedPorts[0].HostPort = 9999

	if orig.Name != "Orig" {
		t.Fatalf("original Name mutated: %s", orig.Name)
	}
	if orig.Runtime.AssignedPorts[0].HostPort != 8080 {
		t.Fatalf("original AssignedPort mutated: %d", orig.Runtime.AssignedPorts[0].HostPort)
	}
}
