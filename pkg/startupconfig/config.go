package startupconfig

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

const (
	NetworkModeLAN       = "lan"
	NetworkModeTailscale = "tailscale"

	DefaultHost              = "127.0.0.1"
	DefaultPort              = 7781
	DefaultDesktopPort       = 5555
	DefaultPeerTransportPort = 7791

	configFileName = "swarm.conf"
	configFileMode = 0o600

	devModeKey                   = "dev_mode"
	devRootKey                   = "dev_root"
	desktopOnboardingCompleteKey = "desktop_onboarding_complete"
	childStartupConfigEnv        = "SWARM_CHILD_STARTUP_CONFIG"
)

type FileConfig struct {
	Path                         string
	Exists                       bool
	DevMode                      bool
	DevRoot                      string
	Host                         string
	Port                         int
	AdvertiseHost                string
	AdvertisePort                int
	DesktopPort                  int
	BypassPermissions            bool
	RetainToolOutputHistory      bool
	V3Diagnostics                bool
	ProviderAPIDiagnostics       bool
	LongSessionDiagnostics       bool
	SwarmName                    string
	DesktopOnboardingComplete    bool
	DesktopOnboardingCompleteSet bool
	Child                        bool
	TailscaleURL                 string
	PeerTransportPort            int
}

type BootstrapFlags struct {
	SwarmName        string
	SwarmNameSet     bool
	Child            bool
	ChildSet         bool
	AdvertiseHost    string
	AdvertiseHostSet bool
	AdvertisePort    int
	AdvertisePortSet bool
	TailscaleURL     string
	TailscaleURLSet  bool
}

func (b BootstrapFlags) HasAny() bool {
	return b.SwarmNameSet || b.ChildSet || b.AdvertiseHostSet || b.AdvertisePortSet || b.TailscaleURLSet
}

func (b BootstrapFlags) Validate() error {
	if b.SwarmNameSet && strings.TrimSpace(b.SwarmName) == "" {
		return errors.New("invalid --swarm-name: value must be non-empty")
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
	configDir, err := storagecontract.ResolveRoot(storagecontract.RootConfig, storagecontract.Options{})
	if err != nil {
		return "", fmt.Errorf("resolve startup config directory: %w", err)
	}
	return storagecontract.Join(configDir, configFileName)
}

func Default(path string) FileConfig {
	return FileConfig{
		Path:                         path,
		DevMode:                      false,
		DevRoot:                      "",
		Host:                         DefaultHost,
		Port:                         DefaultPort,
		AdvertiseHost:                "",
		AdvertisePort:                DefaultPort,
		DesktopPort:                  DefaultDesktopPort,
		BypassPermissions:            false,
		RetainToolOutputHistory:      false,
		V3Diagnostics:                false,
		ProviderAPIDiagnostics:       false,
		LongSessionDiagnostics:       false,
		SwarmName:                    "",
		DesktopOnboardingComplete:    true,
		DesktopOnboardingCompleteSet: false,
		Child:                        false,
		TailscaleURL:                 "",
		PeerTransportPort:            DefaultPeerTransportPort,
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
	parsed.Path = path
	parsed.Exists = true
	if err := validate(parsed); err != nil {
		return FileConfig{}, fmt.Errorf("parse startup config %q: %w", path, err)
	}
	if err := appendMissingKeys(path, textWithoutLegacyIgnoredEntries(text), info.Mode().Perm(), parsed, seen); err != nil {
		return FileConfig{}, err
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
	if err := atomicWriteConfig(cfg.Path, []byte(content), configFileMode); err != nil {
		return fmt.Errorf("write startup config %q: %w", cfg.Path, err)
	}
	return nil
}

func DirectLANDesktopWarning(cfg FileConfig) string {
	if cfg.DesktopPort == 0 {
		return ""
	}
	host := strings.TrimSpace(cfg.Host)
	if host == "" || IsLoopbackHost(host) {
		return ""
	}
	return "Direct LAN desktop access is not implemented safely in this MVP. Keep host=127.0.0.1 and use an SSH tunnel, or use Tailscale, instead of opening the desktop directly on a private LAN address."
}

// IsLoopbackHost reports whether host names a loopback-only interface.
func IsLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func Format(cfg FileConfig) string {
	return fmt.Sprintf(`# Enable source-checkout dev behavior for local child image rebuilds.
# false = runtime-safe behavior only; true = allow dev-only rebuild flow from dev_root.
dev_mode = %t

# Source checkout root used for dev-only local child image rebuilds.
# Leave blank until a rebuild from a source checkout records it.
dev_root = %s

# Network bind host for the Swarm backend.
# Keep this at 127.0.0.1 for local-only use.
# MVP note: direct private-LAN desktop access is not implemented safely yet.
# For another device, keep host at 127.0.0.1 and use SSH tunneling or Tailscale.
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

# Persist verbose V3 diagnostic events.
# Enable temporarily while debugging failed sessions; diagnostics may contain request context.
v3_diagnostics = %t

# Log sanitized outbound provider API request and response payloads to daemon logs.
# This is separate from v3_diagnostics and omits/redacts API keys and auth headers.
provider_api_diagnostics = %t

# Record bounded metadata-only diagnostics for investigating long-session memory and lag.
# Artifacts are private local files under the canonical logs root; changing this requires a restart.
long_session_diagnostics = %t

# Human-readable Swarm name shown in onboarding and discovery surfaces.
# Leave blank to set it later.
swarm_name = %s

# Explicit Desktop onboarding completion marker.
# false = Desktop must continue onboarding; true = Desktop may open the launcher.
desktop_onboarding_complete = %t

# Whether this Swarm should bootstrap as a child.
# false = primary/default, true = child.
child = %t

# Canonical persisted Tailscale URL for bootstrap and connectivity.
# Leave blank when not using a manual Tailscale address.
tailscale_url = %s

# Local-only peer transport port for peer forwarding such as Tailscale Serve or SSH tunneling.
# Changing it requires a restart.
peer_transport_port = %d

`, cfg.DevMode, cfg.DevRoot, cfg.Host, cfg.Port, cfg.AdvertiseHost, cfg.AdvertisePort, cfg.DesktopPort, cfg.BypassPermissions, cfg.RetainToolOutputHistory, cfg.V3Diagnostics, cfg.ProviderAPIDiagnostics, cfg.LongSessionDiagnostics, cfg.SwarmName, cfg.DesktopOnboardingComplete, cfg.Child, cfg.TailscaleURL, cfg.PeerTransportPort)
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
		if isLegacyIgnoredKey(key) {
			continue
		}
		if !allowsEmptyValue(key) && value == "" {
			return FileConfig{}, nil, fmt.Errorf("line %d: value for %q must be non-empty", lineNumber+1, key)
		}
		switch key {
		case "startup_mode":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			// Legacy startup_mode is accepted as inert input only; it no longer
			// controls daemon behavior or gets re-emitted.
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
		case "mode":
			if legacyStartupModeSeen {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate legacy startup mode", lineNumber+1)
			}
			legacyStartupModeSeen = true
			// Legacy mode values are inert input only; they no longer control daemon behavior.
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
		case "v3_diagnostics":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["v3_diagnostics"] = struct{}{}
			v3Diagnostics, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid %s %q", lineNumber+1, key, value)
			}
			cfg.V3Diagnostics = v3Diagnostics
		case "provider_api_diagnostics":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["provider_api_diagnostics"] = struct{}{}
			providerAPIDiagnostics, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid provider_api_diagnostics %q", lineNumber+1, value)
			}
			cfg.ProviderAPIDiagnostics = providerAPIDiagnostics
		case "long_session_diagnostics":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["long_session_diagnostics"] = struct{}{}
			longSessionDiagnostics, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid long_session_diagnostics %q", lineNumber+1, value)
			}
			cfg.LongSessionDiagnostics = longSessionDiagnostics
		case "swarm_name":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen["swarm_name"] = struct{}{}
			cfg.SwarmName = value
		case "swarm" + "_mode":
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			if _, err := strconv.ParseBool(value); err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid legacy %s %q", lineNumber+1, key, value)
			}
		case desktopOnboardingCompleteKey:
			if _, exists := rawSeen[key]; exists {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate key %q", lineNumber+1, key)
			}
			rawSeen[key] = struct{}{}
			seen[desktopOnboardingCompleteKey] = struct{}{}
			onboardingComplete, err := strconv.ParseBool(value)
			if err != nil {
				return FileConfig{}, nil, fmt.Errorf("line %d: invalid %s %q", lineNumber+1, desktopOnboardingCompleteKey, value)
			}
			cfg.DesktopOnboardingComplete = onboardingComplete
			cfg.DesktopOnboardingCompleteSet = true
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
		case "network_mode", "advertise_mode":
			if legacyBootstrapModeSeen {
				return FileConfig{}, nil, fmt.Errorf("line %d: duplicate legacy bootstrap mode", lineNumber+1)
			}
			legacyBootstrapModeSeen = true
			// Legacy bootstrap network mode is ignored; runtime reachability no longer has a mode selector.
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

func textWithoutLegacyIgnoredEntries(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		key, _, ok := strings.Cut(line, "=")
		if ok && isLegacyIgnoredKey(strings.TrimSpace(key)) {
			continue
		}
		kept = append(kept, rawLine)
	}
	return strings.Join(kept, "\n")
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
	if err := atomicWriteConfig(path, []byte(text), perm); err != nil {
		return fmt.Errorf("migrate startup config %q: %w", path, err)
	}
	return nil
}

func atomicWriteConfig(path string, payload []byte, createMode os.FileMode) error {
	dir := filepath.Dir(path)
	mode := createMode.Perm()
	uid, gid := -1, -1
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refuse to replace non-regular startup config %q", path)
		}
		mode = info.Mode().Perm()
		uid, gid = fileOwnership(info)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect startup config %q: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".swarm.conf-*")
	if err != nil {
		return fmt.Errorf("create temporary startup config: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary startup config mode: %w", err)
	}
	if uid >= 0 && gid >= 0 {
		if err := tmp.Chown(uid, gid); err != nil {
			return fmt.Errorf("preserve startup config ownership: %w", err)
		}
	}
	if _, err := tmp.Write(payload); err != nil {
		return fmt.Errorf("write temporary startup config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary startup config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary startup config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace startup config: %w", err)
	}
	committed = true
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open startup config directory for sync: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("sync startup config directory: %w", err)
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
	if _, ok := seen["v3_diagnostics"]; !ok {
		lines = append(lines,
			"# Persist verbose V3 diagnostic events.",
			"# Enable temporarily while debugging failed sessions; diagnostics may contain request context.",
			fmt.Sprintf("v3_diagnostics = %t", cfg.V3Diagnostics),
		)
	}
	if _, ok := seen["provider_api_diagnostics"]; !ok {
		lines = append(lines,
			"# Log sanitized outbound provider API request and response payloads to daemon logs.",
			"# This is separate from v3_diagnostics and omits/redacts API keys and auth headers.",
			fmt.Sprintf("provider_api_diagnostics = %t", cfg.ProviderAPIDiagnostics),
		)
	}
	if _, ok := seen["long_session_diagnostics"]; !ok {
		lines = append(lines,
			"# Record bounded metadata-only diagnostics for investigating long-session memory and lag.",
			"# Artifacts are private local files under the canonical logs root; changing this requires a restart.",
			fmt.Sprintf("long_session_diagnostics = %t", cfg.LongSessionDiagnostics),
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
			"# MVP note: direct private-LAN desktop access is not implemented safely yet.",
			"# Prefer SSH tunneling or Tailscale instead of opening the desktop directly on LAN.",
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
	if _, ok := seen["child"]; !ok {
		lines = append(lines,
			"# Whether this Swarm should bootstrap as a child.",
			"# false = primary/default, true = child.",
			fmt.Sprintf("child = %t", cfg.Child),
		)
	}
	if _, ok := seen["tailscale_url"]; !ok {
		lines = append(lines,
			"# Canonical persisted Tailscale URL for bootstrap and connectivity.",
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
	return lines
}

func validate(cfg FileConfig) error {
	if strings.TrimSpace(cfg.DevRoot) != "" && !filepath.IsAbs(cfg.DevRoot) {
		return fmt.Errorf("invalid %s %q (expected an absolute path)", devRootKey, cfg.DevRoot)
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("host must not be empty")
	}
	if !IsLoopbackHost(cfg.Host) {
		return fmt.Errorf("unsupported non-loopback host %q: authenticated API/desktop exposure is not configured; keep host=%s and use an SSH tunnel or Tailscale forwarding", cfg.Host, DefaultHost)
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
	return nil
}

func allowsEmptyValue(key string) bool {
	switch key {
	case devRootKey, "swarm_name", "tailscale_url", "advertise_host", "advertise_addr", "onboarding_state", "swarm_id":
		return true
	default:
		return false
	}
}

func isLegacyIgnoredKey(key string) bool {
	switch key {
	case "webauth_enabled", "onboarding_state", "swarm_id", "swarm_role", "swarm" + "_mode", "local_transport_port", "tailscale_transport_port":
		return true
	default:
		return false
	}
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
