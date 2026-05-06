package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/appstorage"
)

const (
	ModeSingle = "single"
	ModeBox    = "box"

	StartupModeInteractive = startupconfig.ModeInteractive
	StartupModeBox         = startupconfig.ModeBox
)

type Config struct {
	StartupMode             string
	ConfigPath              string
	Mode                    string
	ListenAddr              string
	DesktopPort             int
	PeerTransportPort       int
	BypassPermissions       bool
	RetainToolOutputHistory bool
	DataDir                 string
	DBPath                  string
	LockPath                string
	StartupCWD              string
}

func Parse(args []string) (Config, error) {
	configPath, err := startupconfig.ResolvePath()
	if err != nil {
		return Config{}, err
	}
	startupCfg, err := startupconfig.Load(configPath)
	if err != nil {
		return Config{}, err
	}

	roots, err := appstorage.DefaultRoots()
	if err != nil {
		return Config{}, err
	}

	defaultDataDir := roots.DataDir
	defaultDBPath := filepath.Join(defaultDataDir, "swarmd.pebble")
	defaultLockPath := filepath.Join(roots.RuntimeDir, "swarmd.lock")
	defaultMode, err := runtimeModeForStartupMode(startupCfg.Mode)
	if err != nil {
		return Config{}, err
	}
	defaultListenAddr := net.JoinHostPort(startupCfg.Host, strconv.Itoa(startupCfg.Port))

	bootstrapArgs, filteredArgs, err := parseBootstrapArgs(args, startupCfg.Exists)
	if err != nil {
		return Config{}, err
	}
	if bootstrapArgs.HasAny() {
		if startupCfg.Exists {
			return Config{}, startupconfig.BootstrapExistingConfigError(configPath)
		}
		if err := bootstrapArgs.Validate(); err != nil {
			return Config{}, err
		}
	}

	fs := flag.NewFlagSet("swarmd", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	cfg := Config{
		StartupMode:             startupCfg.Mode,
		ConfigPath:              configPath,
		Mode:                    defaultMode,
		ListenAddr:              defaultListenAddr,
		DesktopPort:             startupCfg.DesktopPort,
		PeerTransportPort:       startupCfg.PeerTransportPort,
		BypassPermissions:       startupCfg.BypassPermissions,
		RetainToolOutputHistory: startupCfg.RetainToolOutputHistory,
	}
	fs.StringVar(&cfg.Mode, "mode", defaultMode, "runtime mode: single or box")
	fs.StringVar(&cfg.ListenAddr, "listen", defaultListenAddr, "HTTP listen address")
	fs.IntVar(&cfg.DesktopPort, "desktop-port", startupCfg.DesktopPort, "desktop HTTP listen port (0 disables desktop listener)")
	fs.BoolVar(&cfg.BypassPermissions, "bypass-permissions", startupCfg.BypassPermissions, "bypass normal tool permission prompts (exit_plan_mode still requires approval)")
	fs.StringVar(&cfg.DataDir, "data-dir", defaultDataDir, "data directory root")
	fs.StringVar(&cfg.DBPath, "db-path", defaultDBPath, "Pebble database path")
	fs.StringVar(&cfg.LockPath, "lock-path", defaultLockPath, "daemon lock file path")
	fs.StringVar(&cfg.StartupCWD, "cwd", "", "startup working directory binding (defaults to process cwd)")

	if err := fs.Parse(filteredArgs); err != nil {
		return Config{}, err
	}

	if !startupCfg.Exists {
		startupCfg, err = startupConfigFromRuntime(configPath, cfg.Mode, cfg.ListenAddr, cfg.DesktopPort, cfg.BypassPermissions, cfg.RetainToolOutputHistory)
		if err != nil {
			return Config{}, err
		}
		startupCfg, err = startupCfg.ApplyBootstrap(bootstrapArgs)
		if err != nil {
			return Config{}, err
		}
		if err := startupconfig.Write(startupCfg); err != nil {
			return Config{}, err
		}
		cfg.StartupMode = startupCfg.Mode
		cfg.RetainToolOutputHistory = startupCfg.RetainToolOutputHistory
	}

	if err := validateRuntimeMode(cfg.Mode); err != nil {
		return Config{}, err
	}

	if cfg.DesktopPort < 0 || cfg.DesktopPort > 65535 {
		return Config{}, fmt.Errorf("invalid desktop port %d (expected 0-65535)", cfg.DesktopPort)
	}

	if cfg.StartupCWD == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("resolve process cwd: %w", err)
		}
		cfg.StartupCWD = cwd
	}

	if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Clean(filepath.Join(cfg.StartupCWD, cfg.DataDir))
	}
	if !filepath.IsAbs(cfg.DBPath) {
		cfg.DBPath = filepath.Clean(filepath.Join(cfg.StartupCWD, cfg.DBPath))
	}
	if !filepath.IsAbs(cfg.LockPath) {
		cfg.LockPath = filepath.Clean(filepath.Join(cfg.StartupCWD, cfg.LockPath))
	}

	return cfg, nil
}

func validateRuntimeMode(mode string) error {
	switch mode {
	case ModeSingle, ModeBox:
		return nil
	default:
		return fmt.Errorf("invalid mode %q (expected %q or %q)", mode, ModeSingle, ModeBox)
	}
}

func runtimeModeForStartupMode(startupMode string) (string, error) {
	switch startupMode {
	case StartupModeInteractive:
		return ModeSingle, nil
	case StartupModeBox:
		return ModeBox, nil
	default:
		return "", fmt.Errorf("invalid startup mode %q (expected %q or %q)", startupMode, StartupModeInteractive, StartupModeBox)
	}
}

func startupModeForRuntimeMode(runtimeMode string) (string, error) {
	switch runtimeMode {
	case ModeSingle:
		return StartupModeInteractive, nil
	case ModeBox:
		return StartupModeBox, nil
	default:
		return "", fmt.Errorf("invalid mode %q (expected %q or %q)", runtimeMode, ModeSingle, ModeBox)
	}
}

func startupConfigFromRuntime(path, mode, listenAddr string, desktopPort int, bypassPermissions, retainToolOutputHistory bool) (startupconfig.FileConfig, error) {
	startupMode, err := startupModeForRuntimeMode(mode)
	if err != nil {
		return startupconfig.FileConfig{}, err
	}
	if strings.TrimSpace(listenAddr) == "" {
		return startupconfig.FileConfig{}, errors.New("listen address must not be empty")
	}
	host, portText, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return startupconfig.FileConfig{}, fmt.Errorf("invalid listen address %q: %w", listenAddr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return startupconfig.FileConfig{}, fmt.Errorf("invalid listen port %q", portText)
	}
	cfg := startupconfig.Default(path)
	cfg.Mode = startupMode
	cfg.Host = host
	cfg.Port = port
	cfg.DesktopPort = desktopPort
	cfg.BypassPermissions = bypassPermissions
	cfg.RetainToolOutputHistory = retainToolOutputHistory
	if err := validateStartupConfig(cfg); err != nil {
		return startupconfig.FileConfig{}, err
	}
	return cfg, nil
}

func validateStartupConfig(cfg startupconfig.FileConfig) error {
	switch cfg.Mode {
	case StartupModeInteractive, StartupModeBox:
	default:
		return fmt.Errorf("invalid mode %q (expected %q or %q)", cfg.Mode, StartupModeInteractive, StartupModeBox)
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("host must not be empty")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("invalid port %d (expected 1-65535)", cfg.Port)
	}
	if cfg.DesktopPort < 0 || cfg.DesktopPort > 65535 {
		return fmt.Errorf("invalid desktop_port %d (expected 0-65535)", cfg.DesktopPort)
	}
	return nil
}

func parseBootstrapArgs(args []string, startupExists bool) (startupconfig.BootstrapFlags, []string, error) {
	bootstrap := startupconfig.BootstrapFlags{}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--swarm-name":
			if i+1 >= len(args) {
				return startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --swarm-name")
			}
			i++
			bootstrap.SwarmName = args[i]
			bootstrap.SwarmNameSet = true
		case "--child":
			bootstrap.Child = true
			bootstrap.ChildSet = true
		case "--tailscale-url":
			if i+1 >= len(args) {
				return startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --tailscale-url")
			}
			i++
			bootstrap.TailscaleURL = args[i]
			bootstrap.TailscaleURLSet = true
		case "--advertise-host":
			if i+1 >= len(args) {
				return startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --advertise-host")
			}
			i++
			bootstrap.AdvertiseHost = args[i]
			bootstrap.AdvertiseHostSet = true
		case "--advertise-port":
			if i+1 >= len(args) {
				return startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --advertise-port")
			}
			i++
			parsed, err := strconv.Atoi(strings.TrimSpace(args[i]))
			if err != nil {
				return startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --advertise-port %q (expected 1-65535)", args[i])
			}
			bootstrap.AdvertisePort = parsed
			bootstrap.AdvertisePortSet = true
		case "--mode":
			if i+1 >= len(args) {
				return startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --mode")
			}
			value := args[i+1]
			if startupExists || !isBootstrapNetworkMode(value) {
				filtered = append(filtered, arg)
				i++
				filtered = append(filtered, value)
				continue
			}
			i++
			bootstrap.Mode = value
			bootstrap.ModeSet = true
		default:
			if value, ok := consumeInlineFlag(arg, "--swarm-name="); ok {
				bootstrap.SwarmName = value
				bootstrap.SwarmNameSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--child="); ok {
				parsed, err := strconv.ParseBool(strings.TrimSpace(value))
				if err != nil {
					return startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --child %q (expected true or false)", value)
				}
				bootstrap.Child = parsed
				bootstrap.ChildSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--tailscale-url="); ok {
				bootstrap.TailscaleURL = value
				bootstrap.TailscaleURLSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--advertise-host="); ok {
				bootstrap.AdvertiseHost = value
				bootstrap.AdvertiseHostSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--advertise-port="); ok {
				parsed, err := strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					return startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --advertise-port %q (expected 1-65535)", value)
				}
				bootstrap.AdvertisePort = parsed
				bootstrap.AdvertisePortSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--mode="); ok {
				if startupExists || !isBootstrapNetworkMode(value) {
					filtered = append(filtered, arg)
					continue
				}
				bootstrap.Mode = value
				bootstrap.ModeSet = true
				continue
			}
			filtered = append(filtered, arg)
		}
	}
	return bootstrap, filtered, nil
}

func isBootstrapNetworkMode(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case startupconfig.NetworkModeLAN, startupconfig.NetworkModeTailscale:
		return true
	default:
		return false
	}
}

func consumeInlineFlag(arg, prefix string) (string, bool) {
	if !strings.HasPrefix(arg, prefix) {
		return "", false
	}
	return strings.TrimPrefix(arg, prefix), true
}
