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
	"swarm-refactor/swarmtui/pkg/storagecontract"
)

type Config struct {
	ConfigPath              string
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

	storageDefaults, err := resolveDaemonStorageDefaults()
	if err != nil {
		return Config{}, err
	}

	defaultDataDir := storageDefaults.DataDir
	defaultDBPath := storageDefaults.DBPath
	defaultLockPath := storageDefaults.LockPath
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
		ConfigPath:              configPath,
		ListenAddr:              defaultListenAddr,
		DesktopPort:             startupCfg.DesktopPort,
		PeerTransportPort:       startupCfg.PeerTransportPort,
		BypassPermissions:       startupCfg.BypassPermissions,
		RetainToolOutputHistory: startupCfg.RetainToolOutputHistory,
	}
	fs.StringVar(&cfg.ListenAddr, "listen", defaultListenAddr, "HTTP listen address")
	fs.IntVar(&cfg.DesktopPort, "desktop-port", startupCfg.DesktopPort, "desktop HTTP listen port (0 disables desktop listener)")
	fs.BoolVar(&cfg.BypassPermissions, "bypass-permissions", startupCfg.BypassPermissions, "bypass normal tool permission prompts (exit_plan_mode still requires approval)")
	fs.StringVar(&cfg.DataDir, "data-dir", defaultDataDir, "data directory root")
	fs.StringVar(&cfg.DBPath, "db-path", defaultDBPath, "Pebble database path")
	fs.StringVar(&cfg.LockPath, "lock-path", defaultLockPath, "daemon lock file path")
	defaultStartupCWD, err := resolveDefaultStartupCWD()
	if err != nil {
		return Config{}, err
	}
	fs.StringVar(&cfg.StartupCWD, "cwd", defaultStartupCWD, "startup working directory binding (defaults to user home)")

	if err := fs.Parse(filteredArgs); err != nil {
		return Config{}, err
	}

	if cfg.DesktopPort < 0 || cfg.DesktopPort > 65535 {
		return Config{}, fmt.Errorf("invalid desktop port %d (expected 0-65535)", cfg.DesktopPort)
	}

	if strings.TrimSpace(cfg.StartupCWD) == "" {
		cfg.StartupCWD = defaultStartupCWD
	}

	if err := validateDaemonStoragePaths(cfg); err != nil {
		return Config{}, err
	}

	if !startupCfg.Exists {
		startupCfg, err = startupConfigFromRuntime(configPath, cfg.ListenAddr, cfg.DesktopPort, cfg.BypassPermissions, cfg.RetainToolOutputHistory)
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
		cfg.RetainToolOutputHistory = startupCfg.RetainToolOutputHistory
	}

	return cfg, nil
}

type daemonStorageDefaults struct {
	DataDir  string
	DBPath   string
	LockPath string
}

func resolveDaemonStorageDefaults() (daemonStorageDefaults, error) {
	dataDir, err := storagecontract.ResolveRoot(storagecontract.RootData, storagecontract.Options{})
	if err != nil {
		return daemonStorageDefaults{}, fmt.Errorf("resolve daemon data directory: %w", err)
	}
	runtimeDir, err := storagecontract.ResolveRoot(storagecontract.RootRuntime, storagecontract.Options{})
	if err != nil {
		return daemonStorageDefaults{}, fmt.Errorf("resolve daemon runtime directory: %w", err)
	}
	dbPath, err := storagecontract.Join(dataDir, "swarmd.pebble")
	if err != nil {
		return daemonStorageDefaults{}, fmt.Errorf("resolve daemon database path: %w", err)
	}
	lockPath, err := storagecontract.Join(runtimeDir, "swarmd.lock")
	if err != nil {
		return daemonStorageDefaults{}, fmt.Errorf("resolve daemon lock path: %w", err)
	}
	return daemonStorageDefaults{DataDir: dataDir, DBPath: dbPath, LockPath: lockPath}, nil
}

func resolveDefaultStartupCWD() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return "", errors.New("user home directory is unavailable")
	}
	return home, nil
}

func validateDaemonStoragePaths(cfg Config) error {
	opts := storagecontract.Options{WorkspaceRoots: validationWorkspaceRoots(cfg.StartupCWD)}
	for _, item := range []struct {
		flagName string
		path     string
	}{
		{flagName: "data-dir", path: cfg.DataDir},
		{flagName: "db-path", path: cfg.DBPath},
		{flagName: "lock-path", path: cfg.LockPath},
	} {
		if err := storagecontract.ValidateRoot(item.path, opts); err != nil {
			return fmt.Errorf("invalid --%s %q: %w", item.flagName, item.path, err)
		}
	}
	return nil
}

func validationWorkspaceRoots(starts ...string) []string {
	roots := make([]string, 0, len(starts)+1)
	seen := make(map[string]struct{}, len(starts)+1)
	appendRoot := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		root = filepath.Clean(root)
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	for _, start := range starts {
		for _, root := range detectedWorkspaceRoots(start) {
			appendRoot(root)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		for _, root := range detectedWorkspaceRoots(cwd) {
			appendRoot(root)
		}
	}
	return roots
}

func detectedWorkspaceRoots(start string) []string {
	start = strings.TrimSpace(start)
	if start == "" {
		return nil
	}
	if !filepath.IsAbs(start) {
		abs, err := filepath.Abs(start)
		if err != nil {
			return nil
		}
		start = abs
	}
	current := filepath.Clean(start)
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return []string{current}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func startupConfigFromRuntime(path, listenAddr string, desktopPort int, bypassPermissions, retainToolOutputHistory bool) (startupconfig.FileConfig, error) {
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
			if !isBootstrapNetworkMode(value) {
				return startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --mode %q (expected %q or %q)", value, startupconfig.NetworkModeLAN, startupconfig.NetworkModeTailscale)
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
				if !isBootstrapNetworkMode(value) {
					return startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --mode %q (expected %q or %q)", value, startupconfig.NetworkModeLAN, startupconfig.NetworkModeTailscale)
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
