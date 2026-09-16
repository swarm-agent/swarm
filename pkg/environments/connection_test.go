package environments

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConnection_LocalDocker_Valid(t *testing.T) {
	conn := &Connection{
		ID:             "conn-local-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Local Docker",
		Description:    "Local development docker socket",
		Kind:           ConnectionKindLocalDocker,
		Capabilities: ConnectionCapabilities{
			SupportsDocker:      true,
			SupportsSSH:         false,
			SupportsDirectMount: true,
			SupportsPortForward: true,
			RemoteOS:            "linux",
			RemoteArch:          "amd64",
			EngineVersion:       "24.0.7",
		},
		LocalDocker: &LocalDockerConfig{
			SocketPath: "/var/run/docker.sock",
		},
		CreatedAt: 1700000000000,
		UpdatedAt: 1700000000000,
	}

	if err := conn.Validate(); err != nil {
		t.Fatalf("expected valid connection, got: %v", err)
	}

	// Test JSON round-trip
	data, err := json.Marshal(conn)
	if err != nil {
		t.Fatalf("failed to marshal connection: %v", err)
	}

	var roundtrip Connection
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("failed to unmarshal connection: %v", err)
	}

	if roundtrip.ID != conn.ID || roundtrip.Kind != conn.Kind || roundtrip.LocalDocker.SocketPath != conn.LocalDocker.SocketPath {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", roundtrip, conn)
	}
}

func TestConnection_SSH_Valid(t *testing.T) {
	conn := &Connection{
		ID:             "conn-ssh-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Remote GPU Host",
		Kind:           ConnectionKindSSH,
		Capabilities: ConnectionCapabilities{
			SupportsDocker:      true,
			SupportsSSH:         true,
			SupportsDirectMount: false,
			SupportsPortForward: true,
			RemoteOS:            "linux",
		},
		SSH: &SSHConfig{
			Host:           "gpu-box.local",
			Port:           2222,
			User:           "ubuntu",
			IdentityFile:   "/home/user/.ssh/id_ed25519",
			KnownHostsFile: "/home/user/.ssh/known_hosts",
		},
		CreatedAt: 1700000000000,
		UpdatedAt: 1700000000000,
	}

	if err := conn.Validate(); err != nil {
		t.Fatalf("expected valid connection, got: %v", err)
	}

	// Verify default port behavior
	connNoPort := conn.Clone()
	connNoPort.SSH.Port = 0
	if err := connNoPort.Validate(); err != nil {
		t.Fatalf("expected valid connection with 0 port (auto-set to 22), got: %v", err)
	}
	if connNoPort.SSH.Port != 22 {
		t.Fatalf("expected port 22, got %d", connNoPort.SSH.Port)
	}
}

func TestConnection_Validation_Errors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *Connection)
		wantErr string
	}{
		{
			name: "missing id",
			mutate: func(c *Connection) {
				c.ID = ""
			},
			wantErr: "connection id cannot be empty",
		},
		{
			name: "missing account scope",
			mutate: func(c *Connection) {
				c.AccountScopeID = ""
			},
			wantErr: "account_scope_id cannot be empty",
		},
		{
			name: "missing workspace id",
			mutate: func(c *Connection) {
				c.WorkspaceID = ""
			},
			wantErr: "workspace_id cannot be empty",
		},
		{
			name: "missing name",
			mutate: func(c *Connection) {
				c.Name = ""
			},
			wantErr: "connection name cannot be empty",
		},
		{
			name: "invalid kind",
			mutate: func(c *Connection) {
				c.Kind = "invalid_kind"
			},
			wantErr: "unsupported connection kind",
		},
		{
			name: "local docker with ssh config",
			mutate: func(c *Connection) {
				c.Kind = ConnectionKindLocalDocker
				c.SSH = &SSHConfig{Host: "remote", User: "root"}
			},
			wantErr: "ssh configuration must not be provided",
		},
		{
			name: "ssh missing ssh config",
			mutate: func(c *Connection) {
				c.Kind = ConnectionKindSSH
				c.SSH = nil
			},
			wantErr: "ssh configuration is required",
		},
		{
			name: "ssh missing host",
			mutate: func(c *Connection) {
				c.Kind = ConnectionKindSSH
				c.SSH = &SSHConfig{User: "root"}
			},
			wantErr: "ssh host cannot be empty",
		},
		{
			name: "ssh missing user",
			mutate: func(c *Connection) {
				c.Kind = ConnectionKindSSH
				c.SSH = &SSHConfig{Host: "remote"}
			},
			wantErr: "ssh user cannot be empty",
		},
		{
			name: "ssh invalid port",
			mutate: func(c *Connection) {
				c.Kind = ConnectionKindSSH
				c.SSH = &SSHConfig{Host: "remote", User: "root", Port: 70000}
			},
			wantErr: "ssh port must be between 1 and 65535",
		},
		{
			name: "relative identity file",
			mutate: func(c *Connection) {
				c.Kind = ConnectionKindSSH
				c.SSH = &SSHConfig{Host: "remote", User: "root", IdentityFile: "relative/key"}
			},
			wantErr: "identity_file must be an absolute path",
		},
		{
			name: "relative socket path",
			mutate: func(c *Connection) {
				c.Kind = ConnectionKindLocalDocker
				c.LocalDocker = &LocalDockerConfig{SocketPath: "var/run/docker.sock"}
			},
			wantErr: "socket_path must be an absolute path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conn := &Connection{
				ID:             "conn-1",
				AccountScopeID: "acct-1",
				WorkspaceID:    "ws-1",
				Name:           "Test Conn",
				Kind:           ConnectionKindLocalDocker,
			}
			tc.mutate(conn)
			err := conn.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestConnection_CloneImmutability(t *testing.T) {
	orig := &Connection{
		ID:             "conn-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		Name:           "Original",
		Kind:           ConnectionKindSSH,
		SSH: &SSHConfig{
			Host: "orig-host",
			Port: 22,
			User: "orig-user",
		},
	}

	cloned := orig.Clone()
	cloned.Name = "Mutated"
	cloned.SSH.Host = "mutated-host"

	if orig.Name != "Original" {
		t.Fatalf("original Name was mutated: %s", orig.Name)
	}
	if orig.SSH.Host != "orig-host" {
		t.Fatalf("original SSH Host was mutated: %s", orig.SSH.Host)
	}
}

func TestConnection_AssertNoSecretsRaw(t *testing.T) {
	// Good payload without secrets
	goodJSON := []byte(`{"id":"c1","account_scope_id":"a1","workspace_id":"w1","name":"Local","kind":"local_docker"}`)
	if err := AssertNoSecretsRaw(goodJSON); err != nil {
		t.Fatalf("expected no secret error, got: %v", err)
	}

	// Bad payloads with various forbidden secret keys
	badPayloads := [][]byte{
		[]byte(`{"id":"c1","password":"supersecret"}`),
		[]byte(`{"id":"c1","ssh":{"private_key":"-----BEGIN..."}}`),
		[]byte(`{"id":"c1","api_key":"sk-12345"}`),
		[]byte(`{"id":"c1","auth_token":"bearer-xyz"}`),
		[]byte(`{"id":"c1","nested":{"deeper":[{"secret":"forbidden"}]}}`),
	}

	for _, bad := range badPayloads {
		if err := AssertNoSecretsRaw(bad); err == nil {
			t.Fatalf("expected secret error for payload %s, got nil", string(bad))
		}
	}
}
