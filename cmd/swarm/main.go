package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"swarm-refactor/swarmtui/internal/launcher"
	"swarm-refactor/swarmtui/internal/updatehandoff"
	"swarm-refactor/swarmtui/pkg/startupconfig"
)

var defaultInvokedName = "swarm"

func main() {
	args := os.Args[1:]
	invoked := filepath.Base(os.Args[0])
	if strings.TrimSpace(defaultInvokedName) != "" {
		invoked = defaultInvokedName
	}
	if invoked == "swarmdev" {
		args = append([]string{"dev"}, args...)
	}
	if err := run(os.Args[0], args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(argv0 string, args []string) error {
	invoked := filepath.Base(argv0)
	if strings.TrimSpace(defaultInvokedName) != "" {
		invoked = defaultInvokedName
	}
	defaultLane := "main"
	if invoked == "swarmdev" {
		defaultLane = "dev"
	}
	lane := launcher.DefaultLane(defaultLane)
	if len(args) > 0 && (args[0] == "main" || args[0] == "dev") {
		lane = args[0]
		args = args[1:]
	}
	bypassOverride, bootstrap, args, err := parseLaunchFlags(args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			usage()
			return nil
		case "install":
			return runInstallCommand(args[1:])
		case "uninstall":
			return runUninstallCommand(args[1:])
		}
	}
	if interactiveLaunchRequiresGit(args) {
		if err := requireGit(); err != nil {
			return err
		}
	}
	profile, err := launcher.LoadRuntimeProfile(lane, bypassOverride)
	if err != nil {
		return err
	}
	if os.Getenv("SWARM_PENDING_UPDATE_BOOT") != "" {
		if err := launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap}); err != nil {
			return err
		}
		return nil
	}
	if bootstrap.HasAny() {
		if profile.Startup.Exists {
			return startupconfig.BootstrapExistingConfigError(profile.Startup.Path)
		}
		if err := bootstrap.Validate(); err != nil {
			return err
		}
	}
	emitDirectLANDesktopWarning(profile)
	if len(args) > 0 && args[0] == "--desktop" {
		return runDesktop(profile, args[1:])
	}
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "help", "-h", "--help":
		usage()
		return nil
	case "ctl":
		if len(args) < 2 {
			return errors.New("missing swarmctl arguments")
		}
		return launcher.RunCtl(profile, args[1:], false)
	case "auth":
		if len(args) < 2 {
			return errors.New("missing swarmctl auth arguments")
		}
		return launcher.RunCtl(profile, args[1:], true)
	case "start":
		return runStartCommand(profile, args[1:])
	case "stop":
		return runStopCommand(args[1:])
	case "restart":
		return runRestartCommand(profile, args[1:])
	case "status":
		return runStatusCommand(profile, args[1:])
	case "open":
		return runOpenCommand(profile, args[1:])
	case "session":
		return runSessionCommand(profile, args[1:])
	case "server":
		if len(args) < 2 {
			return errors.New("usage: swarm [main|dev] server <run|status>")
		}
		switch args[1] {
		case "on", "off":
			return errors.New("swarm server on/off were removed; manage the installed daemon with swarm start or swarm stop")
		case "run":
			return launcher.RunBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap})
		case "status":
			status := launcher.Status(profile)
			fmt.Printf("status=%s\nhealth=%s\nlane=%s\nlisten=%s\nurl=%s\npid=%s\n", status.Status, status.Health, profile.Lane, profile.Listen, profile.URL, status.PID)
			return nil
		default:
			return errors.New("usage: swarm [main|dev] server <run|status>")
		}
	case "backend-up":
		return launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap})
	case "backend-down":
		return launcher.StopBackend(profile)
	case "backend-restart":
		if err := launcher.StopBackend(profile); err != nil {
			return err
		}
		return launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, ForceRestart: true, Bootstrap: bootstrap})
	case "backend-rebuild":
		buildProfile, err := loadBuildProfile(lane, bypassOverride)
		if err != nil {
			return err
		}
		if err := launcher.BuildSwarmdBinaries(buildProfile); err != nil {
			return err
		}
		if err := launcher.StopBackend(profile); err != nil {
			return err
		}
		return launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, ForceRestart: true, Bootstrap: bootstrap})
	case "update":
		if len(args) < 2 {
			return errors.New("usage: swarm [main|dev] update [apply|dev]")
		}
		switch args[1] {
		case "apply":
			if strings.EqualFold(lane, "dev") {
				return errors.New("update apply is disabled for the dev lane; use update dev")
			}
			return launcher.RunReleaseUpdate(profile, nil)
		case "dev":
			buildProfile, err := loadBuildProfile(lane, bypassOverride)
			if err != nil {
				return err
			}
			return launcher.RunDevUpdate(buildProfile, nil)
		default:
			return errors.New("usage: swarm [main|dev] update [apply|dev]")
		}
	case "backend-build":
		buildProfile, err := loadBuildProfile(lane, bypassOverride)
		if err != nil {
			return err
		}
		if err := launcher.BuildSwarmdBinaries(buildProfile); err != nil {
			return err
		}
		return nil
	case "info":
		if err := launcher.RecordPortFile(profile); err != nil {
			return err
		}
		fmt.Printf("lane=%s\nlisten=%s\nurl=%s\nport=%d\nstate_root=%s\npid_file=%s\nlog_file=%s\nport_record=%s\nstartup_config=%s\nbypass_permissions=%t\nswarm_bin_dir=%s\n",
			profile.Lane,
			profile.Listen,
			profile.URL,
			profile.LanePort,
			profile.StateRoot,
			profile.PIDFile,
			profile.LogFile,
			profile.PortRecord,
			profile.Startup.Path,
			profile.Bypass,
			profile.BinDir,
		)
		return nil
	case "run":
		if len(args) > 0 {
			args = args[1:]
		}
	default:
		// treat all args as tui args
	}
	if err := launcher.EnsureInstalledDaemonReady(profile); err != nil {
		return err
	}
	if err := launcher.RecordPortFile(profile); err != nil {
		return err
	}
	if err := launcher.RunTUI(profile, args); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case updatehandoff.ExitCodeDevUpdateRequested:
				buildProfile, err := loadBuildProfile(lane, bypassOverride)
				if err != nil {
					return err
				}
				return launcher.RunDevUpdate(buildProfile, args)
			case updatehandoff.ExitCodeReleaseUpdateRequested:
				return launcher.RunReleaseUpdate(profile, args)
			}
		}
		return err
	}
	return nil
}

var gitLookPath = exec.LookPath

func requireGit() error {
	if _, err := gitLookPath("git"); err == nil {
		return nil
	}
	return errors.New(`Swarm requires Git for workspaces and managed worktrees, but git was not found on PATH.
Install Git, then retry:
  Ubuntu/Debian: sudo apt update && sudo apt install -y git
  Fedora/RHEL:   sudo dnf install -y git
  Arch Linux:    sudo pacman -S git
Swarm does not install system packages automatically.`)
}

func interactiveLaunchRequiresGit(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "--desktop":
		return !containsHelpArg(args[1:])
	case "open":
		return !containsHelpArg(args[1:])
	case "session":
		if len(args) == 1 {
			return true
		}
		return args[1] == "tui" || args[1] == "open"
	case "run":
		return true
	case "help", "-h", "--help", "install", "uninstall", "start", "stop", "restart", "status", "ctl", "auth", "server", "backend-up", "backend-down", "backend-restart", "backend-rebuild", "backend-build", "update", "info":
		return false
	default:
		// Unknown arguments are forwarded to the TUI.
		return true
	}
}

func containsHelpArg(args []string) bool {
	for _, arg := range args {
		if arg == "help" || arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func runInstallCommand(args []string) error {
	assumeYes, service, handled, err := parseInstallArgs(args)
	if err != nil || handled {
		return err
	}
	fmt.Println("Swarm install plan")
	for _, line := range launcher.InstalledInstallPlan(launcher.InstallServiceOptions{Service: service}) {
		fmt.Printf("  %s\n", line)
	}
	if !assumeYes {
		if err := confirm("Continue with this install? [y/N] "); err != nil {
			return err
		}
	}
	if err := launcher.InstallInstalledDaemon(launcher.InstallServiceOptions{Service: service}); err != nil {
		return err
	}
	if service {
		fmt.Println("Swarm service installed, enabled, and started.")
	} else {
		fmt.Println("Swarm runtime and launchers installed. No daemon service was installed or started.")
		fmt.Println("Configure your supervisor to run: /usr/local/bin/swarm main server run")
	}
	return nil
}

func runUninstallCommand(args []string) error {
	opts := launcher.UninstallOptions{}
	assumeYes := false
	for _, arg := range args {
		switch arg {
		case "--purge":
			opts.Purge = true
		case "--yes", "-y":
			assumeYes = true
		case "help", "-h", "--help":
			fmt.Println("usage: swarm uninstall [--purge] [--yes]")
			return nil
		default:
			return fmt.Errorf("usage: swarm uninstall [--purge] [--yes]")
		}
	}
	fmt.Println("Swarm uninstall plan")
	for _, line := range launcher.UninstallPlan(opts) {
		fmt.Printf("  %s\n", line)
	}
	if opts.Purge {
		fmt.Println("WARNING: --purge deletes daemon config, data, cache, and logs.")
	}
	if !assumeYes {
		if err := confirm("Continue with this uninstall? [y/N] "); err != nil {
			return err
		}
	}
	if err := launcher.UninstallInstalledService(opts); err != nil {
		return err
	}
	fmt.Println("Swarm service uninstalled.")
	if !opts.Purge {
		fmt.Println("Preserved daemon data under /etc/swarmd, /var/lib/swarmd, /var/cache/swarmd, and /var/log/swarmd.")
	}
	return nil
}

func runStartCommand(profile launcher.Profile, args []string) error {
	if handled, err := parseNoArgs(args, "swarm start"); err != nil || handled {
		return err
	}
	if err := launcher.StartInstalledService(); err != nil {
		return err
	}
	if err := launcher.WaitForInstalledDaemonReady(profile); err != nil {
		return err
	}
	fmt.Println("Swarm service started.")
	return nil
}

func runStopCommand(args []string) error {
	if handled, err := parseNoArgs(args, "swarm stop"); err != nil || handled {
		return err
	}
	if err := launcher.StopInstalledService(); err != nil {
		return err
	}
	fmt.Println("Swarm service stopped.")
	return nil
}

func runRestartCommand(profile launcher.Profile, args []string) error {
	if handled, err := parseNoArgs(args, "swarm restart"); err != nil || handled {
		return err
	}
	if err := launcher.RestartInstalledService(); err != nil {
		return err
	}
	if err := launcher.WaitForInstalledDaemonReady(profile); err != nil {
		return err
	}
	fmt.Println("Swarm service restarted.")
	return nil
}

func runStatusCommand(profile launcher.Profile, args []string) error {
	if handled, err := parseNoArgs(args, "swarm status"); err != nil || handled {
		return err
	}
	status := launcher.InstalledServiceStatusForProfile(profile)
	fmt.Printf("service=%s\n", status.Unit)
	fmt.Printf("scope=%s\n", status.Scope)
	fmt.Printf("unit_path=%s\n", status.UnitPath)
	fmt.Printf("installed=%t\n", status.Installed)
	fmt.Printf("enabled=%s\n", status.Enabled)
	fmt.Printf("active=%s\n", status.Active)
	fmt.Printf("daemon_status=%s\n", status.Daemon.Status)
	fmt.Printf("daemon_health=%s\n", status.Daemon.Health)
	fmt.Printf("url=%s\n", profile.URL)
	if status.Daemon.PID != "" {
		fmt.Printf("pid=%s\n", status.Daemon.PID)
	}
	if !status.Installed {
		fmt.Printf("guidance=%s\n", status.InstallGuidance)
	} else if status.Daemon.Health != "healthy" {
		fmt.Printf("guidance=%s\n", status.StartGuidance)
	}
	return nil
}

func runOpenCommand(profile launcher.Profile, args []string) error {
	if handled, err := parseNoArgs(args, "swarm open"); err != nil || handled {
		return err
	}
	if err := launcher.EnsureInstalledDaemonReady(profile); err != nil {
		return err
	}
	url := launcher.DesktopURL(profile, 0)
	if err := launcher.OpenBrowser(url); err != nil {
		return fmt.Errorf("open %s: %w", url, err)
	}
	fmt.Printf("Opened %s\n", url)
	return nil
}

func runSessionCommand(profile launcher.Profile, args []string) error {
	if err := launcher.EnsureInstalledDaemonReady(profile); err != nil {
		return err
	}
	if len(args) == 0 {
		return launcher.RunTUI(profile, nil)
	}
	switch args[0] {
	case "tui", "open":
		return launcher.RunTUI(profile, args[1:])
	case "create", "list", "get", "messages", "send", "run":
		return launcher.RunCtl(profile, append([]string{"session"}, args...), false)
	case "help", "-h", "--help":
		fmt.Println("usage: swarm session [tui|open|create|list|get|messages|send|run] [session-args...]")
		return nil
	default:
		return launcher.RunCtl(profile, append([]string{"session"}, args...), false)
	}
}

func parseInstallArgs(args []string) (bool, bool, bool, error) {
	assumeYes := false
	service := true
	serviceSet := false
	usage := "swarm install [--yes] [--service|--no-service]"
	for _, arg := range args {
		switch arg {
		case "--yes", "-y":
			assumeYes = true
		case "--service", "--systemd":
			if serviceSet && !service {
				return false, false, false, errors.New("choose only one of --service or --no-service")
			}
			service = true
			serviceSet = true
		case "--no-service", "--no-systemd", "--files-only":
			if serviceSet && service {
				return false, false, false, errors.New("choose only one of --service or --no-service")
			}
			service = false
			serviceSet = true
		case "help", "-h", "--help":
			fmt.Println("usage: " + usage)
			return false, false, true, nil
		default:
			return false, false, false, fmt.Errorf("usage: %s", usage)
		}
	}
	return assumeYes, service, false, nil
}

func parseNoArgs(args []string, usage string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Println("usage: " + usage)
		return true, nil
	}
	return false, fmt.Errorf("usage: %s", usage)
}

func confirm(prompt string) error {
	if !stdinIsTerminal() {
		return errors.New("confirmation required; rerun with --yes for non-interactive operation")
	}
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return errors.New("cancelled")
	}
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runDesktop(profile launcher.Profile, args []string) error {
	port := profile.DesktopPort
	for len(args) > 0 {
		switch args[0] {
		case "--port":
			if len(args) < 2 {
				return errors.New("missing value for --port")
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(args[1]))
			if err != nil || parsed < 1 || parsed > 65535 {
				return fmt.Errorf("invalid desktop port: %s (expected 1-65535)", args[1])
			}
			port = parsed
			args = args[2:]
		case "help", "-h", "--help":
			usage()
			return nil
		default:
			return fmt.Errorf("unsupported --desktop argument: %s", args[0])
		}
	}
	if err := launcher.EnsureInstalledDaemonReady(profile); err != nil {
		return err
	}
	url := launcher.DesktopURL(profile, port)
	if err := launcher.OpenBrowser(url); err != nil {
		return fmt.Errorf("open %s: %w", url, err)
	}
	return nil
}

func emitDirectLANDesktopWarning(profile launcher.Profile) {
	if warning := startupconfig.DirectLANDesktopWarning(profile.Startup); warning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
}

func loadBuildProfile(lane string, bypassOverride *bool) (launcher.Profile, error) {
	root, err := launcher.ResolveRoot()
	if err != nil {
		return launcher.Profile{}, fmt.Errorf("source checkout required for build commands: %w", err)
	}
	return launcher.LoadBuildProfile(root, lane, bypassOverride)
}

func parseLaunchFlags(args []string) (*bool, startupconfig.BootstrapFlags, []string, error) {
	out := make([]string, 0, len(args))
	var override *bool
	bootstrap := startupconfig.BootstrapFlags{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--bypass-permissions":
			v := true
			override = &v
		case "--yolo":
			return nil, startupconfig.BootstrapFlags{}, nil, errors.New("--yolo was removed; use --bypass-permissions or set bypass_permissions in swarm.conf")
		case "--swarm-name":
			if i+1 >= len(args) {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --swarm-name")
			}
			i++
			bootstrap.SwarmName = args[i]
			bootstrap.SwarmNameSet = true
		case "--child":
			bootstrap.Child = true
			bootstrap.ChildSet = true
		case "--mode":
			return nil, startupconfig.BootstrapFlags{}, nil, errors.New("--mode was removed; Swarm now runs as an installed always-on daemon")
		case "--advertise-host":
			if i+1 >= len(args) {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --advertise-host")
			}
			i++
			bootstrap.AdvertiseHost = args[i]
			bootstrap.AdvertiseHostSet = true
		case "--advertise-port":
			if i+1 >= len(args) {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --advertise-port")
			}
			i++
			parsed, err := strconv.Atoi(strings.TrimSpace(args[i]))
			if err != nil {
				return nil, startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --advertise-port %q (expected 1-65535)", args[i])
			}
			bootstrap.AdvertisePort = parsed
			bootstrap.AdvertisePortSet = true
		case "--tailscale-url":
			if i+1 >= len(args) {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --tailscale-url")
			}
			i++
			bootstrap.TailscaleURL = args[i]
			bootstrap.TailscaleURLSet = true
		default:
			if value, ok := consumeInlineFlag(arg, "--swarm-name="); ok {
				bootstrap.SwarmName = value
				bootstrap.SwarmNameSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--child="); ok {
				parsed, err := strconv.ParseBool(strings.TrimSpace(value))
				if err != nil {
					return nil, startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --child %q (expected true or false)", value)
				}
				bootstrap.Child = parsed
				bootstrap.ChildSet = true
				continue
			}
			if _, ok := consumeInlineFlag(arg, "--mode="); ok {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("--mode was removed; Swarm now runs as an installed always-on daemon")
			}
			if value, ok := consumeInlineFlag(arg, "--advertise-host="); ok {
				bootstrap.AdvertiseHost = value
				bootstrap.AdvertiseHostSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--advertise-port="); ok {
				parsed, err := strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					return nil, startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --advertise-port %q (expected 1-65535)", value)
				}
				bootstrap.AdvertisePort = parsed
				bootstrap.AdvertisePortSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--tailscale-url="); ok {
				bootstrap.TailscaleURL = value
				bootstrap.TailscaleURLSet = true
				continue
			}
			out = append(out, arg)
		}
	}
	return override, bootstrap, out, nil
}

func consumeInlineFlag(arg, prefix string) (string, bool) {
	if !strings.HasPrefix(arg, prefix) {
		return "", false
	}
	return strings.TrimPrefix(arg, prefix), true
}

func usage() {
	fmt.Print(`swarm launcher

Usage:
  swarm install [--yes] [--service|--no-service]
  swarm uninstall [--purge] [--yes]
  swarm start|stop|restart|status
  swarm open
  swarm session [tui|open|create|list|get|messages|send|run] [session-args...]
  swarm [main|dev] [run] [--swarm-name NAME] [--child] [--advertise-host HOST] [--advertise-port PORT] [--tailscale-url URL] [tui-args...]
  swarm [main|dev] --desktop [--port N]
  swarm [main|dev] server <run|status>
  swarm [main|dev] ctl <swarmctl-args...>
  swarm [main|dev] auth <swarmctl-auth-args...>
  swarm [main|dev] backend-up
  swarm [main|dev] backend-down
  swarm [main|dev] backend-restart
  swarm [main|dev] backend-rebuild
  swarm [main|dev] backend-build
  swarm [main|dev] update apply
  swarm [main|dev] update dev
  swarm [main|dev] info
  swarm help

Alias:
  swarmdev [run] [--swarm-name NAME] [--child] [--advertise-host HOST] [--advertise-port PORT] [--tailscale-url URL] [tui-args...]
  swarmdev --desktop [--port N]
  swarmdev server <run|status>
  swarmdev status|open|session
  swarmdev ctl <swarmctl-args...>
  swarmdev auth <swarmctl-auth-args...>
  swarmdev backend-up|down|restart|rebuild|build|info
  swarmdev update dev
`)
}
