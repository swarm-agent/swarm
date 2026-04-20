package startupconfig

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	ModeInteractive = "interactive"
	ModeBox         = "box"

	NetworkModeLAN       = "lan"
	NetworkModeTailscale = "tailscale"

	PairingStateUnpaired        = "unpaired"
	PairingStateBootstrapReady  = "bootstrap_configured"
	PairingStatePendingApproval = "pending_approval"
	PairingStatePaired          = "paired"
	PairingStateRejected        = "rejected"

	DefaultHost              = "127.0.0.1"
	DefaultPort              = 7781
	DefaultDesktopPort       = 5555
	DefaultPeerTransportPort = 7791

	configDirName  = "swarm"
	configFileName = "swarm.conf"

	startupModeKey        = "startup_mode"
	devModeKey            = "dev_mode"
	devRootKey            = "dev_root"
	swarmModeKey          = "swarm_mode"
	bootstrapModeKey      = "mode"
	childStartupConfigEnv = "SWARM_CHILD_STARTUP_CONFIG"
)

type FileConfig struct {
	Path                    string
	Exists                  bool
	Mode                    string
	DevMode                 bool
	DevRoot                 string
	Host                    string
	Port                    int
	AdvertiseHost           string
	AdvertisePort           int
	DesktopPort             int
	BypassPermissions       bool
	RetainToolOutputHistory bool
	SwarmName               string
	SwarmMode               bool
	Child                   bool
	NetworkMode             string
	TailscaleURL            string
	PeerTransportPort       int
	ParentSwarmID           string
	PairingState            string
	DeployContainer         DeployContainerBootstrap
	RemoteDeploy            RemoteDeployBootstrap
}

type DeployContainerBootstrap struct {
	Enabled                  bool
	HostDriven               bool
	SyncEnabled              bool
	SyncMode                 string
	SyncModules              []string
	SyncOwnerSwarmID         string
	SyncCredentialURL        string
	SyncAgentURL             string
	DeploymentID             string
	HostAPIBaseURL           string
	HostDesktopURL           string
	LocalTransportSocketPath string
	BootstrapSecret          string
	VerificationCode         string
}

type RemoteDeployBootstrap struct {
	Enabled           bool
	SessionID         string
	SessionToken      string
	HostAPIBaseURL    string
	HostDesktopURL    string
	InviteToken       string
	SyncEnabled       bool
	SyncMode          string
	SyncOwnerSwarmID  string
	SyncCredentialURL string
}

type BootstrapFlags struct {
	SwarmName        string
	SwarmNameSet     bool
	Child            bool
	ChildSet         bool
	Mode             string
	ModeSet          bool
	AdvertiseHost    string
	AdvertiseHostSet bool
	AdvertisePort    int
	AdvertisePortSet bool
	TailscaleURL     string
	TailscaleURLSet  bool
}

func (b BootstrapFlags) HasAny() bool {
	return b.SwarmNameSet || b.ChildSet || b.ModeSet || b.AdvertiseHostSet || b.AdvertisePortSet || b.TailscaleURLSet
}

func (b BootstrapFlags) Validate() error {
	if b.SwarmNameSet && strings.TrimSpace(b.SwarmName) == "" {
		return errors.New("invalid --swarm-name: value must be non-empty")
	}
	if b.ModeSet && !isValidNetworkMode(strings.TrimSpace(b.Mode)) {
		return fmt.Errorf("invalid --mode %q (expected %q or %q)", b.Mode, NetworkModeLAN, NetworkModeTailscale)
	}
	if b.AdvertiseHostSet {
		normalizedHost, err := normalizeAdvertiseHost(b.AdvertiseHost)
		if err != nil {
			return fmt.Errorf("invalid --advertise-host: %w", err)
		}
		if normalizedHost == "" {
			return errors.New("invalid --advertise-host: value must be non-empty")
		}
	}
	if b.AdvertisePortSet && (b.AdvertisePort < 1 || b.AdvertisePort > 65535) {
		return fmt.Errorf("invalid --advertise-port %d (expected 1-65535)", b.AdvertisePort)
	}
	if b.TailscaleURLSet && strings.TrimSpace(b.TailscaleURL) == "" {
		return errors.New("invalid --tailscale-url: value must be non-empty")
	}
	return nil
}

func (cfg FileConfig) ApplyBootstrap(flags BootstrapFlags) (FileConfig, error) {
	if err := flags.Validate(); err != nil {
		return FileConfig{}, err
	}
	if flags.SwarmNameSet {
		cfg.SwarmName = strings.TrimSpace(flags.SwarmName)
	}
	if flags.ChildSet {
		cfg.Child = flags.Child
	}
	if flags.ModeSet {
		cfg.NetworkMode = normalizeNetworkMode(flags.Mode)
	}
	if flags.AdvertiseHostSet {
		normalizedHost, err := normalizeAdvertiseHost(flags.AdvertiseHost)
		if err != nil {
			return FileConfig{}, err
		}
		cfg.AdvertiseHost = normalizedHost
	}
	if flags.AdvertisePortSet {
		cfg.AdvertisePort = flags.AdvertisePort
	}
	if flags.TailscaleURLSet {
		cfg.TailscaleURL = strings.TrimSpace(flags.TailscaleURL)
	}
	if err := validate(cfg); err != nil {
		return FileConfig{}, err
	}
	return cfg, nil
}

func ResolvePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(configDir, configDirName, configFileName), nil
}

func Default(path string) FileConfig {
	return FileConfig{
		Path:                    path,
		Mode:                    ModeInteractive,
		DevMode:                 false,
		DevRoot:                 "",
		Host:                    DefaultHost,
		Port:                    DefaultPort,
		AdvertiseHost:           "",
		AdvertisePort:           DefaultPort,
		DesktopPort:             DefaultDesktopPort,
		BypassPermissions:       false,
		RetainToolOutputHistory: false,
		SwarmName:               "",
		SwarmMode:               false,
		Child:                   false,
		NetworkMode:             NetworkModeLAN,
		TailscaleURL:            "",
		PeerTransportPort:       DefaultPeerTransportPort,
		ParentSwarmID:           "",
		PairingState:            "",
		DeployContainer:         DeployContainerBootstrap{},
		RemoteDeploy:            RemoteDeployBootstrap{},
	}
}

func Load(path string) (FileConfig, error) {
	cfg := Default(path)
	if envText := decodeEnvMultiline(strings.TrimSpace(os.Getenv(childStartupConfigEnv))); strings.TrimSpace(envText) != "" {
		log.Printf("startupconfig load source=env env=%q path=%q bytes=%d", childStartupConfigEnv, path, len(envText))
		parsed, _, err := parseEntries(envText, cfg)
		if err != nil {
			return FileConfig{}, fmt.Errorf("parse startup config from %s: %w", childStartupConfigEnv, err)
		}
		parsed.Path = path
		parsed.Exists = true
		if err := validate(parsed); err != nil {
			return FileConfig{}, fmt.Errorf("parse startup config from %s: %w", childStartupConfigEnv, err)
		}
		return parsed, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return FileConfig{}, fmt.Errorf("stat startup config %q: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FileConfig{}, fmt.Errorf("read startup config %q: %w", path, err)
	}
	text := string(data)
	parsed, seen, err := parseEntries(text, cfg)
	if err != nil {
		return FileConfig{}, fmt.Errorf("parse startup config %q: %w", path, err)
	}
	if _, ok := seen["peer_transport_port"]; !ok {
		parsed.PeerTransportPort = chooseAvailablePeerTransportPort(parsed)
	}
	if err := appendMissingKeys(path, text, info.Mode().Perm(), parsed, seen); err != nil {
		return FileConfig{}, err
	}
	for _, key := range requiredKeys() {
		seen[key] = struct{}{}
	}
	parsed.Path = path
	parsed.Exists = true
	if err := validate(parsed); err != nil {
		return FileConfig{}, fmt.Errorf("parse startup config %q: %w", path, err)
	}
	return parsed, nil
}

func chooseAvailablePeerTransportPort(cfg FileConfig) int {
	reserved := []int{cfg.Port}
	if cfg.DesktopPort > 0 {
		reserved = append(reserved, cfg.DesktopPort)
	}
	start := cfg.PeerTransportPort
	if start < 1 || start > 65535 {
		start = DefaultPeerTransportPort
	}
	for port := start; port <= 65535; port++ {
		if slices.Contains(reserved, port) {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port
	}
	return start
}

func Write(cfg FileConfig) error {
	if strings.TrimSpace(cfg.Path) == "" {
		return errors.New("startup config path must not be empty")
	}
	if err := validate(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o755); err != nil {
		return fmt.Errorf("create startup config directory: %w", err)
	}
	content := Format(cfg)
	if err := os.WriteFile(cfg.Path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write startup config %q: %w", cfg.Path, err)
	}
	return nil
}

func Format(cfg FileConfig) string {
	return fmt.Sprintf(`# Swarm startup mode.
# interactive = normal local use; Swarm runs when you launch it.
# box = always-on box mode; Swarm should be treated as an always-running service.
# box does NOT by itself survive reboot/login/logout. For true persistence,
# install/run Swarm under systemd or another OS service manager.
startup_mode = %s

# Enable source-checkout dev behavior for local child image rebuilds.
# false = runtime-safe behavior only; true = allow dev-only rebuild flow from dev_root.
dev_mode = %t

# Source checkout root used for dev-only local child image rebuilds.
# Leave blank until a rebuild from a source checkout records it.
dev_root = %s

# Network bind host for the Swarm backend.
# Keep this at 127.0.0.1 for local-only use.
# Use a non-loopback host only when you intentionally want remote access.
host = %s

# Backend API port.
port = %d

# Canonical LAN host or IP that other machines should use to reach this Swarm.
# Leave blank to detect or confirm it in onboarding.
advertise_host = %s

# Canonical LAN port that other machines should use to reach this Swarm.
# Defaults to the backend API port and changing it requires a restart.
advertise_port = %d

# Desktop/web port. Set to 0 to disable the desktop listener.
desktop_port = %d

# Bypass normal tool permission prompts.
# Plan mode still stays plan mode, and exit_plan_mode still requires approval.
bypass_permissions = %t

# Keep sanitized tool/permission output in persisted history so refresh can show it.
# false keeps the current privacy-preserving placeholder behavior.
retain_tool_output_history = %t

# Human-readable Swarm name shown in onboarding and discovery surfaces.
# Leave blank to set it later.
swarm_name = %s

# Whether this Swarm should participate in shared swarm networking.
# false = standalone local use, true = enable swarm role/pairing/transport settings.
swarm_mode = %t

# Whether this Swarm should bootstrap as a child.
# false = master/default, true = child.
child = %t

# Bootstrap network mode.
# lan = connect over the local network.
# tailscale = connect over a Tailscale URL.
mode = %s

# Canonical persisted Tailscale URL for bootstrap and pairing flows.
# Leave blank when not using a manual Tailscale address.
tailscale_url = %s

# Local-only peer transport port for peer forwarding such as Tailscale Serve or SSH tunneling.
# Changing it requires a restart.
peer_transport_port = %d

# Parent swarm ID for child bootstrap/attach flows.
parent_swarm_id = %s

# Persisted local pairing state.
pairing_state = %s

# Deploy/container child attach bootstrap payload.
deploy_container_enabled = %t
deploy_container_host_driven = %t
deploy_container_sync_enabled = %t
deploy_container_sync_mode = %s
deploy_container_sync_modules = %s
deploy_container_sync_owner_swarm_id = %s
deploy_container_sync_credential_url = %s
deploy_container_sync_agent_url = %s
deploy_container_deployment_id = %s
deploy_container_host_api_base_url = %s
deploy_container_host_desktop_url = %s
deploy_container_local_transport_socket_path = %s
deploy_container_bootstrap_secret = %s
deploy_container_verification_code = %s

# Remote deploy child bootstrap payload.
remote_deploy_enabled = %t
remote_deploy_session_id = %s
remote_deploy_session_token = %s
remote_deploy_host_api_base_url = %s
remote_deploy_host_desktop_url = %s
remote_deploy_invite_token = %s
remote_deploy_sync_enabled = %t
remote_deploy_sync_mode = %s
remote_deploy_sync_owner_swarm_id = %s
remote_deploy_sync_credential_url = %s
`, cfg.Mode, cfg.DevMode, cfg.DevRoot, cfg.Host, cfg.Port, cfg.AdvertiseHost, cfg.AdvertisePort, cfg.DesktopPort, cfg.BypassPermissions, cfg.RetainToolOutputHistory, cfg.SwarmName, cfg.SwarmMode, cfg.Child, cfg.NetworkMode, cfg.TailscaleURL, cfg.PeerTransportPort, cfg.ParentSwarmID, cfg.PairingState, cfg.DeployContainer.Enabled, cfg.DeployContainer.HostDriven, cfg.DeployContainer.SyncEnabled, cfg.DeployContainer.SyncMode, formatCSVList(cfg.DeployContainer.SyncModules), cfg.DeployContainer.SyncOwnerSwarmID, cfg.DeployContainer.SyncCredentialURL, cfg.DeployContainer.SyncAgentURL, cfg.DeployContainer.DeploymentID, cfg.DeployContainer.HostAPIBaseURL, cfg.DeployContainer.HostDesktopURL, cfg.DeployContainer.LocalTransportSocketPath, cfg.DeployContainer.BootstrapSecret, cfg.DeployContainer.VerificationCode, cfg.RemoteDeploy.Enabled, cfg.RemoteDeploy.SessionID, cfg.RemoteDeploy.SessionToken, cfg.RemoteDeploy.HostAPIBaseURL, cfg.RemoteDeploy.HostDesktopURL, cfg.RemoteDeploy.InviteToken, cfg.RemoteDeploy.SyncEnabled, cfg.RemoteDeploy.SyncMode, cfg.RemoteDeploy.SyncOwnerSwarmID, cfg.RemoteDeploy.SyncCredentialURL)
}

func BootstrapExistingConfigError(path string) error {
	return fmt.Errorf("onboarding flags are first-run only; update %s and restart", path)
}

func parseEntries(text string, cfg FileConfig) (FileConfig, map[string]struct{}, error) {
	rawSeen := make(map[string]struct{})
	seen := make(map[string]struct{})
	legacyStartupModeSeen := false
	legacyBootstrapModeSeen := false
	legacyAdvertiseHostSeen := false
	legacyTailscaleURLSeen := false
	for lineNumber, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return FileConfig{}, nil, fmt.Errorf("line %d: expected key = value", lineNumber+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return FileConfig{}, nil, fmt.Errorf("line %d: key must be non-empty", lineNumber+1)
		}
		if !allowsEmptyValue(key) && value == "" {
			return FileConfig{}, nil, fmt.Errorf("line %d: value for %q must be non-empty", lineNumber+1, key)
		}
		switch key {
		case startupModeKey:
			if _, exists := seen[startupModeKey]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen[startupModeKey] = struct{}{}
			cfg.Mode = value
		case devModeKey:
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen[devModeKey] = struct{}{}
			devMode, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid %s %q", lineNumber+1, devModeKey, value)
			}
			cfg.DevMode = devMode
		case devRootKey:
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen[devRootKey] = struct{}{}
			cfg.DevRoot = strings.TrimSpace(value)
			if cfg.DevRoot != "" {
				cfg.DevRoot = filepath.Clean(cfg.DevRoot)
			}
		case bootstrapModeKey:
			if isValidNetworkMode(value) {
				if _, exists := seen[bootstrapModeKey]; exists {
					return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
				}
				seen[bootstrapModeKey] = struct{}{}
				cfg.NetworkMode = normalizeNetworkMode(value)
				continue
			}
			if _, exists := seen[startupModeKey]; exists {
				continue
			}
			if legacyStartupModeSeen {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate legacy startup mode", lineNumber+1)
			}
			legacyStartupModeSeen = true
			cfg.Mode = value
		case "host":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["host"] = struct{}{}
			cfg.Host = value
		case "port":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["port"] = struct{}{}
			port, err := strconv.Atoi(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid port %q", lineNumber+1, value)
			}
			cfg.Port = port
		case "advertise_host":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["advertise_host"] = struct{}{}
			normalizedHost, err := normalizeAdvertiseHost(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid advertise_host %q: %v", lineNumber+1, value, err)
			}
			cfg.AdvertiseHost = normalizedHost
		case "advertise_port":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["advertise_port"] = struct{}{}
			advertisePort, err := strconv.Atoi(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid advertise_port %q", lineNumber+1, value)
			}
			cfg.AdvertisePort = advertisePort
		case "desktop_port":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["desktop_port"] = struct{}{}
			desktopPort, err := strconv.Atoi(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid desktop_port %q", lineNumber+1, value)
			}
			cfg.DesktopPort = desktopPort
		case "bypass_permissions":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["bypass_permissions"] = struct{}{}
			bypassPermissions, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid bypass_permissions %q", lineNumber+1, value)
			}
			cfg.BypassPermissions = bypassPermissions
		case "retain_tool_output_history":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["retain_tool_output_history"] = struct{}{}
			retainToolOutputHistory, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid retain_tool_output_history %q", lineNumber+1, value)
			}
			cfg.RetainToolOutputHistory = retainToolOutputHistory
		case "swarm_name":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["swarm_name"] = struct{}{}
			cfg.SwarmName = value
		case swarmModeKey:
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen[swarmModeKey] = struct{}{}
			swarmMode, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid %s %q", lineNumber+1, swarmModeKey, value)
			}
			cfg.SwarmMode = swarmMode
		case "child":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["child"] = struct{}{}
			child, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid child %q", lineNumber+1, value)
			}
			cfg.Child = child
		case "network_mode":
			if _, exists := seen[bootstrapModeKey]; exists {
				continue
			}
			if legacyBootstrapModeSeen {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate legacy bootstrap mode", lineNumber+1)
			}
			legacyBootstrapModeSeen = true
			cfg.NetworkMode = normalizeNetworkMode(value)
		case "advertise_mode":
			if _, exists := seen[bootstrapModeKey]; exists {
				continue
			}
			if legacyBootstrapModeSeen {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate legacy bootstrap mode", lineNumber+1)
			}
			legacyBootstrapModeSeen = true
			normalizedMode, err := normalizeLegacyAdvertiseMode(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid advertise_mode %q: %v", lineNumber+1, value, err)
			}
			cfg.NetworkMode = normalizedMode
		case "advertise_addr":
			switch {
			case strings.TrimSpace(value) == "":
				continue
			case strings.Contains(value, "://"):
				if _, exists := seen["tailscale_url"]; exists {
					continue
				}
				if legacyTailscaleURLSeen {
					return FileConfig{}, nil, fmt.Errorf("line %d: duplicate legacy tailscale URL", lineNumber+1)
				}
				legacyTailscaleURLSeen = true
				cfg.TailscaleURL = strings.TrimSpace(value)
			default:
				if _, exists := seen["advertise_host"]; exists {
					continue
				}
				if legacyAdvertiseHostSeen {
					return FileConfig{}, nil, fmt.Errorf("line %d: duplicate legacy advertise host", lineNumber+1)
				}
				legacyAdvertiseHostSeen = true
				normalizedHost, err := normalizeAdvertiseHost(value)
				if err != nil {
					return FileConfig{}, nil, fmt.Errorf("line %d: invalid advertise_addr %q: %v", lineNumber+1, value, err)
				}
				cfg.AdvertiseHost = normalizedHost
			}
		case "tailscale_url":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["tailscale_url"] = struct{}{}
			cfg.TailscaleURL = value
		case "local_transport_port":
			continue
		case "peer_transport_port":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["peer_transport_port"] = struct{}{}
			peerTransportPort, err := strconv.Atoi(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid peer_transport_port %q", lineNumber+1, value)
			}
			cfg.PeerTransportPort = peerTransportPort
		case "tailscale_transport_port":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["tailscale_transport_port"] = struct{}{}
			peerTransportPort, err := strconv.Atoi(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid peer_transport_port %q", lineNumber+1, value)
			}
			cfg.PeerTransportPort = peerTransportPort
		case "parent_swarm_id":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["parent_swarm_id"] = struct{}{}
			cfg.ParentSwarmID = strings.TrimSpace(value)
		case "pairing_state":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["pairing_state"] = struct{}{}
			cfg.PairingState = normalizePairingState(value)
		case "deploy_container_enabled":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_enabled"] = struct{}{}
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid deploy_container_enabled %q", lineNumber+1, value)
			}
			cfg.DeployContainer.Enabled = enabled
		case "deploy_container_host_driven":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_host_driven"] = struct{}{}
			hostDriven, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid deploy_container_host_driven %q", lineNumber+1, value)
			}
			cfg.DeployContainer.HostDriven = hostDriven
		case "deploy_container_sync_enabled":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_sync_enabled"] = struct{}{}
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid deploy_container_sync_enabled %q", lineNumber+1, value)
			}
			cfg.DeployContainer.SyncEnabled = enabled
		case "deploy_container_sync_mode":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_sync_mode"] = struct{}{}
			cfg.DeployContainer.SyncMode = strings.TrimSpace(value)
		case "deploy_container_sync_modules":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_sync_modules"] = struct{}{}
			cfg.DeployContainer.SyncModules = parseCSVList(value)
		case "deploy_container_sync_owner_swarm_id":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_sync_owner_swarm_id"] = struct{}{}
			cfg.DeployContainer.SyncOwnerSwarmID = strings.TrimSpace(value)
		case "deploy_container_sync_credential_url":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_sync_credential_url"] = struct{}{}
			cfg.DeployContainer.SyncCredentialURL = strings.TrimSpace(value)
		case "deploy_container_sync_agent_url":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_sync_agent_url"] = struct{}{}
			cfg.DeployContainer.SyncAgentURL = strings.TrimSpace(value)
		case "deploy_container_deployment_id":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_deployment_id"] = struct{}{}
			cfg.DeployContainer.DeploymentID = strings.TrimSpace(value)
		case "deploy_container_host_api_base_url":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_host_api_base_url"] = struct{}{}
			cfg.DeployContainer.HostAPIBaseURL = strings.TrimSpace(value)
		case "deploy_container_host_desktop_url":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_host_desktop_url"] = struct{}{}
			cfg.DeployContainer.HostDesktopURL = strings.TrimSpace(value)
		case "deploy_container_local_transport_socket_path":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_local_transport_socket_path"] = struct{}{}
			cfg.DeployContainer.LocalTransportSocketPath = strings.TrimSpace(value)
		case "deploy_container_bootstrap_secret":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_bootstrap_secret"] = struct{}{}
			cfg.DeployContainer.BootstrapSecret = strings.TrimSpace(value)
		case "deploy_container_verification_code":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["deploy_container_verification_code"] = struct{}{}
			cfg.DeployContainer.VerificationCode = strings.TrimSpace(value)
		case "remote_deploy_enabled":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_enabled"] = struct{}{}
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid remote_deploy_enabled %q", lineNumber+1, value)
			}
			cfg.RemoteDeploy.Enabled = enabled
		case "remote_deploy_session_id":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_session_id"] = struct{}{}
			cfg.RemoteDeploy.SessionID = strings.TrimSpace(value)
		case "remote_deploy_session_token":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_session_token"] = struct{}{}
			cfg.RemoteDeploy.SessionToken = strings.TrimSpace(value)
		case "remote_deploy_host_api_base_url":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_host_api_base_url"] = struct{}{}
			cfg.RemoteDeploy.HostAPIBaseURL = strings.TrimSpace(value)
		case "remote_deploy_host_desktop_url":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_host_desktop_url"] = struct{}{}
			cfg.RemoteDeploy.HostDesktopURL = strings.TrimSpace(value)
		case "remote_deploy_invite_token":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_invite_token"] = struct{}{}
			cfg.RemoteDeploy.InviteToken = strings.TrimSpace(value)
		case "remote_deploy_sync_enabled":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_sync_enabled"] = struct{}{}
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid remote_deploy_sync_enabled %q", lineNumber+1, value)
			}
			cfg.RemoteDeploy.SyncEnabled = enabled
		case "remote_deploy_sync_mode":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_sync_mode"] = struct{}{}
			cfg.RemoteDeploy.SyncMode = strings.TrimSpace(value)
		case "remote_deploy_sync_owner_swarm_id":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_sync_owner_swarm_id"] = struct{}{}
			cfg.RemoteDeploy.SyncOwnerSwarmID = strings.TrimSpace(value)
		case "remote_deploy_sync_credential_url":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["remote_deploy_sync_credential_url"] = struct{}{}
			cfg.RemoteDeploy.SyncCredentialURL = strings.TrimSpace(value)
		default:
			if isLegacyIgnoredKey(key) {
				continue
			}
			return FileConfig{}, nil, fmt.Errorf("line %d: unknown key %q", lineNumber+1, key)
		}
	}
	if _, ok := seen["advertise_port"]; !ok {
		cfg.AdvertisePort = cfg.Port
	}
	return cfg, seen, nil
}

func appendMissingKeys(path, text string, perm os.FileMode, cfg FileConfig, seen map[string]struct{}) error {
	lines := missingKeyLines(cfg, seen)
	if len(lines) == 0 {
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(text), perm); err != nil {
		return fmt.Errorf("migrate startup config %q: %w", path, err)
	}
	return nil
}

func missingKeyLines(cfg FileConfig, seen map[string]struct{}) []string {
	lines := []string{}
	if _, ok := seen["bypass_permissions"]; !ok {
		lines = append(lines,
			"# Bypass normal tool permission prompts.",
			"# Plan mode still stays plan mode, and exit_plan_mode still requires approval.",
			fmt.Sprintf("bypass_permissions = %t", cfg.BypassPermissions),
		)
	}
	if _, ok := seen["retain_tool_output_history"]; !ok {
		lines = append(lines,
			"# Keep sanitized tool/permission output in persisted history so refresh can show it.",
			"# false keeps the current privacy-preserving placeholder behavior.",
			fmt.Sprintf("retain_tool_output_history = %t", cfg.RetainToolOutputHistory),
		)
	}
	if _, ok := seen[devModeKey]; !ok {
		lines = append(lines,
			"# Enable source-checkout dev behavior for local child image rebuilds.",
			"# false = runtime-safe behavior only; true = allow dev-only rebuild flow from dev_root.",
			fmt.Sprintf("%s = %t", devModeKey, cfg.DevMode),
		)
	}
	if _, ok := seen[devRootKey]; !ok {
		lines = append(lines,
			"# Source checkout root used for dev-only local child image rebuilds.",
			"# Leave blank until a rebuild from a source checkout records it.",
			fmt.Sprintf("%s = %s", devRootKey, cfg.DevRoot),
		)
	}
	if _, ok := seen["advertise_host"]; !ok {
		lines = append(lines,
			"# Canonical LAN host or IP that other machines should use to reach this Swarm.",
			"# Leave blank to detect or confirm it in onboarding.",
			fmt.Sprintf("advertise_host = %s", cfg.AdvertiseHost),
		)
	}
	if _, ok := seen["advertise_port"]; !ok {
		lines = append(lines,
			"# Canonical LAN port that other machines should use to reach this Swarm.",
			"# Defaults to the backend API port and changing it requires a restart.",
			fmt.Sprintf("advertise_port = %d", cfg.AdvertisePort),
		)
	}
	if _, ok := seen["swarm_name"]; !ok {
		lines = append(lines,
			"# Human-readable Swarm name shown in onboarding and discovery surfaces.",
			"# Leave blank to set it later.",
			fmt.Sprintf("swarm_name = %s", cfg.SwarmName),
		)
	}
	if _, ok := seen[swarmModeKey]; !ok {
		lines = append(lines,
			"# Whether this Swarm should participate in shared swarm networking.",
			"# false = standalone local use, true = enable swarm role/pairing/transport settings.",
			fmt.Sprintf("%s = %t", swarmModeKey, cfg.SwarmMode),
		)
	}
	if _, ok := seen["child"]; !ok {
		lines = append(lines,
			"# Whether this Swarm should bootstrap as a child.",
			"# false = master/default, true = child.",
			fmt.Sprintf("child = %t", cfg.Child),
		)
	}
	if _, ok := seen[startupModeKey]; !ok {
		lines = append(lines,
			"# Swarm startup mode.",
			"# interactive = normal local use; Swarm runs when you launch it.",
			"# box = always-on box mode; Swarm should be treated as an always-running service.",
			"# box does NOT by itself survive reboot/login/logout. For true persistence,",
			"# install/run Swarm under systemd or another OS service manager.",
			fmt.Sprintf("%s = %s", startupModeKey, cfg.Mode),
		)
	}
	if _, ok := seen[bootstrapModeKey]; !ok {
		lines = append(lines,
			"# Bootstrap network mode.",
			"# lan = connect over the local network.",
			"# tailscale = connect over a Tailscale URL.",
			fmt.Sprintf("%s = %s", bootstrapModeKey, cfg.NetworkMode),
		)
	}
	if _, ok := seen["tailscale_url"]; !ok {
		lines = append(lines,
			"# Canonical persisted Tailscale URL for bootstrap and pairing flows.",
			"# Leave blank when not using a manual Tailscale address.",
			fmt.Sprintf("tailscale_url = %s", cfg.TailscaleURL),
		)
	}
	if _, ok := seen["peer_transport_port"]; !ok {
		lines = append(lines,
			"# Local-only peer transport port for peer forwarding such as Tailscale Serve or SSH tunneling.",
			"# Changing it requires a restart.",
			fmt.Sprintf("peer_transport_port = %d", cfg.PeerTransportPort),
		)
	}
	if _, ok := seen["parent_swarm_id"]; !ok {
		lines = append(lines,
			"# Parent swarm ID for child bootstrap/attach flows.",
			fmt.Sprintf("parent_swarm_id = %s", cfg.ParentSwarmID),
		)
	}
	if _, ok := seen["pairing_state"]; !ok {
		lines = append(lines,
			"# Persisted local pairing state.",
			fmt.Sprintf("pairing_state = %s", cfg.PairingState),
		)
	}
	if _, ok := seen["deploy_container_enabled"]; !ok {
		lines = append(lines,
			"# Deploy/container child attach bootstrap payload.",
			fmt.Sprintf("deploy_container_enabled = %t", cfg.DeployContainer.Enabled),
			fmt.Sprintf("deploy_container_host_driven = %t", cfg.DeployContainer.HostDriven),
			fmt.Sprintf("deploy_container_sync_enabled = %t", cfg.DeployContainer.SyncEnabled),
			fmt.Sprintf("deploy_container_sync_mode = %s", cfg.DeployContainer.SyncMode),
			fmt.Sprintf("deploy_container_sync_modules = %s", formatCSVList(cfg.DeployContainer.SyncModules)),
			fmt.Sprintf("deploy_container_sync_owner_swarm_id = %s", cfg.DeployContainer.SyncOwnerSwarmID),
			fmt.Sprintf("deploy_container_sync_credential_url = %s", cfg.DeployContainer.SyncCredentialURL),
			fmt.Sprintf("deploy_container_sync_agent_url = %s", cfg.DeployContainer.SyncAgentURL),
			fmt.Sprintf("deploy_container_deployment_id = %s", cfg.DeployContainer.DeploymentID),
			fmt.Sprintf("deploy_container_host_api_base_url = %s", cfg.DeployContainer.HostAPIBaseURL),
			fmt.Sprintf("deploy_container_host_desktop_url = %s", cfg.DeployContainer.HostDesktopURL),
			fmt.Sprintf("deploy_container_local_transport_socket_path = %s", cfg.DeployContainer.LocalTransportSocketPath),
			fmt.Sprintf("deploy_container_bootstrap_secret = %s", cfg.DeployContainer.BootstrapSecret),
			fmt.Sprintf("deploy_container_verification_code = %s", cfg.DeployContainer.VerificationCode),
		)
	}
	if _, ok := seen["remote_deploy_enabled"]; !ok {
		lines = append(lines,
			"# Remote deploy child bootstrap payload.",
			fmt.Sprintf("remote_deploy_enabled = %t", cfg.RemoteDeploy.Enabled),
			fmt.Sprintf("remote_deploy_session_id = %s", cfg.RemoteDeploy.SessionID),
			fmt.Sprintf("remote_deploy_session_token = %s", cfg.RemoteDeploy.SessionToken),
			fmt.Sprintf("remote_deploy_host_api_base_url = %s", cfg.RemoteDeploy.HostAPIBaseURL),
			fmt.Sprintf("remote_deploy_host_desktop_url = %s", cfg.RemoteDeploy.HostDesktopURL),
			fmt.Sprintf("remote_deploy_invite_token = %s", cfg.RemoteDeploy.InviteToken),
			fmt.Sprintf("remote_deploy_sync_enabled = %t", cfg.RemoteDeploy.SyncEnabled),
			fmt.Sprintf("remote_deploy_sync_mode = %s", cfg.RemoteDeploy.SyncMode),
			fmt.Sprintf("remote_deploy_sync_owner_swarm_id = %s", cfg.RemoteDeploy.SyncOwnerSwarmID),
			fmt.Sprintf("remote_deploy_sync_credential_url = %s", cfg.RemoteDeploy.SyncCredentialURL),
		)
	}
	return lines
}

func validate(cfg FileConfig) error {
	switch cfg.Mode {
	case ModeInteractive, ModeBox:
	default:
		return fmt.Errorf("invalid %s %q (expected %q or %q)", startupModeKey, cfg.Mode, ModeInteractive, ModeBox)
	}
	if strings.TrimSpace(cfg.DevRoot) != "" && !filepath.IsAbs(cfg.DevRoot) {
		return fmt.Errorf("invalid %s %q (expected an absolute path)", devRootKey, cfg.DevRoot)
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("host must not be empty")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("invalid port %d (expected 1-65535)", cfg.Port)
	}
	if _, err := normalizeAdvertiseHost(cfg.AdvertiseHost); err != nil {
		return fmt.Errorf("invalid advertise_host %q: %w", cfg.AdvertiseHost, err)
	}
	if cfg.AdvertisePort < 1 || cfg.AdvertisePort > 65535 {
		return fmt.Errorf("invalid advertise_port %d (expected 1-65535)", cfg.AdvertisePort)
	}
	if cfg.DesktopPort < 0 || cfg.DesktopPort > 65535 {
		return fmt.Errorf("invalid desktop_port %d (expected 0-65535)", cfg.DesktopPort)
	}
	if cfg.PeerTransportPort < 1 || cfg.PeerTransportPort > 65535 {
		return fmt.Errorf("invalid peer_transport_port %d (expected 1-65535)", cfg.PeerTransportPort)
	}
	if !isValidPairingState(cfg.PairingState) {
		return fmt.Errorf("invalid pairing_state %q", cfg.PairingState)
	}
	return nil
}

func requiredKeys() []string {
	return []string{startupModeKey, devModeKey, devRootKey, "host", "port", "advertise_host", "advertise_port", "desktop_port", "bypass_permissions", "retain_tool_output_history", "swarm_name", swarmModeKey, "child", bootstrapModeKey, "tailscale_url", "peer_transport_port", "parent_swarm_id", "pairing_state", "deploy_container_enabled", "deploy_container_sync_enabled", "deploy_container_sync_mode", "deploy_container_sync_modules", "deploy_container_sync_owner_swarm_id", "deploy_container_sync_credential_url", "deploy_container_sync_agent_url", "deploy_container_deployment_id", "deploy_container_host_api_base_url", "deploy_container_host_desktop_url", "deploy_container_local_transport_socket_path", "deploy_container_bootstrap_secret", "deploy_container_verification_code", "remote_deploy_enabled", "remote_deploy_session_id", "remote_deploy_session_token", "remote_deploy_host_api_base_url", "remote_deploy_host_desktop_url", "remote_deploy_invite_token", "remote_deploy_sync_enabled", "remote_deploy_sync_mode", "remote_deploy_sync_owner_swarm_id", "remote_deploy_sync_credential_url"}
}

func allowsEmptyValue(key string) bool {
	switch key {
	case devRootKey, "swarm_name", "tailscale_url", "advertise_host", "advertise_addr", "onboarding_state", "swarm_id", "parent_swarm_id", "pairing_state", "deploy_container_sync_mode", "deploy_container_sync_modules", "deploy_container_sync_owner_swarm_id", "deploy_container_sync_credential_url", "deploy_container_sync_agent_url", "deploy_container_deployment_id", "deploy_container_host_api_base_url", "deploy_container_host_desktop_url", "deploy_container_local_transport_socket_path", "deploy_container_bootstrap_secret", "deploy_container_verification_code", "remote_deploy_session_id", "remote_deploy_session_token", "remote_deploy_host_api_base_url", "remote_deploy_host_desktop_url", "remote_deploy_invite_token", "remote_deploy_sync_mode", "remote_deploy_sync_owner_swarm_id", "remote_deploy_sync_credential_url":
		return true
	default:
		return false
	}
}

func isLegacyIgnoredKey(key string) bool {
	switch key {
	case "webauth_enabled", "onboarding_state", "swarm_role", "swarm_id", "local_transport_port", "tailscale_transport_port":
		return true
	default:
		return false
	}
}

func parseCSVList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func formatCSVList(values []string) string {
	if len(values) == 0 {
		return ""
	}
	normalized := parseCSVList(strings.Join(values, ","))
	return strings.Join(normalized, ",")
}

func normalizeNetworkMode(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func decodeEnvMultiline(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, `\n`, "\n")
}

func normalizeAdvertiseHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if host, port, err := net.SplitHostPort(value); err == nil && strings.TrimSpace(host) != "" && strings.TrimSpace(port) != "" {
		return "", errors.New("must not include a port; use advertise_port separately")
	}
	if strings.Contains(value, "://") {
		return "", errors.New("must be a host or IP only, without a URL scheme")
	}
	if strings.Contains(value, "/") {
		return "", errors.New("must not contain path separators")
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
	}
	return value, nil
}

func isValidNetworkMode(value string) bool {
	switch normalizeNetworkMode(value) {
	case NetworkModeLAN, NetworkModeTailscale:
		return true
	default:
		return false
	}
}

func normalizeLegacyAdvertiseMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "local", "lan":
		return NetworkModeLAN, nil
	case "tailscale":
		return NetworkModeTailscale, nil
	default:
		return "", fmt.Errorf("expected %q, %q, or %q", "local", NetworkModeLAN, NetworkModeTailscale)
	}
}

func normalizePairingState(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isValidPairingState(value string) bool {
	switch normalizePairingState(value) {
	case "", PairingStateUnpaired, PairingStateBootstrapReady, PairingStatePendingApproval, PairingStatePaired, PairingStateRejected:
		return true
	default:
		return false
	}
}
