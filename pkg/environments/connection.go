package environments

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxIDBytes          = 128
	maxNameBytes        = 256
	maxDescriptionBytes = 2048
)

// ConnectionKind identifies the transport/mechanism used to communicate with an environment host.
type ConnectionKind string

const (
	ConnectionKindLocalDocker ConnectionKind = "local_docker"
	ConnectionKindSSH         ConnectionKind = "ssh"
)

// ConnectionCapabilities describes the supported features of the connection host.
type ConnectionCapabilities struct {
	SupportsDocker      bool   `json:"supports_docker"`
	SupportsSSH         bool   `json:"supports_ssh"`
	SupportsDirectMount bool   `json:"supports_direct_mount"`
	SupportsPortForward bool   `json:"supports_port_forward"`
	RemoteOS            string `json:"remote_os,omitempty"`
	RemoteArch          string `json:"remote_arch,omitempty"`
	EngineVersion       string `json:"engine_version,omitempty"`
}

// LocalDockerConfig configures communication with a local Docker daemon.
// Strictly contains no secrets or credentials.
type LocalDockerConfig struct {
	SocketPath string `json:"socket_path,omitempty"` // e.g. "/var/run/docker.sock", empty for default
	Host       string `json:"host,omitempty"`        // e.g. "unix:///var/run/docker.sock"
}

// SSHConfig configures an SSH connection to a remote host.
// In accordance with security architecture, NO secrets (passwords, private keys, access tokens)
// are stored. Authentication relies strictly on system SSH agent or existing SSH config.
type SSHConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"` // default 22 if <= 0
	User           string `json:"user"`
	IdentityFile   string `json:"identity_file,omitempty"`    // optional path to identity file reference (path only, not key contents)
	KnownHostsFile string `json:"known_hosts_file,omitempty"` // optional path to known_hosts file
}

// Connection represents an authorized connection to an execution host (local machine or remote server).
// Strictly scoped to AccountScopeID and WorkspaceID.
type Connection struct {
	ID             string                 `json:"id"`
	AccountScopeID string                 `json:"account_scope_id"`
	WorkspaceID    string                 `json:"workspace_id"`
	Name           string                 `json:"name"`
	Description    string                 `json:"description,omitempty"`
	Kind           ConnectionKind         `json:"kind"`
	Capabilities   ConnectionCapabilities `json:"capabilities"`
	LocalDocker    *LocalDockerConfig     `json:"local_docker,omitempty"`
	SSH            *SSHConfig             `json:"ssh,omitempty"`
	CreatedAt      int64                  `json:"created_at"`
	UpdatedAt      int64                  `json:"updated_at"`
}

// Validate checks that the Connection adheres to domain constraints, scoping rules, and security policies.
func (c *Connection) Validate() error {
	if c == nil {
		return errors.New("connection is nil")
	}

	c.ID = strings.TrimSpace(c.ID)
	if c.ID == "" {
		return errors.New("connection id cannot be empty")
	}
	if len(c.ID) > maxIDBytes || !utf8.ValidString(c.ID) {
		return fmt.Errorf("connection id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	c.AccountScopeID = strings.TrimSpace(c.AccountScopeID)
	if c.AccountScopeID == "" {
		return errors.New("account_scope_id cannot be empty")
	}
	if len(c.AccountScopeID) > maxIDBytes || !utf8.ValidString(c.AccountScopeID) {
		return fmt.Errorf("account_scope_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	c.WorkspaceID = strings.TrimSpace(c.WorkspaceID)
	if c.WorkspaceID == "" {
		return errors.New("workspace_id cannot be empty")
	}
	if len(c.WorkspaceID) > maxIDBytes || !utf8.ValidString(c.WorkspaceID) {
		return fmt.Errorf("workspace_id exceeds %d bytes or is invalid UTF-8", maxIDBytes)
	}

	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return errors.New("connection name cannot be empty")
	}
	if len(c.Name) > maxNameBytes || !utf8.ValidString(c.Name) {
		return fmt.Errorf("connection name exceeds %d bytes or is invalid UTF-8", maxNameBytes)
	}

	if len(c.Description) > maxDescriptionBytes || !utf8.ValidString(c.Description) {
		return fmt.Errorf("connection description exceeds %d bytes or is invalid UTF-8", maxDescriptionBytes)
	}

	switch c.Kind {
	case ConnectionKindLocalDocker:
		if c.SSH != nil {
			return errors.New("ssh configuration must not be provided for local_docker connection")
		}
		if c.LocalDocker != nil {
			if c.LocalDocker.SocketPath != "" {
				if !filepath.IsAbs(c.LocalDocker.SocketPath) {
					return errors.New("local_docker socket_path must be an absolute path")
				}
			}
		}
	case ConnectionKindSSH:
		if c.LocalDocker != nil {
			return errors.New("local_docker configuration must not be provided for ssh connection")
		}
		if c.SSH == nil {
			return errors.New("ssh configuration is required for ssh connection")
		}
		c.SSH.Host = strings.TrimSpace(c.SSH.Host)
		if c.SSH.Host == "" {
			return errors.New("ssh host cannot be empty")
		}
		c.SSH.User = strings.TrimSpace(c.SSH.User)
		if c.SSH.User == "" {
			return errors.New("ssh user cannot be empty")
		}
		if c.SSH.Port <= 0 {
			c.SSH.Port = 22
		}
		if c.SSH.Port > 65535 {
			return errors.New("ssh port must be between 1 and 65535")
		}
		if c.SSH.IdentityFile != "" && !filepath.IsAbs(c.SSH.IdentityFile) {
			return errors.New("ssh identity_file must be an absolute path when specified")
		}
		if c.SSH.KnownHostsFile != "" && !filepath.IsAbs(c.SSH.KnownHostsFile) {
			return errors.New("ssh known_hosts_file must be an absolute path when specified")
		}
	default:
		return fmt.Errorf("unsupported connection kind: %q", c.Kind)
	}

	return nil
}

// Clone creates a deep copy of the Connection to guarantee immutability.
func (c *Connection) Clone() *Connection {
	if c == nil {
		return nil
	}
	cp := *c
	if c.LocalDocker != nil {
		ld := *c.LocalDocker
		cp.LocalDocker = &ld
	}
	if c.SSH != nil {
		ssh := *c.SSH
		cp.SSH = &ssh
	}
	return &cp
}

// forbiddenSecretKeys lists lowercase keywords that indicate secret/credential leakage.
var forbiddenSecretKeys = []string{
	"password",
	"passwd",
	"private_key",
	"privatekey",
	"secret",
	"api_key",
	"apikey",
	"access_token",
	"accesstoken",
	"auth_token",
	"authtoken",
	"client_secret",
}

// AssertNoSecretsRaw inspects JSON or map representation to ensure no credential or secret keys are present.
func AssertNoSecretsRaw(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	return inspectMapForSecrets(raw)
}

func inspectMapForSecrets(m map[string]any) error {
	for k, v := range m {
		lower := strings.ToLower(k)
		for _, forbidden := range forbiddenSecretKeys {
			if strings.Contains(lower, forbidden) {
				return fmt.Errorf("forbidden secret key detected in payload: %q", k)
			}
		}
		switch val := v.(type) {
		case map[string]any:
			if err := inspectMapForSecrets(val); err != nil {
				return err
			}
		case []any:
			for _, item := range val {
				if itemMap, ok := item.(map[string]any); ok {
					if err := inspectMapForSecrets(itemMap); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
